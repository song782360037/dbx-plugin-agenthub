package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Manager struct {
	dataDir  string
	hubDir   string
	cacheDir string
	tmpDir   string
	trashDir string

	mu            sync.Mutex
	opMu          sync.Mutex
	state         State
	stateFileSeen bool // Protected by mu; a missing file is only valid for a new store.
	previews      map[string]*Preview
	warnings      []string
}

func NewManager() (*Manager, error) {
	dataDir := strings.TrimSpace(os.Getenv("DBX_PLUGIN_DATA_DIR"))
	if dataDir == "" {
		dataDir = strings.TrimSpace(os.Getenv("AGENT_HUB_DATA_DIR"))
	}
	if dataDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("无法确定插件数据目录：%w", err)
		}
		dataDir = filepath.Join(base, "dbx-agenthub")
	}
	manager := &Manager{
		dataDir: dataDir, hubDir: filepath.Join(dataDir, "hub", "skills"),
		cacheDir: filepath.Join(dataDir, "cache"), tmpDir: filepath.Join(dataDir, "tmp"),
		trashDir: filepath.Join(dataDir, "trash"), previews: map[string]*Preview{},
	}
	for _, dir := range []string{manager.dataDir, manager.hubDir, manager.cacheDir, manager.tmpDir, manager.trashDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("无法创建数据目录 %s：%w", dir, err)
		}
	}
	manager.loadState()
	manager.cleanStalePreviews()
	return manager, nil
}

// Keep damaged state on disk so later writes cannot mistake it for a new store.
func (m *Manager) loadState() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = State{SchemaVersion: 1, Skills: map[string]*Record{}, MCPServers: map[string]*MCPRecord{}, Settings: defaultSettings()}
	if err := m.reloadStateLocked(); err != nil {
		m.warnings = append(m.warnings, err.Error())
	}
}

// 读路径不等待正在进行的写操作：写入期间内存状态已是最新视图，跳过重载即可。
func (m *Manager) refreshState() error {
	if !m.opMu.TryLock() {
		return nil
	}
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reloadStateLocked()
}

// Hold mu and, outside startup, opMu so reload cannot discard pending writes.
func (m *Manager) reloadStateLocked() error {
	state, err := readState(filepath.Join(m.dataDir, stateFile))
	if errors.Is(err, os.ErrNotExist) && !m.stateFileSeen {
		return nil
	}
	m.stateFileSeen = true
	if err != nil {
		return err
	}
	m.state = state
	return nil
}

func readState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, fmt.Errorf("无法读取状态文件：%w", err)
	}
	var state *State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("状态文件损坏，已保留原文件：%w", err)
	}
	if state == nil {
		return State{}, errors.New("状态文件必须是 JSON 对象，已保留原文件")
	}
	if state.Skills == nil {
		state.Skills = map[string]*Record{}
	}
	if state.MCPServers == nil {
		state.MCPServers = map[string]*MCPRecord{}
	}
	settings := state.Settings
	defaults := defaultSettings()
	if settings.DefaultMode == "" {
		settings.DefaultMode = defaults.DefaultMode
	}
	if settings.ScanLevel == "" {
		settings.ScanLevel = defaults.ScanLevel
	}
	if settings.MaxDownloadMB <= 0 {
		settings.MaxDownloadMB = defaults.MaxDownloadMB
	}
	if settings.AgentPaths == nil {
		settings.AgentPaths = map[string]string{}
	}
	state.Settings = settings
	for name, record := range state.Skills {
		if record == nil || strings.TrimSpace(name) == "" {
			delete(state.Skills, name)
			continue
		}
		if record.Name == "" {
			record.Name = name
		}
		if record.Targets == nil {
			record.Targets = map[string]*TargetRecord{}
		}
		for _, target := range record.Targets {
			if target == nil {
				return State{}, errors.New("状态文件包含空技能目标，已保留原文件")
			}
		}
	}
	for name, record := range state.MCPServers {
		if record == nil || strings.TrimSpace(name) == "" {
			delete(state.MCPServers, name)
			continue
		}
		if record.Name == "" {
			record.Name = name
		}
		if record.Targets == nil {
			record.Targets = map[string]*MCPTarget{}
		}
		for _, target := range record.Targets {
			if target == nil {
				return State{}, errors.New("状态文件包含空 MCP 目标，已保留原文件")
			}
		}
	}
	state.SchemaVersion = 1
	return *state, nil
}

func (m *Manager) saveStateLocked() error {
	m.state.SchemaVersion = 1
	if err := writeJSONFile(filepath.Join(m.dataDir, stateFile), m.state); err != nil {
		return err
	}
	m.stateFileSeen = true
	return nil
}

func (m *Manager) saveState() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveStateLocked()
}

func (m *Manager) stateSnapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := m.state
	copy.Skills = make(map[string]*Record, len(m.state.Skills))
	for name, record := range m.state.Skills {
		clone := *record
		clone.Targets = map[string]*TargetRecord{}
		for id, target := range record.Targets {
			targetCopy := *target
			clone.Targets[id] = &targetCopy
		}
		copy.Skills[name] = &clone
	}
	copy.MCPServers = make(map[string]*MCPRecord, len(m.state.MCPServers))
	for name, record := range m.state.MCPServers {
		clone := *record
		clone.Config = mcpServerConfigCopy(record.Config)
		clone.Targets = map[string]*MCPTarget{}
		for id, target := range record.Targets {
			targetCopy := *target
			clone.Targets[id] = &targetCopy
		}
		copy.MCPServers[name] = &clone
	}
	return copy
}

