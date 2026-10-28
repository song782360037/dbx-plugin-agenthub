package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func parseHTTPURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errorsf("URL 必须是 http:// 或 https:// 地址")
	}
	return parsed, nil
}

type mcpAdapter struct {
	ID       string
	Name     string
	AppDir   string
	Path     string
	Format   string
	Kind     string
	Detected bool
}

func buildMCPAdapters(_ Settings) []mcpAdapter {
	home, _ := os.UserHomeDir()
	claudeBase := envOr("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	codexBase := envOr("CODEX_HOME", filepath.Join(home, ".codex"))
	piBase := envOr("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
	claudeConfig := filepath.Join(home, ".claude.json")
	if strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) != "" {
		claudeConfig = filepath.Join(claudeBase, ".claude.json")
	}
	items := []struct {
		id, name, appDir, path, format, kind string
	}{
		{"claude-code", "Claude Code", claudeBase, claudeConfig, "json", "claude"},
		{"codex", "Codex", codexBase, filepath.Join(codexBase, "config.toml"), "toml", "codex"},
		{"qoder", "Qoder", filepath.Join(home, ".qoder"), filepath.Join(home, ".qoder", "settings.json"), "json", "qoder"},
		{"qoder-cn", "Qoder CN", filepath.Join(home, ".qoder-cn"), filepath.Join(home, ".qoder-cn", "settings.json"), "json", "qoder"},
		{"trae", "Trae", filepath.Join(home, ".trae"), filepath.Join(home, ".trae", "mcp.json"), "json", "trae"},
		{"trae-cn", "Trae CN", filepath.Join(home, ".trae-cn"), filepath.Join(home, ".trae-cn", "mcp.json"), "json", "trae"},
		{"workbuddy", "WorkBuddy", filepath.Join(home, ".workbuddy"), filepath.Join(home, ".workbuddy", "mcp.json"), "json", "workbuddy"},
		{"cursor", "Cursor", filepath.Join(home, ".cursor"), filepath.Join(home, ".cursor", "mcp.json"), "json", "cursor"},
		{"gemini-cli", "Gemini CLI", filepath.Join(home, ".gemini"), filepath.Join(home, ".gemini", "settings.json"), "json", "gemini"},
		{"pi", "Pi", piBase, filepath.Join(piBase, "mcp.json"), "json", "pi"},
	}
	adapters := make([]mcpAdapter, 0, len(items))
	for _, item := range items {
		adapters = append(adapters, mcpAdapter{
			ID: item.id, Name: item.name, AppDir: item.appDir, Path: item.path,
			Format: item.format, Kind: item.kind,
			Detected: existingDir(item.appDir) || fileExists(item.path),
		})
	}
	return adapters
}

func adapterView(adapter mcpAdapter) MCPAdapterView {
	return MCPAdapterView{ID: adapter.ID, Name: adapter.Name, Path: adapter.Path, Format: adapter.Format, Detected: adapter.Detected}
}

func readMCPAdapter(adapter mcpAdapter) (map[string]mcpEntry, []string, error) {
	if adapter.Format == "toml" {
		return readCodexMCP(adapter)
	}
	return readJSONMCP(adapter)
}

func readJSONMCP(adapter mcpAdapter) (map[string]mcpEntry, []string, error) {
	entries := map[string]mcpEntry{}
	info, err := os.Stat(adapter.Path)
	if os.IsNotExist(err) {
		return entries, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if info.Size() > 16*1024*1024 {
		return nil, nil, fmt.Errorf("MCP 配置超过 16 MiB，已跳过")
	}
	data, err := os.ReadFile(adapter.Path)
	if err != nil {
		return nil, nil, err
	}
	top, err := decodeMCPJSONObject([]byte(strings.TrimPrefix(string(data), "\ufeff")))
	if err != nil {
		return nil, nil, errorsf("JSON 配置必须是有效对象")
	}
	rawServers, ok := top["mcpServers"]
	if !ok {
		return entries, nil, nil
	}
	servers, err := decodeMCPJSONObject(rawServers)
	if err != nil {
		return nil, nil, errorsf("mcpServers 必须是对象")
	}
	var warnings []string
	for name, raw := range servers {
		config, entryWarnings, disabled, parseErr := parseJSONMCPEntry(adapter, raw)
		if parseErr != nil {
			entryWarnings = append(entryWarnings, fmt.Sprintf("%s/%s：%v", adapter.Name, name, parseErr))
		}
		if disabled {
			entryWarnings = append(entryWarnings, adapter.Name+"/"+name+"：客户端已停用")
		}
		rawHash, err := jsonMCPRawHash(raw)
		if err != nil {
			return nil, nil, errorsf("无法计算 JSON 配置指纹")
		}
		entry := mcpEntry{
			AgentID: adapter.ID, AgentName: adapter.Name, Path: adapter.Path,
			Name: name, Config: config, RawHash: rawHash,
			Disabled: disabled, Invalid: parseErr != nil, Warnings: uniqueStrings(entryWarnings),
		}
		if !entry.Invalid {
			entry.ContentHash = mcpConfigHash(config)
		}
		entries[name] = entry
		warnings = append(warnings, entry.Warnings...)
	}
	return entries, uniqueStrings(warnings), nil
}

func decodeMCPJSONObject(data []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, errorsf("必须是 JSON 对象")
	}
	return object, nil
}

func jsonMCPRawHash(raw json.RawMessage) (string, error) {
	// UseNumber avoids rounding private integers beyond float64 precision.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value) // Recursively sorts object keys.
	if err != nil {
		return "", err
	}
	return hashMCPBytes(canonical), nil
}

func parseJSONMCPEntry(adapter mcpAdapter, raw json.RawMessage) (MCPServerConfig, []string, bool, error) {
	fields, err := decodeMCPJSONObject(raw)
	if err != nil {
		return MCPServerConfig{}, nil, false, errorsf("条目不是对象")
	}
	disabled, _ := rawBool(fields["disabled"])
	if enabled, ok := rawBool(fields["enabled"]); ok && !enabled {
		disabled = true
	}
	if err := validateJSONMCPFields(fields); err != nil {
		return MCPServerConfig{}, nil, disabled, err
	}
	var warnings []string
	config := MCPServerConfig{}
	config.Command, _ = rawString(fields["command"])
	config.URL, _ = rawString(fields["url"])
	if httpURL, ok := rawString(fields["httpUrl"]); ok && httpURL != "" {
		config.URL = httpURL
	}
	config.Args = rawStringArray(fields["args"])
	config.Env = rawStringMap(fields["env"])
	if _, exists := fields["env"]; !exists {
		config.Env = rawStringMap(fields["environment"])
	}
	config.Headers = rawStringMap(fields["headers"])
	if _, exists := fields["headers"]; !exists {
		config.Headers = rawStringMap(fields["header"])
	}
	config.TimeoutMS, _ = rawInt(fields["timeout"])
	rawType, _ := rawString(fields["type"])
	config.Transport = normalizeMCPTransport(adapter.Kind, rawType, config.Command, config.URL, fields["httpUrl"] != nil)
	config.Command = normalizeMCPCommandPath(config.Command)
	if err := validateMCPConfig(config); err != nil {
		return config, warnings, disabled, err
	}
	if config.TimeoutMS < 0 {
		return config, warnings, disabled, errorsf("timeout 不能为负数")
	}
	if config.Transport == "stdio" {
		config.Headers = map[string]string{}
	} else {
		config.Env = map[string]string{}
		config.Args = nil
	}
	if _, exists := fields["env_vars"]; exists {
		warnings = append(warnings, "env_vars 是客户端私有字段，不会跨客户端同步")
	}
	if config.Transport == "sse" && (adapter.Kind == "pi" || adapter.Kind == "trae") {
		warnings = append(warnings, adapter.Name+" 不支持 SSE，同步时会要求改用 HTTP")
	}
	return config, uniqueStrings(warnings), disabled, nil
}

func validateJSONMCPFields(fields map[string]json.RawMessage) error {
	for key, raw := range fields {
		valid := true
		switch key {
		case "command", "url", "httpUrl", "type":
			_, valid = rawString(raw)
		case "disabled", "enabled":
			_, valid = rawBool(raw)
		case "timeout":
			_, valid = rawInt(raw)
		case "args":
			var values []json.RawMessage
			valid = json.Unmarshal(raw, &values) == nil && values != nil
			for _, value := range values {
				if _, ok := rawString(value); !ok {
					valid = false
				}
			}
		case "env", "environment", "headers", "header":
			values, err := decodeMCPJSONObject(raw)
			valid = err == nil
			for _, value := range values {
				if _, ok := rawString(value); !ok {
					valid = false
				}
			}
		}
		if !valid {
			return errorsf("字段 %s 类型无效", key)
		}
	}
	return nil
}

func normalizeMCPTransport(kind, rawType, command, url string, hasHTTPURL bool) string {
	value := strings.ToLower(strings.TrimSpace(rawType))
	switch value {
	case "stdio", "local":
		return "stdio"
	case "sse":
		return "sse"
	case "http", "streamable-http", "streamablehttp", "remote", "httpurl":
		return "http"
	}
	if value != "" {
		return ""
	}
	if strings.TrimSpace(command) != "" {
		return "stdio"
	}
	if hasHTTPURL {
		return "http"
	}
	if strings.TrimSpace(url) != "" {
		if kind == "gemini" {
			return "sse"
		}
		return "http"
	}
	return ""
}

func rawString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value, true
	}
	return "", false
}

