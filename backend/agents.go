package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func buildAgents(settings Settings) []AgentSpec {
	home, _ := os.UserHomeDir()
	claudeBase := envOr("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	codexBase := envOr("CODEX_HOME", filepath.Join(home, ".codex"))
	piBase := envOr("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
	bases := []struct {
		id, name, base, note string
	}{
		{"claude-code", "Claude Code", claudeBase, ""},
		{"codex", "Codex", codexBase, "Codex 也可能读取 ~/.agents/skills；同时启用通用目录可能出现同名技能"},
		{"qoder", "Qoder", filepath.Join(home, ".qoder"), ""},
		{"qoder-cn", "Qoder CN", filepath.Join(home, ".qoder-cn"), ""},
		{"trae", "Trae", filepath.Join(home, ".trae"), ""},
		{"trae-cn", "Trae CN", filepath.Join(home, ".trae-cn"), ""},
		{"workbuddy", "WorkBuddy", filepath.Join(home, ".workbuddy"), "WorkBuddy 使用 CodeBuddy 兼容的技能/插件目录"},
		{"pi", "Pi", piBase, "默认 ~/.pi/agent/skills；支持 PI_CODING_AGENT_DIR"},
		{"universal", "通用目录（.agents）", filepath.Join(home, ".agents"), "Pi、Codex、Cursor 等客户端共享的 Agent Skills 目录"},
		{"cursor", "Cursor", filepath.Join(home, ".cursor"), ""},
		{"gemini-cli", "Gemini CLI", filepath.Join(home, ".gemini"), ""},
	}
	agents := make([]AgentSpec, 0, len(bases)+len(settings.CustomAgents))
	for _, item := range bases {
		defaultPath := filepath.Join(item.base, "skills")
		path := defaultPath
		overridden := false
		if custom, ok := settings.AgentPaths[item.id]; ok && strings.TrimSpace(custom) != "" {
			if resolved, err := absoluteClean(custom); err == nil {
				path = resolved
				overridden = true
			}
		}
		detected := existingDir(item.base) || existingDir(path)
		if overridden {
			detected = true
		}
		agents = append(agents, AgentSpec{
			ID: item.id, Name: item.name, Path: path, DefaultPath: defaultPath, Detected: detected,
			Builtin: true, Overridden: overridden, Note: item.note,
		})
	}
	seen := map[string]bool{}
	for _, agent := range agents {
		seen[agent.ID] = true
	}
	for _, custom := range settings.CustomAgents {
		id := strings.TrimSpace(custom.ID)
		if id == "" || seen[id] {
			continue
		}
		path, err := absoluteClean(custom.Path)
		if err != nil || path == "" {
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(custom.Name)
		if name == "" {
			name = id
		}
		agents = append(agents, AgentSpec{
			ID: id, Name: name, Path: path, DefaultPath: path, Detected: existingDir(path), Custom: true,
			Note: "自定义客户端目录",
		})
	}
	return agents
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func findAgent(agents []AgentSpec, id string) (AgentSpec, bool) {
	for _, agent := range agents {
		if agent.ID == id {
			return agent, true
		}
	}
	return AgentSpec{}, false
}

func validateCustomAgents(custom []CustomAgent) ([]CustomAgent, error) {
	out := make([]CustomAgent, 0, len(custom))
	seen := map[string]bool{}
	for _, item := range custom {
		id := strings.TrimSpace(item.ID)
		name := strings.TrimSpace(item.Name)
		path := strings.TrimSpace(item.Path)
		if id == "" && name == "" && path == "" {
			continue
		}
		if id == "" {
			slug := slugify(name)
			if slug == "" {
				return nil, fmt.Errorf("自定义客户端缺少 ID")
			}
			id = slug
		}
		if !skillNamePattern.MatchString(id) {
			return nil, fmt.Errorf("自定义客户端 ID %q 只能使用小写字母、数字和连字符", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("自定义客户端 ID %q 重复", id)
		}
		if name == "" {
			name = id
		}
		resolved, err := absoluteClean(path)
		if err != nil {
			return nil, fmt.Errorf("自定义客户端 %s 的路径无效：%v", id, err)
		}
		if resolved == "" {
			return nil, fmt.Errorf("自定义客户端 %s 缺少技能目录", id)
		}
		seen[id] = true
		out = append(out, CustomAgent{ID: id, Name: name, Path: resolved})
	}
	return out, nil
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			builder.WriteRune(char)
			lastDash = false
		case char == '-' || char == '_' || char == ' ':
			if builder.Len() > 0 && !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}
