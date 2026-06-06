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
	Auth          AuthConfig
}

// AuthConfig selects the operating mode and holds the org-mode auth settings.
// Local mode (the default) requires none of the sub-configs.
type AuthConfig struct {
	// Mode is "local" (default) or "org". It drives every downstream auth branch:
	// local mode injects a fixed admin identity; org mode validates a session.
	Mode string
	// BootstrapAdmins lists identity subjects/emails provisioned as admins on
	// first login. Declared here so the config shape is complete; CONSUMED in
	// Phase 2 (JIT provisioning). Phase 1 reads but does not enforce it.
	BootstrapAdmins []string
	// RBAC holds the role-based-access-control settings (auth mode only). With
	// an empty GroupClaim and no Leads, every authenticated user is a member of
	// the single DefaultGroup — the deployment behaves as one shared workspace
	// with administrators. CONSUMED by the group-resolution helper and the
	// provisioner/middleware (RBAC Phase 1).
	RBAC    RBACConfig
	OIDC    OIDCConfig
	Session SessionConfig
}

// RBACConfig configures department-based RBAC (auth mode only). It is the
// load-time shape; adapter/config maps it into the resolution-time
// identity.GroupConfig (kept separate so domain/config imports nothing from
// domain/identity).
type RBACConfig struct {
	// GroupClaim is the token claim name carrying a user's group membership.
	// Empty ("") means no claim is read and every user falls back to the default
	// group only.
	GroupClaim string
	// DefaultGroup is the sentinel department for claimless users. Defaults to
	// "default".
	DefaultGroup string
	// Leads maps a department name to the identifiers (subjects and/or emails) of
	// its lead users. Department keys and identifiers are normalized to lower-case
	// at load. A lead of a department always sees and can publish-to-public that
	// department even if their token omits it.
	Leads map[string][]string
}

// OIDCConfig configures the self-hosted OIDC authorization-code login flow.
// All fields are required when Mode == "org".
type OIDCConfig struct {
	IssuerURL    string   // e.g. "https://login.example.com" (OIDC discovery root)
	ClientID     string   //
	ClientSecret string   // SECURITY: sensitive; never echoed on a read surface
	RedirectURL  string   // must equal PublicBaseURL + "/auth/callback"
	Scopes       []string // default openid,profile,email
}

// SessionConfig configures server-side cookie sessions (the session is the
// Arbitron credential; OIDC is only the login mechanism).
type SessionConfig struct {
	TTLSeconds   int    // session lifetime; default 43200 (12h)
	CookieName   string // default "arbitron_session"
	CookieSecure bool   // default true; Secure attribute on the session cookie
}

// Mode constants kept as strings here to avoid a domain/identity import cycle
// (domain/identity defines the typed Mode and converts from this string).
const (
	AuthModeLocal = "local"
	AuthModeOrg   = "org"
)

// ProfileConfig is the config-file shape of one profile-registry entry. It
// mirrors domain/provider.Profile but stays in the config package to keep
// config free of a provider-package import; adapter/config maps it across.
type ProfileConfig struct {
	Alias         string
	Kind          string // "chat" | "agent"
	ConnectionRef string // -> Connection.Name; empty = claude-cli no-key (subscription)
	Model         string
	// AllowedTools is the per-profile tool allowlist for agent-kind profiles
	// (PR #18 item 1b). It is forwarded as the per-request tool scope to the
	// agent adapter and takes precedence over the adapter's global
	// cfg.LLM.AllowedTools, so each agent profile (reviewer/coder/...) runs
	// against an explicit minimal allowlist rather than the YOLO all-tools
	// default. Empty/omitted means "use the adapter's global scope" (which,
	// under the fail-closed buildArgs default, is the empty set when unset).
	// Ignored for chat-kind profiles, which are tool-suppressed by construction.
	AllowedTools []string
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
	// TLS configures direct HTTPS serving. When Enabled, exactly one of
	// {CertFile+KeyFile} or {AutocertDomain} must be set. Disabled (default) =
	// plain HTTP, intended for deployments behind a TLS-terminating tunnel/proxy.
	TLS TLSConfig
}

// TLSConfig configures direct HTTPS serving by the arbitron HTTP server.
type TLSConfig struct {
	// Enabled turns on HTTPS. When false, the server listens on plain HTTP.
	Enabled bool
	// CertFile / KeyFile are paths to a PEM cert/key pair (BYO or internal CA).
	CertFile string
	KeyFile  string
	// AutocertDomain enables automatic Let's Encrypt for this hostname; mutually
	// exclusive with CertFile/KeyFile. Requires AutocertCacheDir for cert storage.
	AutocertDomain   string
	AutocertCacheDir string
	// AllowUnauthenticated is the explicit escape hatch for serving direct TLS in
	// local mode (which has NO authentication — a fixed auto-admin) on a
	// non-loopback host. That combination is rejected at load by default because
	// a real cert and a green padlock would otherwise front a fully
	// unauthenticated admin API reachable by an unauthenticated GET. Set true
	// only for a genuinely trusted internal network where the operator
	// understands the API is unauthenticated.
	AllowUnauthenticated bool
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
	// AgentAllowAllTools is the operator knob (a) for the opencode agent path's
	// permission posture. SECURITY: it defaults to FALSE (CLOSED). Setting it
	// true grants every non-claude agent provider driven through opencode run a
	// permission-free, allow-all tool posture; on a credential-bearing subprocess
	// fed model-influenced prompts that is RCE-equivalent (the lethal trifecta),
	// so allow-all is an explicit operator opt-in rather than the shipped default.
	// Per-profile ResolvedBinding.AllowedTools (knob (b)) narrows independently
	// of this switch.
	AgentAllowAllTools bool
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