func rawStringArray(raw json.RawMessage) []string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var stringsValue []string
	if json.Unmarshal(raw, &stringsValue) == nil {
		return stringsValue
	}
	var values []any
	if json.Unmarshal(raw, &values) == nil {
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

func rawStringMap(raw json.RawMessage) map[string]string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]string{}
	}
	var values map[string]string
	if json.Unmarshal(raw, &values) == nil {
		return values
	}
	var generic map[string]any
	if json.Unmarshal(raw, &generic) == nil {
		out := make(map[string]string, len(generic))
		for key, value := range generic {
			out[key] = fmt.Sprint(value)
		}
		return out
	}
	return map[string]string{}
}

func rawInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false
	}
	var number int
	if json.Unmarshal(raw, &number) == nil {
		return number, true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		parsed, err := strconv.Atoi(strings.TrimSpace(text))
		return parsed, err == nil
	}
	return 0, false
}

func rawBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, false
	}
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value, true
	}
	return false, false
}

func errorsf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

func normalizeMCPCommandPath(command string) string {
	if len(command) >= 3 && ((command[0] >= 'A' && command[0] <= 'Z') || (command[0] >= 'a' && command[0] <= 'z')) && command[1] == ':' {
		return filepath.Clean(command)
	}
	if strings.HasPrefix(command, `\\`) {
		return filepath.Clean(command)
	}
	return command
}

