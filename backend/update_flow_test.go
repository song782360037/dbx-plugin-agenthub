package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubAutoUpdateFlow(t *testing.T) {
	repo := newSkillsRepo(t)
	manager, agentDir := newTestManager(t)
	installSkillsRepo(t, manager, "copy", "")
	record := manager.state.Skills["update-skill"]
	if record.Source.ResolvedRef != "v1" || record.Source.Commit != skillsSHA1 {
		t.Fatalf("source = %#v", record.Source)
	}
	target := filepath.Join(agentDir, "update-skill", "SKILL.md")
	if content := readSkillsFile(t, target); !strings.Contains(string(content), "version one") {
		t.Fatalf("initial content = %q", content)
	}

	repo.tag = "v2"
	repo.refs["v2"] = skillsSHA2
	repo.contents[skillsSHA2] = skillsDocument("version two\n")
	results, err := manager.checkUpdates([]string{"update-skill"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != "update" {
		t.Fatalf("check results = %#v", results)
	}
	if _, err := manager.updateSkills(skillsUpdateInput(t, manager)); err != nil {
		t.Fatal(err)
	}
	if content := readSkillsFile(t, target); !strings.Contains(string(content), "version two") {
		t.Fatalf("updated content = %q", content)
	}
	record = manager.state.Skills["update-skill"]
	if record.Source.ResolvedRef != "v2" || record.Source.Commit != skillsSHA2 || record.Available != nil {
		t.Fatalf("record = %#v", record.Source)
	}
	results, err = manager.checkUpdates([]string{"update-skill"}, true)
	if err != nil || results[0].Status != "current" {
		t.Fatalf("post-update check = %#v, %v", results, err)
	}
}
