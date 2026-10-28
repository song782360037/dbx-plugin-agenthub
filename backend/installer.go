package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type targetState struct {
	Exists     bool
	IsDir      bool
	IsLink     bool
	LinkTarget string
	Managed    bool
	Foreign    bool
	Marker     *Marker
	Hash       string
}

func (m *Manager) install(input InstallInput) (*InstallResult, error) {
	preview, ok := m.getPreview(input.PreviewID)
	if !ok {
		return nil, errors.New("来源预览已过期，请重新解析来源")
	}
	settings := m.settingsSnapshot()
	names := uniqueStrings(input.Names)
	if len(names) == 0 {
		return nil, errors.New("请选择至少一个技能")
	}
	if len(names) == 1 && names[0] == "*" {
		names = names[:0]
		for _, candidate := range preview.Candidates {
			names = append(names, candidate.Name)
		}
	}
	selected, err := indexCandidates(preview.Candidates)
	if err != nil {
		return nil, err
	}
	var chosen []Candidate
	for _, name := range names {
		candidate, exists := selected[name]
		if !exists {
			return nil, fmt.Errorf("来源中不存在技能 %s", name)
		}
		if err := validateCandidate(candidate); err != nil {
			return nil, err
		}
		if hasCriticalRisk(candidate.Risk) && !input.AcknowledgeRisk {
			return nil, fmt.Errorf("技能 %s 包含高危风险项，请查看风险详情并明确确认后再安装", name)
		}
		chosen = append(chosen, candidate)
	}
	mode := strings.TrimSpace(input.Mode)
	if mode == "" {
		mode = settings.DefaultMode
	}
	if mode != "auto" && mode != "link" && mode != "copy" {
		return nil, errors.New("同步方式只能是 auto、link 或 copy")
	}
	agents := buildAgents(settings)
	targets := make([]AgentSpec, 0, len(input.Targets))
	for _, id := range uniqueStrings(input.Targets) {
		agent, exists := findAgent(agents, id)
		if !exists {
			return nil, fmt.Errorf("未知客户端：%s", id)
		}
		if withinRoot(m.hubDir, agent.Path) || filepath.Clean(agent.Path) == filepath.Clean(m.hubDir) {
			return nil, fmt.Errorf("客户端 %s 的技能目录不能位于中心库内", agent.Name)
		}
		targets = append(targets, agent)
	}

	var result InstallResult
	err = m.withOpLock(func() error {
		// Preflight every selected skill before writing anything.
		for _, candidate := range chosen {
			if err := verifyReviewedCandidate(preview, candidate, input.AcknowledgeRisk); err != nil {
				return err
			}
			if err := m.preflightInstall(candidate, targets, input.Overwrite); err != nil {
				return fmt.Errorf("%s：%w", candidate.Name, err)
			}
		}
		for _, candidate := range chosen {
			installedTargets, warnings, err := m.installCandidate(preview, candidate, targets, mode, input.Overwrite, input.AcknowledgeRisk)
			if err != nil {
				return fmt.Errorf("安装 %s 失败：%w", candidate.Name, err)
			}
			result.Installed = append(result.Installed, candidate.Name)
			result.Targets += installedTargets
			result.Warnings = append(result.Warnings, warnings...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.Warnings = uniqueStrings(result.Warnings)
	result.Message = fmt.Sprintf("已安装 %d 个技能，同步 %d 个客户端目标", len(result.Installed), result.Targets)
	if len(result.Warnings) > 0 {
		result.Message += fmt.Sprintf("；有 %d 条提示", len(result.Warnings))
	}
	return &result, nil
}

func validateCandidate(candidate Candidate) error {
	if !skillNamePattern.MatchString(candidate.Name) {
		return fmt.Errorf("技能名 %q 不符合规范：只能使用小写字母、数字和连字符，且不能以连字符开头或结尾", candidate.Name)
	}
	if len(candidate.Name) > 64 {
		return fmt.Errorf("技能名 %q 超过 64 字符", candidate.Name)
	}
	if len(candidate.Description) > 1024 {
		return fmt.Errorf("技能 %s 的 description 超过 1024 字符", candidate.Name)
	}
	return nil
}

func verifyReviewedCandidate(preview *Preview, candidate Candidate, acknowledgeRisk bool) error {
	if time.Since(preview.CreatedAt) > 2*time.Hour {
		return errors.New("来源预览已过期，请重新预览")
	}
	if err := validateCandidate(candidate); err != nil {
		return err
	}
	if hasCriticalRisk(candidate.Risk) && !acknowledgeRisk {
		return fmt.Errorf("技能 %s 包含高危风险项，请查看风险详情并明确确认", candidate.Name)
	}
	dir := preview.Root
	if candidate.RelPath != "" {
		var err error
		dir, err = safeJoin(preview.Root, candidate.RelPath)
		if err != nil {
			return err
		}
	}
	hash, _, _, _, err := hashDir(dir)
	if err != nil {
		return err
	}
	if candidate.ContentHash == "" || hash != candidate.ContentHash {
		return fmt.Errorf("技能 %s 的预览内容已变化，请重新预览并确认风险", candidate.Name)
	}
	return nil
}

func (m *Manager) preflightInstall(candidate Candidate, targets []AgentSpec, overwrite bool) error {
	if err := validateCandidate(candidate); err != nil {
		return err
	}
	hub := filepath.Join(m.hubDir, candidate.Name)
	m.mu.Lock()
	record := m.state.Skills[candidate.Name]
	baseline := ""
	var existingTargets []TargetRecord
	if record != nil {
		baseline = record.ContentHash
		for _, target := range record.Targets {
			existingTargets = append(existingTargets, *target)
		}
	}
	m.mu.Unlock()
	if err := checkHubDrift(hub, baseline, overwrite); err != nil {
		return err
	}
	paths := map[string]string{}
	for _, target := range existingTargets {
		if target.Enabled && target.Mode != "external" {
			paths[filepath.Clean(target.Path)] = target.ContentHash
		}
	}
	for _, agent := range targets {
		path := m.skillTargetPath(agent, candidate.Name)
		if samePath(path, hub) || withinRoot(m.hubDir, path) {
			return fmt.Errorf("客户端 %s 的技能目录不能位于中心库内", agent.Name)
		}
		if _, exists := paths[path]; !exists {
			paths[path] = baseline
		}
	}
	for path, targetBaseline := range paths {
		state, err := inspectTarget(path, hub)
		if err != nil {
			return err
		}
		if !state.Exists {
			continue
		}
		if state.IsLink {
			if state.Managed {
				continue
			}
			if !overwrite {
				return fmt.Errorf("%s 已存在一个未纳管的链接，确认覆盖或先手动处理", path)
			}
			continue
		}
		if !state.IsDir {
			return fmt.Errorf("%s 已存在但不是目录，无法自动处理", path)
		}
		if state.Managed {
			if !overwrite && (targetBaseline == "" || state.Hash != targetBaseline) {
				return fmt.Errorf("%s 的副本已被本地修改；确认覆盖后才能更新", path)
			}
			continue
		}
		if !overwrite {
			return fmt.Errorf("%s 已存在未纳管的技能目录，确认覆盖或先收编后再安装", path)
		}
	}
	return nil
}

func (m *Manager) installCandidate(preview *Preview, candidate Candidate, targets []AgentSpec, mode string, overwrite, acknowledgeRisk bool) (int, []string, error) {
	if err := verifyReviewedCandidate(preview, candidate, acknowledgeRisk); err != nil {
		return 0, nil, err
	}
	source := preview.Source
	source.Path = candidate.RelPath
	sourceDir := preview.Root
	if candidate.RelPath != "" {
		joined, err := safeJoin(preview.Root, candidate.RelPath)
		if err != nil {
			return 0, nil, err
		}
		sourceDir = joined
	}
	stage := filepath.Join(m.tmpDir, "install-"+candidate.Name+"-"+randomID(6))
	defer os.RemoveAll(stage)
	if _, _, err := copyTree(sourceDir, stage, copyLimits{MaxFiles: 10000, MaxBytes: 256 * 1024 * 1024}, true); err != nil {
		return 0, nil, err
	}
	hash, files, size, hashWarnings, err := hashDir(stage)
	if err != nil {
		return 0, nil, err
	}
	if candidate.ContentHash == "" || candidate.ContentHash != hash {
		return 0, nil, errors.New("安装内容与已审核预览不一致，请重新预览并确认风险")
	}
	if err := m.preflightInstall(candidate, targets, overwrite); err != nil {
		return 0, nil, err
	}
	marker := Marker{
		ManagedBy: appID, Skill: candidate.Name, HubPath: filepath.Join(m.hubDir, candidate.Name),
		Source: source, ContentHash: hash, InstalledAt: nowString(),
	}
	if err := writeJSONFile(filepath.Join(stage, markerName), marker); err != nil {
		return 0, nil, err
	}
	hub := filepath.Join(m.hubDir, candidate.Name)
	var backup string
	if pathExistsLstat(hub) {
		backup, err = m.moveToTrash(hub, "hub-"+candidate.Name)
		if err != nil {
			return 0, nil, fmt.Errorf("无法备份旧版本：%w", err)
		}
	}
	if err := os.MkdirAll(m.hubDir, 0o755); err != nil {
		return 0, nil, err
	}
	if err := os.Rename(stage, hub); err != nil {
		if backup != "" {
			_ = os.Rename(backup, hub)
		}
		return 0, nil, fmt.Errorf("写入中心库失败：%w", err)
	}

	m.mu.Lock()
	record := m.state.Skills[candidate.Name]
	if record == nil {
		record = &Record{Name: candidate.Name, Targets: map[string]*TargetRecord{}, InstalledAt: nowString()}
		m.state.Skills[candidate.Name] = record
	}
	record.Name = candidate.Name
	record.Description = candidate.Description
	record.Source = source
	record.ContentHash = hash
	record.Files = files
	record.Size = size
	record.UpdatedAt = nowString()
	record.Risk = candidate.Risk
	record.Warnings = uniqueStrings(append(append(append([]string{}, candidate.Warnings...), hashWarnings...), preview.Warnings...))
	record.Available = nil
	if record.Targets == nil {
		record.Targets = map[string]*TargetRecord{}
	}
	existingTargets := make([]TargetRecord, 0, len(record.Targets))
	for _, target := range record.Targets {
		existingTargets = append(existingTargets, *target)
	}
	m.mu.Unlock()
	// Persist the new hub and record before touching client directories; a failed
	// link/copy can then be repaired on the next scan without losing provenance.
	if err := m.saveSkillRecord(candidate.Name); err != nil {
		return 0, nil, err
	}

	var warnings []string
	warnings = append(warnings, preview.Warnings...)
	count := 0
	selectedIDs := map[string]bool{}
	for _, agent := range targets {
		selectedIDs[agent.ID] = true
	}
	for _, target := range existingTargets {
		if selectedIDs[target.AgentID] {
			continue
		}
		if !target.Enabled || target.Mode == "external" {
			continue
		}
		if _, exists := findAgentByPath(targets, target.AgentID); exists {
			continue
		}
		// Re-sync existing managed copy targets so a hub update does not leave stale copies.
		agent, exists := findAgent(buildAgents(m.settingsSnapshot()), target.AgentID)
		if !exists {
			warnings = append(warnings, "记录中的客户端已不存在："+target.AgentID)
			continue
		}
		updated, targetWarnings, err := m.syncTarget(agent, candidate.Name, hub, target.Mode, overwrite)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s 重新同步失败：%v", agent.Name, err))
			continue
		}
		warnings = append(warnings, targetWarnings...)
		m.setTarget(candidate.Name, updated)
		count++
	}
	for _, agent := range targets {
		updated, targetWarnings, err := m.syncTarget(agent, candidate.Name, hub, mode, overwrite)
		if err != nil {
			return count, warnings, err
		}
		warnings = append(warnings, targetWarnings...)
		m.setTarget(candidate.Name, updated)
		count++
	}
	if err := m.saveSkillRecord(candidate.Name); err != nil {
		return count, warnings, err
	}
	return count, uniqueStrings(warnings), nil
}

func findAgentByPath(agents []AgentSpec, id string) (AgentSpec, bool) {
	return findAgent(agents, id)
}

func (m *Manager) setTarget(skill string, target TargetRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.state.Skills[skill]
	if record == nil {
		return
	}
	if record.Targets == nil {
		record.Targets = map[string]*TargetRecord{}
	}
	copy := target
	record.Targets[target.AgentID] = &copy
}

func (m *Manager) saveSkillRecord(skill string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.state.Skills[skill]; !exists {
		return nil
	}
	return m.saveStateLocked()
}

// skillTargetPath retains an adopted folder name within the current client directory.
func (m *Manager) skillTargetPath(agent AgentSpec, name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if record := m.state.Skills[name]; record != nil {
		if target := record.Targets[agent.ID]; target != nil && samePath(filepath.Dir(target.Path), agent.Path) {
			return filepath.Clean(target.Path)
		}
	}
	return filepath.Join(agent.Path, name)
}

func (m *Manager) syncTarget(agent AgentSpec, name, hub, mode string, overwrite bool) (TargetRecord, []string, error) {
	targetPath := m.skillTargetPath(agent, name)
	if filepath.Clean(targetPath) == filepath.Clean(hub) {
		return TargetRecord{}, nil, errors.New("目标目录与中心库相同")
	}
	if withinRoot(hub, targetPath) {
		return TargetRecord{}, nil, errors.New("目标目录不能位于中心库内")
	}
	switch mode {
	case "link", "junction":
		mode = "link"
	case "copy", "auto":
	default:
		mode = "auto"
	}
	if err := os.MkdirAll(agent.Path, 0o755); err != nil {
		return TargetRecord{}, nil, fmt.Errorf("无法创建客户端目录 %s：%w", agent.Path, err)
	}
	state, err := inspectTarget(targetPath, hub)
	if err != nil {
		return TargetRecord{}, nil, err
	}
	hubHash, _, _, _, err := hashDir(hub)
	if err != nil {
		return TargetRecord{}, nil, err
	}
	if state.Exists && !overwrite {
		if !state.Managed {
			return TargetRecord{}, nil, fmt.Errorf("%s 已存在未纳管的技能目录或链接；确认覆盖后才能继续", targetPath)
		}
		if !state.IsLink {
			m.mu.Lock()
			baseline := ""
			if record := m.state.Skills[name]; record != nil {
				baseline = record.ContentHash
				if target := record.Targets[agent.ID]; target != nil && samePath(target.Path, targetPath) {
					baseline = target.ContentHash
				}
			}
			m.mu.Unlock()
			if baseline == "" || state.Hash != baseline {
				return TargetRecord{}, nil, fmt.Errorf("%s 的副本已被本地修改；确认覆盖后才能继续", targetPath)
			}
		}
	}
	effective := mode
	if effective == "auto" && state.Exists && state.Managed {
		switch state.LinkTarget {
		case "":
			effective = "copy"
		default:
			effective = "link"
		}
	}
	if effective == "auto" {
		effective = "link"
	}
	var warnings []string
	if state.Exists {
		if effective == "link" && state.IsLink && state.Managed && samePath(state.LinkTarget, hub) {
			return TargetRecord{AgentID: agent.ID, Path: targetPath, Mode: "link", ContentHash: hubHash, Enabled: true, Scope: "user", UpdatedAt: nowString()}, nil, nil
		}
		if effective == "copy" && !state.IsLink && state.Managed {
			if state.Hash == hubHash {
				return TargetRecord{AgentID: agent.ID, Path: targetPath, Mode: "copy", ContentHash: hubHash, Enabled: true, Scope: "user", UpdatedAt: nowString()}, nil, nil
			}
		}
		if !state.Managed && !overwrite {
			return TargetRecord{}, nil, fmt.Errorf("%s 已存在未纳管的技能目录", targetPath)
		}
		if state.IsLink && !state.Managed && !overwrite {
			return TargetRecord{}, nil, fmt.Errorf("%s 已存在未纳管的链接", targetPath)
		}
		if _, err := m.moveToTrash(targetPath, "target-"+agent.ID+"-"+name); err != nil {
			return TargetRecord{}, nil, fmt.Errorf("无法备份旧目标 %s：%w", targetPath, err)
		}
	}
	temp := targetPath + ".dbx-tmp-" + randomID(4)
	_ = os.RemoveAll(temp)
	defer os.RemoveAll(temp)
	actualMode := effective
	if effective == "link" {
		linkMode, err := createDirLink(hub, temp)
		if err != nil {
			if mode != "auto" {
				return TargetRecord{}, nil, fmt.Errorf("创建链接失败：%w", err)
			}
			warnings = append(warnings, fmt.Sprintf("%s 无法创建链接，已回退为副本：%v", agent.Name, err))
			actualMode = "copy"
			_ = os.RemoveAll(temp)
		} else {
			actualMode = linkMode
		}
	}
	if actualMode == "copy" {
		if _, _, err := copyTree(hub, temp, copyLimits{MaxFiles: 10000, MaxBytes: 256 * 1024 * 1024}, false); err != nil {
			return TargetRecord{}, warnings, fmt.Errorf("复制技能失败：%w", err)
		}
	}
	if err := os.Rename(temp, targetPath); err != nil {
		return TargetRecord{}, warnings, fmt.Errorf("写入目标目录失败：%w", err)
	}
	return TargetRecord{
		AgentID: agent.ID, Path: targetPath, Mode: actualMode,
		ContentHash: hubHash, Enabled: true, Scope: "user", UpdatedAt: nowString(),
	}, warnings, nil
}

func createDirLink(target, link string) (string, error) {
	if err := os.Symlink(target, link); err == nil {
		if pathHasSkillMD(link) {
			return "link", nil
		}
		_ = os.Remove(link)
	}
	if runtime.GOOS != "windows" {
		return "", errors.New("无法创建符号链接")
	}
	command := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if output, err := command.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s", sanitizeLabel(strings.TrimSpace(string(output)), 200))
	}
	if pathHasSkillMD(link) {
		return "junction", nil
	}
	_ = os.Remove(link)
	return "", errors.New("目录联接校验失败")
}

func inspectTarget(path, hub string) (targetState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return targetState{}, nil
	}
	if err != nil {
		return targetState{}, err
	}
	state := targetState{Exists: true}
	if target, ok := detectDirLink(path); ok {
		state.IsLink = true
		state.LinkTarget = target
		state.Managed = samePath(hub, target)
		state.IsDir = existingDir(path)
		return state, nil
	}
	if !info.IsDir() {
		return state, nil
	}
	state.IsDir = true
	marker, foreign, _ := readMarker(path)
	state.Marker = marker
	state.Foreign = foreign
	state.Managed = isManagedMarker(marker) && marker.Skill == filepath.Base(hub) && samePath(marker.HubPath, hub)
	if state.Managed {
		hash, _, _, _, err := hashDir(path)
		if err != nil {
			return state, fmt.Errorf("无法校验目标 %s：%w", path, err)
		}
		state.Hash = hash
	}
	return state, nil
}

func hashDirQuiet(path string) string {
	hash, _, _, _, err := hashDir(path)
	if err != nil {
		return ""
	}
	return hash
}

func pathExistsLstat(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (m *Manager) moveToTrash(src, label string) (string, error) {
	label = slugify(label)
	if label == "" {
		label = "item"
	}
	dest := filepath.Join(m.trashDir, fmt.Sprintf("%s-%d-%s", label, time.Now().Unix(), randomID(3)))
	if err := os.Rename(src, dest); err == nil {
		return dest, nil
	}
	info, err := os.Lstat(src)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return "", err
		}
		if err := os.Symlink(target, dest); err != nil {
			return "", err
		}
		return dest, os.Remove(src)
	}
	if info.IsDir() {
		if _, _, err := copyTreeFiltered(src, dest, copyLimits{MaxFiles: 10000, MaxBytes: 512 * 1024 * 1024}, false, false); err != nil {
			return "", err
		}
		return dest, os.RemoveAll(src)
	}
	if err := copyFile(src, dest); err != nil {
		return "", err
	}
	return dest, os.Remove(src)
}
