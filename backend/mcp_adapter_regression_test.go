package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// These adapter regressions invert the synthetic review reproductions: a pass
// means the bug is absent. No manager, real client home, or network is used.
func regressionMCPAdapter(t *testing.T, kind, text string) mcpAdapter {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("DBX_PLUGIN_DATA_DIR", filepath.Join(home, "data"))
	t.Setenv("AGENT_HUB_DATA_DIR", filepath.Join(home, "data"))
	format := "json"
	if kind == "codex" {
		format = "toml"
	}
	a := mcpAdapter{ID: kind, Kind: kind, Name: kind, AppDir: home, Path: filepath.Join(home, "config."+format), Format: format, Detected: true}
	regressionMCPWrite(t, a.Path, text)
	return a
}

func regressionMCPWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func regressionMCPText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func regressionMCPEntries(t *testing.T, a mcpAdapter) map[string]mcpEntry {
	t.Helper()
	entries, _, err := readMCPAdapter(a)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func regressionJSONServer(t *testing.T, a mcpAdapter, name string) map[string]json.RawMessage {
	t.Helper()
	top, err := decodeMCPJSONObject([]byte(regressionMCPText(t, a.Path)))
	if err != nil {
		t.Fatal(err)
	}
	servers, err := decodeMCPJSONObject(top["mcpServers"])
	if err != nil {
		t.Fatal(err)
	}
	entry, err := decodeMCPJSONObject(servers[name])
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestMCPAdapterRegressionJSONPreservesPrivateFields(t *testing.T) {
	a := regressionMCPAdapter(t, "gemini", `{"theme":{"large":9007199254740993},"mcpServers":{"other":null,"demo":{"command":"node","cwd":"private dir","trust":true,"includeTools":["read"],"excludeTools":["delete"],"enabled":false,"disabled":true,"vendor":{"token":"synthetic-only"}}}}`)
	before := regressionJSONServer(t, a, "demo")
	config := MCPServerConfig{Transport: "stdio", Command: "node", Args: []string{"", "  spaced  ", "a b"}, Env: map[string]string{"FIXTURE": "value"}}
	if err := applyJSONMCP(a, "demo", config, false); err != nil {
		t.Fatal(err)
	}
	after := regressionJSONServer(t, a, "demo")
	for _, key := range []string{"cwd", "trust", "includeTools", "excludeTools", "enabled", "disabled", "vendor"} {
		left, _ := jsonMCPRawHash(before[key])
		right, _ := jsonMCPRawHash(after[key])
		if left != right {
			t.Fatalf("private field %s changed", key)
		}
	}
	entries := regressionMCPEntries(t, a)
	if entries["demo"].Invalid || !entries["demo"].Disabled || !entries["other"].Invalid {
		t.Fatal("disabled or invalid entry existence was lost")
	}
	if !reflect.DeepEqual(entries["demo"].Config.Args, config.Args) {
		t.Fatal("empty/spaced arguments changed")
	}
	if !strings.Contains(regressionMCPText(t, a.Path), "9007199254740993") {
		t.Fatal("unrelated large number changed")
	}
	config = MCPServerConfig{Transport: "http", URL: "https://example.invalid/mcp", Headers: map[string]string{"X-Mode": "fixture"}, TimeoutMS: 1500}
	if err := applyJSONMCP(a, "demo", config, false); err != nil {
		t.Fatal(err)
	}
	after = regressionJSONServer(t, a, "demo")
	for _, key := range []string{"command", "args", "env", "environment", "url", "header"} {
		if _, exists := after[key]; exists {
			t.Fatalf("obsolete transport field %s remained", key)
		}
	}
	entry := regressionMCPEntries(t, a)["demo"]
	if !entry.Disabled || entry.Config.URL != config.URL || entry.ContentHash != mcpConfigHash(config) {
		t.Fatal("HTTP merge changed native enabled state or portable configuration")
	}
}

func TestMCPAdapterRegressionJSONOwnedAliases(t *testing.T) {
	a := regressionMCPAdapter(t, "cursor", `{"mcpServers":{"demo":{"type":"http","httpUrl":"https://example.invalid","url":"https://example.invalid","headers":{"X":"a"},"header":{"Y":"b"},"environment":{"OLD":"x"},"timeout":99,"enabled":true}}}`)
	if err := applyJSONMCP(a, "demo", MCPServerConfig{Transport: "stdio", Command: "new"}, false); err != nil {
		t.Fatal(err)
	}
	fields := regressionJSONServer(t, a, "demo")
	for _, key := range []string{"httpUrl", "url", "headers", "header", "environment"} {
		if _, exists := fields[key]; exists {
			t.Fatalf("stale alias %s remained", key)
		}
	}
	if string(fields["timeout"]) != "99" || string(fields["enabled"]) != "true" {
		t.Fatal("non-owned timeout or enabled flag changed")
	}
}

func TestMCPAdapterRegressionDisabledAndInvalidEntriesExist(t *testing.T) {
	for _, fixture := range []struct{ kind, text string }{
		{"qoder", `{"mcpServers":{"disabled":{"command":"node","disabled":true},"off":{"command":"node","enabled":false},"invalidOff":{"disabled":true},"missing":{},"null":null,"array":[],"scalar":"private-value","badArgs":{"command":"node","args":["ok",2]},"badEnv":{"command":"node","env":{"X":null}},"badType":{"type":"bogus","command":"node"},"badBool":{"command":"node","enabled":null}}}`},
		{"codex", "[mcp_servers]\nnull = 1\narray = []\nscalar = 'private-value'\n[mcp_servers.disabled]\ncommand = 'node'\ndisabled = true\n[mcp_servers.off]\ncommand = 'node'\nenabled = false\n[mcp_servers.invalidOff]\ndisabled = true\n[mcp_servers.missing]\n[mcp_servers.badArgs]\ncommand = 'node'\nargs = ['ok', 2]\n[mcp_servers.badEnv]\ncommand = 'node'\nenv = { X = 1 }\n[mcp_servers.badType]\ncommand = 1\n[mcp_servers.badBool]\ncommand = 'node'\nenabled = 'false'\n"},
	} {
		t.Run(fixture.kind, func(t *testing.T) {
			a := regressionMCPAdapter(t, fixture.kind, fixture.text)
			entries := regressionMCPEntries(t, a)
			if len(entries) != 11 {
				t.Fatalf("entry count = %d; invalid entries must remain visible", len(entries))
			}
			for name, entry := range entries {
				if entry.RawHash == "" {
					t.Fatalf("missing raw fingerprint for %s", name)
				}
				if name == "disabled" || name == "off" {
					if !entry.Disabled || entry.Invalid {
						t.Fatal("native disabled entry was misclassified")
					}
					continue
				}
				if !entry.Invalid || entry.ContentHash != "" {
					t.Fatalf("invalid entry %s treated as portable config", name)
				}
			}
			if !entries["invalidOff"].Disabled {
				t.Fatal("disabled flag lost on invalid entry")
			}
			before := regressionMCPText(t, a.Path)
			if err := applyMCPAdapter(a, filepath.Join(a.AppDir, "backups"), "null", MCPServerConfig{Transport: "stdio", Command: "new"}, false); err == nil {
				t.Fatal("non-object update should fail closed")
			}
			if before != regressionMCPText(t, a.Path) {
				t.Fatal("failed update changed the file")
			}
			if err := applyMCPAdapter(a, filepath.Join(a.AppDir, "backups"), "null", MCPServerConfig{}, true); err != nil {
				t.Fatal(err)
			}
			if _, exists := regressionMCPEntries(t, a)["null"]; exists {
				t.Fatal("invalid entry cannot be explicitly removed")
			}
		})
	}
}

func TestMCPAdapterRegressionInvalidDocumentsFailClosed(t *testing.T) {
	fixtures := []struct{ kind, text string }{
		{"qoder", "null"}, {"qoder", ""}, {"qoder", "[]"}, {"qoder", `{"mcpServers":null}`},
		{"qoder", `{"mcpServers":[]}`}, {"qoder", `{"mcpServers":{"demo":`},
		{"codex", "mcp_servers = 1"}, {"codex", "[[mcp_servers]]\ncommand = 'node'"},
		{"codex", "secret = 'private-value'\n[mcp_servers.demo]\nargs = [\n"},
		{"codex", "[mcp_servers.demo]\ncommand = 'node'\ncommand = 'duplicate'\n"},
	}
	for index, fixture := range fixtures {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			a := regressionMCPAdapter(t, fixture.kind, fixture.text)
			if _, _, err := readMCPAdapter(a); err == nil {
				t.Fatal("invalid document accepted")
			} else if strings.Contains(err.Error(), "private-value") {
				t.Fatal("parser error exposed source credentials")
			}
			for _, remove := range []bool{false, true} {
				if err := applyMCPAdapter(a, filepath.Join(a.AppDir, "backups"), "demo", MCPServerConfig{Transport: "stdio", Command: "node"}, remove); err == nil {
					t.Fatal("invalid document was rewritten")
				}
				if regressionMCPText(t, a.Path) != fixture.text {
					t.Fatal("failed write changed original bytes")
				}
			}
		})
	}
}

