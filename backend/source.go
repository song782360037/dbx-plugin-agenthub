package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxUploadBase64 = 6 * 1024 * 1024

func (m *Manager) previewSource(input SourceInput) (*Preview, error) {
	return m.previewSourceWithSettings(input, m.settingsSnapshot())
}

func (m *Manager) previewSourceWithSettings(input SourceInput, settings Settings) (*Preview, error) {
	kind, err := inferSourceType(input)
	if err != nil {
		return nil, err
	}
	id := randomID(8)
	previewRoot := filepath.Join(m.tmpDir, "preview-"+id)
	if err := os.MkdirAll(previewRoot, 0o755); err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(previewRoot)
		}
	}()
	preview := &Preview{ID: id, Root: previewRoot, TempDir: previewRoot, CreatedAt: time.Now().UTC()}
	var discoverRoot string
	var scope string
	switch kind {
	case "github":
		ref, isGitHub, err := parseGitHubRef(input.Value, input.Ref, "")
		if err != nil || !isGitHub {
			if err == nil {
				err = errors.New("无法识别 GitHub 地址")
			}
			return nil, err
		}
		client, err := newGitHubClientForManager(settings)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		archivePath := filepath.Join(previewRoot, "source.zip")
		archiveHash := ""
		resolvedRef := ""
		commit := ""
		var resolveErr error
		if strings.TrimSpace(ref.Ref) != "" {
			resolvedRef, commit, resolveErr = client.resolveRef(ctx, ref.Owner, ref.Repo, ref.Ref)
			if resolveErr == nil {
				archiveHash, resolveErr = client.downloadArchive(ctx, ref.Owner, ref.Repo, archiveRevision(resolvedRef, commit), archivePath, int64(settings.MaxDownloadMB)*1024*1024)
			}
		} else {
			resolvedRef, commit, resolveErr = client.resolveRef(ctx, ref.Owner, ref.Repo, "")
			if resolveErr == nil {
				archiveHash, resolveErr = client.downloadArchive(ctx, ref.Owner, ref.Repo, archiveRevision(resolvedRef, commit), archivePath, int64(settings.MaxDownloadMB)*1024*1024)
			} else {
				// GitHub API can be rate-limited without a token; codeload may still work.
				var lastDownloadErr error
				for _, fallback := range []string{"main", "master"} {
					hash, downloadErr := client.downloadArchive(ctx, ref.Owner, ref.Repo, fallback, archivePath, int64(settings.MaxDownloadMB)*1024*1024)
					if downloadErr == nil {
						resolvedRef = fallback
						archiveHash = hash
						commit = ""
						resolveErr = nil
						preview.Warnings = append(preview.Warnings, "GitHub API 不可用，已回退到分支 "+fallback+"；检查更新需要配置 Token")
						break
					}
					lastDownloadErr = downloadErr
				}
				if resolveErr != nil && lastDownloadErr != nil {
					resolveErr = fmt.Errorf("GitHub API 不可用（%v）；分支回退也失败（%v）", resolveErr, lastDownloadErr)
				}
			}
		}
		if resolveErr != nil {
			return nil, resolveErr
		}
		repoRoot := filepath.Join(previewRoot, "repo")
		discoverRoot, err = extractGitHubArchive(archivePath, repoRoot, limitsFor(settings))
		if err != nil {
			return nil, err
		}
		_ = os.Remove(archivePath)
		preview.Source = Source{
			Type: "github", Repo: ref.Owner + "/" + ref.Repo, Ref: ref.Ref, ResolvedRef: resolvedRef,
			Commit: commit, ArchiveSHA256: archiveHash,
			Label: ref.Owner + "/" + ref.Repo + "@" + resolvedRef,
		}
		scope = ref.Path
		if strings.TrimSpace(input.Path) != "" {
			scope = strings.TrimSpace(input.Path)
		}
	case "url":
		parsed, err := url.Parse(strings.TrimSpace(input.Value))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, errors.New("下载地址必须是 http:// 或 https:// URL")
		}
		client, err := newHTTPClient(settings)
		if err != nil {
			return nil, err
		}
		downloadPath := filepath.Join(previewRoot, "download")
		archiveHash, contentType, err := downloadFile(client, parsed.String(), downloadPath, int64(settings.MaxDownloadMB)*1024*1024)
		if err != nil {
			return nil, err
		}
		archiveKind, _ := detectArchive(downloadPath)
		if archiveKind != "" {
			discoverRoot, err = extractArchive(downloadPath, filepath.Join(previewRoot, "content"), limitsFor(settings))
			if err != nil {
				return nil, err
			}
			_ = os.Remove(downloadPath)
			preview.Source = Source{Type: "url", URL: parsed.String(), ArchiveSHA256: archiveHash, Label: filepath.Base(parsed.Path)}
		} else {
			if strings.Contains(strings.ToLower(contentType), "text/html") {
				return nil, errors.New("下载地址返回 HTML 页面，不是 SKILL.md 或压缩包")
			}
			contentRoot := filepath.Join(previewRoot, "content")
			if err := os.MkdirAll(contentRoot, 0o755); err != nil {
				return nil, err
			}
			if err := os.Rename(downloadPath, filepath.Join(contentRoot, "SKILL.md")); err != nil {
				return nil, err
			}
			discoverRoot = contentRoot
			preview.Source = Source{Type: "url", URL: parsed.String(), ArchiveSHA256: archiveHash, Label: filepath.Base(parsed.Path)}
		}
		scope = input.Path
	case "zip":
		if input.ZipBase64 == "" {
			return nil, errors.New("缺少压缩包内容")
		}
		encoded := input.ZipBase64
		if index := strings.Index(encoded, ","); index >= 0 && strings.HasPrefix(encoded[:index], "data:") {
			encoded = encoded[index+1:]
		}
		if len(encoded) > maxUploadBase64 {
			return nil, errors.New("上传压缩包超过 4 MiB，请改用本地路径导入")
		}
		data, err := decodeBase64(encoded)
		if err != nil {
			return nil, errors.New("压缩包 base64 解码失败")
		}
		archivePath := filepath.Join(previewRoot, "upload.zip")
		if err := os.WriteFile(archivePath, data, 0o600); err != nil {
			return nil, err
		}
		discoverRoot, err = extractArchive(archivePath, filepath.Join(previewRoot, "content"), limitsFor(settings))
		if err != nil {
			return nil, err
		}
		preview.Source = Source{
			Type: "zip", ArchiveName: sanitizeLabel(input.FileName, 120),
			ArchiveSHA256: hashBytes(data), Label: sanitizeLabel(input.FileName, 120),
		}
		scope = input.Path
	case "local":
		localPath, err := absoluteClean(input.Value)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(localPath)
		if err != nil {
			return nil, fmt.Errorf("本地路径不存在：%s", localPath)
		}
		contentRoot := filepath.Join(previewRoot, "content")
		switch {
		case info.IsDir():
			limits := limitsFor(settings)
			if _, _, err := copyTree(localPath, contentRoot, copyLimits{MaxFiles: limits.MaxFiles, MaxBytes: limits.MaxBytes}, true); err != nil {
				return nil, fmt.Errorf("复制本地目录失败：%w", err)
			}
			discoverRoot = contentRoot
		default:
			archiveKind, _ := detectArchive(localPath)
			if archiveKind != "" {
				discoverRoot, err = extractArchive(localPath, contentRoot, limitsFor(settings))
				if err != nil {
					return nil, err
				}
			} else if strings.EqualFold(filepath.Base(localPath), "SKILL.md") {
				if err := os.MkdirAll(contentRoot, 0o755); err != nil {
					return nil, err
				}
				if err := copyFile(localPath, filepath.Join(contentRoot, "SKILL.md")); err != nil {
					return nil, err
				}
				discoverRoot = contentRoot
			} else {
				return nil, errors.New("本地来源必须是目录、zip/tar.gz 压缩包或 SKILL.md")
			}
		}
		preview.Source = Source{Type: "local", LocalPath: localPath, Label: filepath.Base(localPath)}
		scope = input.Path
	default:
		return nil, errors.New("不支持的来源类型")
	}
	discoverRoot = filepath.Clean(discoverRoot)
	if !withinRoot(previewRoot, discoverRoot) {
		return nil, errors.New("来源解压路径异常")
	}
	preview.Root = discoverRoot
	candidates, warnings, err := discoverSkills(discoverRoot, scope, settings)
	if err != nil {
		return nil, err
	}
	if _, err := indexCandidates(candidates); err != nil {
		return nil, err
	}
	preview.Candidates = candidates
	preview.Warnings = uniqueStrings(append(preview.Warnings, warnings...))
	m.putPreview(preview)
	cleanup = false
	return preview, nil
}

