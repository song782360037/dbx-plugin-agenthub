package main

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Decode the whole document, not just recognized MCP keys. The maintained TOML
// codec preserves semantic values (including dates and nested/inline tables).
// It does not preserve comments or formatting; never use a line parser to patch
// TOML because table boundaries also occur inside multiline strings and arrays.
func readCodexDocument(path string) (map[string]any, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Size() > 16*1024*1024 {
		return nil, errorsf("MCP 配置超过 16 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeCodexDocument(data)
}

func decodeCodexDocument(data []byte) (map[string]any, error) {
	document := map[string]any{}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if err := toml.Unmarshal(data, &document); err != nil {
		// Parser errors may contain source text, including credentials.
		return nil, errorsf("TOML 解析失败，配置未修改")
	}
	if raw, exists := document["mcp_servers"]; exists {
		if _, ok := raw.(map[string]any); !ok {
			return nil, errorsf("mcp_servers 必须是 TOML 表")
		}
	}
	return document, nil
}

func encodeCodexDocument(document map[string]any) ([]byte, error) {
	var buffer bytes.Buffer
	if err := toml.NewEncoder(&buffer).Encode(document); err != nil {
		return nil, errorsf("TOML 包含无法安全编码的值，配置未修改")
	}
	return buffer.Bytes(), nil
}

func codexRawHash(value any) (string, error) {
	// Sorted map encoding is deterministic and retains TOML-specific types,
	// unlike JSON (which cannot represent nan/inf or distinguish local dates).
	encoded, err := encodeCodexDocument(map[string]any{"server": value})
	if err != nil {
		return "", err
	}
	return hashMCPBytes(encoded), nil
}

func readCodexMCP(adapter mcpAdapter) (map[string]mcpEntry, []string, error) {
	document, err := readCodexDocument(adapter.Path)
	if err != nil {
		return nil, nil, err
	}
	entries := map[string]mcpEntry{}
	servers, _ := document["mcp_servers"].(map[string]any)
	var warnings []string
	for name, raw := range servers {
		rawHash, err := codexRawHash(raw)
		if err != nil {
			return nil, nil, err
		}
		entry := mcpEntry{AgentID: adapter.ID, AgentName: adapter.Name, Path: adapter.Path, Name: name, RawHash: rawHash}
		fields, ok := raw.(map[string]any)
		if !ok {
			entry.Invalid = true
			entry.Warnings = append(entry.Warnings, "MCP 条目必须是 TOML 表")
		} else {
			entry.Disabled, _ = fields["disabled"].(bool)
			if enabled, ok := fields["enabled"].(bool); ok && !enabled {
				entry.Disabled = true
			}
			entry.Config, err = parseCodexMCPEntry(fields)
			entry.Invalid = err != nil
			if err != nil {
				entry.Warnings = append(entry.Warnings, err.Error())
			} else {
				entry.ContentHash = mcpConfigHash(entry.Config)
			}
			for _, key := range []string{"env_vars", "env_http_headers", "http_headers_helper"} {
				if _, exists := fields[key]; exists {
					entry.Warnings = append(entry.Warnings, key+" 是 Codex 私有字段，保留在当前客户端但不跨客户端同步")
				}
			}
		}
		if entry.Disabled {
			entry.Warnings = append(entry.Warnings, "客户端已停用")
		}
		for i, warning := range entry.Warnings {
			entry.Warnings[i] = adapter.Name + "/" + name + "：" + warning
		}
		entries[name] = entry
		warnings = append(warnings, entry.Warnings...)
	}
	return entries, uniqueStrings(warnings), nil
}

func parseCodexMCPEntry(fields map[string]any) (MCPServerConfig, error) {
	config := MCPServerConfig{}
	for _, key := range []string{"command", "url", "http_url", "bearer_token_env_var", "http_headers_helper"} {
		if value, exists := fields[key]; exists {
			if _, ok := value.(string); !ok {
				return config, errorsf("字段 %s 必须是字符串", key)
			}
		}
	}
	for _, key := range []string{"enabled", "disabled"} {
		if value, exists := fields[key]; exists {
			if _, ok := value.(bool); !ok {
				return config, errorsf("字段 %s 必须是布尔值", key)
			}
		}
	}
	config.Command, _ = fields["command"].(string)
	config.Command = normalizeMCPCommandPath(config.Command)
	config.URL, _ = fields["url"].(string)
	if config.URL == "" {
		config.URL, _ = fields["http_url"].(string)
	}
	if raw, exists := fields["args"]; exists {
		values, ok := raw.([]any)
		if !ok {
			return config, errorsf("args 必须是字符串数组")
		}
		for _, value := range values {
			arg, ok := value.(string)
			if !ok {
				return config, errorsf("args 必须是字符串数组")
			}
			config.Args = append(config.Args, arg)
		}
	}
	var err error
	config.Env, err = codexStringMap(fields, "env")
	if err != nil {
		return config, err
	}
	config.Headers, err = codexStringMap(fields, "http_headers")
	if err != nil {
		return config, err
	}
	if _, err := codexStringMap(fields, "env_http_headers"); err != nil {
		return config, err
	}
	if bearer, _ := fields["bearer_token_env_var"].(string); bearer != "" {
		if !envKeyPattern.MatchString(bearer) {
			return config, errorsf("bearer_token_env_var 必须是环境变量名")
		}
		// Do not silently discard a second Authorization source on a roundtrip.
		for key := range config.Headers {
			if strings.EqualFold(key, "Authorization") {
				return config, errorsf("Authorization 同时配置了静态值和 bearer 环境变量")
			}
		}
		config.Headers["Authorization"] = "Bearer ${" + bearer + "}"
	}
	if _, exists := fields["bearer_token"]; exists {
		return config, errorsf("Codex 不支持 bearer_token，请使用 bearer_token_env_var")
	}
	// Codex accepts fractional seconds and the legacy millisecond field.
	for _, key := range []string{"startup_timeout_ms", "startup_timeout_sec"} {
		if raw, exists := fields[key]; exists {
			var milliseconds float64
			switch value := raw.(type) {
			case int64:
				milliseconds = float64(value)
			case float64:
				if key == "startup_timeout_ms" {
					return config, errorsf("startup_timeout_ms 必须是整数")
				}
				milliseconds = value
			default:
				return config, errorsf("字段 %s 必须是数字", key)
			}
			if key == "startup_timeout_sec" {
				milliseconds *= 1000
			}
			if math.IsNaN(milliseconds) || math.IsInf(milliseconds, 0) || milliseconds < 0 || milliseconds >= float64(int(^uint(0)>>1)) {
				return config, errorsf("字段 %s 超出范围", key)
			}
			config.TimeoutMS = int(math.Round(milliseconds))
		}
	}
	if config.Command != "" {
		config.Transport = "stdio"
	} else if config.URL != "" {
		config.Transport = "http"
	}
	if err := validateMCPConfig(config); err != nil {
		return config, err
	}
	if config.Transport == "stdio" {
		config.Headers = map[string]string{}
	} else {
		config.Env = map[string]string{}
		config.Args = nil
	}
	return config, nil
}

func codexStringMap(fields map[string]any, key string) (map[string]string, error) {
	out := map[string]string{}
	raw, exists := fields[key]
	if !exists {
		return out, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, errorsf("字段 %s 必须是字符串表", key)
	}
	for name, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, errorsf("字段 %s 必须是字符串表", key)
		}
		out[name] = text
	}
	return out, nil
}

