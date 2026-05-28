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
		"llm.maxresponsebytes": int64(256 * 1024 * 1024),
		"logging.level":        "info",
		"logging.file":         "arbitron.log",
		"tracing.enabled":      false,
		"tracing.exportdir":    "./traces",
		"queue.globalcap":      20,
		"queue.perusercap":     5,
	}
}

// defaultYAMLContent returns a YAML string representing the default config file
// that is written when no config file exists.
func defaultYAMLContent() string {
	return `server:
  host: "127.0.0.1"
  port: 8080
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
logging:
  level: "info"
  file: "arbitron.log"
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

	// Unmarshal into the domain config struct.
	var cfg domainconfig.Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	return &cfg, nil
}
