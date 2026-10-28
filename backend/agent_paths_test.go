package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Claude 的默认配置和显式配置目录使用不同的 MCP 文件位置。
func TestClaudeMCPProfilePaths(t *testing.T) {
	for _, profile := range []string{"default", ".claude", ".claude-work"} {
		t.Run(profile, func(t *testing.T) {
			manager, _ := newTestManager(t)
			home, _ := os.UserHomeDir()
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			configPath := filepath.Join(home, ".claude.json")
			otherPath := filepath.Join(home, ".claude", ".claude.json")
			if profile != "default" {
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, profile))
				configPath, otherPath = filepath.Join(home, profile, ".claude.json"), configPath
			}
			other := `{"mcpServers":{"other-profile":{"command":"other"}}}`
			mustWrite(t, otherPath, other)
			mustWrite(t, configPath, `{"mcpServers":{"active-profile":{"command":"active"}}}`)
			_, entries, warnings := manager.scanMCPAdapters()
			if _, found := entries["claude-code"]["active-profile"]; !found {
				t.Fatalf("active profile not scanned: %v", warnings)
			}
			if _, found := entries["claude-code"]["other-profile"]; found {
				t.Fatal("another profile was scanned")
			}
			if _, err := manager.saveMCP(MCPInput{Name: "new-server", Transport: "stdio", Command: "new", Targets: []string{"claude-code"}, Confirmed: true}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(configPath)
			if err != nil || !strings.Contains(string(data), "new-server") {
				t.Fatalf("active profile was not updated: %v", err)
			}
			data, err = os.ReadFile(otherPath)
			if err != nil || string(data) != other {
				t.Fatalf("unrelated profile changed: %v", err)
			}
		})
	}
}

// Pi 技能扫描、安装和 MCP 同步必须共享原生配置目录。
func TestPiNativePaths(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "default"
		if custom {
			name = "custom"
		}
		t.Run(name, func(t *testing.T) {
			manager, _ := newTestManager(t)
			home, _ := os.UserHomeDir()
			t.Setenv("PI_CODING_AGENT_DIR", "")
			base := filepath.Join(home, ".pi", "agent")
			other := filepath.Join(home, ".pi", "mcp.json")
			if custom {
				other = filepath.Join(base, "mcp.json")
				base = filepath.Join(home, "pi-work")
				t.Setenv("PI_CODING_AGENT_DIR", base)
			}
			untouched := `{"mcpServers":{"other-profile":{"command":"other"}}}`
			mustWrite(t, other, untouched)
			mustWrite(t, filepath.Join(base, "mcp.json"), `{"mcpServers":{"pi-server":{"command":"pi-command"}}}`)
			writeSkill(t, filepath.Join(base, "skills"), "pi-existing", "pi-existing", "Pi fixture")
			agent, found := findAgent(buildAgents(manager.settingsSnapshot()), "pi")
			if !found || agent.Path != filepath.Join(base, "skills") || !agent.Detected {
				t.Fatalf("Pi skill path = %#v", agent)
			}
			skills, warnings := scanAgent(agent)
			if len(skills) != 1 || skills[0].Name != "pi-existing" {
				t.Fatalf("Pi skills not scanned: %#v, %v", skills, warnings)
			}
			_, entries, warnings := manager.scanMCPAdapters()
			if _, found := entries["pi"]["pi-server"]; !found {
				t.Fatalf("Pi MCP not scanned: %v", warnings)
			}
			if _, err := manager.saveMCP(MCPInput{Name: "new-server", Transport: "stdio", Command: "new", Targets: []string{"pi"}, Confirmed: true}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(base, "mcp.json"))
			if err != nil || !strings.Contains(string(data), "new-server") {
				t.Fatalf("Pi MCP not written to native path: %v", err)
			}
			data, err = os.ReadFile(other)
			if err != nil || string(data) != untouched {
				t.Fatalf("unrelated Pi profile changed: %v", err)
			}
			source := t.TempDir()
			writeSkill(t, source, ".", "pi-installed", "Pi install fixture")
			preview, err := manager.previewSource(SourceInput{Type: "local", Value: source})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.install(InstallInput{PreviewID: preview.ID, Names: []string{"pi-installed"}, Targets: []string{"pi"}, Mode: "copy"}); err != nil {
				t.Fatal(err)
			}
			if !fileExists(filepath.Join(base, "skills", "pi-installed", "SKILL.md")) {
				t.Fatal("Pi skill not installed to native path")
			}
		})
	}
}
