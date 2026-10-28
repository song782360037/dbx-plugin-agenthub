package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillContent(t *testing.T) {
	content := "\ufeff---\r\nname: pdf-tools\r\ndescription: >-\r\n  Extract text and tables from PDF files.\r\n  Use when reading PDFs.\r\nallowed-tools: Bash(pdftotext:*), Read\r\n---\r\n# PDF tools\r\nBody\r\n"
	meta := parseSkillContent(content, "pdf-tools")
	if meta.Name != "pdf-tools" {
		t.Fatalf("name = %q", meta.Name)
	}
	if !strings.Contains(meta.Description, "Extract text") || !strings.Contains(meta.Description, "Use when reading PDFs") {
		t.Fatalf("folded description was not parsed: %q", meta.Description)
	}
	if len(meta.RequiredTools) != 2 || meta.RequiredTools[0] != "Bash(pdftotext:*)" {
		t.Fatalf("tools = %#v", meta.RequiredTools)
	}
	if !strings.Contains(meta.Body, "# PDF tools") {
		t.Fatalf("body = %q", meta.Body)
	}
	if len(meta.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", meta.Warnings)
	}
}

func TestParseSkillContentMissingFrontmatter(t *testing.T) {
	meta := parseSkillContent("# hello\n", "hello-skill")
	if meta.Name != "hello-skill" {
		t.Fatalf("name = %q", meta.Name)
	}
	if meta.Description != "" {
		t.Fatalf("description = %q", meta.Description)
	}
	if len(meta.Warnings) == 0 {
		t.Fatal("expected warning for missing frontmatter")
	}
}

func TestParseSkillContentInvalidName(t *testing.T) {
	meta := parseSkillContent("---\nname: Bad_Name\ndescription: test\n---\n", "Bad_Name")
	if !skillNamePattern.MatchString("bad-name") || skillNamePattern.MatchString(meta.Name) {
		t.Fatal("name validation pattern mismatch")
	}
	if len(meta.Warnings) == 0 {
		t.Fatal("expected warning for invalid name")
	}
}

func TestHashDirIgnoresMarkerAndDetectsEdits(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "SKILL.md"), "---\nname: demo\ndescription: demo\n---\nhello\n")
	mustWrite(t, filepath.Join(dir, markerName), `{"managedBy":"x"}`)
	hash1, files, _, _, err := hashDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "SKILL.md" {
		t.Fatalf("files = %#v", files)
	}
	mustWrite(t, filepath.Join(dir, markerName), `{"managedBy":"y","changed":true}`)
	hash2, _, _, _, err := hashDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 {
		t.Fatal("marker file must not affect content hash")
	}
	mustWrite(t, filepath.Join(dir, "SKILL.md"), "changed\n")
	hash3, _, _, _, err := hashDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hash3 == hash1 {
		t.Fatal("content edit did not change hash")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
