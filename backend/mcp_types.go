package main

const mcpMaskedSecret = "••••••"

func isManagedMarker(marker *Marker) bool {
	return marker != nil && (marker.ManagedBy == appID || marker.ManagedBy == legacyAppID)
}

type MCPServerConfig struct {
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	TimeoutMS int               `json:"timeoutMs,omitempty"`
}

type MCPRecord struct {
	Name        string                `json:"name"`
	Config      MCPServerConfig       `json:"config"`
	Source      string                `json:"source"`
	ContentHash string                `json:"contentHash"`
	InstalledAt string                `json:"installedAt"`
	UpdatedAt   string                `json:"updatedAt"`
	Targets     map[string]*MCPTarget `json:"targets"`
	Warnings    []string              `json:"warnings,omitempty"`
}

type MCPTarget struct {
	AgentID     string `json:"agentId"`
	Path        string `json:"path"`
	ConfigKey   string `json:"configKey"`
	Transport   string `json:"transport"`
	ContentHash string `json:"contentHash"`
	RawHash     string `json:"rawHash,omitempty"`
	Enabled     bool   `json:"enabled"`
	UpdatedAt   string `json:"updatedAt"`
}

type MCPTargetView struct {
	AgentID     string `json:"agentId"`
	AgentName   string `json:"agentName"`
	Path        string `json:"path"`
	Managed     bool   `json:"managed"`
	Exists      bool   `json:"exists"`
	Enabled     bool   `json:"enabled"`
	Transport   string `json:"transport,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	Modified    bool   `json:"modified"`
}

type MCPView struct {
	Name        string          `json:"name"`
	Transport   string          `json:"transport"`
	Command     string          `json:"command,omitempty"`
	Args        []string        `json:"args,omitempty"`
	URL         string          `json:"url,omitempty"`
	TimeoutMS   int             `json:"timeoutMs,omitempty"`
	EnvKeys     []string        `json:"envKeys,omitempty"`
	HeaderKeys  []string        `json:"headerKeys,omitempty"`
	Managed     bool            `json:"managed"`
	Status      string          `json:"status"`
	StatusText  string          `json:"statusText"`
	Source      string          `json:"source,omitempty"`
	ContentHash string          `json:"contentHash,omitempty"`
	Targets     []MCPTargetView `json:"targets"`
	UpdatedAt   string          `json:"updatedAt,omitempty"`
	Warnings    []string        `json:"warnings,omitempty"`
}

type MCPDetail struct {
	View   MCPView         `json:"view"`
	Config MCPServerConfig `json:"config"`
	Raw    string          `json:"raw"`
}

type MCPStats struct {
	Managed        int `json:"managed"`
	Synced         int `json:"synced"`
	NeedsAttention int `json:"needsAttention"`
	Untracked      int `json:"untracked"`
	DetectedAgents int `json:"detectedAgents"`
	Installed      int `json:"installed"`
}

type MCPAdapterView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Format   string `json:"format"`
	Detected bool   `json:"detected"`
}

type MCPOverview struct {
	Adapters  []MCPAdapterView `json:"adapters"`
	Servers   []MCPView        `json:"servers"`
	Stats     MCPStats         `json:"stats"`
	Warnings  []string         `json:"warnings,omitempty"`
	ScannedAt string           `json:"scannedAt"`
}

type MCPInput struct {
	OriginalName string            `json:"originalName"`
	Name         string            `json:"name"`
	Transport    string            `json:"transport"`
	Command      string            `json:"command"`
	Args         []string          `json:"args"`
	Env          map[string]string `json:"env"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers"`
	TimeoutMS    int               `json:"timeoutMs"`
	Targets      []string          `json:"targets"`
	Overwrite    bool              `json:"overwrite"`
	Confirmed    bool              `json:"confirmed"`
}

type MCPImportServer struct {
	Name     string          `json:"name"`
	Config   MCPServerConfig `json:"config"`
	Warnings []string        `json:"warnings,omitempty"`
}

type MCPImportResult struct {
	Servers  []MCPImportServer `json:"servers"`
	Warnings []string          `json:"warnings,omitempty"`
}

type mcpEntry struct {
	AgentID     string
	AgentName   string
	Path        string
	Name        string
	Config      MCPServerConfig
	ContentHash string
	RawHash     string
	Disabled    bool
	Invalid     bool
	Warnings    []string
}

