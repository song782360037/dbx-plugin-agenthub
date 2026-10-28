package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
}

func mcpTools() []mcpTool {
	readOnly := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	return []mcpTool{
		{
			Name: "skills_list", Description: "只读列出本机技能及安装客户端；不安装、不更新、不删除。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			Annotations: readOnly,
		},
		{
			Name: "skills_search", Description: "只读按名称或描述搜索本机技能；不安装、不更新、不删除。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 200, "description": "搜索关键词"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "最多返回条数，默认 20"},
				},
				"required": []string{"query"}, "additionalProperties": false,
			},
			Annotations: readOnly,
		},
		{
			Name: "skills_get", Description: "只读查看单个技能的说明、文件列表与安装状态；不修改本机文件。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 64, "description": "技能名"},
				},
				"required": []string{"name"}, "additionalProperties": false,
			},
			Annotations: readOnly,
		},
		{
			Name: "mcp_servers_list", Description: "只读列出本机各客户端的 MCP 服务及同步状态；不修改配置文件。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			Annotations: readOnly,
		},
		{
			Name: "mcp_server_get", Description: "只读查看单个 MCP 服务配置（环境变量和请求头已掩码）；不修改配置文件。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 64, "description": "MCP 服务名"},
				},
				"required": []string{"name"}, "additionalProperties": false,
			},
			Annotations: readOnly,
		},
	}
}

func callMCPTool(manager *Manager, params json.RawMessage) map[string]any {
	var call struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return mcpFailure("MCP 请求格式无效")
	}
	if len(call.Arguments) == 0 || bytes.Equal(bytes.TrimSpace(call.Arguments), []byte("null")) {
		call.Arguments = json.RawMessage(`{}`)
	}
	state := manager.stateSnapshot()
	agents := buildAgents(state.Settings)
	overview := buildOverview(&state, agents, manager.hubDir, manager.initWarnings())
	switch call.Tool {
	case "skills_list":
		var args struct{}
		if err := strictJSON(call.Arguments, &args); err != nil {
			return mcpFailure(err.Error())
		}
		return mcpResult(skillSummaries(overview.Skills, 200), false)
	case "skills_search":
		var args struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := strictJSON(call.Arguments, &args); err != nil {
			return mcpFailure(err.Error())
		}
		query := strings.ToLower(strings.TrimSpace(args.Query))
		if query == "" || len(query) > 200 {
			return mcpFailure("query 必须是 1–200 字符的字符串")
		}
		if args.Limit <= 0 {
			args.Limit = 20
		}
		if args.Limit > 100 {
			args.Limit = 100
		}
		var matches []SkillView
		for _, skill := range overview.Skills {
			if strings.Contains(strings.ToLower(skill.Name+" "+skill.Description), query) {
				matches = append(matches, skill)
				if len(matches) >= args.Limit {
					break
				}
			}
		}
		return mcpResult(skillSummaries(matches, args.Limit), false)
	case "skills_get":
		var args struct {
			Name string `json:"name"`
		}
		if err := strictJSON(call.Arguments, &args); err != nil {
			return mcpFailure(err.Error())
		}
		if !skillNamePattern.MatchString(args.Name) {
			return mcpFailure("技能名不合法")
		}
		detail, err := manager.skillDetail(args.Name)
		if err != nil {
			return mcpFailure(err.Error())
		}
		return mcpResult(detail, false)
	case "mcp_servers_list":
		var args struct{}
		if err := strictJSON(call.Arguments, &args); err != nil {
			return mcpFailure(err.Error())
		}
		mcpOverview := manager.mcpOverview()
		return mcpResult(mcpSummaries(mcpOverview.Servers, 200), false)
	case "mcp_server_get":
		var args struct {
			Name string `json:"name"`
		}
		if err := strictJSON(call.Arguments, &args); err != nil {
			return mcpFailure(err.Error())
		}
		if err := validateMCPName(args.Name); err != nil {
			return mcpFailure(err.Error())
		}
		detail, err := manager.mcpDetail(args.Name, "")
		if err != nil {
			return mcpFailure(err.Error())
		}
		return mcpResult(detail, false)
	default:
		return mcpFailure("工具不存在或不在只读白名单中")
	}
}

func mcpSummaries(servers []MCPView, limit int) []map[string]any {
	if limit <= 0 || limit > len(servers) {
		limit = len(servers)
	}
	out := make([]map[string]any, 0, limit)
	for _, server := range servers[:limit] {
		out = append(out, map[string]any{
			"name": server.Name, "transport": server.Transport, "command": server.Command,
			"url": server.URL, "managed": server.Managed, "status": server.Status,
			"statusText": server.StatusText, "envKeys": server.EnvKeys,
			"headerKeys": server.HeaderKeys, "targets": server.Targets,
		})
	}
	return out
}

func strictJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("参数格式无效：%v", err)
	}
	return nil
}

func skillSummaries(skills []SkillView, limit int) []map[string]any {
	if limit <= 0 || limit > len(skills) {
		limit = len(skills)
	}
	out := make([]map[string]any, 0, limit)
	for _, skill := range skills[:limit] {
		item := map[string]any{
			"name": skill.Name, "description": skill.Description, "managed": skill.Managed,
			"status": skill.Status, "statusText": skill.StatusText, "targets": skill.Targets,
			"files": skill.Files, "size": skill.Size, "updatedAt": skill.UpdatedAt,
		}
		if skill.Source != nil {
			item["source"] = skill.Source
		}
		out = append(out, item)
	}
	return out
}

func mcpResult(value any, failed bool) map[string]any {
	payload, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "结果无法序列化"}}, "isError": true}
	}
	if len(payload) > 7*1024*1024 {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "结果超过 MCP 消息预算，请缩小查询范围"}}, "isError": true}
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(payload)}}, "isError": failed}
}

func mcpFailure(message string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": message}}, "isError": true}
}
