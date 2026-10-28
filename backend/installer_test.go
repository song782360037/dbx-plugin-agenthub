package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallHubOnlyThenDispatchToClients(t *testing.T) {
	manager, agentDir := newTestManager(t)
	source := t.TempDir()
	writeSkill(t, source, ".", "hub-only-skill", "Hub only skill")
	preview, err := manager.previewSource(SourceInput{Type: "local", Value: source})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.install(InstallInput{PreviewID: preview.ID, Names: []string{"hub-only-skill"}, Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Targets != 0 {
		t.Fatalf("hub-only install synced targets: %#v", result)
	}
	overview := buildTestOverview(manager)
	if len(overview.Skills) != 1 || overview.Skills[0].Status != "hub_only" {
		t.Fatalf("overview = %#v", overview.Skills)
	}
	if !fileExists(filepath.Join(manager.hubDir, "hub-only-skill", "SKILL.md")) {
		t.Fatal("hub copy missing")
	}
	if _, err := manager.setTargetEnabled("hub-only-skill", "test-agent", true, "copy", false); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(agentDir, "hub-only-skill", "SKILL.md")) {
		t.Fatal("dispatch did not create the client copy")
	}
	overview = buildTestOverview(manager)
	if overview.Skills[0].Status != "synced" {
		t.Fatalf("status after dispatch = %s", overview.Skills[0].Status)
	}
}

func TestInstallCopyDriftAndRemove(t *testing.T) {
	manager, agentDir := newTestManager(t)
	source := t.TempDir()
	writeSkill(t, source, ".", "demo-skill", "Demo skill")
	mustWrite(t, filepath.Join(source, "scripts", "run.sh"), "#!/bin/sh\necho ok\n")

	preview, err := manager.previewSource(SourceInput{Type: "local", Value: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Candidates) != 1 {
		t.Fatalf("candidates = %#v", preview.Candidates)
	}
	result, err := manager.install(InstallInput{
		PreviewID: preview.ID, Names: []string{"demo-skill"}, Targets: []string{"test-agent"}, Mode: "copy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Installed) != 1 || result.Targets != 1 {
		t.Fatalf("result = %#v", result)
	}
	hub := filepath.Join(manager.hubDir, "demo-skill")
	target := filepath.Join(agentDir, "demo-skill")
	for _, path := range []string{filepath.Join(hub, "SKILL.md"), filepath.Join(target, "SKILL.md"), filepath.Join(target, markerName)} {
		if !fileExists(path) {
			t.Fatalf("missing %s", path)
		}
	}
	var marker Marker
	data, _ := os.ReadFile(filepath.Join(target, markerName))
	if json.Unmarshal(data, &marker) != nil || marker.ManagedBy != appID || marker.Skill != "demo-skill" {
		t.Fatalf("marker = %#v", marker)
	}

	overview := buildTestOverview(manager)
	if len(overview.Skills) != 1 || overview.Skills[0].Status != "synced" {
		t.Fatalf("overview = %#v", overview.Skills)
	}

	mustWrite(t, filepath.Join(target, "SKILL.md"), "---\nname: demo-skill\ndescription: locally changed\n---\n")
	overview = buildTestOverview(manager)
	if overview.Skills[0].Status != "modified" {
		t.Fatalf("expected modified, got %s", overview.Skills[0].Status)
	}

	if _, err := manager.removeSkill("demo-skill", false); err == nil {
		t.Fatal("remove without confirmation should fail")
	}
	if _, err := manager.removeSkill("demo-skill", true); err != nil {
		t.Fatal(err)
	}
	if pathExistsLstat(target) || pathExistsLstat(hub) {
		t.Fatal("target or hub still exists after removal")
	}
	if _, err := os.Stat(filepath.Join(manager.trashDir)); err != nil {
		t.Fatal("trash directory missing")
	}
}

func TestInstallRefusesUntrackedConflict(t *testing.T) {
	manager, agentDir := newTestManager(t)
	source := t.TempDir()
	writeSkill(t, source, ".", "conflict-skill", "New skill")
	untracked := filepath.Join(agentDir, "conflict-skill")
	writeSkill(t, agentDir, "conflict-skill", "conflict-skill", "Existing untracked")
	preview, err := manager.previewSource(SourceInput{Type: "local", Value: source})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.install(InstallInput{
		PreviewID: preview.ID, Names: []string{"conflict-skill"}, Targets: []string{"test-agent"}, Mode: "copy",
	})
	if err == nil {
		t.Fatal("expected conflict without overwrite")
	}
	result, err := manager.install(InstallInput{
		PreviewID: preview.ID, Names: []string{"conflict-skill"}, Targets: []string{"test-agent"}, Mode: "copy", Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Installed) != 1 {
		t.Fatalf("result = %#v", result)
	}
	content, _ := os.ReadFile(filepath.Join(untracked, "SKILL.md"))
	if string(content) == "---\nname: conflict-skill\ndescription: Existing untracked\n---\n# conflict-skill\n" {
		t.Fatal("untracked target was not replaced")
	}
}

func TestAdoptKeepsExternalCopy(t *testing.T) {
	manager, agentDir := newTestManager(t)
	writeSkill(t, agentDir, "adopted-skill", "adopted-skill", "Adopted")
	if _, err := manager.adopt("test-agent", "adopted-skill", false); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	record := manager.state.Skills["adopted-skill"]
	manager.mu.Unlock()
	if record == nil {
		t.Fatal("record missing")
	}
	if record.Targets["test-agent"] == nil || record.Targets["test-agent"].Mode != "external" {
		t.Fatalf("target = %#v", record.Targets)
	}
	if !pathHasSkillMD(filepath.Join(manager.hubDir, "adopted-skill")) {
		t.Fatal("hub copy missing")
	}
	overview := buildTestOverview(manager)
	if len(overview.Skills) != 1 || overview.Skills[0].Status != "synced" {
		t.Fatalf("overview = %#v", overview.Skills)
	}
	if _, err := manager.removeSkill("adopted-skill", true); err != nil {
		t.Fatal(err)
	}
	if !pathHasSkillMD(filepath.Join(agentDir, "adopted-skill")) {
		t.Fatal("external adopted copy must be preserved on removal")
	}
}

func TestLinkOrCopyFallback(t *testing.T) {
	manager, agentDir := newTestManager(t)
	source := t.TempDir()
	writeSkill(t, source, ".", "link-skill", "Link skill")
	preview, err := manager.previewSource(SourceInput{Type: "local", Value: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.install(InstallInput{
		PreviewID: preview.ID, Names: []string{"link-skill"}, Targets: []string{"test-agent"}, Mode: "auto",
	}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(agentDir, "link-skill")
	if !pathHasSkillMD(target) {
		t.Fatal("target must be readable")
	}
	overview := buildTestOverview(manager)
	if len(overview.Skills) != 1 || overview.Skills[0].Status != "synced" {
		t.Fatalf("overview = %#v", overview.Skills)
	}
	mode := overview.Skills[0].Targets[0].Mode
	if mode != "link" && mode != "junction" && mode != "copy" {
		t.Fatalf("mode = %q", mode)
	}
}

func TestLegacyMarkerCanBeAdopted(t *testing.T) {
	manager, agentDir := newTestManager(t)
	writeSkill(t, agentDir, "legacy-skill", "legacy-skill", "Legacy")
	mustWrite(t, filepath.Join(agentDir, "legacy-skill", legacyMarkerName), `{"managedBy":"`+legacyAppID+`","skill":"legacy-skill","contentHash":"x"}`)
	overview := buildTestOverview(manager)
	if len(overview.Skills) != 1 || overview.Skills[0].Targets[0].Managed != true {
		t.Fatalf("legacy marker not recognized: %#v", overview.Skills)
	}
	if _, err := manager.adopt("test-agent", "legacy-skill", false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.removeSkill("legacy-skill", true); err != nil {
		t.Fatal(err)
	}
}

func TestReadUntrackedFile(t *testing.T) {
	manager, agentDir := newTestManager(t)
	writeSkill(t, agentDir, "loose-skill", "loose-skill", "Loose")
	result, err := manager.readSkillFile("loose-skill", "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result["content"].(string), "Loose") {
		t.Fatalf("content = %#v", result)
	}
}

func TestRemoveSkillWithMissingTarget(t *testing.T) {
	manager, agentDir := newTestManager(t)
	source := t.TempDir()
	writeSkill(t, source, ".", "missing-target", "Missing target")
	preview, err := manager.previewSource(SourceInput{Type: "local", Value: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.install(InstallInput{
		PreviewID: preview.ID, Names: []string{"missing-target"}, Targets: []string{"test-agent"}, Mode: "copy",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(agentDir, "missing-target")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.removeSkill("missing-target", true); err != nil {
		t.Fatal(err)
	}
}

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	data := t.TempDir()
	t.Setenv("DBX_PLUGIN_DATA_DIR", data)
	t.Setenv("AGENT_HUB_DATA_DIR", data)
	manager, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(t.TempDir(), "agent-skills")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manager.state.Settings.CustomAgents = []CustomAgent{{ID: "test-agent", Name: "Test Agent", Path: agentDir}}
	return manager, agentDir
}

func buildTestOverview(manager *Manager) Overview {
	state := manager.stateSnapshot()
	agents := buildAgents(state.Settings)
	return buildOverview(&state, agents, manager.hubDir, nil)
}