func TestMCPAdapterRegressionCodexSemanticMergeAndSubtreeRemoval(t *testing.T) {
	text := `model = "fixture"
message = """first line
[mcp_servers.not_a_server]
# still a string
last line"""
created = 1979-05-27T07:32:00Z
local_date = 1979-05-27
local_time = 07:32:00
local_datetime = 1979-05-27T07:32:00
large = 9007199254740993
infinite = inf
not_a_number = nan
list = ["a", "", "  b  "]
metadata = { nested = { enabled = true }, numbers = [1, 2] }
[mcp_servers."demo.dot"]
command = 'node'
args = [
  '',
  '  spaced  ', # comment
  "a b",
]
enabled = false
cwd = 'private dir'
env_vars = ['FIXTURE_SETTING']
enabled_tools = ['read']
startup_timeout_ms = 1001
env.FIXTURE = """multi
line"""
[mcp_servers."demo.dot".vendor]
"a.b" = [1, 2]
[[mcp_servers."demo.dot".vendor.rules]]
name = 'one'
[[mcp_servers."demo.dot".vendor.rules]]
name = 'two'
[mcp_servers."demo.dot".http_headers]
X-Mode = 'fixture'
[mcp_servers."demo.dot.sibling"]
url = 'https://example.invalid/other'
[other]
value = 'untouched'
`
	a := regressionMCPAdapter(t, "codex", text)
	before, err := readCodexDocument(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	entries := regressionMCPEntries(t, a)
	if len(entries) != 2 || entries["demo.dot"].Invalid || !entries["demo.dot"].Disabled {
		t.Fatal("quoted name or child table was not parsed as a single server")
	}
	config := entries["demo.dot"].Config
	if !reflect.DeepEqual(config.Args, []string{"", "  spaced  ", "a b"}) || config.TimeoutMS != 1001 || config.Env["FIXTURE"] != "multi\nline" {
		t.Fatal("multiline values, arguments or millisecond timeout changed")
	}
	if err := applyCodexMCP(a.Path, "demo.dot", config, false); err != nil {
		t.Fatal(err)
	}
	after, _ := readCodexDocument(a.Path)
	oldServer := before["mcp_servers"].(map[string]any)["demo.dot"].(map[string]any)
	newServer := after["mcp_servers"].(map[string]any)["demo.dot"].(map[string]any)
	for _, key := range []string{"cwd", "env_vars", "enabled_tools", "vendor", "enabled"} {
		if !reflect.DeepEqual(oldServer[key], newServer[key]) {
			t.Fatalf("private Codex field %s changed", key)
		}
	}
	if !regressionMCPEntries(t, a)["demo.dot"].Disabled {
		t.Fatal("saving a disabled target silently enabled it")
	}
	if err := applyCodexMCP(a.Path, "demo.dot", MCPServerConfig{}, true); err != nil {
		t.Fatal(err)
	}
	remaining := regressionMCPEntries(t, a)
	if len(remaining) != 1 || remaining["demo.dot.sibling"].Config.URL == "" {
		t.Fatal("removal orphaned children or deleted a similarly named server")
	}
	after, _ = readCodexDocument(a.Path)
	delete(before["mcp_servers"].(map[string]any), "demo.dot")
	want, _ := encodeCodexDocument(before)
	got, _ := encodeCodexDocument(after)
	if string(want) != string(got) {
		t.Fatal("unrelated TOML semantic values changed")
	}
}

func TestMCPAdapterRegressionCodexInlineAndDottedTables(t *testing.T) {
	for _, text := range []string{
		`mcp_servers = { "demo.dot" = { command = 'node', args = ['', ' x '], private = { a = 1 } }, other = { command = 'other' } }`,
		"mcp_servers.'demo.dot'.command = 'node'\nmcp_servers.'demo.dot'.args = ['', ' x ']\nmcp_servers.'demo.dot'.private.a = 1\nmcp_servers.other.command = 'other'\n",
	} {
		a := regressionMCPAdapter(t, "codex", text)
		entry := regressionMCPEntries(t, a)["demo.dot"]
		if entry.Invalid || !reflect.DeepEqual(entry.Config.Args, []string{"", " x "}) {
			t.Fatal("inline/dotted entry parsed incorrectly")
		}
		if err := applyCodexMCP(a.Path, "demo.dot", entry.Config, false); err != nil {
			t.Fatal(err)
		}
		if err := applyCodexMCP(a.Path, "demo.dot", MCPServerConfig{}, true); err != nil {
			t.Fatal(err)
		}
		if entries := regressionMCPEntries(t, a); len(entries) != 1 || entries["other"].Config.Command != "other" {
			t.Fatal("subtree removal damaged inline/dotted siblings")
		}
	}
}

func TestMCPAdapterRegressionCodexHTTPHeadersAndBearer(t *testing.T) {
	a := regressionMCPAdapter(t, "codex", `[mcp_servers.demo]
url = 'https://example.invalid/mcp'
bearer_token_env_var = 'FIXTURE_TOKEN'
enabled = false
[ mcp_servers.demo.http_headers ]
X-Mode = 'fixture'
[mcp_servers.demo.env_http_headers]
X-Private = 'PRIVATE_ENV_NAME'
[mcp_servers.demo.tools.read]
approval_mode = 'prompt'
`)
	entry := regressionMCPEntries(t, a)["demo"]
	if entry.Invalid || entry.Config.Headers["Authorization"] != "Bearer ${FIXTURE_TOKEN}" || entry.Config.Headers["X-Mode"] != "fixture" {
		t.Fatal("Codex HTTP headers or bearer variable did not import")
	}
	if _, exists := entry.Config.Headers["X-Private"]; exists {
		t.Fatal("private environment header mapping became a literal portable header")
	}
	entry.Config.Headers["X-New"] = "supported-custom-value"
	entry.Config.TimeoutMS = 1501
	if err := adapterValidate(a, entry.Config); err != nil {
		t.Fatal("Codex supports custom HTTP headers")
	}
	if err := applyCodexMCP(a.Path, "demo", entry.Config, false); err != nil {
		t.Fatal(err)
	}
	roundtrip := regressionMCPEntries(t, a)["demo"]
	if roundtrip.ContentHash != mcpConfigHash(entry.Config) || !roundtrip.Disabled {
		t.Fatal("Codex HTTP portable config or native disabled state changed")
	}
	document, _ := readCodexDocument(a.Path)
	fields := document["mcp_servers"].(map[string]any)["demo"].(map[string]any)
	if fields["bearer_token_env_var"] != "FIXTURE_TOKEN" || fields["env_http_headers"].(map[string]any)["X-Private"] != "PRIVATE_ENV_NAME" || fields["tools"] == nil {
		t.Fatal("bearer name or private fields changed")
	}
	masked := mcpConfigView(roundtrip.Config)
	encoded, _ := json.Marshal(masked)
	for _, secret := range []string{"FIXTURE_TOKEN", "supported-custom-value", "PRIVATE_ENV_NAME"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("masked config exposed a credential")
		}
	}
	if err := applyCodexMCP(a.Path, "demo", MCPServerConfig{}, true); err != nil {
		t.Fatal(err)
	}
	if len(regressionMCPEntries(t, a)) != 0 {
		t.Fatal("HTTP header tables survived removal")
	}
}