// Codex 配置里通常有注释和大量与 MCP 无关的键，整份重编码会丢掉这些内容。
// 因此只在文本层面替换目标服务的语句块，其余字节保持不变，再解析校验语义。
func applyCodexMCP(path, name string, config MCPServerConfig, remove bool) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	text := ""
	if fileExists(path) {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(data) > 16*1024*1024 {
			return errorsf("MCP 配置超过 16 MiB")
		}
		text = strings.TrimPrefix(string(data), "\ufeff")
	}
	before, err := decodeCodexDocument([]byte(text))
	if err != nil {
		return err
	}
	block := ""
	if remove {
		servers, _ := before["mcp_servers"].(map[string]any)
		if servers == nil {
			return nil
		}
		if _, exists := servers[name]; !exists {
			return nil
		}
	} else {
		if err := validateMCPConfig(config); err != nil {
			return err
		}
		if err := adapterValidate(mcpAdapter{Kind: "codex", Name: "Codex"}, config); err != nil {
			return err
		}
		if servers, ok := before["mcp_servers"].(map[string]any); ok {
			if raw, exists := servers[name]; exists {
				if _, isTable := raw.(map[string]any); !isTable {
					return errorsf("现有 MCP 条目不是 TOML 表，不能安全合并")
				}
			}
		}
		existing, _ := codexServerFields(before, name)
		fields, err := mergeCodexFields(existing, config)
		if err != nil {
			return err
		}
		block, err = encodeCodexServerBlock(name, fields)
		if err != nil {
			return err
		}
	}
	crlf := strings.Contains(text, "\r\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	servers, _ := before["mcp_servers"].(map[string]any)
	lines, err = spliceTOMLServer(lines, name, block, servers, crlf)
	if err != nil {
		return err
	}
	output := []byte(strings.Join(lines, "\n"))
	if len(output) > 0 {
		output = append(output, '\n')
	}
	after, err := decodeCodexDocument(output)
	if err != nil {
		return err
	}
	if err := verifyCodexSplice(before, after, name, config, remove); err != nil {
		return err
	}
	return atomicWriteFile(path, output, mode)
}