// Acquire opMu, file lock, then mu; callers must not hold mu.
func (m *Manager) withOpLock(fn func() error) (err error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	lockPath := filepath.Join(m.dataDir, ".lock")
	// Age and PID cannot establish that a Windows lock owner has exited.
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("无法获取技能管理操作锁，请稍后重试（已有锁不会自动清理）：%w", err)
	}
	// Keep the handle open so Windows cannot delete the active lock.
	defer func() {
		if releaseErr := releaseOpLock(lock); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("无法释放技能管理操作锁：%w", releaseErr))
		}
	}()
	if _, err := lock.WriteString(fmt.Sprintf("%d %s", os.Getpid(), nowString())); err != nil {
		return fmt.Errorf("无法写入技能管理操作锁：%w", err)
	}
	m.mu.Lock()
	err = m.reloadStateLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return fn()
}

func releaseOpLock(lock *os.File) error {
	owned, statErr := lock.Stat()
	closeErr := lock.Close() // Windows requires closing the handle before removal.
	if err := errors.Join(statErr, closeErr); err != nil {
		return err
	}
	current, err := os.Lstat(lock.Name())
	if err != nil {
		return err
	}
	if !os.SameFile(owned, current) {
		return errors.New("操作锁已被替换，已保留现有锁")
	}
	return os.Remove(lock.Name())
}

func (m *Manager) updateSettings(input SettingsInput) (SettingsView, error) {
	if err := m.withOpLock(func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		settings := m.state.Settings
		if input.Proxy != nil {
			proxy := strings.TrimSpace(*input.Proxy)
			if proxy != "" {
				parsed, err := url.Parse(proxy)
				if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
					return errors.New("代理地址必须是 http:// 或 https:// URL")
				}
			}
			settings.Proxy = proxy
		}
		if input.ClearToken {
			settings.GitHubToken = ""
		} else if input.GitHubToken != nil && strings.TrimSpace(*input.GitHubToken) != "" {
			settings.GitHubToken = strings.TrimSpace(*input.GitHubToken)
		}
		if input.DefaultMode != nil {
			mode := strings.TrimSpace(*input.DefaultMode)
			if mode != "auto" && mode != "link" && mode != "copy" {
				return errors.New("默认同步方式只能是 auto、link 或 copy")
			}
			settings.DefaultMode = mode
		}
		if input.ScanLevel != nil {
			level := strings.TrimSpace(*input.ScanLevel)
			if level != "basic" && level != "off" {
				return errors.New("扫描级别只能是 basic 或 off")
			}
			settings.ScanLevel = level
		}
		if input.MaxDownloadMB != nil {
			size := *input.MaxDownloadMB
			if size < 1 || size > 512 {
				return errors.New("下载上限必须在 1–512 MiB 之间")
			}
			settings.MaxDownloadMB = size
		}
		if input.AgentPaths != nil {
			paths := map[string]string{}
			for id, raw := range input.AgentPaths {
				id = strings.TrimSpace(id)
				if !skillNamePattern.MatchString(id) {
					return fmt.Errorf("客户端 ID %q 不合法", id)
				}
				raw = strings.TrimSpace(raw)
				if raw == "" {
					continue
				}
				resolved, err := absoluteClean(raw)
				if err != nil {
					return fmt.Errorf("客户端 %s 的技能目录无效：%v", id, err)
				}
				paths[id] = resolved
			}
			settings.AgentPaths = paths
		}
		if input.CustomAgents != nil {
			custom, err := validateCustomAgents(input.CustomAgents)
			if err != nil {
				return err
			}
			settings.CustomAgents = custom
		}
		previous := m.state.Settings
		m.state.Settings = settings
		if err := m.saveStateLocked(); err != nil {
			m.state.Settings = previous
			return err
		}
		return nil
	}); err != nil {
		return SettingsView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return settingsView(m.state.Settings), nil
}

func (m *Manager) cleanStalePreviews() {
	entries, err := os.ReadDir(m.tmpDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-2 * time.Hour)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(m.tmpDir, entry.Name()))
		}
	}
	m.mu.Lock()
	for id, preview := range m.previews {
		if time.Since(preview.CreatedAt) > 2*time.Hour {
			delete(m.previews, id)
		}
	}
	m.mu.Unlock()
}

func (m *Manager) getPreview(id string) (*Preview, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	preview, ok := m.previews[id]
	return preview, ok
}

func (m *Manager) putPreview(preview *Preview) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.previews[preview.ID] = preview
}

func (m *Manager) dropPreview(id string) {
	m.mu.Lock()
	preview := m.previews[id]
	delete(m.previews, id)
	m.mu.Unlock()
	if preview != nil {
		target := preview.TempDir
		if target == "" {
			target = preview.Root
		}
		_ = os.RemoveAll(target)
	}
}

func (m *Manager) settingsSnapshot() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Settings
}

func (m *Manager) initWarnings() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.warnings...)
}
