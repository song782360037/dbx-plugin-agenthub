package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	dbx "github.com/t8y2/dbx/plugins/sdk/go/dbx-plugin-sdk"
)

type plugin struct {
	manager *Manager
	initErr error
}

func (p *plugin) Handle(_ dbx.RequestContext, method string, params json.RawMessage, _ *dbx.Emitter) (any, *dbx.PluginError) {
	if p.initErr != nil {
		return nil, dbx.NewError(-32000, "插件初始化失败："+p.initErr.Error())
	}
	if method != "mcp/tools" {
		if err := p.manager.refreshState(); err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
	}
	switch method {
	case "mcp/tools":
		return map[string]any{"tools": mcpTools()}, nil
	case "mcp/call":
		return callMCPTool(p.manager, params), nil
	case "skills/getOverview", "skills/scan":
		return p.overview()
	case "skills/getSkill":
		var request struct {
			Name string `json:"name"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.skillDetail(strings.TrimSpace(request.Name))
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "skills/previewSource":
		var request struct {
			Source SourceInput `json:"source"`
			SourceInput
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		input := request.Source
		if strings.TrimSpace(input.Value) == "" && strings.TrimSpace(input.ZipBase64) == "" {
			input = request.SourceInput
		}
		preview, err := p.manager.previewSource(input)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return preview, nil
	case "skills/install":
		var input InstallInput
		if err := decodeParams(params, &input); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.install(input)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "skills/removeSkill":
		var request struct {
			Name      string `json:"name"`
			Confirmed bool   `json:"confirmed"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.removeSkill(request.Name, request.Confirmed)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "skills/setTarget":
		var request struct {
			Name      string `json:"name"`
			AgentID   string `json:"agentId"`
			Enabled   bool   `json:"enabled"`
			Mode      string `json:"mode"`
			Confirmed bool   `json:"confirmed"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.setTargetEnabled(request.Name, request.AgentID, request.Enabled, request.Mode, request.Confirmed)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "skills/adopt":
		var request struct {
			AgentID string `json:"agentId"`
			Name    string `json:"name"`
			Link    *bool  `json:"link"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		link := true
		if request.Link != nil {
			link = *request.Link
		}
		result, err := p.manager.adopt(request.AgentID, request.Name, link)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "skills/checkUpdates":
		var request struct {
			Names []string `json:"names"`
			Force bool     `json:"force"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.checkUpdates(request.Names, request.Force)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return map[string]any{"results": result}, nil
	case "skills/previewUpdate":
		var request struct {
			Names []string `json:"names"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		previews, err := p.manager.previewUpdates(request.Names)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return map[string]any{"previews": previews}, nil
	case "skills/update":
		var request UpdateInput
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.updateSkills(request)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "skills/readFile":
		var request struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.readSkillFile(request.Name, request.Path)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "skills/saveSettings":
		var input SettingsInput
		if err := decodeParams(params, &input); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		settings, err := p.manager.updateSettings(input)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return map[string]any{"success": true, "settings": settings}, nil
	case "skills/testConnection":
		result, err := p.manager.testConnection()
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "mcp/getOverview":
		return p.manager.mcpOverview(), nil
	case "mcp/getServer":
		var request struct {
			Name    string `json:"name"`
			AgentID string `json:"agentId"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.mcpDetail(strings.TrimSpace(request.Name), strings.TrimSpace(request.AgentID))
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "mcp/importJSON":
		var request struct {
			Text string `json:"text"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.importMCPJSON(request.Text)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "mcp/save":
		var input MCPInput
		if err := decodeParams(params, &input); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.saveMCP(input)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "mcp/remove":
		var request struct {
			Name      string `json:"name"`
			Confirmed bool   `json:"confirmed"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.removeMCP(request.Name, request.Confirmed)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "mcp/setTarget":
		var request struct {
			Name      string `json:"name"`
			AgentID   string `json:"agentId"`
			Enabled   bool   `json:"enabled"`
			Confirmed bool   `json:"confirmed"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.setMCPTargetEnabled(request.Name, request.AgentID, request.Enabled, request.Confirmed)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	case "mcp/adopt":
		var request struct {
			AgentID string `json:"agentId"`
			Name    string `json:"name"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, dbx.NewError(-32602, err.Error())
		}
		result, err := p.manager.adoptMCP(request.AgentID, request.Name)
		if err != nil {
			return nil, dbx.NewError(-32000, err.Error())
		}
		return result, nil
	default:
		return nil, dbx.MethodNotFound(method)
	}
}

func (p *plugin) overview() (any, *dbx.PluginError) {
	state := p.manager.stateSnapshot()
	agents := buildAgents(state.Settings)
	overview := buildOverview(&state, agents, p.manager.hubDir, p.manager.initWarnings())
	return overview, nil
}

func decodeParams(params json.RawMessage, target any) error {
	if len(params) == 0 || string(params) == "null" {
		params = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(params)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("请求参数格式错误：%v", err)
	}
	return nil
}

var pluginMetadata = dbx.Metadata{
	ID: appID, Version: appVersion, Capabilities: []string{"workbench"},
}

func main() {
	manager, err := NewManager()
	p := &plugin{manager: manager, initErr: err}
	if err != nil {
		log.Printf("skills manager init failed: %v", err)
	}
	server := dbx.NewServer(pluginMetadata, p)
	if err := server.Serve(); err != nil {
		log.Fatal(err)
	}
}
