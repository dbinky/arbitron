// Package config implements the koanf-based configuration loader for Arbitron.
// It loads configuration from defaults, an optional YAML file, and environment
// variables (with env vars taking highest precedence).
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	koanffile "github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	domainconfig "github.com/arbitron/arbitron/domain/config"
)

// defaults returns the default configuration values as a flat map keyed for koanf.
func defaults() map[string]interface{} {
	return map[string]interface{}{
		"server.host":    "127.0.0.1",
		"server.port":    8080,
		"database.path":  "./arbitron.db",
		"workspace.path": "./workspaces",
		// SECURITY: bounds one parallel_group launch's eager working-area
		// copies (size x (tasks+1)); see PR #76 security thread 4309095005.
		"workspace.grouplaunchmaxbytes": domainconfig.DefaultGroupLaunchMaxBytes,
		// Optional capability-ruleset override (profile-editing Phase 2). Empty =>
		// the composition root defaults it to ~/.arbitron/capabilities.yaml; a
		// missing file falls back to the embedded default ruleset.
		"capabilitiespath":  "",
		"samples.seed":      true,
		"samples.sourcedir": "",
		// Total arrange/review attempts and browser settings for saved revisions.
		"layout.maxattempts":      DefaultLayoutMaxAttempts,
		"layout.browserpath":      "",
		"layout.artifactdir":      "",
		"layout.browsernosandbox": false,
		"llm.provider":            "anthropic",
		"llm.apikey":              "",
		"llm.binarypath":          "",
		"llm.model":               "",
		// SECURITY: 30 minutes upper bound on a single provider call.
		// Default 0 (unbounded) is a queue-pinning DoS — see PR #8 security
		// thread 3291944120.
		"llm.timeoutseconds": 1800,
		// SECURITY: default false. Flipping to true grants the agent
		// permission-free tool execution (RCE-equivalent under hostile
		// prompts). See PR #8 security thread 3291940088.
		"llm.skippermissions": false,
		"llm.allowedtools":    []string{},
		// SECURITY: 256 MB per-request stdout cap; bounds the OOM DoS from
		// hostile prompts that flood tool output. See PR #8 security thread
		// 3291941918.
		"llm.maxresponsebytes": int64(256 * 1024 * 1024),
		// SECURITY: default CLOSED (false). agentallowalltools grants the opencode
		// agent subprocess permission-free tool execution (allow-all). On a
		// credential-bearing, tool-using subprocess fed model-influenced prompts
		// that is a lethal-trifecta / RCE-equivalent posture, so allow-all is an
		// explicit operator opt-in, never the shipped default. Per-profile
		// allowedtools (knob (b)) narrows independently. See PR #23 security HIGH.
		"llm.agentallowalltools": false,
		// Org-readiness Phase 2: deployment-wide default agent backend. The org
		// default is claude-code → LiteLLM. An empty value is floored back to
		// claude-code in normalize so the policy lives in exactly one place.
		"llm.defaultagentbackend":     "claude-code",
		"logging.level":               "info",
		"logging.file":                "arbitron.log",
		"tracing.enabled":             false,
		"tracing.exportdir":           "./traces",
		"queue.globalcap":             20,
		"queue.perusercap":            5,
		"script.envpassthrough":       []string{},
		"server.tls.enabled":          false,
		"server.tls.certfile":         "",
		"server.tls.keyfile":          "",
		"server.tls.autocertdomain":   "",
		"server.tls.autocertcachedir": "",
		// Auth (Phase 6): local mode by default (single auto-admin, no login).
		// Org-mode OIDC fields are validated fail-fast in validateAuth.
		"auth.mode":              "local",
		"auth.bootstrapadmins":   []string{},
		"auth.oidc.issuerurl":    "",
		"auth.oidc.clientid":     "",
		"auth.oidc.clientsecret": "",
		"auth.oidc.redirecturl":  "",
		"auth.oidc.scopes":       []string{"openid", "profile", "email"},
		// Edge-trusted SSO (auth mode "edge" only). No secret here: only the
		// public team domain + AUD tag(s). Validated fail-fast in validateAuth.
		"auth.edge.teamdomain":      "",
		"auth.edge.audiences":       []string{},
		"auth.session.ttlseconds":   43200, // 12h
		"auth.session.cookiename":   "arbitron_session",
		"auth.session.cookiesecure": true,
		// RBAC (auth mode only). With an empty groupclaim and no leads, every
		// authenticated user is a member of the single default group.
		"auth.rbac.groupclaim":   "",
		"auth.rbac.defaultgroup": "default",
		// auth.rbac.leads is a map[string][]string; it has no flat default (an
		// absent key unmarshals to a nil map). Configured via YAML or the
		// ARBITRON_AUTH_RBAC_LEADS_<DEPT> env overlay below.
		// auth.rbac.mapping is a map[string]string (raw-claim -> friendly dept); like
		// leads it has no flat default and is supplied via YAML or the
		// ARBITRON_AUTH_RBAC_MAPPING_<RAWVALUE> env overlay below.
		// Profile registry: alias -> capability kind + model-id. A fresh
		// install ships a chat/agent pair so a basic workflow validates. Every
		// shipped default FOLLOWS an Anthropic tier, so none goes stale: a
		// pinned profile must name a fixed model version, and an empty model or
		// a mutable alias ("sonnet") is refused.
		//
		// The reviewer/coder/planner/interviewer/assessor aliases are the
		// in-repo default registry for the workflows shipped in this repo
		// (samples/pr-review). samples/legacy-feature-pipeline instead needs the
		// DB-only admin profiles in its PROFILES.md. Shipping them as
		// defaults (overridable by the deployment config.yaml) lets a fresh
		// clone validate those workflows honestly instead of passing
		// workflow_validate and then dying mid-run with "unknown profile alias"
		// (PR #18 Must Fix item 4, comment 4614860006).
		// DEFENSE-IN-DEPTH (PR #18 item 1b): each AGENT profile carries an
		// explicit minimal AllowedTools allowlist documenting what it is meant to
		// touch (reviewers read + scoped gh/git, NO Write/Edit/bare Bash since
		// they process attacker-authored PR content; coder gets full dev tools;
		// interviewer/planner get write scopes for artifact authoring).
		// IMPORTANT (VERIFIED, PR #18): this allowlist is NOT a security boundary
		// for these profiles — agents run with --dangerously-skip-permissions,
		// which the claude CLI confirmed BYPASSES --allowedTools entirely. The
		// scopes only take effect if skip-permissions is disabled, and document
		// intent. The real isolation boundary is the OS/container the agent runs
		// in (no host secrets, controlled egress). Deployment config.yaml SHOULD
		// mirror these scopes when overriding.
		"profiles": []map[string]interface{}{
			{"alias": "default-chat", "kind": "chat", "selection_mode": "following", "follow_target": map[string]interface{}{"provider": "anthropic", "tier": "sonnet"}},
			{"alias": "default-agent", "kind": "agent", "selection_mode": "following", "follow_target": map[string]interface{}{"provider": "anthropic", "tier": "sonnet"}},
			{"alias": "assessor", "kind": "chat", "selection_mode": "following", "follow_target": map[string]interface{}{"provider": "anthropic", "tier": "sonnet"}},
			{"alias": "interviewer", "kind": "agent", "selection_mode": "following", "follow_target": map[string]interface{}{"provider": "anthropic", "tier": "sonnet"}, "allowedtools": []string{"Read", "Write", "Grep", "Glob", "Bash"}},
			{"alias": "reviewer", "kind": "agent", "selection_mode": "following", "follow_target": map[string]interface{}{"provider": "anthropic", "tier": "sonnet"}, "allowedtools": []string{"Read", "Grep", "Glob", "Bash(gh:*)", "Bash(git:*)"}},
			{"alias": "coder", "kind": "agent", "backend": "claude-code", "selection_mode": "following", "follow_target": map[string]interface{}{"provider": "anthropic", "tier": "opus"}, "allowedtools": []string{"Read", "Write", "Edit", "Grep", "Glob", "Bash"}},
			{"alias": "planner", "kind": "agent", "backend": "claude-code", "selection_mode": "following", "follow_target": map[string]interface{}{"provider": "anthropic", "tier": "opus"}, "allowedtools": []string{"Read", "Write", "Grep", "Glob", "Bash"}},
		},
	}
}

