package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPQoderJSONRoundTrip(t *testing.T) {
	manager, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	qoderDir := filepath.Join(home, ".qoder")
	settingsPath := filepath.Join(qoderDir, "settings.json")
	mustWrite(t, settingsPath, `{"theme":"dark","mcpServers":{}}`)

	result, err := manager.saveMCP(MCPInput{
		Name: "demo-mcp", Transport: "stdio", Command: "npx",
		Args:    []string{"-y", "demo-mcp@latest"},
		Env:     map[string]string{"DEMO_TOKEN": "secret-value"},
		Targets: []string{"qoder"}, Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result["message"].(string), "demo-mcp") {
		t.Fatalf("message = %v", result)
	}
	data, _ := os.ReadFile(settingsPath)
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["theme"] != "dark" {
		t.Fatal("unrelated settings were lost")
	}
	servers := parsed["mcpServers"].(map[string]any)
	entry := servers["demo-mcp"].(map[string]any)
	if entry["type"] != "stdio" || entry["command"] != "npx" {
		t.Fatalf("entry = %#v", entry)
	}
	env := entry["env"].(map[string]any)
	if env["DEMO_TOKEN"] != "secret-value" {
		t.Fatalf("secret not written: %#v", env)
	}

	detail, err := manager.mcpDetail("demo-mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Config.Env["DEMO_TOKEN"] != mcpMaskedSecret {
		t.Fatalf("detail did not mask secret: %#v", detail.Config.Env)
	}
	overview := manager.mcpOverview()
	server := findMCPServer(overview, "demo-mcp")
	if server == nil || server.Status != "synced" || !server.Managed {
		t.Fatalf("overview = %#v", server)
	}

	// Saving with the masked value must preserve the real secret.
	if _, err := manager.saveMCP(MCPInput{
		OriginalName: "demo-mcp", Name: "demo-mcp", Transport: "stdio", Command: "npx",
		Args:    []string{"-y", "demo-mcp@latest"},
		Env:     map[string]string{"DEMO_TOKEN": mcpMaskedSecret},
		Targets: []string{"qoder"}, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(settingsPath)
	if !strings.Contains(string(data), "secret-value") {
		t.Fatal("masked secret was overwritten instead of preserved")
	}

	// Local edit should be reported as modified.
	original, _ := os.ReadFile(settingsPath)
	mustWrite(t, settingsPath, strings.Replace(string(original), `"npx"`, `"uvx"`, 1))
	overview = manager.mcpOverview()
	server = findMCPServer(overview, "demo-mcp")
	if server == nil || server.Status != "modified" {
		t.Fatalf("status after local edit = %#v", server)
	}
	// Restore the tracked config before removing it.
	if err := os.WriteFile(settingsPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.removeMCP("demo-mcp", true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(settingsPath)
	if strings.Contains(string(data), "demo-mcp") {
		t.Fatal("server not removed from qoder")
	}
	if !strings.Contains(string(data), `"theme"`) {
		t.Fatal("unrelated settings lost after removal")
	}
	backups, _ := os.ReadDir(filepath.Join(manager.dataDir, "backups", "mcp"))
	if len(backups) == 0 {
		t.Fatal("config backups were not created")
	}
}

func TestMCPCodexTOMLRoundTrip(t *testing.T) {
	manager, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, ".codex", "config.toml")
	mustWrite(t, configPath, `model = "gpt-5"

# keep this comment
[mcp_servers.existing]
command = "node"
args = ["server.js"]

[other]
enabled = true
`)
	_, err := manager.saveMCP(MCPInput{
		Name: "codex-demo", Transport: "stdio", Command: "npx",
		Args: []string{"-y", "codex-demo"}, Env: map[string]string{"TOKEN": "abc"},
		TimeoutMS: 30000, Targets: []string{"codex"}, Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(configPath)
	text := string(data)
	for _, expected := range []string{"# keep this comment", "[mcp_servers.existing]", "[other]"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in:\n%s", expected, text)
		}
	}
	servers, _, err := readCodexMCP(mcpAdapter{ID: "codex", Name: "Codex", Kind: "codex", Path: configPath, Format: "toml"})
	if err != nil {
		t.Fatal(err)
	}
	saved := servers["codex-demo"]
	if saved.Config.Command != "npx" || saved.Config.Env["TOKEN"] != "abc" || saved.Config.TimeoutMS != 30000 {
		t.Fatalf("codex roundtrip = %#v", saved.Config)
	}
	overview := manager.mcpOverview()
	if findMCPServer(overview, "existing") == nil || findMCPServer(overview, "codex-demo") == nil {
		t.Fatalf("codex servers not scanned: %#v", overview.Servers)
	}
	if server := findMCPServer(overview, "codex-demo"); server == nil || server.Status != "synced" {
		t.Fatalf("codex status = %#v", server)
	}
	if _, err := manager.removeMCP("codex-demo", true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(configPath)
	text = string(data)
	if strings.Contains(text, "codex-demo") {
		t.Fatal("codex block not removed")
	}
	if !strings.Contains(text, "# keep this comment") || !strings.Contains(text, "[mcp_servers.existing]") || !strings.Contains(text, "[other]") {
		t.Fatalf("unrelated codex config damaged:\n%s", text)
	}
}

func TestMCPConflictAndOverwrite(t *testing.T) {
	manager, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".qoder", "settings.json")
	mustWrite(t, settingsPath, `{"mcpServers":{"conflict":{"command":"old"}}}`)
	_, err := manager.saveMCP(MCPInput{
		Name: "conflict", Transport: "stdio", Command: "new", Targets: []string{"qoder"}, Confirmed: true,
	})
	if err == nil {
		t.Fatal("expected conflict without overwrite")
	}
	_, err = manager.saveMCP(MCPInput{
		Name: "conflict", Transport: "stdio", Command: "new", Targets: []string{"qoder"},
		Overwrite: true, Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(settingsPath)
	if !strings.Contains(string(data), `"new"`) {
		t.Fatalf("config not overwritten: %s", data)
	}
}

func TestMCPImportJSONMultiple(t *testing.T) {
	manager, _ := newTestManager(t)
	result, err := manager.importMCPJSON(`{
	  "mcpServers": {
	    "stdio-one": {"command": "npx", "args": ["-y", "one"]},
	    "http-one": {"type": "streamable-http", "url": "https://example.com/mcp", "headers": {"Authorization": "Bearer x"}}
	  }
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Servers) != 2 {
		t.Fatalf("servers = %#v", result.Servers)
	}
	byName := map[string]MCPImportServer{}
	for _, server := range result.Servers {
		byName[server.Name] = server
	}
	if byName["stdio-one"].Config.Transport != "stdio" {
		t.Fatalf("stdio transport = %#v", byName["stdio-one"])
	}
	if byName["http-one"].Config.Transport != "http" || byName["http-one"].Config.URL != "https://example.com/mcp" {
		t.Fatalf("http transport = %#v", byName["http-one"])
	}
}

func TestMCPGeminiHTTPURLAndPiSSE(t *testing.T) {
	manager, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	mustWrite(t, filepath.Join(home, ".gemini", "settings.json"), `{"mcpServers":{"remote":{"httpUrl":"https://example.com/mcp","headers":{"X":"y"}}}}`)
	overview := manager.mcpOverview()
	server := findMCPServer(overview, "remote")
	if server == nil || server.Transport != "http" || server.URL != "https://example.com/mcp" {
		t.Fatalf("gemini server = %#v", server)
	}
	pi := mcpAdapter{Kind: "pi", Name: "Pi"}
	if err := adapterValidate(pi, MCPServerConfig{Transport: "sse", URL: "https://example.com/sse"}); err == nil {
		t.Fatal("pi should reject sse")
	}
}

func TestMCPAdopt(t *testing.T) {
	manager, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	mustWrite(t, filepath.Join(home, ".workbuddy", "mcp.json"), `{"mcpServers":{"adopt-me":{"type":"stdio","command":"node","args":["a.js"]}}}`)
	if _, err := manager.adoptMCP("workbuddy", "adopt-me"); err != nil {
		t.Fatal(err)
	}
	overview := manager.mcpOverview()
	server := findMCPServer(overview, "adopt-me")
	if server == nil || !server.Managed || server.Status != "synced" {
		t.Fatalf("adopted server = %#v", server)
	}
	if _, err := manager.removeMCP("adopt-me", true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".workbuddy", "mcp.json"))
	if strings.Contains(string(data), "adopt-me") {
		t.Fatal("adopted server not removed from client")
	}
}

func findMCPServer(overview MCPOverview, name string) *MCPView {
	for index := range overview.Servers {
		if overview.Servers[index].Name == name {
			return &overview.Servers[index]
		}
	}
	return nil
}
