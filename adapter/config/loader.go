// Package config implements the koanf-based configuration loader for Arbitron.
// It loads configuration from defaults, an optional YAML file, and environment
// variables (with env vars taking highest precedence).
package config

import (
	"fmt"
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
		"llm.provider":   "anthropic",
		"llm.apikey":     "",
		"llm.binarypath": "",
		"llm.model":      "",
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
		"llm.maxresponsebytes":  int64(256 * 1024 * 1024),
		"logging.level":         "info",
		"logging.file":          "arbitron.log",
		"tracing.enabled":       false,
		"tracing.exportdir":     "./traces",
		"queue.globalcap":       20,
		"queue.perusercap":      5,
		"script.envpassthrough": []string{},
		// Profile registry: alias -> capability kind + model-id. A fresh
		// install ships a chat/agent pair so a basic workflow validates; empty
		// model means "use the claude-cli default model" (cfg.LLM.Model).
		//
		// The reviewer/coder/planner/interviewer/assessor aliases are the
		// in-repo default registry for the workflows shipped in this repo
		// (workspaces/pr-review, workspaces/feature-pipeline). Shipping them as
		// defaults (overridable by the deployment config.yaml) lets a fresh
		// clone validate those workflows honestly instead of passing
		// workflow_validate and then dying mid-run with "unknown profile alias"
		// (PR #18 Must Fix item 4, comment 4614860006).
		"profiles": []map[string]interface{}{
			{"alias": "default-chat", "kind": "chat", "model": ""},
			{"alias": "default-agent", "kind": "agent", "model": ""},
			{"alias": "assessor", "kind": "chat", "model": ""},
			{"alias": "interviewer", "kind": "agent", "model": ""},
			{"alias": "reviewer", "kind": "agent", "model": ""},
			{"alias": "coder", "kind": "agent", "model": ""},
			{"alias": "planner", "kind": "agent", "model": ""},
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
database:
  path: "./arbitron.db"
workspace:
  path: "./workspaces"
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
  # kind (chat | agent) and a model-id. Nodes reference a backend by alias, so
  # a config swap repoints aliases without touching workflow.json. An empty
  # model means "use the claude-cli default model" (llm.model). In Phase 1
  # every profile resolves to claude-cli; chat selects the no-tool ChatAdapter,
  # agent selects the tool-using AgentAdapter.
  - alias: "default-chat"
    kind: "chat"
    model: ""
  - alias: "default-agent"
    kind: "agent"
    model: ""
  # In-repo default registry for the workflows shipped in this repo
  # (workspaces/pr-review, workspaces/feature-pipeline). Override the model-id
  # per alias to repoint a persona at a specific backend; remove an alias only
  # if you are not running its workflow.
  - alias: "assessor"
    kind: "chat"
    model: ""
  - alias: "interviewer"
    kind: "agent"
    model: ""
  - alias: "reviewer"
    kind: "agent"
    model: ""
  - alias: "coder"
    kind: "agent"
    model: ""
  - alias: "planner"
    kind: "agent"
    model: ""
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

	// Unmarshal into the domain config struct.
	var cfg domainconfig.Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	normalize(&cfg)

	return &cfg, nil
}

// DefaultTimeoutSeconds is the floor applied to a non-positive
// llm.timeoutseconds. SECURITY: a 0 (or negative) value would silently disable
// every per-call deadline — the only deadline the credential-bearing HTTP
// adapters and the claude-cli/opencode subprocesses have — letting a wedged or
// attacker-pointed endpoint hang a node forever and starve the worker pool.
// Flooring it keeps the whole timeout story from being disabled by misconfig.
const DefaultTimeoutSeconds = 1800

// normalize applies safety floors and value normalization to a loaded config in
// place. It clamps a non-positive llm.timeoutseconds up to DefaultTimeoutSeconds
// so a misconfigured 0 cannot disable every deadline, and trims trailing
// slashes from the public base URL so callers can concatenate "/instances/<id>"
// deterministically.
func normalize(cfg *domainconfig.Config) {
	if cfg.LLM.TimeoutSeconds <= 0 {
		cfg.LLM.TimeoutSeconds = DefaultTimeoutSeconds
	}
	cfg.Server.PublicBaseURL = strings.TrimRight(cfg.Server.PublicBaseURL, "/")
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