func codexServerFields(document map[string]any, name string) (map[string]any, bool) {
	servers, _ := document["mcp_servers"].(map[string]any)
	if servers == nil {
		return nil, false
	}
	raw, exists := servers[name]
	if !exists {
		return nil, false
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	return fields, true
}

var codexOwnedKeys = map[string]bool{
	"command": true, "args": true, "env": true, "url": true, "http_url": true,
	"http_headers": true, "bearer_token_env_var": true,
	"startup_timeout_sec": true, "startup_timeout_ms": true,
}

func mergeCodexFields(existing map[string]any, config MCPServerConfig) (map[string]any, error) {
	oldHeaders, err := codexStringMap(existing, "http_headers")
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	for key, value := range existing {
		if codexOwnedKeys[key] {
			continue
		}
		fields[key] = value
	}
	if config.Transport == "stdio" {
		fields["command"] = config.Command
		if len(config.Args) > 0 {
			fields["args"] = config.Args
		}
		if len(config.Env) > 0 {
			fields["env"] = config.Env
		}
	} else {
		fields["url"] = config.URL
		headers := mcpServerConfigCopy(config).Headers
		// 静态 Authorization 头保持原样，只有值变化或来自 bearer 环境变量时才改写。
		if bearer, ok := codexBearerToken(headers); ok && oldHeaders["Authorization"] != headers["Authorization"] {
			fields["bearer_token_env_var"] = bearer
			delete(headers, "Authorization")
		}
		if len(headers) > 0 {
			fields["http_headers"] = headers
		}
	}
	if config.TimeoutMS > 0 {
		if config.TimeoutMS%1000 == 0 {
			fields["startup_timeout_sec"] = int64(config.TimeoutMS / 1000)
		} else {
			fields["startup_timeout_sec"] = float64(config.TimeoutMS) / 1000
		}
	}
	return fields, nil
}

func encodeCodexServerBlock(name string, fields map[string]any) (string, error) {
	encoded, err := encodeCodexDocument(map[string]any{"mcp_servers": map[string]any{name: fields}})
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(encoded), "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "[mcp_servers]" {
			continue
		}
		kept = append(kept, line)
	}
	block := strings.Trim(strings.Join(kept, "\n"), "\n")
	if block == "" {
		return "", errorsf("无法生成 MCP 配置")
	}
	return block + "\n", nil
}

type tomlScanState struct {
	basicMulti   bool
	literalMulti bool
	arrays       int
}

func (state *tomlScanState) atTop() bool {
	return !state.basicMulti && !state.literalMulti && state.arrays == 0
}

