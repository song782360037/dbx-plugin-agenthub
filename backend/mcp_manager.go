package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func (m *Manager) mcpRecordsSnapshot() map[string]*MCPRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*MCPRecord, len(m.state.MCPServers))
	for name, record := range m.state.MCPServers {
		copy := *record
		copy.Config = mcpServerConfigCopy(record.Config)
		copy.Targets = map[string]*MCPTarget{}
		for id, target := range record.Targets {
			targetCopy := *target
			copy.Targets[id] = &targetCopy
		}
		out[name] = &copy
	}
	return out
}

func (m *Manager) scanMCPAdapters() ([]mcpAdapter, map[string]map[string]mcpEntry, []string) {
	settings := m.settingsSnapshot()
	adapters := buildMCPAdapters(settings)
	byAgent := map[string]map[string]mcpEntry{}
	var warnings []string
	for _, adapter := range adapters {
		if !adapter.Detected {
			continue
		}
		entries, adapterWarnings, err := readMCPAdapter(adapter)
		warnings = append(warnings, adapterWarnings...)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s：%v", adapter.Name, err))
			continue
		}
		byAgent[adapter.ID] = entries
	}
	return adapters, byAgent, uniqueStrings(warnings)
}

func (m *Manager) mcpOverview() MCPOverview {
	adapters, byAgent, warnings := m.scanMCPAdapters()
	records := m.mcpRecordsSnapshot()
	adapterNames := map[string]string{}
	adapterViews := make([]MCPAdapterView, 0, len(adapters))
	for _, adapter := range adapters {
		adapterNames[adapter.ID] = adapter.Name
		adapterViews = append(adapterViews, adapterView(adapter))
	}
	namesSet := map[string]bool{}
	for name := range records {
		namesSet[name] = true
	}
	for _, entries := range byAgent {
		for name := range entries {
			namesSet[name] = true
		}
	}
	var views []MCPView
	for name := range namesSet {
		if record := records[name]; record != nil {
			view := mcpViewFromRecord(record, mergeMCPEntries(byAgent, name))
			for index := range view.Targets {
				view.Targets[index].AgentName = adapterNames[view.Targets[index].AgentID]
			}
			for agentID, entries := range byAgent {
				entry, exists := entries[name]
				if !exists {
					continue
				}
				if _, tracked := record.Targets[agentID]; tracked {
					continue
				}
				modified := entry.ContentHash != record.ContentHash
				view.Targets = append(view.Targets, MCPTargetView{
					AgentID: agentID, AgentName: adapterNames[agentID], Path: entry.Path,
					Managed: false, Exists: true, Enabled: !entry.Disabled, Transport: entry.Config.Transport,
					ContentHash: entry.ContentHash, Modified: modified,
				})
				view.Warnings = append(view.Warnings, fmt.Sprintf("%s 中存在同名但未纳管的 MCP 配置", adapterNames[agentID]))
			}
			sortMCPTargets(view.Targets)
			view.Status = mcpStatus(record, view.Targets)
			view.StatusText = mcpStatusText(view.Status)
			view.Warnings = uniqueStrings(view.Warnings)
			views = append(views, view)
			continue
		}
		var entries []mcpEntry
		for _, agentEntries := range byAgent {
			if entry, exists := agentEntries[name]; exists {
				entries = append(entries, entry)
			}
		}
		if len(entries) == 0 {
			continue
		}
		view := mcpUntrackedView(name, entries)
		for index := range view.Targets {
			view.Targets[index].AgentName = adapterNames[view.Targets[index].AgentID]
		}
		views = append(views, view)
	}
	sort.Slice(views, func(left, right int) bool {
		if views[left].Managed != views[right].Managed {
			return views[left].Managed
		}
		return strings.ToLower(views[left].Name) < strings.ToLower(views[right].Name)
	})
	stats := MCPStats{Managed: len(records)}
	detected := 0
	for _, adapter := range adapters {
		if adapter.Detected {
			detected++
		}
	}
	for _, view := range views {
		if !view.Managed {
			stats.Untracked++
		}
		switch view.Status {
		case "synced":
			stats.Synced++
		case "untracked":
			// Counted as untracked above; no separate attention bucket.
		default:
			stats.NeedsAttention++
		}
		stats.Installed += len(view.Targets)
	}
	stats.DetectedAgents = detected
	return MCPOverview{
		Adapters: adapterViews, Servers: views, Stats: stats,
		Warnings: uniqueStrings(warnings), ScannedAt: nowString(),
	}
}

