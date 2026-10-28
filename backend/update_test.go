package main

import (
	"path/filepath"
	"testing"
)

func TestCheckUpdatesRequiresRecordedCommit(t *testing.T) {
	manager, _ := newTestManager(t)
	manager.state.Skills["github-skill"] = &Record{
		Name: "github-skill", ContentHash: "hash",
		Source:  Source{Type: "github", Repo: "owner/repo", Ref: "main", Path: "skills/github-skill"},
		Targets: map[string]*TargetRecord{},
	}
	results, err := manager.checkUpdates([]string{"github-skill"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != "unsupported" {
		t.Fatalf("results = %#v", results)
	}
}

func TestSyncTargetAcceptsJunctionMode(t *testing.T) {
	manager, agentDir := newTestManager(t)
	source := t.TempDir()
	writeSkill(t, source, ".", "junction-skill", "Junction skill")
	preview, err := manager.previewSource(SourceInput{Type: "local", Value: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.install(InstallInput{
		PreviewID: preview.ID, Names: []string{"junction-skill"}, Targets: []string{"test-agent"}, Mode: "copy",
	}); err != nil {
		t.Fatal(err)
	}
	hub := filepath.Join(manager.hubDir, "junction-skill")
	agent, _ := findAgent(buildAgents(manager.settingsSnapshot()), "test-agent")
	target, _, err := manager.syncTarget(agent, "junction-skill", hub, "junction", true)
	if err != nil {
		t.Fatal(err)
	}
	if target.Mode != "link" && target.Mode != "junction" && target.Mode != "copy" {
		t.Fatalf("unexpected mode %q", target.Mode)
	}
	if !pathHasSkillMD(filepath.Join(agentDir, "junction-skill")) {
		t.Fatal("target unreadable")
	}
}
