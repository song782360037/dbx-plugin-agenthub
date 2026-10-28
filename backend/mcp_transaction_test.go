package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPFailedTargetLeavesFilesAndRecordUnchanged(t *testing.T) {
	m, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".qoder", "settings.json")
	mustWrite(t, path, `{"theme":"dark","mcpServers":{}}`)
	input := MCPInput{Name: "demo", Transport: "stdio", Command: "old", Targets: []string{"qoder"}, Confirmed: true}
	if _, err := m.saveMCP(input); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	stateBefore, _ := os.ReadFile(filepath.Join(m.dataDir, stateFile))
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent", "mcp.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	input.Command, input.Targets = "new", []string{"qoder", "pi"}
	if _, err := m.saveMCP(input); err == nil {
		t.Fatal("expected blocked target failure")
	}
	after, _ := os.ReadFile(path)
	stateAfter, _ := os.ReadFile(filepath.Join(m.dataDir, stateFile))
	if !bytes.Equal(before, after) || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatal("failed save changed existing files or state")
	}
	if m.mcpRecordsSnapshot()["demo"].Config.Command != "old" {
		t.Fatal("in-memory record partially committed")
	}
}

func TestMCPTransactionRollsBackOnStateFailure(t *testing.T) {
	m, _ := newTestManager(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	before := `{"theme":"dark","mcpServers":{"demo":{"command":"old"}}}`
	mustWrite(t, path, before)
	adapter := mcpAdapter{ID: "qoder", Name: "Qoder", Kind: "qoder", Path: path, Format: "json", Detected: true}
	change, err := m.prepareMCPChange(adapter, "demo", MCPServerConfig{Transport: "stdio", Command: "new"}, false)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("state persistence unavailable")
	if err := m.commitMCPChanges([]*mcpFileChange{change}, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("unexpected failure: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != before {
		t.Fatal("original file not restored byte for byte")
	}
}

func TestMCPTransactionRejectsConcurrentFileEdit(t *testing.T) {
	m, _ := newTestManager(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	mustWrite(t, path, `{"mcpServers":{"demo":{"command":"old"}}}`)
	adapter := mcpAdapter{ID: "qoder", Name: "Qoder", Kind: "qoder", Path: path, Format: "json", Detected: true}
	change, err := m.prepareMCPChange(adapter, "demo", MCPServerConfig{Transport: "stdio", Command: "new"}, false)
	if err != nil {
		t.Fatal(err)
	}
	local := `{"mcpServers":{"demo":{"command":"external-edit"}}}`
	mustWrite(t, path, local)
	called := false
	err = m.commitMCPChanges([]*mcpFileChange{change}, func() error { called = true; return nil })
	if err == nil || called {
		t.Fatal("concurrent edit was not rejected")
	}
	after, _ := os.ReadFile(path)
	if string(after) != local {
		t.Fatal("concurrent edit was overwritten")
	}
}

func TestMCPDisabledAndInvalidEntriesNeedOverwrite(t *testing.T) {
	for _, entry := range []string{`{"command":"old","disabled":true}`, `{"command":"old","enabled":false}`, `{"privateOnly":true}`} {
		t.Run(entry, func(t *testing.T) {
			m, _ := newTestManager(t)
			home, _ := os.UserHomeDir()
			path := filepath.Join(home, ".qoder", "settings.json")
			before := `{"mcpServers":{"demo":` + entry + `}}`
			mustWrite(t, path, before)
			if _, err := m.saveMCP(MCPInput{Name: "demo", Transport: "stdio", Command: "new", Targets: []string{"qoder"}, Confirmed: true}); err == nil {
				t.Fatal("untracked entry overwritten")
			}
			after, _ := os.ReadFile(path)
			if string(after) != before {
				t.Fatal("configuration changed on rejected save")
			}
		})
	}
}

func TestMCPPrivateFieldDriftRequiresConfirmation(t *testing.T) {
	m, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	mustWrite(t, path, `{"mcpServers":{"demo":{"command":"node","trust":false,"includeTools":["read"]}}}`)
	if _, err := m.adoptMCP("gemini-cli", "demo"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	local := strings.Replace(string(before), `"trust":false`, `"trust":true`, 1)
	mustWrite(t, path, local)
	input := MCPInput{Name: "demo", Transport: "stdio", Command: "node", Targets: []string{"gemini-cli"}, Confirmed: true}
	if _, err := m.saveMCP(input); err == nil {
		t.Fatal("private field drift ignored")
	}
	if _, err := m.removeMCP("demo", true); err == nil {
		t.Fatal("private field drift ignored on delete")
	}
	if _, err := m.setMCPTargetEnabled("demo", "gemini-cli", false, true); err == nil {
		t.Fatal("private field drift ignored on disable")
	}
	input.Overwrite = true
	if _, err := m.saveMCP(input); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	var compact bytes.Buffer
	if err := json.Compact(&compact, after); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compact.String(), `"trust":true`) || !strings.Contains(compact.String(), "includeTools") {
		t.Fatal("private fields lost after confirmed save")
	}
}

func TestMCPCodexMaskedResave(t *testing.T) {
	m, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), "model = \"demo\"\n")
	input := MCPInput{Name: "demo", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer ${DEMO_TOKEN}"}, Targets: []string{"codex"}, Confirmed: true}
	if _, err := m.saveMCP(input); err != nil {
		t.Fatal(err)
	}
	detail, err := m.mcpDetail("demo", "")
	if err != nil {
		t.Fatal(err)
	}
	input.Headers = detail.Config.Headers
	if _, err := m.saveMCP(input); err != nil {
		t.Fatalf("masked resave failed: %v", err)
	}
	if m.mcpRecordsSnapshot()["demo"].Config.Headers["Authorization"] != "Bearer ${DEMO_TOKEN}" {
		t.Fatal("masked value replaced original")
	}
}

func TestMCPArgumentsAndNewMaskValidation(t *testing.T) {
	m, _ := newTestManager(t)
	input := MCPInput{Name: "demo", Transport: "stdio", Command: "node", Args: []string{"", " spaced "}, Confirmed: true}
	if _, err := m.saveMCP(input); err != nil {
		t.Fatal(err)
	}
	args := m.mcpRecordsSnapshot()["demo"].Config.Args
	if len(args) != 2 || args[0] != "" || args[1] != " spaced " {
		t.Fatal("arguments changed")
	}
	input.Env = map[string]string{"NEW": mcpMaskedSecret}
	if _, err := m.saveMCP(input); err == nil {
		t.Fatal("new masked value accepted without original secret")
	}
}

func TestMCPConfigRelocationDoesNotGrantOwnership(t *testing.T) {
	m, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	original := filepath.Join(home, ".codex", "config.toml")
	mustWrite(t, original, "model = 'demo'\n")
	input := MCPInput{Name: "demo", Transport: "stdio", Command: "node", Targets: []string{"codex"}, Confirmed: true}
	if _, err := m.saveMCP(input); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(original)
	newHome := t.TempDir()
	moved := filepath.Join(newHome, "config.toml")
	mustWrite(t, moved, string(before))
	t.Setenv("CODEX_HOME", newHome)
	if _, err := m.removeMCP("demo", true); err == nil {
		t.Fatal("relocation granted delete ownership")
	}
	if _, err := m.setMCPTargetEnabled("demo", "codex", false, true); err == nil {
		t.Fatal("relocation granted disable ownership")
	}
	input.Command = "new"
	if _, err := m.saveMCP(input); err == nil {
		t.Fatal("relocation granted update ownership")
	}
	for _, file := range []string{original, moved} {
		after, _ := os.ReadFile(file)
		if !bytes.Equal(before, after) {
			t.Fatal("relocation modified a config")
		}
	}
	if err := os.Remove(moved); err != nil {
		t.Fatal(err)
	}
	if _, err := m.removeMCP("demo", true); err == nil {
		t.Fatal("missing relocated config discarded original managed target")
	}
}

func TestMCPManagedDetailRejectsAlternateSecretSource(t *testing.T) {
	m, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".qoder", "settings.json")
	mustWrite(t, path, `{"mcpServers":{"demo":{"command":"node","env":{"MODE":"central"}}}}`)
	if _, err := m.adoptMCP("qoder", "demo"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, `{"mcpServers":{"demo":{"command":"node","env":{"MODE":"client"}}}}`)
	if _, err := m.mcpDetail("demo", "qoder"); err == nil {
		t.Fatal("editable detail mixed central and alternate source")
	}
}

func TestMCPCodexMultilineQuoteRunsAndBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	text := "model = \"x\"\r\n\r\n[mcp_servers.other]\r\ncommand = 'node'\r\nargs = [\"\"\"a\"\"\"\"]\r\n\r\n[other]\r\nvalue = 'untouched'\r\n"
	mustWrite(t, path, text)
	config := MCPServerConfig{Transport: "stdio", Command: "npx", Env: map[string]string{"K": "v"}}
	if err := applyCodexMCP(path, "demo", config, false); err != nil {
		t.Fatalf("save beside four-quote ending failed: %v", err)
	}
	document, err := readCodexDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if document["model"] != "x" || document["other"] == nil {
		t.Fatal("unrelated config changed")
	}
	if _, exists := document["mcp_servers"].(map[string]any)["demo"]; !exists {
		t.Fatal("new server missing")
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte("\r\n")) {
		t.Fatal("CRLF line endings were not preserved")
	}
	if err := applyCodexMCP(path, "other", MCPServerConfig{}, true); err != nil {
		t.Fatalf("remove beside four-quote ending failed: %v", err)
	}
	document, err = readCodexDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := document["mcp_servers"].(map[string]any)["other"]; exists || document["model"] != "x" {
		t.Fatal("removal damaged unrelated config")
	}

	bomPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(bomPath, append([]byte("\xef\xbb\xbf"), []byte("model = 'bom'\n[mcp_servers.existing]\ncommand = 'node'\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _, err := readCodexMCP(mcpAdapter{ID: "codex", Name: "Codex", Kind: "codex", Path: bomPath, Format: "toml"})
	if err != nil || entries["existing"].Config.Command != "node" {
		t.Fatalf("BOM config could not be read: %v %#v", err, entries)
	}
	if err := applyCodexMCP(bomPath, "added", config, false); err != nil {
		t.Fatalf("BOM config could not be written: %v", err)
	}
	document, err = readCodexDocument(bomPath)
	if err != nil || document["model"] != "bom" {
		t.Fatalf("BOM config damaged: %v %#v", err, document)
	}
}

func TestMCPCodexInlineSectionKeepsLaterRootKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	mustWrite(t, path, "mcp_servers = { other = { command = 'node' } }\nmodel = 'gpt-5'\n")
	config := MCPServerConfig{Transport: "stdio", Command: "npx"}
	if err := applyCodexMCP(path, "demo", config, false); err != nil {
		t.Fatalf("inline update failed: %v", err)
	}
	document, err := readCodexDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if document["model"] != "gpt-5" {
		t.Fatal("root key after inline table was captured by a new table")
	}
	servers := document["mcp_servers"].(map[string]any)
	if servers["other"] == nil || servers["demo"] == nil {
		t.Fatalf("servers = %#v", servers)
	}
	if err := applyCodexMCP(path, "other", MCPServerConfig{}, true); err != nil {
		t.Fatalf("inline removal failed: %v", err)
	}
	document, err = readCodexDocument(path)
	if err != nil || document["model"] != "gpt-5" || document["mcp_servers"].(map[string]any)["other"] != nil {
		t.Fatalf("inline removal damaged config: %v %#v", err, document)
	}
}

func TestMCPDetailUsesExplicitConflictSource(t *testing.T) {
	m, _ := newTestManager(t)
	home, _ := os.UserHomeDir()
	mustWrite(t, filepath.Join(home, ".qoder", "settings.json"), `{"mcpServers":{"demo":{"command":"qoder-command"}}}`)
	mustWrite(t, filepath.Join(home, ".gemini", "settings.json"), `{"mcpServers":{"demo":{"command":"gemini-command"}}}`)
	for id, command := range map[string]string{"qoder": "qoder-command", "gemini-cli": "gemini-command"} {
		detail, err := m.mcpDetail("demo", id)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Config.Command != command || detail.View.Command != command {
			t.Fatal("detail mixed different client sources")
		}
	}
}