func TestMCPAdapterRegressionStaticBearerLookingHeaderStaysStatic(t *testing.T) {
	a := regressionMCPAdapter(t, "codex", "[mcp_servers.demo]\nurl = 'https://example.invalid'\nhttp_headers = { Authorization = 'Bearer ${LITERAL}' }\n")
	entry := regressionMCPEntries(t, a)["demo"]
	if err := applyCodexMCP(a.Path, "demo", entry.Config, false); err != nil {
		t.Fatal(err)
	}
	doc, _ := readCodexDocument(a.Path)
	fields := doc["mcp_servers"].(map[string]any)["demo"].(map[string]any)
	if _, exists := fields["bearer_token_env_var"]; exists {
		t.Fatal("static header silently changed to an environment lookup")
	}
}

func TestMCPAdapterRegressionRawAndPortableFingerprints(t *testing.T) {
	for _, fixture := range []struct{ kind, first, reordered, changed string }{
		{"qoder", `{"mcpServers":{"demo":{"command":"node","private":{"a":9007199254740992,"b":2}}}}`, ` {"mcpServers": {"demo": {"private": {"b":2,"a":9007199254740992}, "command":"node"}}}`, `{"mcpServers":{"demo":{"command":"node","private":{"a":9007199254740993,"b":2}}}}`},
		{"codex", "[mcp_servers.demo]\ncommand = 'node'\nprivate = { a = 1, b = 2 }\n", "# ignored comment\n[mcp_servers.demo]\nprivate.b = 2\nprivate.a = 1\ncommand = \"node\"\n", "[mcp_servers.demo]\ncommand = 'node'\nprivate = { a = 2, b = 2 }\n"},
	} {
		t.Run(fixture.kind, func(t *testing.T) {
			a := regressionMCPAdapter(t, fixture.kind, fixture.first)
			first := regressionMCPEntries(t, a)["demo"]
			regressionMCPWrite(t, a.Path, fixture.reordered)
			reordered := regressionMCPEntries(t, a)["demo"]
			if first.RawHash == "" || first.RawHash != reordered.RawHash {
				t.Fatal("raw fingerprint depends on key order or formatting")
			}
			regressionMCPWrite(t, a.Path, fixture.changed)
			changed := regressionMCPEntries(t, a)["demo"]
			if first.RawHash == changed.RawHash || first.ContentHash != changed.ContentHash {
				t.Fatal("private edit not distinguished from portable config")
			}
		})
	}
	config := MCPServerConfig{Transport: "stdio", Command: "node"}
	original := mcpConfigHash(config)
	config.TimeoutMS = 1
	if original == mcpConfigHash(config) {
		t.Fatal("portable hash ignored timeout")
	}
	config.TimeoutMS = 0
	config.Args = []string{""}
	if original == mcpConfigHash(config) {
		t.Fatal("portable hash ignored an empty argument")
	}
}