// Preserve the human-readable ref, but download the immutable snapshot when known.
func archiveRevision(ref, commit string) string {
	if commit != "" {
		return commit
	}
	return ref
}

func indexCandidates(candidates []Candidate) (map[string]Candidate, error) {
	indexed := make(map[string]Candidate, len(candidates))
	for _, candidate := range candidates {
		if _, exists := indexed[candidate.Name]; exists {
			return nil, fmt.Errorf("来源中存在重名技能 %q；请指定唯一的技能子目录后重新预览", candidate.Name)
		}
		indexed[candidate.Name] = candidate
	}
	return indexed, nil
}

func inferSourceType(input SourceInput) (string, error) {
	if strings.TrimSpace(input.ZipBase64) != "" {
		return "zip", nil
	}
	kind := strings.ToLower(strings.TrimSpace(input.Type))
	switch kind {
	case "github", "url", "zip", "local":
		return kind, nil
	}
	value := strings.TrimSpace(input.Value)
	if value == "" {
		return "", errors.New("请填写 GitHub 地址、下载地址或本地路径")
	}
	if _, isGitHub, _ := parseGitHubRef(value, input.Ref, ""); isGitHub {
		return "github", nil
	}
	if expanded := expandHome(value); existingPath(expanded) {
		return "local", nil
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return "url", nil
	}
	return "", errors.New("无法识别来源；GitHub 仓库请使用 owner/repo 或完整 URL")
}

