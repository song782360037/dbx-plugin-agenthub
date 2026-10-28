package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type UpdateStatus struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Commit  string `json:"commit,omitempty"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

type UpdateInput struct {
	Names           []string `json:"names,omitempty"`
	PreviewIDs      []string `json:"previewIds"`
	OverwriteLocal  bool     `json:"overwriteLocal"`
	Confirmed       bool     `json:"confirmed"`
	AcknowledgeRisk bool     `json:"acknowledgeRisk"`
}

type UpdatePreview struct {
	Name      string    `json:"name"`
	PreviewID string    `json:"previewId"`
	Candidate Candidate `json:"candidate"`
	Source    Source    `json:"source"`
	Warnings  []string  `json:"warnings,omitempty"`
}

// Server-only binding: ordinary source previews cannot bypass the update risk scan.
type updateReview struct {
	Name        string
	ContentHash string
	Source      Source
	UpdatedAt   string
}

func (m *Manager) checkUpdates(names []string, force bool) ([]UpdateStatus, error) {
	settings := m.settingsSnapshot()
	client, err := newGitHubClientForManager(settings)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for _, name := range names {
		selected[strings.TrimSpace(name)] = true
	}
	var results []UpdateStatus
	err = m.withOpLock(func() error {
		m.mu.Lock()
		records := make([]Record, 0, len(m.state.Skills))
		for name, record := range m.state.Skills {
			if len(selected) > 0 && !selected[name] {
				continue
			}
			records = append(records, *record)
		}
		m.mu.Unlock()
		for _, record := range records {
			if record.Source.Type != "github" || record.Source.Repo == "" {
				results = append(results, UpdateStatus{Name: record.Name, Status: "unsupported", Message: "该来源不支持检查更新"})
				continue
			}
			if record.Source.Commit == "" {
				m.mu.Lock()
				if current := m.state.Skills[record.Name]; current != nil {
					current.Available = nil
				}
				m.mu.Unlock()
				results = append(results, UpdateStatus{Name: record.Name, Status: "unsupported", Message: "安装时未记录 commit（可能因为 GitHub API 不可用），请配置 Token 后重新安装再检查"})
				continue
			}
			if !force && record.LastCheckedAt != "" {
				if last, err := time.Parse(time.RFC3339, record.LastCheckedAt); err == nil && time.Since(last) < 10*time.Minute {
					results = append(results, UpdateStatus{Name: record.Name, Status: "cached", Message: "10 分钟内已检查过"})
					continue
				}
			}
			owner, repo, ok := strings.Cut(record.Source.Repo, "/")
			if !ok {
				results = append(results, UpdateStatus{Name: record.Name, Status: "error", Error: "来源仓库格式无效"})
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			resolvedRef, latest, checkErr := client.resolveRef(ctx, owner, repo, record.Source.Ref)
			cancel()
			if checkErr == nil && latest == "" {
				checkErr = errors.New("无法获取来源 commit，请稍后重试")
			}
			changed := false
			if checkErr == nil && latest != record.Source.Commit {
				// Source.Commit identifies the downloaded repository snapshot, NOT the
				// last path commit. Compare candidate bytes when snapshots differ so an
				// unrelated repository change never becomes a perpetual skill update.
				preview, err := m.previewSourceWithSettings(SourceInput{
					Type: "github", Value: record.Source.Repo, Ref: latest, Path: record.Source.Path,
				}, settings)
				if err != nil {
					checkErr = err
				} else {
					candidate, err := candidateForRecord(preview, record)
					checkErr = err
					if err == nil {
						changed = candidate.ContentHash != record.ContentHash
					}
					m.dropPreview(preview.ID)
				}
			}
			m.mu.Lock()
			if current := m.state.Skills[record.Name]; current != nil {
				current.LastCheckedAt = nowString()
				if checkErr != nil {
					current.Available = &AvailableUpdate{CheckedAt: current.LastCheckedAt, Error: checkErr.Error()}
					results = append(results, UpdateStatus{Name: record.Name, Status: "error", Error: checkErr.Error()})
				} else if changed {
					current.Available = &AvailableUpdate{Commit: latest, Version: resolvedRef, CheckedAt: current.LastCheckedAt}
					results = append(results, UpdateStatus{Name: record.Name, Status: "update", Commit: latest})
				} else {
					current.Available = nil
					results = append(results, UpdateStatus{Name: record.Name, Status: "current", Commit: latest})
				}
			}
			m.mu.Unlock()
		}
		return m.saveState()
	})
	return results, err
}

func candidateForRecord(preview *Preview, record Record) (Candidate, error) {
	candidates, err := indexCandidates(preview.Candidates)
	if err != nil {
		return Candidate{}, err
	}
	candidate, ok := candidates[record.Name]
	if !ok || filepath.Clean(candidate.RelPath) != filepath.Clean(record.Source.Path) {
		return Candidate{}, fmt.Errorf("%s 在来源路径中未找到唯一的同名技能", record.Name)
	}
	if err := validateCandidate(candidate); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}

func (m *Manager) updateRecord(name string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.state.Skills[name]
	if record == nil {
		return Record{}, fmt.Errorf("技能 %s 不在中心库中", name)
	}
	if record.Source.Type != "github" || record.Source.Repo == "" {
		return Record{}, fmt.Errorf("%s 的来源不支持自动更新", name)
	}
	copy := *record
	copy.Targets = map[string]*TargetRecord{}
	for id, target := range record.Targets {
		targetCopy := *target
		copy.Targets[id] = &targetCopy
	}
	return copy, nil
}

func (m *Manager) previewUpdates(names []string) ([]UpdatePreview, error) {
	names = uniqueStrings(names)
	settings := m.settingsSnapshot()
	// Update approval must always include risk findings, even if routine scanning is off.
	settings.ScanLevel = "basic"
	previews := []UpdatePreview{}
	err := m.withOpLock(func() error {
		if len(names) == 0 {
			m.mu.Lock()
			for name, record := range m.state.Skills {
				if record.Source.Type == "github" && record.Source.Repo != "" {
					names = append(names, name)
				}
			}
			m.mu.Unlock()
			sort.Strings(names)
		}
		if len(names) == 0 {
			return errors.New("没有可更新的技能")
		}
		for _, name := range names {
			record, err := m.updateRecord(name)
			if err != nil {
				return err
			}
			preview, err := m.previewSourceWithSettings(SourceInput{
				Type: "github", Value: record.Source.Repo, Ref: record.Source.Ref, Path: record.Source.Path,
			}, settings)
			if err != nil {
				return fmt.Errorf("%s 下载新版本失败：%w", name, err)
			}
			candidate, err := candidateForRecord(preview, record)
			if err != nil {
				m.dropPreview(preview.ID)
				return err
			}
			m.mu.Lock()
			preview.Update = &updateReview{Name: name, ContentHash: record.ContentHash, Source: record.Source, UpdatedAt: record.UpdatedAt}
			m.mu.Unlock()
			previews = append(previews, UpdatePreview{Name: name, PreviewID: preview.ID, Candidate: candidate, Source: preview.Source, Warnings: preview.Warnings})
		}
		return nil
	})
	if err != nil {
		for _, preview := range previews {
			m.dropPreview(preview.PreviewID)
		}
		return nil, err
	}
	return previews, nil
}

func (m *Manager) updateSkills(input UpdateInput) (map[string]any, error) {
	if !input.Confirmed {
		return nil, errors.New("更新技能需要明确确认")
	}
	ids := uniqueStrings(input.PreviewIDs)
	if len(ids) == 0 {
		return nil, errors.New("请先调用 skills/previewUpdate 查看候选版本与风险，再提交 previewIds")
	}
	var updated []string
	var warnings []string
	err := m.withOpLock(func() error {
		type pendingUpdate struct {
			preview   *Preview
			candidate Candidate
			targets   []AgentSpec
		}
		var pending []pendingUpdate
		names := map[string]bool{}
		agents := buildAgents(m.settingsSnapshot())
		// Validate the entire reviewed batch before replacing any hub or target.
		for _, id := range ids {
			preview, ok := m.getPreview(id)
			if !ok || time.Since(preview.CreatedAt) > 2*time.Hour || preview.Update == nil {
				return errors.New("更新预览无效或已过期，请重新调用 skills/previewUpdate")
			}
			review := preview.Update
			if names[review.Name] {
				return fmt.Errorf("技能 %s 有多个更新预览，请只选择一个", review.Name)
			}
			names[review.Name] = true
			record, err := m.updateRecord(review.Name)
			if err != nil {
				return err
			}
			if record.Source != review.Source || record.ContentHash != review.ContentHash || record.UpdatedAt != review.UpdatedAt {
				return fmt.Errorf("%s 在预览后已变化，请重新预览更新", record.Name)
			}
			candidate, err := candidateForRecord(preview, record)
			if err != nil {
				return err
			}
			if err := verifyReviewedCandidate(preview, candidate, input.AcknowledgeRisk); err != nil {
				return err
			}
			if err := m.preflightUpdate(record, input.OverwriteLocal); err != nil {
				return fmt.Errorf("%s：%w", record.Name, err)
			}
			var targets []AgentSpec
			for agentID, target := range record.Targets {
				if !target.Enabled || target.Mode == "external" {
					continue
				}
				if agent, exists := findAgent(agents, agentID); exists {
					targets = append(targets, agent)
				}
			}
			if err := m.preflightInstall(candidate, targets, input.OverwriteLocal); err != nil {
				return err
			}
			pending = append(pending, pendingUpdate{preview: preview, candidate: candidate, targets: targets})
		}
		if len(input.Names) > 0 {
			requested := uniqueStrings(input.Names)
			if len(requested) != len(names) {
				return errors.New("names 与更新预览不匹配")
			}
			for _, name := range requested {
				if !names[name] {
					return errors.New("names 与更新预览不匹配")
				}
			}
		}
		for _, item := range pending {
			_, installWarnings, err := m.installCandidate(item.preview, item.candidate, item.targets, "auto", input.OverwriteLocal, input.AcknowledgeRisk)
			if err != nil {
				return fmt.Errorf("%s 更新失败：%w", item.candidate.Name, err)
			}
			m.dropPreview(item.preview.ID)
			warnings = append(warnings, installWarnings...)
			updated = append(updated, item.candidate.Name)
		}
		return nil
	})
	if err != nil {
		if len(updated) > 0 {
			return nil, fmt.Errorf("已更新 %s；%w", strings.Join(updated, "、"), err)
		}
		return nil, err
	}
	return map[string]any{
		"success": true, "updated": updated, "warnings": uniqueStrings(warnings),
		"message": fmt.Sprintf("已更新 %d 个技能", len(updated)),
	}, nil
}

func (m *Manager) preflightUpdate(record Record, overwriteLocal bool) error {
	hub := filepath.Join(m.hubDir, record.Name)
	if err := checkHubDrift(hub, record.ContentHash, overwriteLocal); err != nil {
		return err
	}
	for _, target := range record.Targets {
		if !target.Enabled || target.Mode == "external" {
			continue
		}
		state, err := inspectTarget(target.Path, hub)
		if err != nil {
			return err
		}
		if !state.Exists {
			continue
		}
		if state.IsLink {
			if !samePath(state.LinkTarget, hub) {
				return fmt.Errorf("%s 的链接指向了其他目录，无法自动更新", target.Path)
			}
			continue
		}
		if !state.Managed {
			return fmt.Errorf("%s 未纳管，无法自动更新", target.Path)
		}
		if !overwriteLocal && (target.ContentHash == "" || state.Hash != target.ContentHash) {
			return fmt.Errorf("%s 的副本已被本地修改；勾选“覆盖本地修改”后再更新", target.Path)
		}
	}
	return nil
}

func checkHubDrift(hub, baseline string, overwrite bool) error {
	if overwrite || !pathExistsLstat(hub) {
		return nil
	}
	hash, _, _, _, err := hashDir(hub)
	if err != nil {
		return fmt.Errorf("无法校验中心库 %s：%w", hub, err)
	}
	if baseline == "" || hash != baseline {
		return fmt.Errorf("中心库 %s 已被本地修改；确认覆盖本地修改后才能继续", hub)
	}
	return nil
}

func (m *Manager) testConnection() (map[string]any, error) {
	settings := m.settingsSnapshot()
	client, err := newGitHubClientForManager(settings)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var result struct {
		Rate struct {
			Limit     int   `json:"limit"`
			Remaining int   `json:"remaining"`
			Reset     int64 `json:"reset"`
		} `json:"rate"`
	}
	if err := client.getJSON(ctx, client.apiURL+"/rate_limit", &result); err != nil {
		return nil, err
	}
	auth := "匿名"
	if client.token != "" {
		auth = "已配置 Token"
	}
	return map[string]any{
		"success": true, "auth": auth, "limit": result.Rate.Limit,
		"remaining": result.Rate.Remaining,
		"resetAt":   time.Unix(result.Rate.Reset, 0).UTC().Format(time.RFC3339),
		"message":   fmt.Sprintf("GitHub API 可访问（%s，剩余 %d/%d 次）", auth, result.Rate.Remaining, result.Rate.Limit),
	}, nil
}
