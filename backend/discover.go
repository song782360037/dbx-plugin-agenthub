package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type pluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Skills  any    `json:"skills"`
}

func discoverSkills(root, scope string, settings Settings) ([]Candidate, []string, error) {
	root = filepath.Clean(root)
	start := root
	if strings.TrimSpace(scope) != "" {
		target, err := safeJoin(root, scope)
		if err != nil {
			return nil, nil, err
		}
		start = target
	}
	info, err := os.Stat(start)
	if err != nil {
		return nil, nil, fmt.Errorf("来源路径不存在：%s", scope)
	}
	pluginRoots, pluginWarnings := discoverPluginRoots(root, settings)
	var warnings []string
	warnings = append(warnings, pluginWarnings...)
	if info.Mode().IsRegular() {
		if !strings.EqualFold(filepath.Base(start), "SKILL.md") {
			return nil, warnings, fmt.Errorf("指定的文件不是 SKILL.md")
		}
		candidate, err := buildCandidate(root, filepath.Dir(start), pluginForDir(filepath.Dir(start), pluginRoots), settings)
		if err != nil {
			return nil, warnings, err
		}
		return []Candidate{candidate}, warnings, nil
	}
	if !info.IsDir() {
		return nil, warnings, fmt.Errorf("来源不是目录或 SKILL.md")
	}

	skillDirs := map[string]bool{}
	err = filepath.WalkDir(start, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == start {
			return nil
		}
		relToStart, relErr := filepath.Rel(start, path)
		if relErr != nil {
			return relErr
		}
		depth := len(strings.Split(filepath.ToSlash(relToStart), "/"))
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) || shouldSkipHiddenDir(entry.Name()) {
				return filepath.SkipDir
			}
			if depth > 8 {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(entry.Name(), "SKILL.md") {
			return nil
		}
		skillDirs[filepath.Dir(path)] = true
		if len(skillDirs) > 400 {
			return fmt.Errorf("发现的技能超过 400 个，请指定更具体的子目录")
		}
		return nil
	})
	if err != nil {
		return nil, warnings, err
	}

	// A shallower SKILL.md shadows nested ones (matches npx-skills discovery).
	var ordered []string
	for dir := range skillDirs {
		shadowed := false
		parent := filepath.Dir(dir)
		for withinRoot(root, parent) {
			if skillDirs[parent] {
				shadowed = true
				break
			}
			if parent == root {
				break
			}
			parent = filepath.Dir(parent)
		}
		if !shadowed {
			ordered = append(ordered, dir)
		}
	}
	sort.Slice(ordered, func(left, right int) bool {
		return strings.ToLower(ordered[left]) < strings.ToLower(ordered[right])
	})
	candidates := make([]Candidate, 0, len(ordered))
	names := map[string]int{}
	for _, dir := range ordered {
		candidate, err := buildCandidate(root, dir, pluginForDir(dir, pluginRoots), settings)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s：%v", relOrBase(root, dir), err))
			continue
		}
		names[candidate.Name]++
		candidates = append(candidates, candidate)
	}
	for index := range candidates {
		if names[candidates[index].Name] > 1 {
			candidates[index].Warnings = append(candidates[index].Warnings, "同一来源中存在重名技能，安装前请确认目标客户端会如何覆盖")
		}
		if strings.Contains(filepath.ToSlash(candidates[index].RelPath), "/.system/") || strings.HasSuffix(filepath.ToSlash(candidates[index].RelPath), ".system") {
			candidates[index].Warnings = append(candidates[index].Warnings, "这是系统内置技能目录，通常不建议手动安装")
		}
	}
	if len(candidates) == 0 {
		warnings = append(warnings, "没有发现包含 SKILL.md 的技能")
	}
	return candidates, uniqueStrings(warnings), nil
}

func shouldSkipHiddenDir(name string) bool {
	if !strings.HasPrefix(name, ".") {
		return false
	}
	switch strings.ToLower(name) {
	case ".claude", ".agents", ".curated", ".experimental", ".system", ".claude-plugin", ".codex-plugin", ".qoder-plugin", ".trae-plugin", ".codebuddy-plugin":
		return false
	}
	return true
}

func buildCandidate(root, dir string, plugin *PluginInfo, settings Settings) (Candidate, error) {
	dirName := filepath.Base(dir)
	meta, err := parseSkillMD(filepath.Join(dir, "SKILL.md"), dirName)
	if err != nil {
		return Candidate{}, err
	}
	hash, files, size, hashWarnings, err := hashDir(dir)
	if err != nil {
		return Candidate{}, err
	}
	rel, err := relSlash(root, dir)
	if err != nil {
		return Candidate{}, err
	}
	if rel == "." {
		rel = ""
	}
	content := meta.Body
	if len(content) > 16000 {
		content = content[:16000]
	}
	risk := []Finding(nil)
	if settings.ScanLevel != "off" {
		risk = scanRisk(dir, files)
	}
	warnings := append([]string{}, meta.Warnings...)
	warnings = append(warnings, hashWarnings...)
	candidate := Candidate{
		Name: meta.Name, Description: meta.Description, RelPath: rel,
		Files: len(files), Size: size, ContentHash: hash, Risk: risk,
		Warnings: uniqueStrings(warnings), Plugin: plugin, ContentPreview: content,
		RequiredTools: meta.RequiredTools,
	}
	return candidate, nil
}