func (state *tomlScanState) scanLine(line string) {
	for index := 0; index < len(line); index++ {
		char := line[index]
		if state.basicMulti {
			if char == '\\' {
				index++
				continue
			}
			if char == '"' {
				// 结束分隔符是连续引号的最后三个，前面的引号属于字符串内容。
				if run := tomlQuoteRun(line, index, '"'); run >= 3 {
					state.basicMulti = false
					index += run - 1
				}
			}
			continue
		}
		if state.literalMulti {
			if char == '\'' {
				if run := tomlQuoteRun(line, index, '\''); run >= 3 {
					state.literalMulti = false
					index += run - 1
				}
			}
			continue
		}
		switch char {
		case '#':
			return
		case '"':
			if strings.HasPrefix(line[index:], `"""`) {
				state.basicMulti = true
				index += 2
				continue
			}
			index = skipTOMLString(line, index, '"')
		case '\'':
			if strings.HasPrefix(line[index:], `'''`) {
				state.literalMulti = true
				index += 2
				continue
			}
			index = skipTOMLString(line, index, '\'')
		case '[':
			state.arrays++
		case ']':
			if state.arrays > 0 {
				state.arrays--
			}
		}
	}
}

func tomlQuoteRun(line string, start int, quote byte) int {
	run := 0
	for start+run < len(line) && line[start+run] == quote {
		run++
	}
	return run
}

func skipTOMLString(line string, start int, quote byte) int {
	for index := start + 1; index < len(line); index++ {
		if quote == '"' && line[index] == '\\' {
			index++
			continue
		}
		if line[index] == quote {
			return index
		}
	}
	return len(line)
}

type tomlStatement struct {
	start, end int
	header     bool
	path       []string
}

func scanTOMLStatements(lines []string) ([]tomlStatement, error) {
	var statements []tomlStatement
	state := tomlScanState{}
	var table []string
	start := -1
	for index, line := range lines {
		if start < 0 {
			start = index
		}
		state.scanLine(line)
		if !state.atTop() {
			continue
		}
		statement := tomlStatement{start: start, end: index + 1}
		first := strings.TrimSpace(tomlStripComment(lines[start]))
		if strings.HasPrefix(first, "[") {
			statement.header = true
			path, err := parseTOMLHeaderPath(first)
			if err != nil {
				return nil, err
			}
			statement.path = path
			table = path
		} else if key, ok := tomlStatementKey(first); ok {
			keyPath, err := parseTOMLKeyPath(key)
			if err != nil {
				return nil, err
			}
			path := make([]string, 0, len(table)+len(keyPath))
			path = append(path, table...)
			path = append(path, keyPath...)
			statement.path = path
		}
		statements = append(statements, statement)
		start = -1
	}
	if start >= 0 {
		return nil, errorsf("TOML 语句未闭合，配置未修改")
	}
	return statements, nil
}

// 表头和键都在单行内完成，多行字符串不参与这段判断。
func tomlStripComment(line string) string {
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case '#':
			return line[:index]
		case '"', '\'':
			quote := line[index]
			if strings.HasPrefix(line[index:], strings.Repeat(string(quote), 3)) {
				return line
			}
			index = skipTOMLString(line, index, quote)
		}
	}
	return line
}

func tomlStatementKey(line string) (string, bool) {
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case '"', '\'':
			quote := line[index]
			if strings.HasPrefix(line[index:], strings.Repeat(string(quote), 3)) {
				return "", false
			}
			index = skipTOMLString(line, index, quote)
		case '=':
			return strings.TrimSpace(line[:index]), true
		}
	}
	return "", false
}

func parseTOMLHeaderPath(line string) ([]string, error) {
	line = strings.TrimSpace(line)
	double := strings.HasPrefix(line, "[[")
	open := 1
	if double {
		open = 2
	}
	if len(line) < open+1 || !strings.HasSuffix(line, strings.Repeat("]", open)) {
		return nil, errorsf("TOML 表头格式无效，配置未修改")
	}
	inner := strings.TrimSpace(line[open : len(line)-open])
	path, err := parseTOMLKeyPath(inner)
	if err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, errorsf("TOML 表头为空，配置未修改")
	}
	return path, nil
}

