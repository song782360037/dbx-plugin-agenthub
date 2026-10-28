package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (m *Manager) removeSkill(name string, confirmed bool) (map[string]any, error) {
	if !confirmed {
		return nil, errors.New("删除技能需要明确确认")
	}
	name = strings.TrimSpace(name)
	if !skillNamePattern.MatchString(name) {
		return nil, errors.New("技能名不合法")
	}
	var warnings []string
	err := m.withOpLock(func() error {
		m.mu.Lock()
		record := m.state.Skills[name]
		if record == nil {
			m.mu.Unlock()
			return errors.New("该技能不在中心库中，未纳管的技能请先收编或手动删除")
		}
		targets := make([]TargetRecord, 0, len(record.Targets))
		for _, target := range record.Targets {
			if target.Enabled {
				targets = append(targets, *target)
			}
		}
		m.mu.Unlock()
		hub := filepath.Join(m.hubDir, name)
		checked := map[string]bool{}
		for _, target := range targets {
			if target.Mode == "external" {
				warnings = append(warnings, fmt.Sprintf("%s 中的副本未纳管，已保留：%s", target.AgentID, target.Path))
				continue
			}
			key := strings.ToLower(filepath.Clean(target.Path))
			if checked[key] {
				continue
			}
			checked[key] = true
			if err := m.checkRemovableTarget(name, hub, target.Path); err != nil {
				return err
			}
		}
		for _, target := range targets {
			if target.Mode == "external" {
				continue
			}
			if !pathExistsLstat(target.Path) {
				continue
			}
			key := strings.ToLower(filepath.Clean(target.Path))
			if !checked[key] {
				continue
			}
			checked[key] = false
			if _, err := m.moveToTrash(target.Path, "remove-target-"+target.AgentID+"-"+name); err != nil {
				return fmt.Errorf("移除 %s 失败：%w", target.Path, err)
			}
		}
		if pathExistsLstat(hub) {
			if _, err := m.moveToTrash(hub, "remove-hub-"+name); err != nil {
				return fmt.Errorf("备份中心库失败：%w", err)
			}
		}
		m.mu.Lock()
		delete(m.state.Skills, name)
		m.mu.Unlock()
		return m.saveState()
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "warnings": uniqueStrings(warnings), "message": "技能已移除，文件已移动到插件回收站"}, nil
}

func (m *Manager) checkRemovableTarget(name, hub, path string) error {
	state, err := inspectTarget(path, hub)
	if err != nil {
		return err
	}
	if !state.Exists {
		return nil
	}
	target := filepath.Join(m.hubDir, name)
	if state.IsLink {
		if !samePath(state.LinkTarget, target) {
			return fmt.Errorf("%s 不是本插件创建的链接，不能自动删除", path)
		}
		return nil
	}
	if !state.IsDir {
		return fmt.Errorf("%s 不是技能目录，不能自动删除", path)
	}
	if state.Marker == nil || !isManagedMarker(state.Marker) || state.Marker.Skill != name {
		return fmt.Errorf("%s 未纳管，不能自动删除", path)
	}
	return nil
}

func (m *Manager) setTargetEnabled(name, agentID string, enabled bool, mode string, confirmed bool) (map[string]any, error) {
	name = strings.TrimSpace(name)
	agentID = strings.TrimSpace(agentID)
	if !skillNamePattern.MatchString(name) || !skillNamePattern.MatchString(agentID) {
		return nil, errors.New("技能名或客户端 ID 不合法")
	}
	if !enabled && !confirmed {
		return nil, errors.New("停用客户端目标需要明确确认")
	}
	var warnings []string
	err := m.withOpLock(func() error {
		m.mu.Lock()
		record := m.state.Skills[name]
		if record == nil {
			m.mu.Unlock()
			return errors.New("该技能不在中心库中")
		}
		existing := record.Targets[agentID]
		m.mu.Unlock()
		hub := filepath.Join(m.hubDir, name)
		if !pathHasSkillMD(hub) {
			return errors.New("中心库中的技能文件缺失，请先重新安装")
		}
		if enabled {
			agents := buildAgents(m.settingsSnapshot())
			agent, exists := findAgent(agents, agentID)
			if !exists {
				return fmt.Errorf("未知客户端：%s", agentID)
			}
			if mode == "" {
				if existing != nil && existing.Mode != "external" {
					mode = existing.Mode
				} else {
					mode = m.settingsSnapshot().DefaultMode
				}
			}
			target, targetWarnings, err := m.syncTarget(agent, name, hub, mode, confirmed)
			if err != nil {
				return err
			}
			warnings = append(warnings, targetWarnings...)
			m.setTarget(name, target)
			return m.saveState()
		}
		if existing == nil || existing.Mode == "external" {
			return errors.New("该目标不是由本插件管理的，不能自动停用")
		}
		if !existing.Enabled {
			return nil
		}
		if pathExistsLstat(existing.Path) {
			if err := m.checkRemovableTarget(name, hub, existing.Path); err != nil {
				return err
			}
			if _, err := m.moveToTrash(existing.Path, "disable-"+agentID+"-"+name); err != nil {
				return err
			}
		}
		m.mu.Lock()
		existing.Enabled = false
		existing.UpdatedAt = nowString()
		m.mu.Unlock()
		return m.saveState()
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "warnings": uniqueStrings(warnings)}, nil
}

func (m *Manager) adopt(agentID, name string, link bool) (map[string]any, error) {
	agentID = strings.TrimSpace(agentID)
	name = strings.TrimSpace(name)
	if !skillNamePattern.MatchString(agentID) || name == "" {
		return nil, errors.New("客户端 ID 或技能名不合法")
	}
	agents := buildAgents(m.settingsSnapshot())
	agent, exists := findAgent(agents, agentID)
	if !exists {
		return nil, fmt.Errorf("未知客户端：%s", agentID)
	}
	entries, _ := scanAgent(agent)
	var entry *ScanEntry
	for index := range entries {
		if entries[index].Name == name {
			copy := entries[index]
			entry = &copy
			break
		}
		if entry == nil && entries[index].DirName == name {
			copy := entries[index]
			entry = &copy
		}
	}
	if entry == nil {
		return nil, errors.New("没有在目标客户端中找到该技能")
	}
	if !skillNamePattern.MatchString(entry.Name) {
		return nil, fmt.Errorf("技能名 %q 不符合规范，不能收编", entry.Name)
	}
	var warnings []string
	err := m.withOpLock(func() error {
		m.mu.Lock()
		if _, exists := m.state.Skills[entry.Name]; exists {
			m.mu.Unlock()
			return errors.New("中心库已有同名技能")
		}
		m.mu.Unlock()
		hub := filepath.Join(m.hubDir, entry.Name)
		if pathExistsLstat(hub) {
			return errors.New("中心库已有同名目录但未记录，请先手动处理")
		}
		stage := filepath.Join(m.tmpDir, "adopt-"+entry.Name+"-"+randomID(6))
		defer os.RemoveAll(stage)
		sourcePath := entry.Path
		if resolved, err := filepath.EvalSymlinks(entry.Path); err == nil {
			sourcePath = resolved
		}
		if _, _, err := copyTree(sourcePath, stage, copyLimits{MaxFiles: 10000, MaxBytes: 256 * 1024 * 1024}, true); err != nil {
			return err
		}
		hash, files, size, hashWarnings, err := hashDir(stage)
		if err != nil {
			return err
		}
		source := Source{Type: "adopted", LocalPath: entry.Path, Label: entry.AgentName}
		marker := Marker{ManagedBy: appID, Skill: entry.Name, HubPath: hub, Source: source, ContentHash: hash, InstalledAt: nowString()}
		if err := writeJSONFile(filepath.Join(stage, markerName), marker); err != nil {
			return err
		}
		risk := []Finding(nil)
		if m.settingsSnapshot().ScanLevel != "off" {
			risk = scanRisk(stage, files)
		}
		if err := os.Rename(stage, hub); err != nil {
			return err
		}
		now := nowString()
		record := &Record{
			Name: entry.Name, Description: entry.Description, Source: source,
			ContentHash: hash, Files: files, Size: size, InstalledAt: now, UpdatedAt: now,
			Risk: risk, Warnings: uniqueStrings(append(entry.Warnings, hashWarnings...)),
			Targets: map[string]*TargetRecord{
				agentID: {AgentID: agentID, Path: entry.Path, Mode: "external", ContentHash: entry.ContentHash, Enabled: true, Scope: "user", UpdatedAt: now},
			},
		}
		m.mu.Lock()
		m.state.Skills[entry.Name] = record
		m.mu.Unlock()
		if err := m.saveState(); err != nil {
			return err
		}
		if link {
			target, targetWarnings, err := m.syncTarget(agent, entry.Name, hub, "link", true)
			if err != nil {
				return fmt.Errorf("收编成功，但替换为链接失败：%w", err)
			}
			warnings = append(warnings, targetWarnings...)
			m.setTarget(entry.Name, target)
			if err := m.saveState(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "warnings": uniqueStrings(warnings), "message": "技能已纳入中心库"}, nil
}

func (m *Manager) readSkillFile(name, rel string) (map[string]any, error) {
	if !skillNamePattern.MatchString(name) {
		return nil, errors.New("技能名不合法")
	}
	root := filepath.Join(m.hubDir, name)
	if !pathHasSkillMD(root) {
		agents := buildAgents(m.settingsSnapshot())
		entries, _ := scanAllAgents(agents)
		found := false
		for _, entry := range entries {
			if entry.Name == name {
				root = entry.Path
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("技能不在中心库或客户端目录中")
		}
	}
	target, err := safeJoin(root, rel)
	if err != nil {
		return nil, err
	}
	if lstat, err := os.Lstat(target); err == nil && lstat.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("不预览符号链接文件")
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("文件不存在")
	}
	if info.Size() > 1024*1024 {
		return nil, errors.New("文件超过 1 MiB，工作台不提供在线预览")
	}
	binary := isBinaryFile(target)
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"path": rel, "binary": binary, "size": info.Size()}
	if binary {
		result["content"] = ""
	} else {
		result["content"] = string(data)
	}
	return result, nil
}

func (m *Manager) skillDetail(name string) (map[string]any, error) {
	overviewState := m.stateSnapshot()
	agents := buildAgents(overviewState.Settings)
	overview := buildOverview(&overviewState, agents, m.hubDir, m.initWarnings())
	var view *SkillView
	for index := range overview.Skills {
		if overview.Skills[index].Name == name {
			copy := overview.Skills[index]
			view = &copy
			break
		}
	}
	if view == nil {
		return nil, errors.New("技能不存在")
	}
	content := ""
	files := []FileEntry(nil)
	if view.Managed {
		hub := filepath.Join(m.hubDir, name)
		data, readErr := os.ReadFile(filepath.Join(hub, "SKILL.md"))
		if readErr == nil {
			if len(data) > 2*1024*1024 {
				data = data[:2*1024*1024]
			}
			content = string(data)
			if _, body, ok := splitFrontmatter(content); ok {
				content = body
			}
		}
		_, files, _, _, _ = hashDir(hub)
	} else {
		entries, _ := scanAllAgents(agents)
		for _, entry := range entries {
			if entry.Name == name {
				data, readErr := os.ReadFile(filepath.Join(entry.Path, "SKILL.md"))
				if readErr == nil {
					if len(data) > 2*1024*1024 {
						data = data[:2*1024*1024]
					}
					content = string(data)
					if _, body, ok := splitFrontmatter(content); ok {
						content = body
					}
				}
				files = entry.Files
				break
			}
		}
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return map[string]any{"skill": view, "content": content, "files": files}, nil
}
