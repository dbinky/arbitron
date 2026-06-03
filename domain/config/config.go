// Package config defines the domain configuration types for Arbitron.
package config

// Config is the top-level configuration structure for the Arbitron system.
type Config struct {
	Server        ServerConfig
	Database      DatabaseConfig
	Workspace     WorkspaceConfig
	LLM           LLMConfig
	Logging       LoggingConfig
	Tracing       TracingConfig
	Queue         QueueConfig
	Script        ScriptConfig
	Notifications NotificationsConfig
	Profiles      []ProfileConfig
}

// ProfileConfig is the config-file shape of one profile-registry entry. It
// mirrors domain/provider.Profile but stays in the config package to keep
// config free of a provider-package import; adapter/config maps it across.
type ProfileConfig struct {
	Alias         string
	Kind          string // "chat" | "agent"
	ConnectionRef string // -> Connection.Name; empty = claude-cli no-key (subscription)
	Model         string
}

// ScriptConfig holds settings for script/tool_call process nodes.
type ScriptConfig struct {
	// EnvPassthrough lists engine-env variable NAMES (not values) that script
	// and tool_call nodes may inherit, in addition to the base allowlist.
	// SECURITY: secrets stay in the deploy environment; only names live here.
	// Without this, a process node sees only the base allowlist.
	EnvPassthrough []string
}

// QueueConfig holds queue management settings.
type QueueConfig struct {
	GlobalCap  int // 0 = unlimited
	PerUserCap int // 0 = unlimited
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host string
	Port int
	// PublicBaseURL is the externally reachable base URL of the dashboard
	// (e.g. "http://localhost:8080"), used to build deep links in outbound
	// notifications. Static: set via config file / env only, never UI-editable.
	// Any trailing "/" is trimmed by the loader so callers can concatenate
	// "/instances/<id>" deterministically.
	PublicBaseURL string
}

// TeamsConfig holds the single global Microsoft Teams webhook settings.
// Stored in memory only (admin-editable at runtime, not persisted), mirroring
// the QueueConfig precedent.
type TeamsConfig struct {
	// WebhookURL is the Teams Workflows webhook URL. Treated as sensitive:
	// never echoed back on any read surface.
	WebhookURL string
	// Enabled gates delivery. When false, Teams forwarding is a clean no-op.
	Enabled bool
}

// NotificationsConfig groups external notification-channel settings. Today it
// holds only Teams; future channels join here.
type NotificationsConfig struct {
	Teams TeamsConfig
}

// DatabaseConfig holds database connection settings.
type DatabaseConfig struct {
	Path string
}

// WorkspaceConfig holds workspace directory settings.
type WorkspaceConfig struct {
	Path string
}

// LLMConfig holds LLM provider settings.
//
// TODO(claude-cli sub-config): If provider-specific policy fields accumulate
// (e.g., MaxTurns, Verbose, additional claude knobs), refactor into a typed
// `ClaudeCLI` sub-config on this struct. Today's flat shape is intentional —
// see the architect/10x debate on PR #8 (threads 3291921376 / 3291934464).
type LLMConfig struct {
	Provider string
	APIKey   string
	// BinaryPath is the path to the `claude` binary (claude-cli provider).
	// Empty means auto-resolve from PATH.
	BinaryPath string
	// Model is the default model passed to the provider when a node does not
	// specify one.
	Model string
	// TimeoutSeconds bounds a single provider call.
	// SECURITY: a non-positive value is a trivial queue-pinning / hang-forever
	// DoS — a stuck or hostile subprocess or HTTP endpoint runs forever and
	// never frees its queue slot, and it is the only deadline the shared
	// http.Client relies on. The config loader therefore FLOORS a non-positive
	// value up to the sane default (1800s / 30 minutes); 0 can no longer
	// silently disable every deadline.
	TimeoutSeconds int
	// SkipPermissions, when true, emits `--dangerously-skip-permissions` to the
	// claude CLI, removing every tool-permission prompt. The agent will run
	// Bash/Write/Edit/etc. with no human in the loop.
	//
	// SECURITY: this is RCE-equivalent whenever any input that reaches an LLM
	// prompt can be influenced by an untrusted caller (workflow inputs,
	// upstream node outputs, file contents, tool responses). The default is
	// false. Only flip in deployments where every prompt input is trusted, or
	// pair with a strict AllowedTools allowlist.
	SkipPermissions bool
	// AllowedTools is forwarded to claude as `--allowedTools` (comma-joined).
	// Required for permissionful runs (SkipPermissions=false) because
	// headless `--print` mode cannot show interactive approval prompts.
	AllowedTools []string
	// MaxResponseBytes caps the per-request total bytes the runner will
	// accept from claude's stdout. Default 256 MB. 0 means no cap (test
	// convenience only).
	//
	// SECURITY: bounds the DoS surface where a hostile prompt instructs the
	// agent to emit unbounded tool output (e.g. `dd if=/dev/urandom`).
	// Without this, an unbounded ReadBytes('\n') would OOM the engine.
	MaxResponseBytes int64
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level string
	File  string
}

// TracingConfig holds tracing/observability settings.
type TracingConfig struct {
	Enabled   bool
	ExportDir string
}