// defaultYAMLContent returns a YAML string representing the default config file
// that is written when no config file exists.
func defaultYAMLContent() string {
	return `server:
  host: "127.0.0.1"
  port: 8080
  # publicbaseurl: ""
  #   Externally reachable base URL of the dashboard (e.g.
  #   "http://localhost:8080"). Used to build deep links in outbound
  #   notifications (Teams). Trailing slashes are trimmed automatically.
  #   Override via ARBITRON_SERVER_PUBLICBASEURL.
  # tls:
  #   enabled: false
  #     Serve HTTPS directly. Leave false when behind a TLS-terminating
  #     tunnel/proxy (e.g. cloudflared); set true to serve HTTPS from arbitron.
  #   certfile: ""   # PEM cert path (BYO / internal CA). Requires keyfile.
  #   keyfile:  ""   # PEM key path.
  #   autocertdomain: ""    # automatic Let's Encrypt for this host (mutually
  #                         # exclusive with certfile/keyfile). Needs public DNS
  #                         # and reachable :80/:443.
  #   autocertcachedir: ""  # directory to cache autocert certificates.
database:
  path: "./arbitron.db"
workspace:
  path: "./workspaces"
  # grouplaunchmaxbytes: 17179869184
  #   Budget (bytes) for one parallel_group launch's eager copies: the parent
  #   working area's size x (tasks + 1) must not exceed it. Default 16 GiB;
  #   0 or negative uses the default (the budget cannot be disabled).
llm:
  provider: "anthropic"
  apikey: ""
  # claude-cli policy knobs (used when provider == "claude-cli"):
  #
  # timeoutseconds: 1800
  #   Per-call subprocess timeout (seconds). 0 = unbounded (DISCOURAGED — a
  #   single stuck call pins one queue slot forever).
  #
  # skippermissions: false
  #   SECURITY: setting true emits --dangerously-skip-permissions, letting the
  #   agent run any tool (Bash, Write, Edit, ...) with no human approval. This
  #   is RCE-equivalent whenever any prompt input can be influenced by an
  #   untrusted caller. Leave false unless every prompt input is fully trusted.
  #
  # allowedtools: []
  #   Forwarded to claude as --allowedTools (comma-joined). Required when
  #   skippermissions: false, because --print mode cannot prompt for approval.
  #
  # maxresponsebytes: 268435456   # 256 MiB
  #   Per-request stdout cap. Bounds OOM DoS from hostile prompts that flood
  #   tool output. 0 = no cap (test convenience only).
  #
  # agentallowalltools: false
  #   SECURITY (knob (a)): default false (CLOSED). Setting true grants every
  #   opencode-driven (non-claude) agent provider permission-free, allow-all
  #   tool execution. On a credential-bearing subprocess fed model-influenced
  #   prompts this is RCE-equivalent (the lethal trifecta), so leave it false
  #   unless every prompt input is fully trusted; enabling it is an explicit
  #   operator opt-in. Per-profile allowedtools (knob (b)) narrows independently.
  #   NOTE: the opencode agent path requires a modern opencode binary (>= the
  #   adapter's minimum version, enforced at the first agent call).
script:
  # envpassthrough: []
  #   SECURITY: names (not values) of engine environment variables that script
  #   and tool_call nodes may inherit, in addition to the base allowlist
  #   (PATH, HOME, SHELL, LANG, LC_*, ...). Use this to expose e.g. an LLM API
  #   key or router URL to a script WITHOUT leaking every engine secret. The
  #   secret VALUES live in the deploy environment; only names go here.
  #   Override via ARBITRON_SCRIPT_ENVPASSTHROUGH=NAME1,NAME2.
profiles:
  # The profile registry maps a node's model_profile alias to a capability
  # kind (chat | agent) and a model. Nodes reference a backend by alias, so a
  # config swap repoints aliases without touching workflow.json. Chat selects
  # the no-tool ChatAdapter, agent selects the tool-using AgentAdapter.
  #
  # A profile is pinned (the default) or following. A pinned profile names one
  # fixed model version (e.g. "claude-sonnet-5-5"); runs pin it at trigger
  # time, so an empty model or a mutable alias such as "sonnet" refuses
  # triggers. A following profile tracks the newest eligible model for a
  # provider and tier; with no model it starts from the newest candidate, and a
  # starting model it names must be confirmed usable on its own route. It needs
  # catalog evidence, so while the catalog is unreachable it stays pending and
  # is seeded by a later refresh. Completed entries are managed in the DB, not
  # by later config edits. A pinned example:
  #   - alias: "fast"
  #     kind: "chat"
  #     model: "claude-haiku-4-5"
  #
  # Every shipped default FOLLOWS an Anthropic tier on the Claude Code
  # subscription, so a fresh install runs the bundled samples out of the box
  # and moves to newer models with no config edits.
  - alias: "default-chat"
    kind: "chat"
    selection_mode: "following"
    follow_target: { provider: "anthropic", tier: "sonnet" }
  - alias: "default-agent"
    kind: "agent"
    selection_mode: "following"
    follow_target: { provider: "anthropic", tier: "sonnet" }
  # In-repo default registry for the workflows shipped in this repo
  # (samples/pr-review, samples/legacy-feature-pipeline). Repoint a persona at another
  # tier or backend by editing its entry; remove an alias only if you are not
  # running its workflow.
  #
  # SECURITY (PR #18 item 1b): each AGENT profile sets an explicit minimal
  # allowedtools allowlist so a tool-enabled agent runs bounded instead of
  # against the YOLO all-tools default. Reviewers are read + comment only
  # (no Write/Edit, no bare Bash) because they read attacker-authored PR
  # content; the coder gets full dev tools; interviewer/planner write artifacts.
  # Mirror these scopes if you override this block.
  - alias: "assessor"
    kind: "chat"
    selection_mode: "following"
    follow_target: { provider: "anthropic", tier: "sonnet" }
  - alias: "interviewer"
    kind: "agent"
    selection_mode: "following"
    follow_target: { provider: "anthropic", tier: "sonnet" }
    allowedtools: ["Read", "Write", "Grep", "Glob", "Bash"]
  #
  # MIXED-MODEL EXAMPLE (commented — NOT the default): to run the interviewer on
  # the codex backend (ChatGPT subscription, codex exec) instead of claude-code,
  # replace the interviewer block above with the one below. The backend axis is
  # INDEPENDENT of the provider/connection; repoint a persona by editing its
  # backend/tier without touching workflow.json. NOTE: codex requires the codex
  # CLI installed AND logged in, and the chosen model must be available on your
  # ChatGPT plan (access is account-dependent) — see
  # docs/agent-security-model.md for the bypass-approvals-and-sandbox posture and
  # docs/install/README.md for the codex prerequisites. It is shipped commented so
  # the bundled default always runs out of the box.
  #
  #   - alias: "interviewer"
  #     kind: "agent"
  #     backend: "codex"
  #     selection_mode: "following"
  #     follow_target: { provider: "openai", tier: "sol" }
  #     allowedtools: ["Read", "Write", "Grep", "Glob", "Bash"]
  #
  - alias: "reviewer"
    kind: "agent"
    selection_mode: "following"
    follow_target: { provider: "anthropic", tier: "sonnet" }
    allowedtools: ["Read", "Grep", "Glob", "Bash(gh:*)", "Bash(git:*)"]
  - alias: "coder"
    kind: "agent"
    backend: "claude-code"
    selection_mode: "following"
    follow_target: { provider: "anthropic", tier: "opus" }
    allowedtools: ["Read", "Write", "Edit", "Grep", "Glob", "Bash"]
  - alias: "planner"
    kind: "agent"
    backend: "claude-code"
    selection_mode: "following"
    follow_target: { provider: "anthropic", tier: "opus" }
    allowedtools: ["Read", "Write", "Grep", "Glob", "Bash"]
logging:
  level: "info"
  file: "arbitron.log"
notifications:
  # teams:
  #   webhookurl: ""
  #     Microsoft Teams Workflows webhook URL. Sensitive: never echoed back on
  #     read. Override via ARBITRON_NOTIFICATIONS_TEAMS_WEBHOOKURL.
  #   enabled: false
  #     When true, every in-app notification is also forwarded to the Teams
  #     channel (best-effort, silent). Override via
  #     ARBITRON_NOTIFICATIONS_TEAMS_ENABLED.
tracing:
  enabled: false
  exportdir: "./traces"
layout:
  maxattempts: 5
  #   Total arrangement-and-review attempts per saved workflow revision. Each
  #   attempt arranges the graph top-to-bottom, renders it and visually reviews
  #   readability; failing candidates are refined and reviewed again. When the
  #   budget is spent the best layout is kept and marked as needing attention.
  #   Values below 1 are clamped to 1 with a startup warning.
  #   Override via ARBITRON_LAYOUT_MAXATTEMPTS.
  # browserpath: ""
  #   Chrome/Chromium executable used to render and review layouts. Empty
  #   (default) auto-detects an installed Chrome/Chromium. On macOS a
  #   chrome-headless-shell on PATH is preferred over Google Chrome.app, which
  #   flickers the Dock on every launch; prefer setting an absolute path to
  #   chrome-headless-shell here over relying on PATH.
  #   Override via ARBITRON_LAYOUT_BROWSERPATH.
  # artifactdir: ""
  #   Directory for rendered review artifacts (overview and tile captures).
  #   Empty (default) uses <directory of database.path>/layout-artifacts.
  #   Override via ARBITRON_LAYOUT_ARTIFACTDIR.
  # browsernosandbox: false
  #   SECURITY: true launches the review browser with --no-sandbox. Only set it
  #   in a container that cannot provide the Chrome sandbox.
  #   Override via ARBITRON_LAYOUT_BROWSERNOSANDBOX.
# samples:
#   seed: true
#     When true (default), the server seeds the built-in sample workflows into
#     the workspace on first boot. Set false to skip seeding (e.g. in a
#     production deployment that manages workflows externally).
#     Override via ARBITRON_SAMPLES_SEED=false.
#   sourcedir: ""
#     Directory from which sample workflows are read during seeding. Empty
#     string (default) means no-op: the server will not seed even if seed is
#     true. Shared installs populate this with the absolute path to the
#     extracted samples tarball (e.g. /var/lib/arbitron/samples).
#     Override via ARBITRON_SAMPLES_SOURCEDIR=/path/to/samples.
# auth:
#   mode: "local"            # "local" (default, single auto-admin) or "org"
#   bootstrapadmins: []      # subjects/emails provisioned as admin on first login (Phase 2)
#                            #   Override via ARBITRON_AUTH_BOOTSTRAPADMINS=a@x.com,b@x.com
#   oidc:
#     issuerurl: ""          # OIDC discovery root; REQUIRED in org mode
#     clientid: ""           # REQUIRED in org mode
#     clientsecret: ""       # REQUIRED in org mode; SECURITY: sensitive, never echoed
#     redirecturl: ""        # REQUIRED in org mode; = publicbaseurl + "/auth/callback"
#     scopes: ["openid", "profile", "email"]   # ARBITRON_AUTH_OIDC_SCOPES=openid,groups
#   edge:                      # auth mode "edge" only (Cloudflare Access / edge-trusted SSO)
#     teamdomain: ""           # Cloudflare team domain, e.g. "https://acme.cloudflareaccess.com"
#                              #   It is BOTH the JWKS source and the expected issuer.
#                              #   REQUIRED in edge mode. Override via ARBITRON_AUTH_EDGE_TEAMDOMAIN
#     audiences: []            # application AUD tag(s) to accept; a token whose aud is not
#                              #   listed is rejected. REQUIRED in edge mode.
#                              #   Override via ARBITRON_AUTH_EDGE_AUDIENCES=aud-one,aud-two
#                              #   NOTE: no secret here — verification uses the edge proxy's
#                              #   PUBLIC signing keys discovered from teamdomain.
#   session:
#     ttlseconds: 43200      # 12h; floored to the default if non-positive
#     cookiename: "arbitron_session"
#     cookiesecure: true     # set false ONLY for local http dev
#   rbac:
#     groupclaim: ""           # token claim carrying group membership; "" => default group only
#                              #   Override via ARBITRON_AUTH_RBAC_GROUPCLAIM=groups
#     defaultgroup: "default"  # sentinel department for claimless users
#                              #   Override via ARBITRON_AUTH_RBAC_DEFAULTGROUP=default
#     leads:                   # department -> list of lead subjects/emails (case-insensitive)
#       engineering: ["alice@corp.example", "bob-subject-id"]
#                              #   Per-dept env overlay: ARBITRON_AUTH_RBAC_LEADS_ENGINEERING=alice@corp.example,bob-subject-id
`
}

