package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseGitHubRef(t *testing.T) {
	cases := []struct {
		value, ref, path, owner, repo, wantRef, wantPath string
		isGitHub                                         bool
	}{
		{"anthropics/skills", "", "", "anthropics", "skills", "", "", true},
		{"anthropics/skills@v1.2.3", "", "", "anthropics", "skills", "v1.2.3", "", true},
		{"https://github.com/obra/superpowers", "", "", "obra", "superpowers", "", "", true},
		{"https://github.com/anthropics/skills/tree/main/skills/pdf", "", "", "anthropics", "skills", "main", "skills/pdf", true},
		{"https://github.com/anthropics/skills/blob/main/skills/pdf/SKILL.md", "", "", "anthropics", "skills", "main", "skills/pdf/SKILL.md", true},
		{"https://raw.githubusercontent.com/openai/skills/main/skills/.curated/pdf/SKILL.md", "", "", "openai", "skills", "main", "skills/.curated/pdf/SKILL.md", true},
		{"owner/repo", "dev", "skills/x", "owner", "repo", "dev", "skills/x", true},
		{"https://gitlab.com/org/repo", "", "", "", "", "", "", false},
	}
	for _, test := range cases {
		result, isGitHub, err := parseGitHubRef(test.value, test.ref, test.path)
		if err != nil {
			t.Fatalf("%s: %v", test.value, err)
		}
		if isGitHub != test.isGitHub {
			t.Fatalf("%s: isGitHub = %v", test.value, isGitHub)
		}
		if !isGitHub {
			continue
		}
		if result.Owner != test.owner || result.Repo != test.repo || result.Ref != test.wantRef || result.Path != test.wantPath {
			t.Fatalf("%s: %#v", test.value, result)
		}
	}
}

func TestLatestCommitForPath(t *testing.T) {
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenPath = request.URL.Path
		if request.URL.Query().Get("path") != "skills/pdf" {
			t.Errorf("path query = %q", request.URL.Query().Get("path"))
		}
		_, _ = writer.Write([]byte(`[{"sha":"abc123"}]`))
	}))
	defer server.Close()
	client := &githubClient{http: server.Client(), apiURL: server.URL}
	sha, err := client.latestCommitForPath(t.Context(), "anthropics", "skills", "main", "skills/pdf/")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "abc123" {
		t.Fatalf("sha = %q", sha)
	}
	if !strings.Contains(seenPath, "/repos/anthropics/skills/commits") {
		t.Fatalf("path = %q", seenPath)
	}
}

func TestGitHubRateLimitMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-RateLimit-Remaining", "0")
		writer.Header().Set("X-RateLimit-Reset", "1790000000")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer server.Close()
	client := &githubClient{http: server.Client(), apiURL: server.URL}
	var out any
	err := client.getJSON(t.Context(), server.URL, &out)
	if err == nil || !strings.Contains(err.Error(), "速率限制") {
		t.Fatalf("expected rate limit error, got %v", err)
	}
}