func existingPath(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func decodeBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	data, err := base64.StdEncoding.DecodeString(value)
	if err == nil {
		return data, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

func extractGitHubArchive(archivePath, dest string, limits archiveLimits) (string, error) {
	if _, err := extractArchiveRaw(archivePath, dest, limits); err != nil {
		return "", err
	}
	return topLevelRoot(dest), nil
}

func topLevelRoot(dest string) string {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return dest
	}
	var dirs []fs.DirEntry
	var files int
	for _, entry := range entries {
		if entry.IsDir() {
			if strings.EqualFold(entry.Name(), "__MACOSX") {
				continue
			}
			dirs = append(dirs, entry)
		} else {
			files++
		}
	}
	if files == 0 && len(dirs) == 1 {
		return filepath.Join(dest, dirs[0].Name())
	}
	return dest
}

func downloadFile(client *http.Client, endpoint, target string, maxBytes int64) (string, string, error) {
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("User-Agent", "dbx-agenthub/"+appVersion)
	request.Header.Set("Accept", "*/*")
	response, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return "", "", fmt.Errorf("下载失败（HTTP %d）：%s", response.StatusCode, sanitizeLabel(string(body), 200))
	}
	if response.ContentLength > maxBytes && response.ContentLength > 0 {
		return "", "", fmt.Errorf("下载内容超过 %d MiB 限制", maxBytes/1024/1024)
	}
	hash, err := writeLimited(response.Body, target, maxBytes)
	if err != nil {
		return "", "", err
	}
	return hash, response.Header.Get("Content-Type"), nil
}