// Load reads configuration from defaults, an optional YAML file at configPath,
// and environment variables. The precedence order (highest to lowest) is:
//
//  1. Environment variables (ARBITRON_ prefix, plus ANTHROPIC_API_KEY)
//  2. YAML config file
//  3. Built-in defaults
//
// If the YAML file does not exist, a default config file is created at configPath.
func Load(configPath string) (*domainconfig.Config, error) {
	k := koanf.New(".")

	// Layer 1: defaults.
	if err := k.Load(confmap.Provider(defaults(), "."), nil); err != nil {
		return nil, fmt.Errorf("loading defaults: %w", err)
	}

	// Layer 2: YAML file (if it exists).
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Create the default config file with restrictive permissions.
		dir := filepath.Dir(configPath)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("creating config directory: %w", err)
		}
		if err := os.WriteFile(configPath, []byte(defaultYAMLContent()), 0600); err != nil {
			return nil, fmt.Errorf("writing default config file: %w", err)
		}
	}

	if err := k.Load(koanffile.Provider(configPath), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("loading config file %s: %w", configPath, err)
	}

	// Layer 3: ARBITRON_ prefixed environment variables.
	// The env provider strips the prefix and lowercases, using _ as the delimiter
	// which maps to "." in the koanf key hierarchy.
	if err := k.Load(env.Provider("ARBITRON_", ".", func(s string) string {
		return strings.ToLower(strings.Replace(
			strings.TrimPrefix(s, "ARBITRON_"), "_", ".", -1))
	}), nil); err != nil {
		return nil, fmt.Errorf("loading env vars: %w", err)
	}

	// Special case: ANTHROPIC_API_KEY maps to llm.apikey.
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		if err := k.Load(confmap.Provider(map[string]interface{}{
			"llm.apikey": apiKey,
		}, "."), nil); err != nil {
			return nil, fmt.Errorf("loading ANTHROPIC_API_KEY: %w", err)
		}
	}

	// ARBITRON_SCRIPT_ENVPASSTHROUGH arrives as a comma string via the env
	// provider; overlay it as a real slice so it unmarshals into []string.
	if raw := os.Getenv("ARBITRON_SCRIPT_ENVPASSTHROUGH"); raw != "" {
		if err := k.Load(confmap.Provider(map[string]interface{}{
			"script.envpassthrough": splitCSV(raw),
		}, "."), nil); err != nil {
			return nil, fmt.Errorf("loading script.envpassthrough: %w", err)
		}
	}

	// ARBITRON_AUTH_BOOTSTRAPADMINS / ARBITRON_AUTH_OIDC_SCOPES arrive as comma
	// strings via the env provider; overlay them as real slices so they
	// unmarshal into []string (mirrors the script.envpassthrough overlay).
	if raw := os.Getenv("ARBITRON_AUTH_BOOTSTRAPADMINS"); raw != "" {
		if err := k.Load(confmap.Provider(map[string]interface{}{
			"auth.bootstrapadmins": splitCSV(raw),
		}, "."), nil); err != nil {
			return nil, fmt.Errorf("loading auth.bootstrapadmins: %w", err)
		}
	}
	if raw := os.Getenv("ARBITRON_AUTH_OIDC_SCOPES"); raw != "" {
		if err := k.Load(confmap.Provider(map[string]interface{}{
			"auth.oidc.scopes": splitCSV(raw),
		}, "."), nil); err != nil {
			return nil, fmt.Errorf("loading auth.oidc.scopes: %w", err)
		}
	}

	// ARBITRON_AUTH_EDGE_AUDIENCES arrives as a comma string via the env
	// provider; overlay it as a real slice so it unmarshals into []string
	// (mirrors the auth.oidc.scopes overlay above).
	if raw := os.Getenv("ARBITRON_AUTH_EDGE_AUDIENCES"); raw != "" {
		if err := k.Load(confmap.Provider(map[string]interface{}{
			"auth.edge.audiences": splitCSV(raw),
		}, "."), nil); err != nil {
			return nil, fmt.Errorf("loading auth.edge.audiences: %w", err)
		}
	}

	// ARBITRON_AUTH_RBAC_LEADS_<DEPT>=id1,id2 overlays one department's lead list
	// as a real slice (the env provider would otherwise leave it a comma string).
	// Department names with non-alphanumeric characters are not expressible via
	// env and must use the YAML file. Example:
	//   ARBITRON_AUTH_RBAC_LEADS_ENGINEERING=alice@corp.example,bob-subject
	const leadsEnvPrefix = "ARBITRON_AUTH_RBAC_LEADS_"
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		name, val := kv[:eq], kv[eq+1:]
		if !strings.HasPrefix(name, leadsEnvPrefix) || val == "" {
			continue
		}
		dept := strings.ToLower(strings.TrimPrefix(name, leadsEnvPrefix))
		if dept == "" {
			continue
		}
		if err := k.Load(confmap.Provider(map[string]interface{}{
			"auth.rbac.leads." + dept: splitCSV(val),
		}, "."), nil); err != nil {
			return nil, fmt.Errorf("loading auth.rbac.leads.%s: %w", dept, err)
		}
	}

	// ARBITRON_AUTH_RBAC_MAPPING_<RAWVALUE>=friendly overlays one raw-claim ->
	// friendly-department entry. The suffix after the prefix is the raw claim value
	// (lower-cased; raw values with characters not expressible as env names must use
	// the YAML file). Example:
	//   ARBITRON_AUTH_RBAC_MAPPING_GUID-123=engineering
	const mappingEnvPrefix = "ARBITRON_AUTH_RBAC_MAPPING_"
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		name, val := kv[:eq], kv[eq+1:]
		if !strings.HasPrefix(name, mappingEnvPrefix) || val == "" {
			continue
		}
		rawKey := strings.ToLower(strings.TrimPrefix(name, mappingEnvPrefix))
		if rawKey == "" {
			continue
		}
		if err := k.Load(confmap.Provider(map[string]interface{}{
			"auth.rbac.mapping." + rawKey: val,
		}, "."), nil); err != nil {
			return nil, fmt.Errorf("loading auth.rbac.mapping.%s: %w", rawKey, err)
		}
	}

	// Unmarshal into the domain config struct.
	var cfg domainconfig.Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	normalize(&cfg)

	// validateAuth is the first non-clamping validation in Load: an org-mode
	// deployment missing OIDC config cannot perform a single login, so fail
	// fast at load rather than 500 on the first /auth/login.
	if err := validateAuth(&cfg); err != nil {
		return nil, err
	}

	if err := validateTLS(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// DefaultTimeoutSeconds is the floor applied to a non-positive
// llm.timeoutseconds. SECURITY: a 0 (or negative) value would silently disable
// every per-call deadline — the only deadline the credential-bearing HTTP
// adapters and the claude-cli/opencode subprocesses have — letting a wedged or
// attacker-pointed endpoint hang a node forever and starve the worker pool.
// Flooring it keeps the whole timeout story from being disabled by misconfig.
const DefaultTimeoutSeconds = 1800

// DefaultAgentBackend is the deployment default agent backend (org-readiness
// Phase 2) applied when llm.defaultagentbackend is empty. The org default is
// claude-code → LiteLLM. Flooring an empty value here keeps the backend
// fallback policy in exactly one place.
const DefaultAgentBackend = "claude-code"

// DefaultSessionTTLSeconds is the floor applied to a non-positive
// auth.session.ttlseconds. A zero/negative TTL would mint never-expiring or
// instantly-expired sessions, so it is clamped to the sane 12h default
// (mirroring the timeout floor).
const DefaultSessionTTLSeconds = 43200

// DefaultLayoutMaxAttempts is the total arrangement-and-review attempt budget
// for a saved workflow revision. Values below one are clamped in normalize.
const DefaultLayoutMaxAttempts = 5

// normalize applies safety floors and value normalization to a loaded config in
// place. It clamps a non-positive llm.timeoutseconds up to DefaultTimeoutSeconds
// so a misconfigured 0 cannot disable every deadline, and trims trailing
// slashes from the public base URL so callers can concatenate "/instances/<id>"
// deterministically. It also clamps layout.maxattempts to at least one and
// trims browser/artifact paths. LoadWarnings is reset before normalization so
// warnings can only be produced here, never injected through YAML or env.
func normalize(cfg *domainconfig.Config) {
	cfg.LoadWarnings = nil
	if cfg.LLM.TimeoutSeconds <= 0 {
		cfg.LLM.TimeoutSeconds = DefaultTimeoutSeconds
	}
	// Org-readiness Phase 2: floor an empty (or whitespace-only) default agent
	// backend to claude-code. This is the SINGLE deployment-policy seam for the
	// backend fallback so a blank value cannot silently leave the deployment
	// without a default backend; the executor and the factory both read it.
	if strings.TrimSpace(cfg.LLM.DefaultAgentBackend) == "" {
		cfg.LLM.DefaultAgentBackend = DefaultAgentBackend
	}
	cfg.Server.PublicBaseURL = strings.TrimRight(cfg.Server.PublicBaseURL, "/")
	cfg.Auth.Mode = strings.ToLower(strings.TrimSpace(cfg.Auth.Mode))
	// Trim the edge teamdomain so a YAML value with surrounding whitespace
	// reaches edge.New as a clean URL rather than causing a confusing parse error.
	cfg.Auth.Edge.TeamDomain = strings.TrimSpace(cfg.Auth.Edge.TeamDomain)
	// Trim GroupClaim so a padded YAML value (" groups ") does not cause the OIDC
	// provider or edge verifier to look for the wrong claim name. The edge verifier
	// also trims at construction; this makes both paths consistent and the config
	// struct self-consistent (whitespace-only GroupClaim → "" → no group extraction).
	cfg.Auth.RBAC.GroupClaim = strings.TrimSpace(cfg.Auth.RBAC.GroupClaim)
	if cfg.Auth.Mode == "" {
		cfg.Auth.Mode = domainconfig.AuthModeLocal
	}
	if cfg.Auth.Session.TTLSeconds <= 0 {
		cfg.Auth.Session.TTLSeconds = DefaultSessionTTLSeconds
	}
	if cfg.Auth.Session.CookieName == "" {
		cfg.Auth.Session.CookieName = "arbitron_session"
	}
	if cfg.Layout.MaxAttempts < 1 {
		cfg.LoadWarnings = append(cfg.LoadWarnings,
			fmt.Sprintf("layout.maxattempts %d is below 1; using 1", cfg.Layout.MaxAttempts))
		cfg.Layout.MaxAttempts = 1
	}
	cfg.Layout.BrowserPath = strings.TrimSpace(cfg.Layout.BrowserPath)
	cfg.Layout.ArtifactDir = strings.TrimSpace(cfg.Layout.ArtifactDir)
}

// validateAuth enforces the org-mode contract: a deployment that turns on org
// mode without a complete OIDC config cannot perform a single login, so we fail
// fast at load rather than 500 on the first /auth/login. Mirrors the
// timeout-floor fail-fast precedent. Local mode requires nothing; an unknown
// mode (which normalize cannot produce, but a hand-set value could) is rejected.
func validateAuth(cfg *domainconfig.Config) error {
	switch cfg.Auth.Mode {
	case domainconfig.AuthModeLocal:
		return nil
	case domainconfig.AuthModeOrg:
		var missing []string
		if strings.TrimSpace(cfg.Auth.OIDC.IssuerURL) == "" {
			missing = append(missing, "auth.oidc.issuerurl")
		}
		if strings.TrimSpace(cfg.Auth.OIDC.ClientID) == "" {
			missing = append(missing, "auth.oidc.clientid")
		}
		if strings.TrimSpace(cfg.Auth.OIDC.ClientSecret) == "" {
			missing = append(missing, "auth.oidc.clientsecret")
		}
		if strings.TrimSpace(cfg.Auth.OIDC.RedirectURL) == "" {
			missing = append(missing, "auth.oidc.redirecturl")
		}
		if len(missing) > 0 {
			return fmt.Errorf("org mode requires OIDC config; missing: %s", strings.Join(missing, ", "))
		}
		return nil
	case domainconfig.AuthModeEdge:
		var missing []string
		if strings.TrimSpace(cfg.Auth.Edge.TeamDomain) == "" {
			missing = append(missing, "auth.edge.teamdomain")
		}
		if !hasNonEmptyEntry(cfg.Auth.Edge.Audiences) {
			missing = append(missing, "auth.edge.audiences")
		}
		if len(missing) > 0 {
			return fmt.Errorf("edge mode requires edge config; missing: %s", strings.Join(missing, ", "))
		}
		return nil
	default:
		return fmt.Errorf("invalid auth.mode %q (want %q, %q, or %q)", cfg.Auth.Mode, domainconfig.AuthModeLocal, domainconfig.AuthModeOrg, domainconfig.AuthModeEdge)
	}
}

// validateTLS enforces the direct-HTTPS contract fail-fast at load: when TLS is
// enabled, exactly one of {certfile+keyfile} or {autocertdomain} must be set,
// and autocert requires a cache directory. Mirrors the validateAuth precedent.
func validateTLS(cfg *domainconfig.Config) error {
	t := cfg.Server.TLS
	if !t.Enabled {
		return nil
	}
	hasCert := t.CertFile != ""
	hasKey := t.KeyFile != ""
	hasAutocert := t.AutocertDomain != ""
	if hasCert != hasKey {
		return fmt.Errorf("server.tls.enabled requires both certfile and keyfile")
	}
	if hasCert && hasAutocert {
		return fmt.Errorf("server.tls: set either cert/key or autocertdomain, not both")
	}
	if !hasCert && !hasAutocert {
		return fmt.Errorf("server.tls.enabled requires cert/key or autocertdomain")
	}
	if hasAutocert && t.AutocertCacheDir == "" {
		return fmt.Errorf("server.tls autocertdomain requires autocertcachedir")
	}
	// Autocert provisions its certificate over the ACME tls-alpn-01/HTTP-01 flow
	// and Let's Encrypt only validates the cert against the standard HTTPS port;
	// serving on any other port provisions a cert but leaves the server silently
	// unreachable. Cert/key and disabled modes leave the port free.
	if hasAutocert && cfg.Server.Port != 443 {
		return fmt.Errorf("server.tls autocertdomain requires server.port 443 (got %d); autocert provisions for the standard HTTPS port and any other port leaves the server unreachable", cfg.Server.Port)
	}
	// Local mode has NO authentication (a fixed auto-admin). Direct TLS on a
	// non-loopback host fronts a fully unauthenticated admin API behind a
	// legitimate-looking padlock — the exploit path is an unauthenticated GET.
	// Fail fast unless the operator explicitly opts in via AllowUnauthenticated.
	if cfg.Auth.Mode == domainconfig.AuthModeLocal && !t.AllowUnauthenticated && !isLoopbackHost(cfg.Server.Host) {
		return fmt.Errorf("server.tls.enabled with auth.mode=local (local mode has no authentication) on non-loopback host %q exposes an unauthenticated admin API behind TLS; bind to a loopback address, use auth.mode=org, or set server.tls.allowunauthenticated=true to override", cfg.Server.Host)
	}
	return nil
}

// isLoopbackHost reports whether host is a loopback bind address. It accepts the
// "localhost" alias and any loopback IP literal (127.0.0.0/8, ::1); an empty
// host (bind all interfaces) is treated as non-loopback.
func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// hasNonEmptyEntry reports whether ss contains at least one non-whitespace
// string. Used by validateAuth to catch YAML slices like ["  "] that have
// non-zero length but carry only whitespace entries.
func hasNonEmptyEntry(ss []string) bool {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

// splitCSV splits a comma-separated list, trimming spaces and dropping empties.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
