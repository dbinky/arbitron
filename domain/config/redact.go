package config

// RedactedConfig is the safe-to-echo projection of Config shared by every read
// surface (HTTP GET /system/config and the MCP system_config tool).
//
// SECURITY: it is a positive allow-list. Only the fields named here are ever
// serialized, and sensitive values (LLM API key, Teams webhook URL) are masked.
// A newly added Config field therefore defaults to NOT exposed — the previous
// MCP path marshaled the raw *config.Config and deny-list-redacted a single
// field, which failed open whenever a new secret landed.
type RedactedConfig struct {
	Server    RedactedServer    `json:"server"`
	Database  RedactedDatabase  `json:"database"`
	Workspace RedactedWorkspace `json:"workspace"`
	LLM       RedactedLLM       `json:"llm"`
	Logging   RedactedLogging   `json:"logging"`
	Tracing   RedactedTracing   `json:"tracing"`
	Queue     RedactedQueue     `json:"queue"`
	Teams     RedactedTeams     `json:"teams"`
}

type RedactedServer struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	PublicBaseURL string `json:"publicBaseUrl"`
}

type RedactedDatabase struct {
	Configured bool `json:"configured"`
}

type RedactedWorkspace struct {
	Path string `json:"path"`
}

type RedactedLLM struct {
	Provider string `json:"provider"`
	APIKey   string `json:"apiKey"` // always "**REDACTED**"; never the raw key
}

type RedactedLogging struct {
	Level string `json:"level"`
	File  string `json:"file"`
}

type RedactedTracing struct {
	Enabled   bool   `json:"enabled"`
	ExportDir string `json:"exportDir"`
}

// RedactedQueue carries the configured caps plus the live queue counts. The
// counts are not part of Config; callers fill them in after projecting.
type RedactedQueue struct {
	GlobalCap    int `json:"globalCap"`
	PerUserCap   int `json:"perUserCap"`
	RunningCount int `json:"runningCount"`
	QueuedCount  int `json:"queuedCount"`
	WaitingCount int `json:"waitingCount"`
}

type RedactedTeams struct {
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhookUrl"` // redacted: "**REDACTED**" or ""
}

// Redact projects cfg into the safe-to-echo RedactedConfig. The Teams settings
// are passed separately so callers source them from the synchronized
// SettingsStore rather than reading the shared Config struct unlocked. Live
// queue counts default to zero; callers set them after projecting.
func Redact(cfg *Config, teams TeamsConfig) RedactedConfig {
	return RedactedConfig{
		Server: RedactedServer{
			Host:          cfg.Server.Host,
			Port:          cfg.Server.Port,
			PublicBaseURL: cfg.Server.PublicBaseURL,
		},
		Database:  RedactedDatabase{Configured: cfg.Database.Path != ""},
		Workspace: RedactedWorkspace{Path: cfg.Workspace.Path},
		LLM: RedactedLLM{
			Provider: cfg.LLM.Provider,
			APIKey:   RedactedSecret,
		},
		Logging: RedactedLogging{Level: cfg.Logging.Level, File: cfg.Logging.File},
		Tracing: RedactedTracing{Enabled: cfg.Tracing.Enabled, ExportDir: cfg.Tracing.ExportDir},
		Queue:   RedactedQueue{GlobalCap: cfg.Queue.GlobalCap, PerUserCap: cfg.Queue.PerUserCap},
		Teams: RedactedTeams{
			Enabled:    teams.Enabled,
			WebhookURL: RedactWebhookURL(teams.WebhookURL),
		},
	}
}