func relOrBase(root, dir string) string {
	if rel, err := relSlash(root, dir); err == nil {
		return rel
	}
	return filepath.Base(dir)
}

type pluginRoot struct {
	Info PluginInfo
	Path string
}

func discoverPluginRoots(root string, settings Settings) ([]pluginRoot, []string) {
	var roots []pluginRoot
	var warnings []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(root, path)
			if len(strings.Split(filepath.ToSlash(rel), "/")) > 6 {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(entry.Name())
		kind := ""
		manifestName := ""
		switch name {
		case "plugin.json":
			parent := strings.ToLower(filepath.Base(filepath.Dir(path)))
			switch parent {
			case ".claude-plugin":
				kind = "claude"
			case ".codex-plugin":
				kind = "codex"
			case ".qoder-plugin":
				kind = "qoder"
			case ".trae-plugin":
				kind = "trae"
			case ".codebuddy-plugin":
				kind = "codebuddy"
			default:
				return nil
			}
			manifestName = filepath.Dir(filepath.Dir(path))
		case "marketplace.json":
			parent := strings.ToLower(filepath.Base(filepath.Dir(path)))
			if parent != ".claude-plugin" && !strings.HasSuffix(filepath.ToSlash(path), ".agents/plugins/marketplace.json") {
				return nil
			}
			kind = "marketplace"
			manifestName = filepath.Dir(filepath.Dir(path))
		default:
			return nil
		}
		info, parseWarnings := parsePluginManifest(path, kind)
		warnings = append(warnings, parseWarnings...)
		if kind == "marketplace" {
			for _, pluginPath := range info {
				roots = append(roots, pluginRoot{Info: PluginInfo{Kind: "plugin", Root: pluginPath}, Path: pluginPath})
			}
			return nil
		}
		if len(info) == 0 {
			roots = append(roots, pluginRoot{Info: PluginInfo{Kind: kind, Root: manifestName}, Path: manifestName})
			return nil
		}
		for _, skillRoot := range info {
			roots = append(roots, pluginRoot{Info: PluginInfo{Kind: kind, Root: manifestName}, Path: skillRoot})
		}
		return nil
	})
	return roots, uniqueStrings(warnings)
}

func parsePluginManifest(path, kind string) ([]string, []string) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 2*1024*1024 {
		return nil, []string{fmt.Sprintf("无法读取插件 manifest %s", path)}
	}
	base := filepath.Dir(filepath.Dir(path))
	if kind == "marketplace" {
		if strings.HasSuffix(filepath.ToSlash(path), ".agents/plugins/marketplace.json") {
			base = filepath.Dir(filepath.Dir(filepath.Dir(path)))
		}
		var market struct {
			Plugins []struct {
				Name   string          `json:"name"`
				Source json.RawMessage `json:"source"`
			} `json:"plugins"`
		}
		if json.Unmarshal(data, &market) != nil {
			return nil, []string{fmt.Sprintf("无法解析插件市场 %s", path)}
		}
		var roots []string
		var warnings []string
		for _, plugin := range market.Plugins {
			var source string
			if json.Unmarshal(plugin.Source, &source) == nil && source != "" && !strings.Contains(source, "://") {
				target := filepath.Join(base, filepath.FromSlash(source))
				if existingDir(target) {
					roots = append(roots, target)
				}
				continue
			}
			var object struct {
				Source string `json:"source"`
				Repo   string `json:"repo"`
			}
			if json.Unmarshal(plugin.Source, &object) == nil && object.Repo != "" {
				warnings = append(warnings, fmt.Sprintf("市场插件 %s 使用外部仓库 %s，请单独安装", plugin.Name, object.Repo))
			}
		}
		return roots, uniqueStrings(warnings)
	}
	var manifest pluginManifest
	if json.Unmarshal(data, &manifest) != nil {
		return nil, []string{fmt.Sprintf("无法解析插件 manifest %s", path)}
	}
	var roots []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		target := value
		if !filepath.IsAbs(target) {
			target = filepath.Join(base, filepath.FromSlash(target))
		}
		if existingDir(target) {
			roots = append(roots, target)
		}
	}
	switch typed := manifest.Skills.(type) {
	case string:
		add(typed)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				add(text)
			}
		}
	}
	if len(roots) == 0 {
		if fallback := filepath.Join(base, "skills"); existingDir(fallback) {
			roots = append(roots, fallback)
		}
	}
	return uniqueStrings(roots), nil
}

func pluginForDir(dir string, roots []pluginRoot) *PluginInfo {
	var best *PluginInfo
	bestLen := 0
	for _, root := range roots {
		if withinRoot(root.Path, dir) && len(root.Path) > bestLen {
			info := root.Info
			best = &info
			bestLen = len(root.Path)
		}
	}
	return best
}
