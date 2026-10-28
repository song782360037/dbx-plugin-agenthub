package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxHashFiles = 6000
	maxHashBytes = 128 * 1024 * 1024
)

func hashDir(root string) (string, []FileEntry, int64, []string, error) {
	root = filepath.Clean(root)
	walkRoot := root
	if target, ok := detectDirLink(root); ok {
		walkRoot = filepath.Clean(target)
	} else if resolved, err := filepath.EvalSymlinks(root); err == nil {
		walkRoot = filepath.Clean(resolved)
	}
	var files []FileEntry
	var total int64
	var warnings []string
	count := 0
	err := filepath.WalkDir(walkRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == walkRoot {
			return nil
		}
		rel, err := relSlash(walkRoot, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isMarkerName(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		count++
		if count > maxHashFiles {
			return fmt.Errorf("文件数量超过扫描限制（%d）", maxHashFiles)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				target = "?"
			}
			files = append(files, FileEntry{Path: rel, Size: 0, SHA256: "link:" + target, Binary: false})
			warnings = append(warnings, "技能包含符号链接："+rel+"，已按链接目标文本参与校验")
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("技能包含不支持的文件类型：%s", rel)
		}
		total += info.Size()
		if total > maxHashBytes {
			return fmt.Errorf("技能体积超过扫描限制（%d MiB）", maxHashBytes/1024/1024)
		}
		hash, err := hashFileBounded(path, info.Size())
		if err != nil {
			return err
		}
		files = append(files, FileEntry{
			Path: rel, Size: info.Size(), SHA256: hash,
			Executable: isExecutable(info.Mode()), Binary: isBinaryFile(path),
		})
		return nil
	})
	if err != nil {
		return "", nil, 0, warnings, err
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	hash := sha256.New()
	for _, file := range files {
		fmt.Fprintf(hash, "%s\x00%d\x00%s\x00%t\n", file.Path, file.Size, file.SHA256, file.Executable)
	}
	return hex.EncodeToString(hash.Sum(nil)), files, total, warnings, nil
}

func hashFileBounded(path string, size int64) (string, error) {
	if size < 0 || size > maxHashBytes {
		return "", fmt.Errorf("文件超过校验限制：%s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	// Stream every byte, while bounding reads if a file grows during the scan.
	read, err := io.Copy(hash, io.LimitReader(file, size+1))
	if err != nil {
		return "", err
	}
	if read != size {
		return "", fmt.Errorf("校验期间文件大小发生变化：%s", path)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readMarker(dir string) (*Marker, bool, []string) {
	var warnings []string
	markerPath := filepath.Join(dir, markerName)
	if !fileExists(markerPath) {
		markerPath = filepath.Join(dir, legacyMarkerName)
	}
	if data, err := os.ReadFile(markerPath); err == nil {
		var marker Marker
		if json.Unmarshal(data, &marker) == nil && marker.Skill != "" {
			return &marker, false, warnings
		}
		warnings = append(warnings, "无法解析 "+filepath.Base(markerPath)+"，按未纳管技能处理")
	}
	foreign := false
	for _, name := range []string{".ccgui-managed.json", ".skills-manager-source.json", "_meta.json", "_skillhub_meta.json"} {
		if fileExists(filepath.Join(dir, name)) {
			foreign = true
			break
		}
	}
	return nil, foreign, warnings
}

func scanAgent(agent AgentSpec) ([]ScanEntry, []string) {
	if !existingDir(agent.Path) {
		return nil, nil
	}
	entries, err := os.ReadDir(agent.Path)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s：无法读取 %s：%v", agent.Name, agent.Path, err)}
	}
	var out []ScanEntry
	var warnings []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		dir := filepath.Join(agent.Path, name)
		info, statErr := os.Stat(dir)
		if statErr != nil || !info.IsDir() {
			continue
		}
		if !pathHasSkillMD(dir) {
			continue
		}
		meta, err := parseSkillMD(filepath.Join(dir, "SKILL.md"), name)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s/%s：%v", agent.Name, name, err))
			continue
		}
		hash, files, size, hashWarnings, err := hashDir(dir)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s/%s：%v", agent.Name, name, err))
			continue
		}
		marker, foreign, markerWarnings := readMarker(dir)
		isLink := false
		linkTarget := ""
		if resolved, ok := detectDirLink(dir); ok {
			isLink = true
			linkTarget = resolved
		}
		entryWarnings := append([]string{}, meta.Warnings...)
		entryWarnings = append(entryWarnings, hashWarnings...)
		entryWarnings = append(entryWarnings, markerWarnings...)
		if isLink && linkTarget != "" && !withinRoot(filepath.Dir(agent.Path), linkTarget) {
			// A link outside the client tree is allowed for reading, but it is not managed by us.
		}
		out = append(out, ScanEntry{
			AgentID: agent.ID, AgentName: agent.Name, Path: dir, DirName: name,
			Name: meta.Name, Description: meta.Description, ContentHash: hash,
			Files: files, Size: size, Marker: marker, ForeignMark: foreign,
			IsLink: isLink, LinkTarget: linkTarget, Warnings: entryWarnings,
		})
	}
	return out, warnings
}

func scanAllAgents(agents []AgentSpec) ([]ScanEntry, []string) {
	var entries []ScanEntry
	var warnings []string
	seenPaths := map[string]string{}
	for _, agent := range agents {
		if !agent.Detected && !existingDir(agent.Path) {
			continue
		}
		clean := strings.ToLower(filepath.Clean(agent.Path))
		if previous, ok := seenPaths[clean]; ok {
			warnings = append(warnings, fmt.Sprintf("%s 与 %s 使用同一技能目录，已跳过重复扫描：%s", agent.Name, previous, agent.Path))
			continue
		}
		seenPaths[clean] = agent.Name
		agentEntries, agentWarnings := scanAgent(agent)
		entries = append(entries, agentEntries...)
		warnings = append(warnings, agentWarnings...)
	}
	return entries, warnings
}

func statusText(status string) string {
	switch status {
	case "synced":
		return "已同步"
	case "partial":
		return "部分同步"
	case "update":
		return "有更新"
	case "modified":
		return "本地已修改"
	case "conflict":
		return "冲突"
	case "untracked":
		return "未纳管"
	case "hub_only":
		return "仅中心库"
	case "missing":
		return "中心库缺失"
	default:
		return "待检查"
	}
}

func buildOverview(state *State, agents []AgentSpec, hubPath string, scanWarnings []string) Overview {
	entries, entryWarnings := scanAllAgents(agents)
	scanWarnings = append(scanWarnings, entryWarnings...)
	byName := map[string][]ScanEntry{}
	for _, entry := range entries {
		byName[entry.Name] = append(byName[entry.Name], entry)
	}
	views := make([]SkillView, 0, len(state.Skills)+len(byName))
	agentNames := map[string]string{}
	for _, agent := range agents {
		agentNames[agent.ID] = agent.Name
	}
	managedNames := map[string]bool{}
	for name, record := range state.Skills {
		managedNames[name] = true
		views = append(views, buildManagedView(record, byName[name], hubPath, agentNames))
	}
	for name, scanEntries := range byName {
		if managedNames[name] {
			continue
		}
		views = append(views, buildUntrackedView(name, scanEntries))
	}
	sort.Slice(views, func(left, right int) bool {
		if views[left].Managed != views[right].Managed {
			return views[left].Managed
		}
		return strings.ToLower(views[left].Name) < strings.ToLower(views[right].Name)
	})
	stats := Stats{Managed: len(state.Skills)}
	for _, view := range views {
		if !view.Managed {
			stats.Untracked++
		}
		switch view.Status {
		case "synced":
			stats.Synced++
		case "untracked":
			// Counted as untracked above; no separate attention bucket.
		case "partial", "update", "modified", "conflict", "missing", "hub_only":
			stats.NeedsAttention++
		}
		for _, target := range view.Targets {
			if target.Enabled {
				stats.InstalledTargets++
			}
		}
	}
	for _, agent := range agents {
		if agent.Detected {
			stats.DetectedAgents++
		}
	}
	return Overview{
		Agents: agents, Skills: views, Stats: stats,
		Settings: settingsView(state.Settings), HubPath: hubPath,
		Warnings: uniqueStrings(scanWarnings), ScannedAt: nowString(),
	}
}

func buildManagedView(record *Record, entries []ScanEntry, hubPath string, agentNames map[string]string) SkillView {
	view := SkillView{
		Name: record.Name, Description: record.Description, Managed: true,
		Source: &record.Source, ContentHash: record.ContentHash, Files: len(record.Files),
		Size: record.Size, UpdatedAt: record.UpdatedAt, Available: record.Available,
		Risk: record.Risk, Warnings: append([]string{}, record.Warnings...),
		HubPath: filepath.Join(hubPath, record.Name),
	}
	entryByAgent := map[string]ScanEntry{}
	for _, entry := range entries {
		entryByAgent[entry.AgentID] = entry
	}
	seenPaths := map[string]bool{}
	for agentID, target := range record.Targets {
		entry, found := entryByAgent[agentID]
		if found {
			delete(entryByAgent, agentID)
		}
		managed := found && (target.Mode == "link" || target.Mode == "junction" || isManagedMarker(entry.Marker))
		modified := found && entry.ContentHash != record.ContentHash
		view.Targets = append(view.Targets, TargetView{
			AgentID: agentID, AgentName: displayAgentName(entry, agentID, agentNames), Path: target.Path,
			Mode: target.Mode, Managed: managed, Tracked: true, Exists: found, Enabled: target.Enabled,
			ContentHash: contentHashOf(entry), Modified: modified, LinkTarget: linkTargetOf(entry),
		})
		seenPaths[strings.ToLower(filepath.Clean(target.Path))] = true
	}
	for _, entry := range entryByAgent {
		if seenPaths[strings.ToLower(filepath.Clean(entry.Path))] {
			continue
		}
		view.Targets = append(view.Targets, TargetView{
			AgentID: entry.AgentID, AgentName: entry.AgentName, Path: entry.Path,
			Mode: "external", Managed: false, Exists: true,
			Enabled: true, ContentHash: entry.ContentHash,
			Modified: entry.ContentHash != record.ContentHash, LinkTarget: entry.LinkTarget,
		})
		view.Warnings = append(view.Warnings, fmt.Sprintf("%s 中存在同名但未纳管的副本", entry.AgentName))
	}
	sortTargets(view.Targets)
	view.Status = managedStatus(record, view.Targets, filepath.Join(hubPath, record.Name))
	view.StatusText = statusText(view.Status)
	view.Warnings = uniqueStrings(view.Warnings)
	return view
}

func buildUntrackedView(name string, entries []ScanEntry) SkillView {
	view := SkillView{Name: name, Managed: false, Targets: []TargetView{}}
	hashes := map[string]bool{}
	desc := ""
	for _, entry := range entries {
		hashes[entry.ContentHash] = true
		if desc == "" {
			desc = entry.Description
		}
		managedMarker := entry.Marker != nil
		view.Targets = append(view.Targets, TargetView{
			AgentID: entry.AgentID, AgentName: entry.AgentName, Path: entry.Path,
			Mode: "external", Managed: managedMarker, Exists: true, Enabled: true,
			ContentHash: entry.ContentHash, Modified: false, LinkTarget: entry.LinkTarget,
		})
		if len(entry.Warnings) > 0 {
			view.Warnings = append(view.Warnings, entry.Warnings...)
		}
		if view.Description == "" {
			view.Description = entry.Description
		}
		if entry.Files != nil {
			view.Files = len(entry.Files)
			view.Size = entry.Size
		}
	}
	view.Description = desc
	sortTargets(view.Targets)
	if len(hashes) > 1 {
		view.Status = "conflict"
	} else {
		view.Status = "untracked"
	}
	if len(entries) == 1 {
		view.ContentHash = entries[0].ContentHash
	}
	view.StatusText = statusText(view.Status)
	view.Warnings = uniqueStrings(view.Warnings)
	return view
}

func managedStatus(record *Record, targets []TargetView, hubDir string) string {
	if record.Available != nil && record.Available.Error == "" && record.Available.Commit != "" && record.Available.Commit != record.Source.Commit {
		return "update"
	}
	if !pathHasSkillMD(hubDir) {
		return "missing"
	}
	if len(targets) == 0 {
		return "hub_only"
	}
	hashes := map[string]bool{}
	missing := false
	modified := false
	externalConflict := false
	enabled := 0
	for _, target := range targets {
		if !target.Enabled {
			continue
		}
		enabled++
		if !target.Exists {
			missing = true
			continue
		}
		if target.ContentHash != "" {
			hashes[target.ContentHash] = true
		}
		if target.Modified {
			modified = true
		}
		if !target.Managed && target.ContentHash != record.ContentHash {
			externalConflict = true
		}
	}
	if len(hashes) > 1 || externalConflict {
		return "conflict"
	}
	if modified {
		return "modified"
	}
	if missing {
		return "partial"
	}
	if enabled == 0 {
		return "hub_only"
	}
	return "synced"
}

func agentName(entry ScanEntry, agentID string) string {
	if entry.AgentName != "" {
		return entry.AgentName
	}
	return agentID
}

func displayAgentName(entry ScanEntry, agentID string, agentNames map[string]string) string {
	if name := agentName(entry, agentID); name != agentID {
		return name
	}
	if name := agentNames[agentID]; name != "" {
		return name
	}
	return agentID
}

func contentHashOf(entry ScanEntry) string {
	if entry.Path == "" {
		return ""
	}
	return entry.ContentHash
}

func linkTargetOf(entry ScanEntry) string {
	return entry.LinkTarget
}

func sortTargets(targets []TargetView) {
	sort.Slice(targets, func(left, right int) bool {
		return targets[left].AgentID < targets[right].AgentID
	})
}

func settingsView(settings Settings) SettingsView {
	paths := settings.AgentPaths
	if paths == nil {
		paths = map[string]string{}
	}
	return SettingsView{
		Proxy: settings.Proxy, HasToken: strings.TrimSpace(settings.GitHubToken) != "",
		DefaultMode: settings.DefaultMode, ScanLevel: settings.ScanLevel,
		MaxDownloadMB: settings.MaxDownloadMB, AgentPaths: paths,
		CustomAgents: append([]CustomAgent{}, settings.CustomAgents...),
	}
}