func TestMCPAdapterRegressionViewsRespectRawAndNativeState(t *testing.T) {
	config := MCPServerConfig{Transport: "stdio", Command: "node"}
	hash := mcpConfigHash(config)
	record := &MCPRecord{Name: "demo", Config: config, ContentHash: hash, Targets: map[string]*MCPTarget{
		"qoder": {AgentID: "qoder", Path: "synthetic-only", RawHash: "before", ContentHash: hash, Enabled: true},
	}}
	entry := mcpEntry{Name: "demo", Config: config, ContentHash: hash, RawHash: "after"}
	entries := map[string]mcpEntry{"qoder": entry}
	view := mcpViewFromRecord(record, entries)
	if view.Status != "modified" || !view.Targets[0].Modified {
		t.Fatal("raw-only local edit was shown as synced")
	}
	entry.RawHash = "before"
	entry.Disabled = true
	entries["qoder"] = entry
	view = mcpViewFromRecord(record, entries)
	if view.Status == "synced" || view.Targets[0].Enabled || !view.Targets[0].Exists || !view.Targets[0].Managed {
		t.Fatal("native disabled target was shown as enabled/synced/unmanaged")
	}
	entry.Disabled = false
	entry.Invalid = true
	entries["qoder"] = entry
	view = mcpViewFromRecord(record, entries)
	if view.Status == "synced" || !view.Targets[0].Modified {
		t.Fatal("invalid target was shown as synced")
	}
	view = mcpViewFromRecord(record, nil)
	if view.Targets[0].Exists || view.Targets[0].Enabled || !view.Targets[0].Managed {
		t.Fatal("tracked ownership incorrectly depends on native existence")
	}
	entry.Invalid = false
	record.Targets["qoder"].Enabled = false
	entries["qoder"] = entry
	view = mcpViewFromRecord(record, entries)
	if !view.Targets[0].Enabled {
		t.Fatal("stale tracked enabled flag hid native enabled state")
	}
}