func mergeMCPEntries(byAgent map[string]map[string]mcpEntry, name string) map[string]mcpEntry {
	out := map[string]mcpEntry{}
	for agentID, entries := range byAgent {
		if entry, exists := entries[name]; exists {
			out[agentID] = entry
		}
	}
	return out
}

func mcpUntrackedView(name string, entries []mcpEntry) MCPView {
	sort.Slice(entries, func(i, j int) bool { return entries[i].AgentID < entries[j].AgentID })
	first := entries[0]
	hashes := map[string]bool{}
	view := MCPView{
		Name: name, Transport: first.Config.Transport, Command: first.Config.Command,
		Args: append([]string{}, first.Config.Args...), URL: first.Config.URL,
		TimeoutMS: first.Config.TimeoutMS, EnvKeys: sortedKeys(first.Config.Env),
		HeaderKeys: sortedKeys(first.Config.Headers), Managed: false, Source: "client",
		Warnings: append([]string{}, first.Warnings...),
	}
	for _, entry := range entries {
		hashes[entry.ContentHash] = true
		view.Targets = append(view.Targets, MCPTargetView{
			AgentID: entry.AgentID, AgentName: entry.AgentName, Path: entry.Path,
			Managed: false, Exists: true, Enabled: !entry.Disabled, Transport: entry.Config.Transport,
			ContentHash: entry.ContentHash,
		})
	}
	if len(hashes) == 1 {
		view.ContentHash = first.ContentHash
		view.Status = "untracked"
	} else {
		view.Status = "conflict"
	}
	view.StatusText = mcpStatusText(view.Status)
	sortMCPTargets(view.Targets)
	view.Warnings = uniqueStrings(view.Warnings)
	return view
}

func (m *Manager) mcpDetail(name, agentID string) (MCPDetail, error) {
	overview := m.mcpOverview()
	var view *MCPView
	for index := range overview.Servers {
		if overview.Servers[index].Name == name {
			copy := overview.Servers[index]
			view = &copy
			break
		}
	}
	if view == nil {
		return MCPDetail{}, errors.New("MCP 服务不存在")
	}
	var config MCPServerConfig
	records := m.mcpRecordsSnapshot()
	if record := records[name]; record != nil {
		if agentID != "" {
			return MCPDetail{}, errors.New("已纳管服务只能编辑中心库配置，不能混用客户端来源的敏感值")
		}
		config = record.Config
	} else {
		_, byAgent, _ := m.scanMCPAdapters()
		if agentID == "" && len(view.Targets) > 0 {
			agentID = view.Targets[0].AgentID
		}
		entry, exists := byAgent[agentID][name]
		if !exists {
			return MCPDetail{}, errors.New("所选客户端中没有该 MCP 服务，请刷新后重试")
		}
		config = entry.Config
		view.Transport, view.Command, view.URL = config.Transport, config.Command, config.URL
		view.Args = append([]string{}, config.Args...)
		view.EnvKeys, view.HeaderKeys = sortedKeys(config.Env), sortedKeys(config.Headers)
		view.TimeoutMS = config.TimeoutMS
		view.Warnings = uniqueStrings(append(view.Warnings, entry.Warnings...))
	}
	masked := mcpConfigView(config)
	raw, _ := json.MarshalIndent(mcpCanonicalMap(masked), "", "  ")
	return MCPDetail{View: *view, Config: masked, Raw: string(raw)}, nil
}

func mcpCanonicalMap(config MCPServerConfig) map[string]any {
	out := map[string]any{"transport": config.Transport}
	if config.Command != "" {
		out["command"] = config.Command
	}
	if len(config.Args) > 0 {
		out["args"] = config.Args
	}
	if len(config.Env) > 0 {
		out["env"] = config.Env
	}
	if config.URL != "" {
		out["url"] = config.URL
	}
	if len(config.Headers) > 0 {
		out["headers"] = config.Headers
	}
	if config.TimeoutMS > 0 {
		out["timeoutMs"] = config.TimeoutMS
	}
	return out
}

