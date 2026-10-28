package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxSkillMDSize = 2 * 1024 * 1024

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type skillMeta struct {
	Name           string
	Description    string
	Body           string
	Warnings       []string
	RequiredTools  []string
	FrontmatterRaw string
}

func parseSkillMD(path, dirName string) (skillMeta, error) {
	info, err := os.Stat(path)
	if err != nil {
		return skillMeta{}, err
	}
	if info.Size() > maxSkillMDSize {
		return skillMeta{}, fmt.Errorf("SKILL.md 超过 2 MiB，已拒绝解析")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return skillMeta{}, err
	}
	return parseSkillContent(string(data), dirName), nil
}

func parseSkillContent(content, dirName string) skillMeta {
	content = strings.TrimPrefix(content, "\ufeff")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	meta := skillMeta{Name: dirName, Body: content}
	front, body, ok := splitFrontmatter(content)
	if !ok {
		meta.Warnings = append(meta.Warnings, "缺少 YAML frontmatter，名称回退为目录名")
		return meta
	}
	meta.Body = body
	meta.FrontmatterRaw = front
	var values map[string]any
	if err := yaml.Unmarshal([]byte(front), &values); err != nil {
		meta.Warnings = append(meta.Warnings, "frontmatter 不是有效 YAML："+sanitizeLabel(err.Error(), 180))
		return meta
	}
	if rawName, exists := values["name"]; exists {
		if name, ok := rawName.(string); ok && strings.TrimSpace(name) != "" {
			meta.Name = strings.TrimSpace(name)
		} else {
			meta.Warnings = append(meta.Warnings, "name 不是非空字符串，已回退为目录名")
		}
	} else {
		meta.Warnings = append(meta.Warnings, "frontmatter 缺少 name，已回退为目录名")
	}
	if rawDescription, exists := values["description"]; exists {
		if description, ok := rawDescription.(string); ok {
			meta.Description = strings.TrimSpace(description)
		}
	}
	if meta.Description == "" {
		meta.Warnings = append(meta.Warnings, "frontmatter 缺少 description，部分客户端不会加载该技能")
	}
	if len(meta.Description) > 1024 {
		meta.Warnings = append(meta.Warnings, fmt.Sprintf("description 超过 1024 字符（当前 %d）", len(meta.Description)))
	}
	if meta.Name != dirName {
		meta.Warnings = append(meta.Warnings, fmt.Sprintf("技能名 %q 与目录名 %q 不一致；其他客户端可能按目录名索引", meta.Name, dirName))
	}
	if !skillNamePattern.MatchString(meta.Name) {
		meta.Warnings = append(meta.Warnings, "name 不符合小写字母、数字和连字符规范，部分客户端会拒绝加载")
	}
	if len(meta.Name) > 64 {
		meta.Warnings = append(meta.Warnings, "name 超过 64 字符，部分客户端会拒绝加载")
	}
	if rawTools, exists := values["allowed-tools"]; exists {
		meta.RequiredTools = extractToolNames(rawTools)
	}
	if _, exists := values["context"]; exists {
		meta.Warnings = append(meta.Warnings, "包含 context 字段；只有 Claude Code 等少数客户端支持")
	}
	if _, exists := values["hooks"]; exists {
		meta.Warnings = append(meta.Warnings, "包含 hooks 字段；只有部分客户端支持")
	}
	return meta
}

func splitFrontmatter(content string) (front, body string, ok bool) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", content, false
	}
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			front = strings.Join(lines[1:index], "\n")
			body = strings.Join(lines[index+1:], "\n")
			return front, body, true
		}
	}
	return "", content, false
}

func extractToolNames(value any) []string {
	var raw []string
	switch typed := value.(type) {
	case string:
		raw = strings.Split(typed, ",")
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				raw = append(raw, text)
			}
		}
	case []string:
		raw = typed
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
			if len(out) >= 20 {
				break
			}
		}
	}
	return out
}