func parseTOMLKeyPath(text string) ([]string, error) {
	var path []string
	for index := 0; index < len(text); {
		char := text[index]
		switch {
		case char == ' ' || char == '\t':
			index++
		case char == '.':
			return nil, errorsf("TOML 键路径格式无效，配置未修改")
		case char == '"':
			end := skipTOMLString(text, index, '"')
			if end >= len(text) {
				return nil, errorsf("TOML 键路径格式无效，配置未修改")
			}
			segment, err := strconv.Unquote(text[index : end+1])
			if err != nil {
				return nil, errorsf("TOML 键路径格式无效，配置未修改")
			}
			path = append(path, segment)
			index = end + 1
		case char == '\'':
			end := skipTOMLString(text, index, '\'')
			if end >= len(text) {
				return nil, errorsf("TOML 键路径格式无效，配置未修改")
			}
			path = append(path, text[index+1:end])
			index = end + 1
		default:
			start := index
			for index < len(text) && ((text[index] >= 'a' && text[index] <= 'z') || (text[index] >= 'A' && text[index] <= 'Z') || (text[index] >= '0' && text[index] <= '9') || text[index] == '_' || text[index] == '-') {
				index++
			}
			if start == index {
				return nil, errorsf("TOML 键路径格式无效，配置未修改")
			}
			path = append(path, text[start:index])
		}
		for index < len(text) && (text[index] == ' ' || text[index] == '\t') {
			index++
		}
		if index >= len(text) {
			break
		}
		if text[index] != '.' {
			return nil, errorsf("TOML 键路径格式无效，配置未修改")
		}
		index++
	}
	if len(path) == 0 {
		return nil, errorsf("TOML 键路径为空，配置未修改")
	}
	return path, nil
}

func spliceTOMLServer(lines []string, name, block string, servers map[string]any, crlf bool) ([]string, error) {
	statements, err := scanTOMLStatements(lines)
	if err != nil {
		return nil, err
	}
	remove := make([]bool, len(lines))
	insertAt := -1
	inline := -1
	for _, statement := range statements {
		if len(statement.path) >= 2 && statement.path[0] == "mcp_servers" && statement.path[1] == name {
			for index := statement.start; index < statement.end; index++ {
				remove[index] = true
			}
		} else if len(statement.path) == 1 && statement.path[0] == "mcp_servers" && !statement.header {
			inline = statement.start
			for index := statement.start; index < statement.end; index++ {
				remove[index] = true
			}
		}
	}
	if inline >= 0 {
		// 内联写法无法做局部键替换，改为等价的表写法，其他顶层内容保持原样。
		others := map[string]any{}
		for key, value := range servers {
			if key != name {
				others[key] = value
			}
		}
		replacement := ""
		if len(others) > 0 {
			section, err := encodeCodexMCPSection(others)
			if err != nil {
				return nil, err
			}
			replacement += section
		}
		replacement += block
		lines = append([]string{}, lines...)
		if strings.TrimSpace(replacement) == "" {
			replacement = ""
		}
		// 追加到文件末尾：插在中间会让后续顶层键被最后一张表“吞掉”。
		return rebuildTOMLLines(lines, remove, len(lines), replacement, crlf), nil
	}
	if block == "" {
		if !anyTOMLRemoved(remove) {
			return lines, nil
		}
		insertAt = len(lines)
	} else {
		// 顶层点号键（如 mcp_servers.other.command）依赖所在表上下文，插到中间
		// 会改变其含义；统一追加到文件末尾可保持原有语句语义不变。
		insertAt = len(lines)
	}
	return rebuildTOMLLines(lines, remove, insertAt, block, crlf), nil
}

func rebuildTOMLLines(lines []string, remove []bool, insertAt int, replacement string, crlf bool) []string {
	var blockLines []string
	if strings.TrimSpace(replacement) != "" {
		blockLines = strings.Split(strings.TrimSuffix(replacement, "\n"), "\n")
		if crlf {
			for index := range blockLines {
				blockLines[index] += "\r"
			}
		}
	}
	var out []string
	inserted := false
	insert := func() {
		if inserted || len(blockLines) == 0 {
			return
		}
		inserted = true
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, blankTOMLine(crlf))
		}
		out = append(out, blockLines...)
		out = append(out, blankTOMLine(crlf))
	}
	for index, line := range lines {
		if index == insertAt {
			insert()
		}
		if remove[index] {
			continue
		}
		out = append(out, line)
	}
	if insertAt >= len(lines) {
		if !inserted {
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, blankTOMLine(crlf))
			}
			out = append(out, blockLines...)
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}

