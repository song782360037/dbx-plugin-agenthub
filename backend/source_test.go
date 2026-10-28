package main

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreviewDirectSkillURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/markdown")
		_, _ = writer.Write([]byte("---\nname: web-skill\ndescription: From URL\n---\n# Web skill\n"))
	}))
	defer server.Close()
	t.Setenv("NO_PROXY", "*")
	manager, _ := newTestManager(t)
	preview, err := manager.previewSource(SourceInput{Type: "url", Value: server.URL + "/SKILL.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Candidates) != 1 || preview.Candidates[0].Name != "web-skill" {
		t.Fatalf("candidates = %#v", preview.Candidates)
	}
	if preview.Source.Type != "url" || preview.Source.ArchiveSHA256 == "" {
		t.Fatalf("source = %#v", preview.Source)
	}
}

func TestPreviewZipURL(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, _ := writer.Create("skills/url-skill/SKILL.md")
	_, _ = entry.Write([]byte("---\nname: url-skill\ndescription: From zip URL\n---\n# URL skill\n"))
	_ = writer.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/zip")
		_, _ = writer.Write(archive.Bytes())
	}))
	defer server.Close()
	t.Setenv("NO_PROXY", "*")
	manager, _ := newTestManager(t)
	preview, err := manager.previewSource(SourceInput{Type: "url", Value: server.URL + "/skill.zip"})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Candidates) != 1 || preview.Candidates[0].Name != "url-skill" {
		t.Fatalf("candidates = %#v", preview.Candidates)
	}
}

func TestPreviewRejectsHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte("<html>not a skill</html>"))
	}))
	defer server.Close()
	t.Setenv("NO_PROXY", "*")
	manager, _ := newTestManager(t)
	if _, err := manager.previewSource(SourceInput{Type: "url", Value: server.URL}); err == nil {
		t.Fatal("expected HTML response to be rejected")
	}
}
