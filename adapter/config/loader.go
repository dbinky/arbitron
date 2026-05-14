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
		"server.host":       "127.0.0.1",
		"server.port":       8080,
		"database.path":     "./arbitron.db",
		"workspace.path":    "./workspaces",
		"llm.provider":      "anthropic",
		"llm.apikey":        "",
		"logging.level":     "info",
		"logging.file":      "arbitron.log",
		"tracing.enabled":   false,
		"tracing.exportdir": "./traces",
		"queue.globalcap":   20,
		"queue.perusercap":  5,
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