func encodeCodexMCPSection(servers map[string]any) (string, error) {
	encoded, err := encodeCodexDocument(map[string]any{"mcp_servers": servers})
	if err != nil {
		return "", err
	}
	text := strings.Trim(strings.ReplaceAll(string(encoded), "\r\n", "\n"), "\n")
	if text == "" {
		return "", errorsf("无法生成 MCP 配置")
	}
	return text + "\n", nil
}

func anyTOMLRemoved(remove []bool) bool {
	for _, value := range remove {
		if value {
			return true
		}
	}
	return false
}

func blankTOMLine(crlf bool) string {
	if crlf {
		return "\r"
	}
	return ""
}

func verifyCodexSplice(before, after map[string]any, name string, config MCPServerConfig, remove bool) error {
	if err := compareCodexUnrelated(before, after, name); err != nil {
		return err
	}
	servers, _ := after["mcp_servers"].(map[string]any)
	if remove {
		if _, exists := servers[name]; exists {
			return errorsf("生成的配置仍包含待删除的服务，配置未修改")
		}
		return nil
	}
	fields, ok := servers[name].(map[string]any)
	if !ok {
		return errorsf("生成的 MCP 配置无法正确读取，配置未修改")
	}
	parsed, err := parseCodexMCPEntry(fields)
	if err != nil {
		return err
	}
	if !sameMCPConfig(parsed, config) {
		return errorsf("生成的 MCP 配置与预期不一致，配置未修改")
	}
	original, _ := codexServerFields(before, name)
	for key, value := range original {
		if codexOwnedKeys[key] {
			continue
		}
		want, err := canonicalTOMLValue(value)
		if err != nil {
			return err
		}
		got, err := canonicalTOMLValue(fields[key])
		if err != nil {
			return err
		}
		if want != got {
			return fmt.Errorf("字段 %s 是 Codex 私有配置，无法在修改后保留；配置未修改", key)
		}
	}
	return nil
}

func compareCodexUnrelated(before, after map[string]any, name string) error {
	clean := func(document map[string]any) map[string]any {
		out := make(map[string]any, len(document))
		for key, value := range document {
			out[key] = value
		}
		servers, _ := out["mcp_servers"].(map[string]any)
		if servers == nil {
			return out
		}
		copied := make(map[string]any, len(servers))
		for key, value := range servers {
			if key == name {
				continue
			}
			copied[key] = value
		}
		if len(copied) == 0 {
			delete(out, "mcp_servers")
		} else {
			out["mcp_servers"] = copied
		}
		return out
	}
	want, err := encodeCodexDocument(clean(before))
	if err != nil {
		return err
	}
	got, err := encodeCodexDocument(clean(after))
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return errorsf("修改会破坏其他 TOML 配置，已放弃写入")
	}
	return nil
}

func canonicalTOMLValue(value any) (string, error) {
	encoded, err := encodeCodexDocument(map[string]any{"value": value})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func sameMCPConfig(left, right MCPServerConfig) bool {
	if left.Transport != right.Transport || normalizeMCPCommandPath(left.Command) != normalizeMCPCommandPath(right.Command) || left.URL != right.URL || left.TimeoutMS != right.TimeoutMS {
		return false
	}
	if len(left.Args) != len(right.Args) {
		return false
	}
	for index := range left.Args {
		if left.Args[index] != right.Args[index] {
			return false
		}
	}
	return sameStringMap(left.Env, right.Env) && sameStringMap(left.Headers, right.Headers)
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func codexBearerToken(headers map[string]string) (string, bool) {
	value, ok := headers["Authorization"]
	if !ok || !strings.HasPrefix(value, "Bearer ${") || !strings.HasSuffix(value, "}") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(value, "Bearer ${"), "}")
	return name, envKeyPattern.MatchString(name)
}