func mcpConfigHash(config MCPServerConfig) string {
	hash := sha256.New()
	command := normalizeMCPCommandPath(config.Command)
	fmt.Fprintf(hash, "%s\x00%s\x00", config.Transport, command)
	for _, arg := range config.Args {
		fmt.Fprintf(hash, "%s\x00", arg)
	}
	fmt.Fprintf(hash, "%s\x00", config.URL)
	envKeys := make([]string, 0, len(config.Env))
	for key := range config.Env {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		fmt.Fprintf(hash, "env:%s=%s\x00", key, config.Env[key])
	}
	headerKeys := make([]string, 0, len(config.Headers))
	for key := range config.Headers {
		headerKeys = append(headerKeys, key)
	}
	sort.Strings(headerKeys)
	for _, key := range headerKeys {
		fmt.Fprintf(hash, "header:%s=%s\x00", key, config.Headers[key])
	}
	// Keep legacy hashes compatible when no timeout was configured.
	if config.TimeoutMS != 0 {
		fmt.Fprintf(hash, "timeout:%d\x00", config.TimeoutMS)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func hashMCPBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func validateMCPName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errorsf("MCP 服务名不能为空")
	}
	if len(name) > 64 {
		return errorsf("MCP 服务名不能超过 64 字符")
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return errorsf("MCP 服务名只能使用字母、数字、点、下划线和连字符")
	}
	return nil
}

