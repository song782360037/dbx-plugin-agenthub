package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type mcpFileChange struct {
	adapter     mcpAdapter
	before      []byte
	after       []byte
	existed     bool
	mode        os.FileMode
	entry       mcpEntry
	entryExists bool
	result      mcpEntry
}

func mcpEntryMatchesTarget(entry mcpEntry, record *MCPRecord, agentID string) bool {
	if record == nil || entry.Invalid {
		return false
	}
	target := record.Targets[agentID]
	if target == nil || !samePath(entry.Path, target.Path) {
		return false
	}
	return target.RawHash != "" && entry.RawHash == target.RawHash
}

func validateMCPTargetPath(record *MCPRecord, adapter mcpAdapter) error {
	if record != nil {
		if target := record.Targets[adapter.ID]; target != nil && !samePath(target.Path, adapter.Path) {
			return fmt.Errorf("%s 的配置路径已变更；请恢复原配置路径后再操作，旧文件未修改", adapter.Name)
		}
	}
	return nil
}

func readMCPFile(path string) ([]byte, os.FileMode, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, 0o600, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, errors.New("MCP 配置必须是普通文件，不能替换目录或符号链接")
	}
	if info.Size() > 16*1024*1024 {
		return nil, 0, false, errors.New("MCP 配置超过 16 MiB")
	}
	data, err := os.ReadFile(path)
	return data, info.Mode().Perm(), true, err
}

func (m *Manager) prepareMCPChange(adapter mcpAdapter, name string, config MCPServerConfig, remove bool) (*mcpFileChange, error) {
	if err := validateMCPTargetPath(m.mcpRecordsSnapshot()[name], adapter); err != nil {
		return nil, err
	}
	before, mode, existed, err := readMCPFile(adapter.Path)
	if err != nil {
		return nil, err
	}
	change := &mcpFileChange{adapter: adapter, before: before, existed: existed, mode: mode}
	if remove && !existed {
		return change, nil
	}
	stage, err := os.MkdirTemp(filepath.Join(m.dataDir, "tmp"), "mcp-edit-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	staged := adapter
	staged.Path = filepath.Join(stage, "config")
	if existed {
		if err := os.WriteFile(staged.Path, before, 0o600); err != nil {
			return nil, err
		}
	}
	entries, _, err := readMCPAdapter(staged)
	if err != nil {
		return nil, err
	}
	change.entry, change.entryExists = entries[name]
	change.entry.Path = adapter.Path
	if adapter.Format == "toml" {
		err = applyCodexMCP(staged.Path, name, config, remove)
	} else {
		err = applyJSONMCP(staged, name, config, remove)
	}
	if err != nil {
		return nil, err
	}
	change.after, err = os.ReadFile(staged.Path)
	if err != nil {
		return nil, err
	}
	entries, _, err = readMCPAdapter(staged)
	if err != nil {
		return nil, err
	}
	result, exists := entries[name]
	if remove && exists {
		return nil, errors.New("生成的配置仍包含待删除的 MCP 服务")
	}
	if !remove && (!exists || result.Invalid) {
		return nil, errors.New("生成的 MCP 配置无法正确读取")
	}
	change.result = result
	return change, nil
}

func (change *mcpFileChange) checkBefore() error {
	data, mode, exists, err := readMCPFile(change.adapter.Path)
	if err != nil {
		return err
	}
	if exists != change.existed || !bytes.Equal(data, change.before) || (exists && mode != change.mode) {
		return errors.New("配置在预览后被其他程序修改，请刷新后重试")
	}
	return nil
}

func (change *mcpFileChange) rollback() error {
	data, _, exists, err := readMCPFile(change.adapter.Path)
	if err != nil {
		return err
	}
	if !exists || !bytes.Equal(data, change.after) {
		return errors.New("配置已被其他程序修改，未自动回滚；请从备份恢复")
	}
	if change.existed {
		return atomicWriteFile(change.adapter.Path, change.before, change.mode)
	}
	return os.Remove(change.adapter.Path)
}

func (m *Manager) commitMCPChanges(changes []*mcpFileChange, save func() error) error {
	var paths []string
	for _, change := range changes {
		path := filepath.Clean(change.adapter.Path)
		for _, previous := range paths {
			if samePath(path, previous) {
				return errors.New("多个 MCP 客户端指向同一配置文件，无法安全同时写入")
			}
		}
		paths = append(paths, path)
		if err := change.checkBefore(); err != nil {
			return fmt.Errorf("%s：%w", change.adapter.Name, err)
		}
	}
	var written []*mcpFileChange
	fail := func(cause error) error {
		for index := len(written) - 1; index >= 0; index-- {
			change := written[index]
			if err := change.rollback(); err != nil {
				cause = errors.Join(cause, fmt.Errorf("%s 回滚失败：%w", change.adapter.Name, err))
			}
		}
		return cause
	}
	for _, change := range changes {
		if bytes.Equal(change.before, change.after) {
			continue
		}
		if err := change.checkBefore(); err != nil {
			return fail(fmt.Errorf("%s：%w", change.adapter.Name, err))
		}
		if change.existed {
			if err := backupFile(change.adapter.Path, filepath.Join(m.dataDir, "backups", "mcp"), change.adapter.ID); err != nil {
				return fail(err)
			}
		}
		if err := change.checkBefore(); err != nil {
			return fail(err)
		}
		if err := atomicWriteFile(change.adapter.Path, change.after, change.mode); err != nil {
			return fail(fmt.Errorf("%s 写入失败：%w", change.adapter.Name, err))
		}
		written = append(written, change)
	}
	if err := save(); err != nil {
		return fail(err)
	}
	return nil
}

func (m *Manager) replaceMCPRecord(name string, record *MCPRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := m.state.MCPServers[name]
	if record == nil {
		delete(m.state.MCPServers, name)
	} else {
		m.state.MCPServers[name] = record
	}
	if err := m.saveStateLocked(); err != nil {
		if previous == nil {
			delete(m.state.MCPServers, name)
		} else {
			m.state.MCPServers[name] = previous
		}
		return err
	}
	return nil
}
