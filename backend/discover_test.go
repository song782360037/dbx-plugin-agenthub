package main

import (
	"path/filepath"
	"testing"
)

func TestDiscoverSkillsWithPluginManifestAndShadowing(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skills/alpha", "alpha", "Alpha skill")
	writeSkill(t, root, "skills/alpha/nested", "nested", "Nested skill")
	writeSkill(t, root, "skills/beta", "beta", "Beta skill")
	mustWrite(t, filepath.Join(root, ".claude-plugin", "plugin.json"), `{"name":"demo-plugin","version":"1.2.3","skills":"./skills/"}`)

	candidates, warnings, err := discoverSkills(root, "", defaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	names := map[string]Candidate{}
	for _, candidate := range candidates {
		names[candidate.Name] = candidate
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 skills (nested shadowed), got %#v", names)
	}
	if _, exists := names["nested"]; exists {
		t.Fatal("nested skill should be shadowed by parent SKILL.md")
	}
	if names["alpha"].Plugin == nil || names["alpha"].Plugin.Kind != "claude" {
		t.Fatalf("plugin info missing: %#v", names["alpha"].Plugin)
	}
	if names["alpha"].RelPath != "skills/alpha" {
		t.Fatalf("rel path = %q", names["alpha"].RelPath)
	}
}

func TestDiscoverSkillsScope(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skills/alpha", "alpha", "Alpha")
	writeSkill(t, root, "skills/beta", "beta", "Beta")
	candidates, _, err := discoverSkills(root, "skills/beta", defaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Name != "beta" {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0].RelPath != "skills/beta" {
		t.Fatalf("rel = %q", candidates[0].RelPath)
	}
}

func TestDiscoverSkillsSingleFile(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "one", "one", "One")
	candidates, _, err := discoverSkills(root, "one/SKILL.md", defaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].RelPath != "one" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestDiscoverMarketplace(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "plugins/one/skills/one", "one", "One")
	mustWrite(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), `{
	  "name":"demo",
	  "plugins":[{"name":"one","source":"./plugins/one"}]
	}`)
	candidates, _, err := discoverSkills(root, "", defaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Name != "one" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func writeSkill(t *testing.T, root, rel, name, description string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, rel, "SKILL.md"), "---\nname: "+name+"\ndescription: "+description+"\n---\n# "+name+"\n")
}