func validateMCPConfig(config MCPServerConfig) error {
	switch config.Transport {
	case "stdio":
		if strings.TrimSpace(config.Command) == "" {
			return errorsf("stdio 服务必须填写 command")
		}
		if strings.TrimSpace(config.URL) != "" {
			return errorsf("stdio 服务不能同时填写 URL")
		}
	case "http", "sse":
		if strings.TrimSpace(config.URL) == "" {
			return errorsf("HTTP/SSE 服务必须填写 URL")
		}
		parsed, err := parseHTTPURL(config.URL)
		if err != nil {
			return err
		}
		_ = parsed
		if strings.TrimSpace(config.Command) != "" {
			return errorsf("HTTP/SSE 服务不能同时填写 command")
		}
	default:
		return errorsf("transport 只能是 stdio、http 或 sse")
	}
	for key := range config.Env {
		if !envKeyPattern.MatchString(key) {
			return fmt.Errorf("环境变量名 %q 不合法", key)
		}
	}
	return nil
}

func adapterValidate(adapter mcpAdapter, config MCPServerConfig) error {
	if config.Transport == "sse" && (adapter.Kind == "pi" || adapter.Kind == "trae" || adapter.Kind == "codex") {
		return fmt.Errorf("%s 不支持 SSE 传输", adapter.Name)
	}
	return nil
}

func adapterConfigJSON(adapter mcpAdapter, config MCPServerConfig) map[string]any {
	entry := map[string]any{}
	switch adapter.Kind {
	case "gemini":
		if config.Transport == "stdio" {
			entry["command"] = config.Command
			if len(config.Args) > 0 {
				entry["args"] = config.Args
			}
			entry["env"] = config.Env
		} else if config.Transport == "sse" {
			entry["url"] = config.URL
			if len(config.Headers) > 0 {
				entry["headers"] = config.Headers
			}
		} else {
			entry["httpUrl"] = config.URL
			if len(config.Headers) > 0 {
				entry["headers"] = config.Headers
			}
		}
		if config.TimeoutMS > 0 {
			entry["timeout"] = config.TimeoutMS
		}
	case "pi":
		entry["type"] = config.Transport
		if config.Transport == "stdio" {
			entry["command"] = config.Command
			if len(config.Args) > 0 {
				entry["args"] = config.Args
			}
			entry["env"] = config.Env
		} else {
			entry["url"] = config.URL
			if len(config.Headers) > 0 {
				entry["headers"] = config.Headers
			}
		}
		if config.TimeoutMS > 0 {
			entry["timeout"] = config.TimeoutMS
		}
	default:
		switch adapter.Kind {
		case "qoder", "workbuddy":
			switch config.Transport {
			case "stdio":
				entry["type"] = "stdio"
			case "sse":
				entry["type"] = "sse"
			default:
				entry["type"] = "streamable-http"
			}
		default:
			entry["type"] = config.Transport
		}
		if config.Transport == "stdio" {
			entry["command"] = config.Command
			if len(config.Args) > 0 {
				entry["args"] = config.Args
			}
			entry["env"] = config.Env
		} else {
			entry["url"] = config.URL
			if len(config.Headers) > 0 {
				entry["headers"] = config.Headers
			}
		}
		if config.TimeoutMS > 0 && (adapter.Kind == "qoder" || adapter.Kind == "workbuddy") {
			entry["timeout"] = config.TimeoutMS
		}
	}
	if _, ok := entry["env"]; ok && len(config.Env) == 0 {
		delete(entry, "env")
	}
	if _, ok := entry["headers"]; ok && len(config.Headers) == 0 {
		delete(entry, "headers")
	}
	return entry
}

