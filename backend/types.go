package main

import "time"

const (
	appID            = "io.github.song782360037.agenthub"
	legacyAppID      = "io.github.song782360037.skillsmanager"
	appVersion       = "0.1.0"
	markerName       = ".agenthub.json"
	legacyMarkerName = ".dbx-skills.json"
	stateFile        = "state.json"
)

type AgentSpec struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	DefaultPath string `json:"defaultPath"`
	Detected    bool   `json:"detected"`
	Builtin     bool   `json:"builtin"`
	Custom      bool   `json:"custom"`
	Overridden  bool   `json:"overridden"`
	Note        string `json:"note,omitempty"`
}

type CustomAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type Settings struct {
	Proxy         string            `json:"proxy,omitempty"`
	GitHubToken   string            `json:"githubToken,omitempty"`
	DefaultMode   string            `json:"defaultMode"`
	ScanLevel     string            `json:"scanLevel"`
	MaxDownloadMB int               `json:"maxDownloadMB"`
	AgentPaths    map[string]string `json:"agentPaths,omitempty"`
	CustomAgents  []CustomAgent     `json:"customAgents,omitempty"`
}

func defaultSettings() Settings {
	return Settings{
		DefaultMode:   "auto",
		ScanLevel:     "basic",
		MaxDownloadMB: 50,
		AgentPaths:    map[string]string{},
	}
}

type SettingsView struct {
	Proxy         string            `json:"proxy"`
	HasToken      bool              `json:"hasToken"`
	DefaultMode   string            `json:"defaultMode"`
	ScanLevel     string            `json:"scanLevel"`
	MaxDownloadMB int               `json:"maxDownloadMB"`
	AgentPaths    map[string]string `json:"agentPaths"`
	CustomAgents  []CustomAgent     `json:"customAgents"`
}

type Source struct {
	Type          string `json:"type"`
	Repo          string `json:"repo,omitempty"`
	Ref           string `json:"ref,omitempty"`
	ResolvedRef   string `json:"resolvedRef,omitempty"`
	Path          string `json:"path,omitempty"`
	URL           string `json:"url,omitempty"`
	LocalPath     string `json:"localPath,omitempty"`
	ArchiveName   string `json:"archiveName,omitempty"`
	Commit        string `json:"commit,omitempty"`
	ArchiveSHA256 string `json:"archiveSha256,omitempty"`
	Label         string `json:"label,omitempty"`
}

type PluginInfo struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Root    string `json:"root,omitempty"`
}

type Finding struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
}

type FileEntry struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256,omitempty"`
	Executable bool   `json:"executable,omitempty"`
	Binary     bool   `json:"binary,omitempty"`
}

type Candidate struct {
	Name           string      `json:"name"`
	Description    string      `json:"description"`
	RelPath        string      `json:"relPath"`
	Files          int         `json:"files"`
	Size           int64       `json:"size"`
	ContentHash    string      `json:"contentHash"`
	Risk           []Finding   `json:"risk,omitempty"`
	Warnings       []string    `json:"warnings,omitempty"`
	Plugin         *PluginInfo `json:"plugin,omitempty"`
	ContentPreview string      `json:"contentPreview,omitempty"`
	RequiredTools  []string    `json:"requiredTools,omitempty"`
}

type Preview struct {
	ID         string        `json:"id"`
	Source     Source        `json:"source"`
	Root       string        `json:"-"`
	TempDir    string        `json:"-"`
	Candidates []Candidate   `json:"candidates"`
	Warnings   []string      `json:"warnings,omitempty"`
	CreatedAt  time.Time     `json:"createdAt"`
	Update     *updateReview `json:"-"`
}

type TargetRecord struct {
	AgentID     string `json:"agentId"`
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	ContentHash string `json:"contentHash"`
	Enabled     bool   `json:"enabled"`
	Scope       string `json:"scope"`
	UpdatedAt   string `json:"updatedAt"`
}

type Record struct {
	Name          string                   `json:"name"`
	Description   string                   `json:"description"`
	Source        Source                   `json:"source"`
	ContentHash   string                   `json:"contentHash"`
	Files         []FileEntry              `json:"files,omitempty"`
	Size          int64                    `json:"size"`
	InstalledAt   string                   `json:"installedAt"`
	UpdatedAt     string                   `json:"updatedAt"`
	Targets       map[string]*TargetRecord `json:"targets"`
	LastCheckedAt string                   `json:"lastCheckedAt,omitempty"`
	Available     *AvailableUpdate         `json:"available,omitempty"`
	Risk          []Finding                `json:"risk,omitempty"`
	Warnings      []string                 `json:"warnings,omitempty"`
}

