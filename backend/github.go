package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const githubAPI = "https://api.github.com"

type githubRef struct {
	Owner string
	Repo  string
	Ref   string
	Path  string
}

var shortRepoPattern = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:@([^\s]+))?$`)

type githubClient struct {
	http         *http.Client
	token        string
	apiURL       string
	codeloadBase string
}

var newGitHubClientForManager = newGitHubClient

func newGitHubClient(settings Settings) (*githubClient, error) {
	client, err := newHTTPClient(settings)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(settings.GitHubToken)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	}
	return &githubClient{http: client, token: token, apiURL: githubAPI, codeloadBase: "https://codeload.github.com"}, nil
}

func newHTTPClient(settings Settings) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxy := strings.TrimSpace(settings.Proxy)
	if proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, errors.New("代理地址必须是 http:// 或 https:// URL")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	return &http.Client{Transport: transport, Timeout: 120 * time.Second}, nil
}

func parseGitHubRef(value, ref, subPath string) (*githubRef, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false, nil
	}
	if match := shortRepoPattern.FindStringSubmatch(value); match != nil && !strings.Contains(value, "://") {
		result := &githubRef{Owner: match[1], Repo: strings.TrimSuffix(match[2], ".git"), Ref: ref, Path: strings.Trim(subPath, "/")}
		if result.Ref == "" {
			result.Ref = match[3]
		}
		if result.Owner == "" || result.Repo == "" {
			return nil, false, errors.New("GitHub 仓库格式应为 owner/repo")
		}
		return result, true, nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, false, nil
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "github.com" && host != "www.github.com" && host != "raw.githubusercontent.com" {
		return nil, false, nil
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return nil, false, errors.New("GitHub URL 缺少 owner/repo")
	}
	result := &githubRef{Owner: parts[0], Repo: strings.TrimSuffix(parts[1], ".git"), Ref: ref, Path: strings.Trim(subPath, "/")}
	if host == "raw.githubusercontent.com" {
		if len(parts) < 4 {
			return nil, false, errors.New("raw.githubusercontent.com URL 缺少 ref 或文件路径")
		}
		if result.Ref == "" {
			result.Ref = parts[2]
		}
		if result.Path == "" {
			result.Path = strings.Join(parts[3:], "/")
		}
		return result, true, nil
	}
	if len(parts) >= 4 {
		switch parts[2] {
		case "tree", "blob", "raw":
			if result.Ref == "" {
				result.Ref = parts[3]
			}
			if result.Path == "" && len(parts) > 4 {
				result.Path = strings.Join(parts[4:], "/")
			}
		case "releases", "tags":
			// owner/repo/releases or owner/repo/tags: use default ref.
		}
	}
	if strings.HasSuffix(result.Repo, ".git") {
		result.Repo = strings.TrimSuffix(result.Repo, ".git")
	}
	if result.Owner == "" || result.Repo == "" {
		return nil, false, errors.New("无法解析 GitHub 地址")
	}
	return result, true, nil
}

func (c *githubClient) getJSON(ctx context.Context, endpoint string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "dbx-agenthub/"+appVersion)
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return githubError(response, body)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8*1024*1024))
	return decoder.Decode(out)
}

func githubError(response *http.Response, body []byte) error {
	var payload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
		if response.Header.Get("X-RateLimit-Remaining") == "0" {
			reset := response.Header.Get("X-RateLimit-Reset")
			if seconds, err := strconv.ParseInt(reset, 10, 64); err == nil {
				reset = time.Unix(seconds, 0).UTC().Format("2006-01-02 15:04:05 UTC")
			}
			return fmt.Errorf("GitHub API 速率限制已用尽（重置时间 %s）；请配置 GITHUB_TOKEN 或稍后重试", reset)
		}
	}
	return fmt.Errorf("GitHub API %d：%s", response.StatusCode, sanitizeLabel(message, 240))
}

func (c *githubClient) resolveRef(ctx context.Context, owner, repo, requested string) (string, string, error) {
	var ref = strings.TrimSpace(requested)
	if ref == "" {
		var release struct {
			TagName string `json:"tag_name"`
		}
		releaseURL := fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.apiURL, url.PathEscape(owner), url.PathEscape(repo))
		if err := c.getJSON(ctx, releaseURL, &release); err == nil && release.TagName != "" {
			ref = release.TagName
		}
	}
	if ref == "" {
		var tags []struct {
			Name string `json:"name"`
		}
		tagsURL := fmt.Sprintf("%s/repos/%s/%s/tags?per_page=1", c.apiURL, url.PathEscape(owner), url.PathEscape(repo))
		if err := c.getJSON(ctx, tagsURL, &tags); err == nil && len(tags) > 0 {
			ref = tags[0].Name
		}
	}
	if ref == "" {
		var repository struct {
			DefaultBranch string `json:"default_branch"`
		}
		repoURL := fmt.Sprintf("%s/repos/%s/%s", c.apiURL, url.PathEscape(owner), url.PathEscape(repo))
		if err := c.getJSON(ctx, repoURL, &repository); err != nil {
			return "", "", err
		}
		ref = repository.DefaultBranch
	}
	if ref == "" {
		return "", "", errors.New("无法确定 GitHub 默认分支，请手动填写 ref")
	}
	commit := ""
	var commitInfo struct {
		SHA string `json:"sha"`
	}
	commitURL := fmt.Sprintf("%s/repos/%s/%s/commits/%s", c.apiURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	if err := c.getJSON(ctx, commitURL, &commitInfo); err == nil {
		commit = commitInfo.SHA
	}
	return ref, commit, nil
}

func (c *githubClient) downloadArchive(ctx context.Context, owner, repo, ref, target string, maxBytes int64) (string, error) {
	codeloadBase := c.codeloadBase
	if codeloadBase == "" {
		codeloadBase = "https://codeload.github.com"
	}
	candidates := []string{
		fmt.Sprintf("%s/%s/%s/zip/%s", strings.TrimRight(codeloadBase, "/"), url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref)),
		fmt.Sprintf("%s/repos/%s/%s/zipball/%s", c.apiURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref)),
	}
	var lastErr error
	for _, endpoint := range candidates {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		request.Header.Set("User-Agent", "dbx-agenthub/"+appVersion)
		if c.token != "" {
			request.Header.Set("Authorization", "Bearer "+c.token)
		}
		response, err := c.http.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		if response.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			response.Body.Close()
			lastErr = githubError(response, body)
			continue
		}
		if response.ContentLength > maxBytes && response.ContentLength > 0 {
			response.Body.Close()
			lastErr = fmt.Errorf("GitHub 压缩包超过 %d MiB 限制", maxBytes/1024/1024)
			continue
		}
		hash, err := writeLimited(response.Body, target, maxBytes)
		response.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return hash, nil
	}
	if lastErr == nil {
		lastErr = errors.New("GitHub 下载失败")
	}
	return "", lastErr
}

func (c *githubClient) latestCommitForPath(ctx context.Context, owner, repo, ref, skillPath string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/commits?sha=%s&per_page=1", c.apiURL, url.PathEscape(owner), url.PathEscape(repo), url.QueryEscape(ref))
	if skillPath != "" && skillPath != "." {
		endpoint += "&path=" + url.QueryEscape(path.Clean(skillPath))
	}
	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := c.getJSON(ctx, endpoint, &commits); err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "", errors.New("没有找到该路径的提交")
	}
	return commits[0].SHA, nil
}

func writeLimited(reader io.Reader, target string, maxBytes int64) (string, error) {
	if err := os.MkdirAll(filepathDir(target), 0o755); err != nil {
		return "", err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hashWriter := newSHA256Writer(file)
	written, err := io.Copy(hashWriter, io.LimitReader(reader, maxBytes+1))
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if written > maxBytes {
		return "", fmt.Errorf("下载内容超过 %d MiB 限制", maxBytes/1024/1024)
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hashWriter.Hex(), nil
}

func filepathDir(path string) string {
	index := strings.LastIndexAny(path, `/\`)
	if index < 0 {
		return "."
	}
	return path[:index]
}