func applyMCPAdapter(adapter mcpAdapter, backupDir, name string, config MCPServerConfig, remove bool) error {
	if !adapter.Detected {
		return fmt.Errorf("%s 未检测到", adapter.Name)
	}
	info, statErr := os.Stat(adapter.Path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if statErr == nil && !info.Mode().IsRegular() {
		return errorsf("MCP 配置路径不是普通文件")
	}
	if remove && os.IsNotExist(statErr) {
		return nil
	}
	if err := adapterValidate(adapter, config); err != nil && !remove {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(adapter.Path), 0o755); err != nil {
		return err
	}
	if statErr == nil {
		if err := backupFile(adapter.Path, backupDir, adapter.ID); err != nil {
			return err
		}
	}
	if adapter.Format == "toml" {
		return applyCodexMCP(adapter.Path, name, config, remove)
	}
	return applyJSONMCP(adapter, name, config, remove)
}

func applyJSONMCP(adapter mcpAdapter, name string, config MCPServerConfig, remove bool) error {
	top := map[string]json.RawMessage{}
	data, err := os.ReadFile(adapter.Path)
	if err == nil {
		top, err = decodeMCPJSONObject([]byte(strings.TrimPrefix(string(data), "\ufeff")))
		if err != nil {
			return errorsf("JSON 配置必须是有效对象")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := top["mcpServers"]; ok {
		servers, err = decodeMCPJSONObject(raw)
		if err != nil {
			return errorsf("mcpServers 必须是对象")
		}
	}
	if remove {
		delete(servers, name)
	} else {
		if err := validateMCPConfig(config); err != nil {
			return err
		}
		entry := map[string]json.RawMessage{}
		if raw, exists := servers[name]; exists {
			entry, err = decodeMCPJSONObject(raw)
			if err != nil {
				return errorsf("现有 MCP 条目不是对象，不能安全合并")
			}
		}
		// Only these portable fields are owned. In particular cwd, trust, tool
		// filters and native disabled/enabled flags belong to the client.
		for _, key := range []string{"type", "command", "args", "env", "environment", "url", "httpUrl", "headers", "header"} {
			delete(entry, key)
		}
		switch adapter.Kind {
		case "gemini", "pi", "qoder", "workbuddy":
			delete(entry, "timeout")
		}
		for key, value := range adapterConfigJSON(adapter, config) {
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			entry[key] = encoded
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, _, _, err := parseJSONMCPEntry(adapter, encoded); err != nil {
			return fmt.Errorf("合并后的 MCP 条目无效：%w", err)
		}
		servers[name] = encoded
	}
	serversRaw, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	top["mcpServers"] = serversRaw
	output, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	output = append(output, '\n')
	if _, err := decodeMCPJSONObject(output); err != nil {
		return errorsf("生成的 JSON 无效")
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(adapter.Path); err == nil {
		mode = info.Mode().Perm()
	}
	return atomicWriteFile(adapter.Path, output, mode)
}

func backupFile(path, backupDir, label string) error {
	source, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer source.Close()
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	// CreateTemp uses O_EXCL and 0600, so even simultaneous backups cannot
	// truncate a previous before-image or expose its credentials to other users.
	pattern := fmt.Sprintf("%s-%s-*-%s", slugify(label), timeNowString(), filepath.Base(path))
	target, err := os.CreateTemp(backupDir, pattern)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = target.Close()
			_ = os.Remove(target.Name())
		}
	}()
	if _, err := io.Copy(target, source); err != nil {
		return err
	}
	if err := target.Sync(); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}

func timeNowString() string {
	return strings.ReplaceAll(strings.ReplaceAll(nowString(), ":", ""), "-", "")
}
