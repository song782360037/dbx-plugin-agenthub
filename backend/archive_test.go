package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeArchivePath(t *testing.T) {
	blocked := []string{
		"../evil", "a/../../evil", "/etc/passwd", `C:\Windows\evil`, `..\evil`,
		"skill/CON.txt", "skill/aux", "skill/name.", "skill/name ", "skill/a:b", "skill/a*b",
	}
	for _, name := range blocked {
		if _, err := safeArchivePath(name); err == nil {
			t.Fatalf("expected %q to be rejected", name)
		}
	}
	allowed := []string{"skill/SKILL.md", "skill/scripts/run.sh", "a/b/c.txt", "SKILL.md"}
	for _, name := range allowed {
		if _, err := safeArchivePath(name); err != nil {
			t.Fatalf("expected %q to be allowed: %v", name, err)
		}
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("nope"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	file.Close()
	dest := t.TempDir()
	if _, err := extractArchive(archive, dest, archiveLimits{MaxFiles: 10, MaxBytes: 1024 * 1024}); err == nil {
		t.Fatal("expected traversal archive to be rejected")
	}
	if _, err := os.Stat(filepath.Join(dest, "..", "escape.txt")); err == nil {
		t.Fatal("archive escaped destination")
	}
}

func TestExtractZipRejectsSymlink(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "link.zip")
	file, _ := os.Create(archive)
	writer := zip.NewWriter(file)
	header := &zip.FileHeader{Name: "skill/link"}
	header.SetMode(os.ModeSymlink | 0o777)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("/etc/passwd"))
	_ = writer.Close()
	_ = file.Close()
	if _, err := extractArchive(archive, t.TempDir(), archiveLimits{MaxFiles: 10, MaxBytes: 1024}); err == nil {
		t.Fatal("expected symlink archive to be rejected")
	}
}

func TestExtractZipNoZipBomb(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bomb.zip")
	file, _ := os.Create(archive)
	writer := zip.NewWriter(file)
	entry, _ := writer.Create("SKILL.md")
	_, _ = entry.Write(make([]byte, 2*1024*1024))
	_ = writer.Close()
	_ = file.Close()
	if _, err := extractArchive(archive, t.TempDir(), archiveLimits{MaxFiles: 10, MaxBytes: 1024}); err == nil {
		t.Fatal("expected size limit to be enforced")
	}
}

func TestExtractTarGzRejectsLink(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "link.tar.gz")
	file, _ := os.Create(archive)
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	_ = tarWriter.WriteHeader(&tar.Header{Name: "skill/SKILL.md", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg})
	_, _ = tarWriter.Write([]byte("test"))
	_ = tarWriter.WriteHeader(&tar.Header{Name: "skill/link", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_ = tarWriter.Close()
	_ = gzipWriter.Close()
	_ = file.Close()
	if _, err := extractArchive(archive, t.TempDir(), archiveLimits{MaxFiles: 10, MaxBytes: 1024 * 1024}); err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("expected tar symlink rejection, got %v", err)
	}
}
