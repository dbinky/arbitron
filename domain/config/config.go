// Package config defines the domain configuration types for Arbitron.
package config

// Config is the top-level configuration structure for the Arbitron system.
type Config struct {
	Server    ServerConfig
	Database  DatabaseConfig
	Workspace WorkspaceConfig
	LLM       LLMConfig
	Logging   LoggingConfig
	Tracing   TracingConfig
	Queue     QueueConfig
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
	// TimeoutSeconds bounds a single provider call. 0 means no timeout.
	// SECURITY: 0 is a trivial queue-pinning DoS — a stuck or hostile
	// subprocess runs forever and never frees its queue slot. The configured
	// default is 1800 (30 minutes); explicitly setting 0 is supported but
	// discouraged.
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