type AvailableUpdate struct {
	Commit    string `json:"commit,omitempty"`
	Version   string `json:"version,omitempty"`
	CheckedAt string `json:"checkedAt"`
	Error     string `json:"error,omitempty"`
}

type State struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Skills        map[string]*Record    `json:"skills"`
	MCPServers    map[string]*MCPRecord `json:"mcpServers"`
	Settings      Settings              `json:"settings"`
}

type Marker struct {
	ManagedBy   string `json:"managedBy"`
	Skill       string `json:"skill"`
	HubPath     string `json:"hubPath"`
	Source      Source `json:"source"`
	ContentHash string `json:"contentHash"`
	InstalledAt string `json:"installedAt"`
}

type TargetView struct {
	AgentID     string `json:"agentId"`
	AgentName   string `json:"agentName"`
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	Managed     bool   `json:"managed"`
	Tracked     bool   `json:"tracked"`
	Exists      bool   `json:"exists"`
	Enabled     bool   `json:"enabled"`
	ContentHash string `json:"contentHash,omitempty"`
	Modified    bool   `json:"modified"`
	LinkTarget  string `json:"linkTarget,omitempty"`
}

type SkillView struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Managed     bool             `json:"managed"`
	Status      string           `json:"status"`
	StatusText  string           `json:"statusText"`
	Source      *Source          `json:"source,omitempty"`
	ContentHash string           `json:"contentHash,omitempty"`
	Targets     []TargetView     `json:"targets"`
	Files       int              `json:"files"`
	Size        int64            `json:"size"`
	UpdatedAt   string           `json:"updatedAt,omitempty"`
	Available   *AvailableUpdate `json:"available,omitempty"`
	Risk        []Finding        `json:"risk,omitempty"`
	Warnings    []string         `json:"warnings,omitempty"`
	HubPath     string           `json:"hubPath,omitempty"`
}

type Stats struct {
	Managed          int `json:"managed"`
	Synced           int `json:"synced"`
	NeedsAttention   int `json:"needsAttention"`
	Untracked        int `json:"untracked"`
	DetectedAgents   int `json:"detectedAgents"`
	InstalledTargets int `json:"installedTargets"`
}

type Overview struct {
	Agents    []AgentSpec  `json:"agents"`
	Skills    []SkillView  `json:"skills"`
	Stats     Stats        `json:"stats"`
	Settings  SettingsView `json:"settings"`
	HubPath   string       `json:"hubPath"`
	Warnings  []string     `json:"warnings,omitempty"`
	ScannedAt string       `json:"scannedAt"`
}

type ScanEntry struct {
	AgentID     string
	AgentName   string
	Path        string
	DirName     string
	Name        string
	Description string
	ContentHash string
	Files       []FileEntry
	Size        int64
	Marker      *Marker
	ForeignMark bool
	IsLink      bool
	LinkTarget  string
	Warnings    []string
}

type SourceInput struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Ref       string `json:"ref"`
	Path      string `json:"path"`
	ZipBase64 string `json:"zipBase64"`
	FileName  string `json:"fileName"`
}

type InstallInput struct {
	PreviewID       string   `json:"previewId"`
	Names           []string `json:"names"`
	Targets         []string `json:"targets"`
	Mode            string   `json:"mode"`
	Overwrite       bool     `json:"overwrite"`
	AcknowledgeRisk bool     `json:"acknowledgeRisk"`
}

type InstallResult struct {
	Installed []string `json:"installed"`
	Targets   int      `json:"targets"`
	Warnings  []string `json:"warnings,omitempty"`
	Message   string   `json:"message"`
}

type SettingsInput struct {
	Proxy         *string           `json:"proxy"`
	GitHubToken   *string           `json:"githubToken"`
	ClearToken    bool              `json:"clearToken"`
	DefaultMode   *string           `json:"defaultMode"`
	ScanLevel     *string           `json:"scanLevel"`
	MaxDownloadMB *int              `json:"maxDownloadMB"`
	AgentPaths    map[string]string `json:"agentPaths"`
	CustomAgents  []CustomAgent     `json:"customAgents"`
}