func mcpServerConfigCopy(config MCPServerConfig) MCPServerConfig {
	copy := config
	copy.Args = append([]string{}, config.Args...)
	copy.Env = map[string]string{}
	for key, value := range config.Env {
		copy.Env[key] = value
	}
	copy.Headers = map[string]string{}
	for key, value := range config.Headers {
		copy.Headers[key] = value
	}
	return copy
}

func mcpConfigView(config MCPServerConfig) MCPServerConfig {
	copy := mcpServerConfigCopy(config)
	for key, value := range copy.Env {
		if value != "" {
			copy.Env[key] = mcpMaskedSecret
		}
	}
	for key, value := range copy.Headers {
		if value != "" {
			copy.Headers[key] = mcpMaskedSecret
		}
	}
	return copy
}

func mergeMCPSecrets(oldConfig, newConfig MCPServerConfig) MCPServerConfig {
	merged := mcpServerConfigCopy(newConfig)
	merge := func(oldValues, newValues map[string]string) {
		for key, value := range newValues {
			if value == mcpMaskedSecret {
				if previous, ok := oldValues[key]; ok {
					newValues[key] = previous
				}
			}
		}
	}
	if merged.Env == nil {
		merged.Env = map[string]string{}
	}
	if merged.Headers == nil {
		merged.Headers = map[string]string{}
	}
	merge(oldConfig.Env, merged.Env)
	merge(oldConfig.Headers, merged.Headers)
	return merged
}

func mcpViewFromRecord(record *MCPRecord, entries map[string]mcpEntry) MCPView {
	view := MCPView{
		Name: record.Name, Transport: record.Config.Transport, Command: record.Config.Command,
		Args: append([]string{}, record.Config.Args...), URL: record.Config.URL,
		TimeoutMS: record.Config.TimeoutMS, Managed: true, Source: record.Source,
		ContentHash: record.ContentHash, UpdatedAt: record.UpdatedAt,
		Warnings: append([]string{}, record.Warnings...),
	}
	view.EnvKeys = sortedKeys(record.Config.Env)
	view.HeaderKeys = sortedKeys(record.Config.Headers)
	for agentID, target := range record.Targets {
		if target == nil {
			continue
		}
		entry, found := entries[agentID]
		exists := found && entry.Name == record.Name
		modified := false
		if exists {
			if target.RawHash != "" {
				modified = entry.RawHash != target.RawHash
			} else {
				expected := target.ContentHash
				if expected == "" {
					expected = record.ContentHash
				}
				modified = entry.ContentHash != expected
			}
			modified = modified || entry.Invalid
			view.Warnings = append(view.Warnings, entry.Warnings...)
		}
		view.Targets = append(view.Targets, MCPTargetView{
			AgentID: agentID, AgentName: agentID, Path: target.Path,
			Managed: true, Exists: exists, Enabled: exists && !entry.Disabled && !entry.Invalid,
			Transport: entry.Config.Transport, ContentHash: entry.ContentHash, Modified: modified,
		})
	}
	view.Warnings = uniqueStrings(view.Warnings)
	sortMCPTargets(view.Targets)
	view.Status = mcpStatus(record, view.Targets)
	view.StatusText = mcpStatusText(view.Status)
	return view
}

func mcpStatus(record *MCPRecord, targets []MCPTargetView) string {
	if len(targets) == 0 {
		return "hub_only"
	}
	missing := false
	modified := false
	hashes := map[string]bool{}
	for _, target := range targets {
		if !target.Exists {
			missing = true
			continue
		}
		if !target.Enabled {
			missing = true
		}
		if target.ContentHash != "" {
			hashes[target.ContentHash] = true
		}
		if target.Modified {
			modified = true
		}
	}
	if len(hashes) > 1 {
		return "conflict"
	}
	if modified {
		return "modified"
	}
	if missing {
		return "partial"
	}
	return "synced"
}

func mcpStatusText(status string) string {
	switch status {
	case "synced":
		return "已同步"
	case "partial":
		return "部分同步"
	case "modified":
		return "本地已修改"
	case "conflict":
		return "冲突"
	case "untracked":
		return "未纳管"
	case "hub_only":
		return "仅中心库"
	default:
		return "待检查"
	}
}

func sortMCPTargets(targets []MCPTargetView) {
	for left := 0; left < len(targets); left++ {
		for right := left + 1; right < len(targets); right++ {
			if targets[right].AgentID < targets[left].AgentID {
				targets[left], targets[right] = targets[right], targets[left]
			}
		}
	}
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(values []string) {
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if values[right] < values[left] {
				values[left], values[right] = values[right], values[left]
			}
		}
	}
}
