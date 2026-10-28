package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Backup fallback must retain all files, including directories excluded from skill imports.
func TestTrashFallbackPreservesCompleteDirectory(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	files := []string{"SKILL.md", ".git/config", ".vscode/settings.json", ".venv/local", "node_modules/local/index.js", markerName}
	for _, rel := range files {
		mustWrite(t, filepath.Join(src, filepath.FromSlash(rel)), "original:"+rel)
	}
	// A missing destination parent makes Rename fail on every platform, exercising the copy fallback.
	m := &Manager{trashDir: filepath.Join(t.TempDir(), "trash")}
	dest, err := m.moveToTrash(src, "complete-backup")
	if err != nil {
		t.Fatal(err)
	}
	if pathExistsLstat(src) {
		t.Fatal("source still exists after successful backup")
	}
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil || string(data) != "original:"+rel {
			t.Errorf("backup lost or changed %s: %v", rel, err)
		}
	}
}

// A failed backup must leave the original directory available.
func TestTrashFallbackFailurePreservesSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	mustWrite(t, filepath.Join(src, ".git", "config"), "original")
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	mustWrite(t, blocked, "block")
	m := &Manager{trashDir: blocked}
	if _, err := m.moveToTrash(src, "failed-backup"); err == nil {
		t.Fatal("backup unexpectedly succeeded")
	}
	data, err := os.ReadFile(filepath.Join(src, ".git", "config"))
	if err != nil || string(data) != "original" {
		t.Fatalf("failed backup damaged source: %v", err)
	}
}

// Adoption and later lifecycle operations must retain the actual source directory name.
func TestAdoptDifferentDirectoryNameLifecycle(t *testing.T) {
	m, agentDir := newTestManager(t)
	writeSkill(t, agentDir, "old-folder", "canonical-skill", "Adoption fixture")
	writeSkill(t, agentDir, "canonical-skill", "unrelated-skill", "Keep this directory")
	unrelated := filepath.Join(agentDir, "canonical-skill", "SKILL.md")
	before, err := os.ReadFile(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.adopt("test-agent", "canonical-skill", true); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(agentDir, "old-folder")
	if target, linked := detectDirLink(actual); !linked || !samePath(target, filepath.Join(m.hubDir, "canonical-skill")) {
		t.Fatal("adoption did not replace the actual source with a hub link")
	}
	if got := m.stateSnapshot().Skills["canonical-skill"].Targets["test-agent"].Path; !samePath(got, actual) {
		t.Fatalf("recorded target = %s", got)
	}
	if _, err := m.setTargetEnabled("canonical-skill", "test-agent", false, "", true); err != nil {
		t.Fatal(err)
	}
	if pathExistsLstat(actual) {
		t.Fatal("disable left the adopted source active")
	}
	if _, err := m.setTargetEnabled("canonical-skill", "test-agent", true, "", false); err != nil {
		t.Fatal(err)
	}
	// Reinstall traverses the update preflight and target synchronization paths.
	source := t.TempDir()
	writeSkill(t, source, ".", "canonical-skill", "New version")
	preview, err := m.previewSource(SourceInput{Type: "local", Value: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.install(InstallInput{PreviewID: preview.ID, Names: []string{"canonical-skill"}, Targets: []string{"test-agent"}, Mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	if !pathHasSkillMD(actual) {
		t.Fatal("reinstall abandoned the actual target")
	}
	if _, err := m.removeSkill("canonical-skill", true); err != nil {
		t.Fatal(err)
	}
	if pathExistsLstat(actual) {
		t.Fatal("remove left the adopted skill active")
	}
	after, err := os.ReadFile(unrelated)
	if err != nil || string(after) != string(before) {
		t.Fatalf("lifecycle changed unrelated canonical directory: %v", err)
	}
}