func TestMCPAdapterRegressionBackupsAreUniqueAndPrivate(t *testing.T) {
	a := regressionMCPAdapter(t, "qoder", "before")
	backupDir := filepath.Join(a.AppDir, "backups")
	for _, value := range []string{"before", "after", "latest"} {
		regressionMCPWrite(t, a.Path, value)
		if err := backupFile(a.Path, backupDir, "qoder"); err != nil {
			t.Fatal(err)
		}
	}
	files, err := os.ReadDir(backupDir)
	if err != nil || len(files) != 3 {
		t.Fatal("rapid backups overwrote an earlier before-image")
	}
	contents := map[string]bool{}
	for _, file := range files {
		path := filepath.Join(backupDir, file.Name())
		contents[regressionMCPText(t, path)] = true
		info, err := file.Info()
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatal("backup permissions are not private")
		}
		// Windows rejects removal if a backup handle was left open.
		if err := os.Remove(path); err != nil {
			t.Fatal("backup file was not closed")
		}
	}
	if !contents["before"] || !contents["after"] || !contents["latest"] {
		t.Fatal("original backup content was lost")
	}
	if err := backupFile(a.AppDir, backupDir, "bad-source"); err == nil {
		t.Fatal("directory backup should fail")
	}
	if files, _ := os.ReadDir(backupDir); len(files) != 0 {
		t.Fatal("failed backup left an incomplete file")
	}
}
