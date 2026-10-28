package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPHTTPTypeMapping(t *testing.T) {
	manager, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	mustWrite(t, filepath.Join(home, ".qoder", "settings.json"), `{"mcpServers":{}}`)
	mustWrite(t, filepath.Join(home, ".gemini", "settings.json"), `{"mcpServers":{}}`)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), `model = "x"`+"\n")

	if _, err := manager.saveMCP(MCPInput{
		Name: "remote", Transport: "http", URL: "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer token"},
		Targets: []string{"qoder", "gemini-cli"}, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	var qoder map[string]any
	qoderData, _ := os.ReadFile(filepath.Join(home, ".qoder", "settings.json"))
	_ = json.Unmarshal(qoderData, &qoder)
	qoderEntry := qoder["mcpServers"].(map[string]any)["remote"].(map[string]any)
	if qoderEntry["type"] != "streamable-http" || qoderEntry["url"] != "https://example.com/mcp" {
		t.Fatalf("qoder entry = %#v", qoderEntry)
	}
	var gemini map[string]any
	geminiData, _ := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	_ = json.Unmarshal(geminiData, &gemini)
	geminiEntry := gemini["mcpServers"].(map[string]any)["remote"].(map[string]any)
	if geminiEntry["httpUrl"] != "https://example.com/mcp" || geminiEntry["url"] != nil {
		t.Fatalf("gemini entry = %#v", geminiEntry)
	}
	overview := manager.mcpOverview()
	server := findMCPServer(overview, "remote")
	if server == nil || server.Status != "synced" || server.Transport != "http" {
		t.Fatalf("overview = %#v", server)
	}

	if _, err := manager.saveMCP(MCPInput{
		Name: "codex-headers", Transport: "http", URL: "https://example.com/mcp",
		Headers: map[string]string{"X-Token": "value"}, Targets: []string{"codex"}, Confirmed: true,
	}); err != nil {
		t.Fatalf("codex supports custom headers: %v", err)
	}
	codexHeaders, _, err := readCodexMCP(mcpAdapter{ID: "codex", Name: "Codex", Kind: "codex", Path: filepath.Join(home, ".codex", "config.toml"), Format: "toml"})
	if err != nil {
		t.Fatal(err)
	}
	if codexHeaders["codex-headers"].Config.Headers["X-Token"] != "value" {
		t.Fatalf("codex custom header lost: %#v", codexHeaders["codex-headers"].Config)
	}
	if _, err := manager.saveMCP(MCPInput{
		Name: "codex-http", Transport: "http", URL: "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${CODEX_TOKEN}"},
		Targets: []string{"codex"}, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	codexData, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(codexData), "bearer_token_env_var") {
		t.Fatalf("codex bearer config missing:\n%s", codexData)
	}
	bearerEntries, _, err := readCodexMCP(mcpAdapter{ID: "codex", Name: "Codex", Kind: "codex", Path: filepath.Join(home, ".codex", "config.toml"), Format: "toml"})
	if err != nil {
		t.Fatal(err)
	}
	if bearerEntries["codex-http"].Config.Headers["Authorization"] != "Bearer ${CODEX_TOKEN}" {
		t.Fatalf("codex bearer header lost: %#v", bearerEntries["codex-http"].Config)
	}
	overview = manager.mcpOverview()
	if server := findMCPServer(overview, "codex-http"); server == nil || server.Status != "synced" {
		t.Fatalf("codex http overview = %#v", server)
	}
	if _, err := manager.saveMCP(MCPInput{
		Name: "blocked-sse", Transport: "sse", URL: "https://example.com/sse", Targets: []string{"codex"}, Confirmed: true,
	}); err == nil {
		t.Fatal("codex should reject sse")
	}
}
