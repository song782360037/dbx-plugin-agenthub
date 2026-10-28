package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func stateTestManager(t *testing.T, dir string) *Manager {
	t.Helper()
	t.Setenv("DBX_PLUGIN_DATA_DIR", dir)
	manager, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func stateTestDisk(t *testing.T, dir string) State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestStateManagersPreserveSequentialWrites(t *testing.T) {
	dir := t.TempDir()
	a := stateTestManager(t, dir)
	b := stateTestManager(t, dir)
	preview := &Preview{ID: "pending", CreatedAt: time.Now(), Root: t.TempDir()}
	b.putPreview(preview)

	proxy := "http://127.0.0.1:7890"
	if _, err := a.updateSettings(SettingsInput{Proxy: &proxy}); err != nil {
		t.Fatal(err)
	}
	mode := "copy"
	if _, err := b.updateSettings(SettingsInput{DefaultMode: &mode}); err != nil {
		t.Fatal(err)
	}
	if err := a.withOpLock(func() error {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.state.Skills["first"] = &Record{Name: "first", Targets: map[string]*TargetRecord{}}
		return a.saveStateLocked()
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.withOpLock(func() error {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.state.MCPServers["server"] = &MCPRecord{Name: "server", Targets: map[string]*MCPTarget{}}
		return b.saveStateLocked()
	}); err != nil {
		t.Fatal(err)
	}
	state := stateTestDisk(t, dir)
	if state.Settings.Proxy != proxy || state.Settings.DefaultMode != mode || state.Skills["first"] == nil || state.MCPServers["server"] == nil {
		t.Fatalf("earlier writes were lost: %#v", state)
	}

	// A stale manager must also respect deletions rather than resurrect records.
	if err := a.withOpLock(func() error {
		a.mu.Lock()
		delete(a.state.Skills, "first")
		a.state.Skills["second"] = &Record{Name: "second", Targets: map[string]*TargetRecord{}}
		a.mu.Unlock()
		return a.saveState()
	}); err != nil {
		t.Fatal(err)
	}
	level := "off"
	if _, err := b.updateSettings(SettingsInput{ScanLevel: &level}); err != nil {
		t.Fatal(err)
	}
	state = stateTestDisk(t, dir)
	if state.Settings.Proxy != proxy || state.Settings.DefaultMode != mode || state.Settings.ScanLevel != level || state.Skills["first"] != nil || state.Skills["second"] == nil || state.MCPServers["server"] == nil {
		t.Fatalf("settings/records were lost or resurrected: %#v", state)
	}
	if got, ok := b.getPreview(preview.ID); !ok || got != preview {
		t.Fatal("disk reload discarded the pending preview")
	}
	if _, err := os.Stat(preview.Root); err != nil {
		t.Fatalf("disk reload removed preview files: %v", err)
	}
}

func TestStateLoadAndReloadNormalizeDefaults(t *testing.T) {
	for _, settings := range []string{`{}`, `{"maxDownloadMB":-1,"agentPaths":null}`} {
		t.Run(settings, func(t *testing.T) {
			dir := t.TempDir()
			stale := stateTestManager(t, dir)
			data := []byte(`{"skills":{"skill":{},"nil":null," ":{}},"mcpServers":{"server":{},"nil":null," ":{}},"settings":` + settings + `}`)
			if err := os.WriteFile(filepath.Join(dir, stateFile), data, 0o600); err != nil {
				t.Fatal(err)
			}
			loaded := stateTestManager(t, dir)
			check := func(state State) {
				t.Helper()
				if !reflect.DeepEqual(state.Settings, defaultSettings()) {
					t.Fatalf("defaults not restored: %#v", state.Settings)
				}
				if state.SchemaVersion != 1 || len(state.Skills) != 1 || len(state.MCPServers) != 1 {
					t.Fatalf("state not normalized: %#v", state)
				}
				if r := state.Skills["skill"]; r == nil || r.Name != "skill" || r.Targets == nil {
					t.Fatalf("skill not normalized: %#v", r)
				}
				if r := state.MCPServers["server"]; r == nil || r.Name != "server" || r.Targets == nil {
					t.Fatalf("MCP record not normalized: %#v", r)
				}
			}
			check(loaded.stateSnapshot())
			if err := stale.withOpLock(func() error {
				check(stale.stateSnapshot()) // Reload must release mu before the callback.
				return stale.saveState()
			}); err != nil {
				t.Fatal(err)
			}
			persisted := stateTestDisk(t, dir).Settings
			defaults := defaultSettings()
			if persisted.DefaultMode != defaults.DefaultMode || persisted.ScanLevel != defaults.ScanLevel || persisted.MaxDownloadMB != defaults.MaxDownloadMB {
				t.Fatalf("normalized defaults were not saved: %#v", persisted)
			}
		})
	}
}

func TestStateReloadPreservesExplicitSettings(t *testing.T) {
	dir := t.TempDir()
	m := stateTestManager(t, dir)
	want := Settings{
		Proxy: "http://127.0.0.1:7890", GitHubToken: "test-only-token",
		DefaultMode: "copy", ScanLevel: "off", MaxDownloadMB: 99,
		AgentPaths:   map[string]string{"test": t.TempDir()},
		CustomAgents: []CustomAgent{{ID: "custom", Name: "Custom", Path: t.TempDir()}},
	}
	if err := writeJSONFile(filepath.Join(dir, stateFile), State{Settings: want}); err != nil {
		t.Fatal(err)
	}
	if err := m.withOpLock(func() error {
		if got := m.settingsSnapshot(); !reflect.DeepEqual(got, want) {
			t.Fatalf("explicit settings changed: %#v", got)
		}
		state := m.stateSnapshot()
		if state.Skills == nil || state.MCPServers == nil {
			t.Fatal("missing record maps were not initialized")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStateOperationRejectsInvalidDisk(t *testing.T) {
	for _, fixture := range []struct {
		name string
		data string
	}{
		{"malformed", `{"settings":`},
		{"empty", ""},
		{"null", "null"},
		{"array", "[]"},
		{"invalid-settings", `{"settings":{"maxDownloadMB":"bad"}}`},
		{"null-skill-target", `{"skills":{"demo":{"targets":{"qoder":null}}}}`},
		{"null-mcp-target", `{"mcpServers":{"demo":{"targets":{"qoder":null}}}}`},
		{"directory", ""},
		{"missing", ""},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			m := stateTestManager(t, dir)
			proxy := "http://127.0.0.1:7890"
			if _, err := m.updateSettings(SettingsInput{Proxy: &proxy}); err != nil {
				t.Fatal(err)
			}
			before := m.stateSnapshot()
			preview := &Preview{ID: "pending", CreatedAt: time.Now()}
			m.putPreview(preview)
			path := filepath.Join(dir, stateFile)
			valid, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch fixture.name {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing":
			default:
				if err := os.WriteFile(path, []byte(fixture.data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			if err := m.withOpLock(func() error {
				called = true
				return m.saveState()
			}); err == nil || called {
				t.Fatalf("operation was not blocked: error=%v called=%v", err, called)
			}
			if _, err := m.updateSettings(SettingsInput{}); err == nil {
				t.Fatal("settings update accepted invalid disk state")
			}
			if !reflect.DeepEqual(m.stateSnapshot(), before) {
				t.Fatal("failed reload changed the in-memory state")
			}
			if got, ok := m.getPreview(preview.ID); !ok || got != preview {
				t.Fatal("failed reload discarded previews")
			}
			if _, err := os.Stat(filepath.Join(dir, ".lock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed reload left an operation lock: %v", err)
			}
			switch fixture.name {
			case "directory":
				if info, err := os.Stat(path); err != nil || !info.IsDir() {
					t.Fatalf("read failure replaced the state directory: %v", err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing state was recreated: %v", err)
				}
			default:
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, []byte(fixture.data)) {
					t.Fatalf("invalid state was moved or overwritten: %q, %v", got, err)
				}
			}
			if err := os.WriteFile(path, valid, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.updateSettings(SettingsInput{}); err != nil {
				t.Fatalf("operation could not recover after state repair: %v", err)
			}
		})
	}
}

func TestStateStartupPreservesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, stateFile)
	data := []byte(`{"skills":`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	m := stateTestManager(t, dir)
	if len(m.initWarnings()) == 0 {
		t.Fatal("missing startup warning")
	}
	if _, err := m.updateSettings(SettingsInput{}); err == nil {
		t.Fatal("startup corruption was treated as a new store")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("corrupt file changed: %q, %v", got, err)
	}
}

func TestStateExistingLocksFailClosed(t *testing.T) {
	for _, content := range []string{fmt.Sprintf("%d active-owner", os.Getpid()), "unknown-owner", ""} {
		t.Run(content, func(t *testing.T) {
			dir := t.TempDir()
			m := stateTestManager(t, dir)
			path := filepath.Join(dir, ".lock")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			called := false
			if err := m.withOpLock(func() error { called = true; return nil }); err == nil || called {
				t.Fatalf("old lock was ignored: error=%v called=%v", err, called)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != content {
				t.Fatalf("existing lock was changed: %q, %v", got, err)
			}
		})
	}
}

func TestStateActiveOperationLockIsNotStolen(t *testing.T) {
	dir := t.TempDir()
	a := stateTestManager(t, dir)
	b := stateTestManager(t, dir)
	if err := a.withOpLock(func() error {
		path := filepath.Join(dir, ".lock")
		for _, age := range []time.Duration{0, time.Hour} {
			modified := time.Now().Add(-age)
			if err := os.Chtimes(path, modified, modified); err != nil {
				return err
			}
			called := false
			if err := b.withOpLock(func() error { called = true; return nil }); err == nil || called {
				return fmt.Errorf("active lock was ignored (age %s): error=%v called=%v", age, err, called)
			}
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("active lock disappeared: %w", err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.withOpLock(func() error { return nil }); err != nil {
		t.Fatalf("lock not released by its owner: %v", err)
	}
}

func TestStateRefreshSeesOtherManagerWrites(t *testing.T) {
	dir := t.TempDir()
	a := stateTestManager(t, dir)
	b := stateTestManager(t, dir)
	mode := "copy"
	if _, err := a.updateSettings(SettingsInput{DefaultMode: &mode}); err != nil {
		t.Fatal(err)
	}
	if err := b.refreshState(); err != nil {
		t.Fatal(err)
	}
	if b.settingsSnapshot().DefaultMode != mode {
		t.Fatal("read refresh missed another manager's write")
	}
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.refreshState(); err == nil {
		t.Fatal("read refresh accepted invalid state")
	}
	if b.settingsSnapshot().DefaultMode != mode {
		t.Fatal("failed read refresh discarded valid snapshot")
	}
}

func TestStateRefreshDoesNotBlockOnActiveOperation(t *testing.T) {
	m := stateTestManager(t, t.TempDir())
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.withOpLock(func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	if err := m.refreshState(); err != nil {
		t.Fatalf("read refresh failed during operation: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStateOperationReleasesLocksAfterFailure(t *testing.T) {
	m := stateTestManager(t, t.TempDir())
	failure := errors.New("callback failed")
	if err := m.withOpLock(func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("callback error lost: %v", err)
	}
	func() {
		defer func() {
			if got := recover(); got != failure {
				t.Errorf("callback panic lost: %v", got)
			}
		}()
		_ = m.withOpLock(func() error { panic(failure) })
	}()
	if err := m.withOpLock(func() error {
		_ = m.stateSnapshot()
		_ = m.settingsSnapshot()
		_ = m.initWarnings()
		return nil
	}); err != nil {
		t.Fatalf("locks not released after callback failure: %v", err)
	}
}