func (m *Manager) saveMCP(input MCPInput) (map[string]any, error) {
	if !input.Confirmed {
		return nil, errors.New("保存 MCP 配置需要确认目标变更")
	}
	name := strings.TrimSpace(input.Name)
	if input.OriginalName != "" && input.OriginalName != name {
		return nil, errors.New("暂不支持修改 MCP 服务名，请新建后移除旧服务")
	}
	if err := validateMCPName(name); err != nil {
		return nil, err
	}
	config, err := mcpConfigFromInput(input)
	if err != nil {
		return nil, err
	}
	targets := uniqueStrings(input.Targets)
	var warnings []string
	err = m.withOpLock(func() error {
		adapterByID := map[string]mcpAdapter{}
		for _, adapter := range buildMCPAdapters(m.settingsSnapshot()) {
			adapterByID[adapter.ID] = adapter
		}
		previous := m.mcpRecordsSnapshot()[name]
		if previous != nil {
			config = mergeMCPSecrets(previous.Config, config)
		}
		for _, values := range []map[string]string{config.Env, config.Headers} {
			for _, value := range values {
				if value == mcpMaskedSecret {
					return errors.New("新增的环境变量或请求头不能使用掩码，请填写实际值")
				}
			}
		}
		if err := validateMCPConfig(config); err != nil {
			return err
		}
		now := nowString()
		record := &MCPRecord{
			Name: name, Config: mcpServerConfigCopy(config), ContentHash: mcpConfigHash(config),
			Source: "manual", InstalledAt: now, UpdatedAt: now, Targets: map[string]*MCPTarget{},
		}
		if previous != nil {
			record.InstalledAt = previous.InstalledAt
		}
		selected := map[string]bool{}
		var changes []*mcpFileChange
		for _, agentID := range targets {
			adapter, exists := adapterByID[agentID]
			if !exists {
				return fmt.Errorf("未知 MCP 客户端：%s", agentID)
			}
			if !adapter.Detected {
				return fmt.Errorf("%s 未检测到", adapter.Name)
			}
			if err := adapterValidate(adapter, config); err != nil {
				return err
			}
			change, err := m.prepareMCPChange(adapter, name, config, false)
			if err != nil {
				return fmt.Errorf("%s：%w", adapter.Name, err)
			}
			if change.entryExists && !input.Overwrite && !mcpEntryMatchesTarget(change.entry, previous, agentID) {
				return fmt.Errorf("%s 已存在未纳管或本地修改的同名 MCP 配置；请先纳入管理，或明确确认覆盖", adapter.Name)
			}
			selected[agentID] = true
			changes = append(changes, change)
			entry := change.result
			record.Targets[agentID] = &MCPTarget{
				AgentID: agentID, Path: adapter.Path, ConfigKey: name, Transport: entry.Config.Transport,
				ContentHash: entry.ContentHash, RawHash: entry.RawHash, Enabled: !entry.Disabled, UpdatedAt: now,
			}
			if entry.Disabled {
				warnings = append(warnings, adapter.Name+" 的原有停用状态已保留")
			}
		}
		if previous != nil {
			for agentID := range previous.Targets {
				if selected[agentID] {
					continue
				}
				adapter, exists := adapterByID[agentID]
				if !exists {
					return fmt.Errorf("未知 MCP 客户端 %s，无法安全移除目标", agentID)
				}
				change, err := m.prepareMCPChange(adapter, name, config, true)
				if err != nil {
					return fmt.Errorf("%s：%w", adapter.Name, err)
				}
				if change.entryExists && !input.Overwrite && !mcpEntryMatchesTarget(change.entry, previous, agentID) {
					return fmt.Errorf("%s 的配置已被本地修改；确认覆盖后才能从该客户端移除", adapter.Name)
				}
				changes = append(changes, change)
			}
		}
		return m.commitMCPChanges(changes, func() error { return m.replaceMCPRecord(name, record) })
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"success": true, "warnings": uniqueStrings(warnings),
		"message": fmt.Sprintf("MCP 服务 %s 已保存并同步到 %d 个客户端", name, len(targets)),
	}, nil
}

func mcpConfigFromInput(input MCPInput) (MCPServerConfig, error) {
	config := MCPServerConfig{
		Transport: strings.ToLower(strings.TrimSpace(input.Transport)),
		Command:   strings.TrimSpace(input.Command),
		URL:       strings.TrimSpace(input.URL),
		TimeoutMS: input.TimeoutMS,
		Env:       map[string]string{},
		Headers:   map[string]string{},
	}
	config.Args = append([]string{}, input.Args...)
	for key, value := range input.Env {
		key = strings.TrimSpace(key)
		if key != "" {
			config.Env[key] = value
		}
	}
	for key, value := range input.Headers {
		key = strings.TrimSpace(key)
		if key != "" {
			config.Headers[key] = value
		}
	}
	if config.Transport == "" {
		if config.Command != "" {
			config.Transport = "stdio"
		} else if config.URL != "" {
			config.Transport = "http"
		}
	}
	switch config.Transport {
	case "streamable-http", "streamablehttp":
		config.Transport = "http"
	}
	if config.Transport == "stdio" {
		config.URL = ""
		config.Headers = map[string]string{}
		config.Command = normalizeMCPCommandPath(config.Command)
	} else {
		config.Command = ""
		config.Args = nil
		config.Env = map[string]string{}
	}
	if config.TimeoutMS < 0 || config.TimeoutMS > 600000 {
		return MCPServerConfig{}, errors.New("超时必须是 0–600000 毫秒")
	}
	if err := validateMCPConfig(config); err != nil {
		return MCPServerConfig{}, err
	}
	return config, nil
}

func (m *Manager) removeMCP(name string, confirmed bool) (map[string]any, error) {
	if !confirmed {
		return nil, errors.New("删除 MCP 服务需要明确确认")
	}
	name = strings.TrimSpace(name)
	if err := validateMCPName(name); err != nil {
		return nil, err
	}
	err := m.withOpLock(func() error {
		adapterByID := map[string]mcpAdapter{}
		for _, adapter := range buildMCPAdapters(m.settingsSnapshot()) {
			adapterByID[adapter.ID] = adapter
		}
		record := m.mcpRecordsSnapshot()[name]
		if record == nil {
			return errors.New("该 MCP 服务不在中心库中")
		}
		var changes []*mcpFileChange
		for agentID := range record.Targets {
			adapter, exists := adapterByID[agentID]
			if !exists {
				return fmt.Errorf("未知 MCP 客户端 %s，无法安全移除目标", agentID)
			}
			change, err := m.prepareMCPChange(adapter, name, record.Config, true)
			if err != nil {
				return fmt.Errorf("%s：%w", adapter.Name, err)
			}
			if change.entryExists && !mcpEntryMatchesTarget(change.entry, record, agentID) {
				return fmt.Errorf("%s 的 MCP 配置已被本地修改，不能自动删除", adapter.Name)
			}
			changes = append(changes, change)
		}
		return m.commitMCPChanges(changes, func() error { return m.replaceMCPRecord(name, nil) })
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "message": "MCP 服务已从中心库和纳管客户端移除"}, nil
}

func (m *Manager) setMCPTargetEnabled(name, agentID string, enabled, confirmed bool) (map[string]any, error) {
	name = strings.TrimSpace(name)
	agentID = strings.TrimSpace(agentID)
	if err := validateMCPName(name); err != nil {
		return nil, err
	}
	if !enabled && !confirmed {
		return nil, errors.New("停用 MCP 目标需要明确确认")
	}
	adapters := buildMCPAdapters(m.settingsSnapshot())
	adapterByID := map[string]mcpAdapter{}
	for _, adapter := range adapters {
		adapterByID[adapter.ID] = adapter
	}
	adapter, exists := adapterByID[agentID]
	if !exists {
		return nil, fmt.Errorf("未知 MCP 客户端：%s", agentID)
	}
	if !adapter.Detected {
		return nil, fmt.Errorf("%s 未检测到", adapter.Name)
	}
	err := m.withOpLock(func() error {
		record := m.mcpRecordsSnapshot()[name]
		if record == nil {
			return errors.New("该 MCP 服务不在中心库中")
		}
		if enabled {
			if err := adapterValidate(adapter, record.Config); err != nil {
				return err
			}
		} else if record.Targets[agentID] == nil {
			return errors.New("该客户端的 MCP 配置尚未纳入管理，不能停用")
		}
		change, err := m.prepareMCPChange(adapter, name, record.Config, !enabled)
		if err != nil {
			return fmt.Errorf("%s：%w", adapter.Name, err)
		}
		if change.entryExists && !mcpEntryMatchesTarget(change.entry, record, agentID) {
			return fmt.Errorf("%s 已存在未纳管或本地修改的 MCP 配置，请先编辑确认覆盖", adapter.Name)
		}
		if enabled {
			entry := change.result
			if entry.Disabled {
				return fmt.Errorf("%s 中的服务已被原生配置停用，请先在客户端启用；不会自动清除停用标记", adapter.Name)
			}
			record.Targets[agentID] = &MCPTarget{
				AgentID: agentID, Path: adapter.Path, ConfigKey: name, Transport: entry.Config.Transport,
				ContentHash: entry.ContentHash, RawHash: entry.RawHash, Enabled: true, UpdatedAt: nowString(),
			}
		} else {
			delete(record.Targets, agentID)
		}
		return m.commitMCPChanges([]*mcpFileChange{change}, func() error { return m.replaceMCPRecord(name, record) })
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

func (m *Manager) adoptMCP(agentID, name string) (map[string]any, error) {
	agentID = strings.TrimSpace(agentID)
	name = strings.TrimSpace(name)
	if err := validateMCPName(name); err != nil {
		return nil, err
	}
	adapters := buildMCPAdapters(m.settingsSnapshot())
	var adapter *mcpAdapter
	for index := range adapters {
		if adapters[index].ID == agentID {
			copy := adapters[index]
			adapter = &copy
			break
		}
	}
	if adapter == nil {
		return nil, fmt.Errorf("未知 MCP 客户端：%s", agentID)
	}
	if !adapter.Detected {
		return nil, fmt.Errorf("%s 未检测到", adapter.Name)
	}
	err := m.withOpLock(func() error {
		if m.mcpRecordsSnapshot()[name] != nil {
			return errors.New("中心库已有同名 MCP 服务")
		}
		entries, _, err := readMCPAdapter(*adapter)
		if err != nil {
			return err
		}
		entry, exists := entries[name]
		if !exists || entry.Invalid {
			return errors.New("目标客户端中没有可纳入管理的有效 MCP 配置")
		}
		if err := validateMCPConfig(entry.Config); err != nil {
			return err
		}
		now := nowString()
		record := &MCPRecord{
			Name: name, Config: entry.Config, Source: "adopted", ContentHash: entry.ContentHash,
			InstalledAt: now, UpdatedAt: now, Warnings: entry.Warnings,
			Targets: map[string]*MCPTarget{
				agentID: {
					AgentID: agentID, Path: adapter.Path, ConfigKey: name, Transport: entry.Config.Transport,
					ContentHash: entry.ContentHash, RawHash: entry.RawHash, Enabled: !entry.Disabled, UpdatedAt: now,
				},
			},
		}
		return m.replaceMCPRecord(name, record)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "message": "MCP 服务已纳入中心库"}, nil
}

func (m *Manager) importMCPJSON(text string) (MCPImportResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return MCPImportResult{}, errors.New("JSON 内容为空")
	}
	if len(text) > 1024*1024 {
		return MCPImportResult{}, errors.New("JSON 超过 1 MiB")
	}
	adapter := mcpAdapter{ID: "json", Name: "JSON", Kind: "generic"}
	var result MCPImportResult
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		return result, fmt.Errorf("JSON 解析失败：%v", err)
	}
	var servers map[string]json.RawMessage
	if raw, exists := top["mcpServers"]; exists {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return result, errors.New("mcpServers 必须是对象")
		}
	} else {
		name := "imported"
		if raw, exists := top["name"]; exists {
			if value, ok := rawString(raw); ok && value != "" {
				name = value
			}
		}
		servers = map[string]json.RawMessage{name: json.RawMessage(text)}
	}
	for name, raw := range servers {
		config, warnings, disabled, err := parseJSONMCPEntry(adapter, raw)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s：%v", name, err))
			continue
		}
		if disabled {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s：已停用，已跳过", name))
			continue
		}
		if err := validateMCPName(name); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s：%v", name, err))
		}
		result.Servers = append(result.Servers, MCPImportServer{Name: name, Config: config, Warnings: uniqueStrings(warnings)})
	}
	if len(result.Servers) == 0 {
		return result, errors.New("没有可导入的 MCP 服务")
	}
	return result, nil
}
