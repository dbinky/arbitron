# Phase 1 Task 1: Project Scaffolding & Configuration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Initialize the Go module, create the directory structure, define the domain config type, and implement the koanf-based config loader.

**Architecture:** Hexagonal architecture. Domain config types live in `domain/config/` with zero imports. The koanf adapter lives in `adapter/config/` and returns domain types.

**Tech Stack:** Go 1.23+, koanf v2

---

### Task 1: Go Module and Directory Structure

**Files:**
- Create: `go.mod`
- Create: `domain/config/config.go`
- Create: `adapter/config/loader.go`
- Create: `adapter/config/loader_test.go`

- [ ] **Step 1: Initialize Go module**

```bash
cd ~/Repos/arbitron
go mod init github.com/arbitron/arbitron
```

- [ ] **Step 2: Create directory structure**

```bash
mkdir -p cmd/arbitron
mkdir -p domain/{workflow,instance,variable,engine,config}
mkdir -p adapter/{postgres/migrations,postgres/queries,filesystem,anthropic,http,config,observability,eventbus}
```

- [ ] **Step 3: Write the domain config type**

Create `domain/config/config.go`:

```go
package config

type Config struct {
	Server    ServerConfig
	Database  DatabaseConfig
	Workspace WorkspaceConfig
	LLM       LLMConfig
	Logging   LoggingConfig
	Tracing   TracingConfig
}

type ServerConfig struct {
	Host string
	Port int
}

type DatabaseConfig struct {
	URL string
}

type WorkspaceConfig struct {
	Path string
}

type LLMConfig struct {
	Provider string
	APIKey   string
}

type LoggingConfig struct {
	Level string
	File  string
}

type TracingConfig struct {
	Enabled   bool
	ExportDir string
}
```

- [ ] **Step 4: Install koanf dependencies**

```bash
go get github.com/knadh/koanf/v2
go get github.com/knadh/koanf/providers/file
go get github.com/knadh/koanf/providers/env
go get github.com/knadh/koanf/providers/confmap
go get github.com/knadh/koanf/parsers/yaml
```

- [ ] **Step 5: Write failing tests for config loader — defaults**

Create `adapter/config/loader_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	adaptercfg "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/config"
)

func TestLoad_Defaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg, err := adaptercfg.Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("expected host 127.0.0.1, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 8090 {
		t.Errorf("expected port 8090, got %d", cfg.Server.Port)
	}
	if cfg.Database.URL != "postgres://localhost:5432/arbitron" {
		t.Errorf("expected default DB URL, got %s", cfg.Database.URL)
	}
	if cfg.LLM.Provider != "anthropic" {
		t.Errorf("expected provider anthropic, got %s", cfg.LLM.Provider)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("expected log level info, got %s", cfg.Logging.Level)
	}
	if cfg.Tracing.Enabled != true {
		t.Error("expected tracing enabled by default")
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

```bash
go test ./adapter/config/ -v -run TestLoad_Defaults
```

Expected: FAIL — `adaptercfg.Load` not defined.

- [ ] **Step 7: Implement config loader**

Create `adapter/config/loader.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	domainconfig "github.com/arbitron/arbitron/domain/config"
)

var defaults = map[string]interface{}{
	"server.host":      "127.0.0.1",
	"server.port":      8090,
	"database.url":     "postgres://localhost:5432/arbitron",
	"workspace.path":   "~/.arbitron/workflows/",
	"llm.provider":     "anthropic",
	"llm.apikey":       "",
	"logging.level":    "info",
	"logging.file":     "~/.arbitron/logs/arbitron.log",
	"tracing.enabled":  true,
	"tracing.exportdir": "~/.arbitron/traces/",
}

func Load(configPath string) (*domainconfig.Config, error) {
	k := koanf.New(".")

	if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
		return nil, err
	}

	if _, err := os.Stat(configPath); err == nil {
		if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		if err := writeDefaultConfig(configPath); err != nil {
			return nil, err
		}
	}

	if err := k.Load(env.Provider("ARBITRON_", ".", func(s string) string {
		return strings.Replace(strings.ToLower(strings.TrimPrefix(s, "ARBITRON_")), "_", ".", -1)
	}), nil); err != nil {
		return nil, err
	}

	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		k.Set("llm.apikey", apiKey)
	}

	cfg := &domainconfig.Config{}
	cfg.Server.Host = k.String("server.host")
	cfg.Server.Port = k.Int("server.port")
	cfg.Database.URL = k.String("database.url")
	cfg.Workspace.Path = k.String("workspace.path")
	cfg.LLM.Provider = k.String("llm.provider")
	cfg.LLM.APIKey = k.String("llm.apikey")
	cfg.Logging.Level = k.String("logging.level")
	cfg.Logging.File = k.String("logging.file")
	cfg.Tracing.Enabled = k.Bool("tracing.enabled")
	cfg.Tracing.ExportDir = k.String("tracing.exportdir")

	return cfg, nil
}

func writeDefaultConfig(configPath string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return err
	}
	content := `server:
  host: "127.0.0.1"
  port: 8090

database:
  url: "postgres://localhost:5432/arbitron"

workspace:
  path: "~/.arbitron/workflows/"

llm:
  provider: "anthropic"
  apikey: ""

logging:
  level: "info"
  file: "~/.arbitron/logs/arbitron.log"

tracing:
  enabled: true
  exportdir: "~/.arbitron/traces/"
`
	return os.WriteFile(configPath, []byte(content), 0644)
}
```

- [ ] **Step 8: Run test to verify it passes**

```bash
go test ./adapter/config/ -v -run TestLoad_Defaults
```

Expected: PASS

- [ ] **Step 9: Write tests for YAML override and env var override**

Add to `adapter/config/loader_test.go`:

```go
func TestLoad_YAMLOverride(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	yamlContent := `server:
  host: "0.0.0.0"
  port: 9090
database:
  url: "postgres://myhost:5432/mydb"
`
	os.WriteFile(configPath, []byte(yamlContent), 0644)

	cfg, err := adaptercfg.Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected host 0.0.0.0, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if cfg.Database.URL != "postgres://myhost:5432/mydb" {
		t.Errorf("expected custom DB URL, got %s", cfg.Database.URL)
	}
	// Defaults still apply for unset values
	if cfg.LLM.Provider != "anthropic" {
		t.Errorf("expected default provider, got %s", cfg.LLM.Provider)
	}
}

func TestLoad_EnvVarOverride(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	t.Setenv("ARBITRON_SERVER_PORT", "7070")
	t.Setenv("ARBITRON_DATABASE_URL", "postgres://envhost:5432/envdb")

	cfg, err := adaptercfg.Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.Port != 7070 {
		t.Errorf("expected port 7070 from env, got %d", cfg.Server.Port)
	}
	if cfg.Database.URL != "postgres://envhost:5432/envdb" {
		t.Errorf("expected env DB URL, got %s", cfg.Database.URL)
	}
}

func TestLoad_EnvVarOverridesYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	yamlContent := `server:
  port: 9090
`
	os.WriteFile(configPath, []byte(yamlContent), 0644)
	t.Setenv("ARBITRON_SERVER_PORT", "7070")

	cfg, err := adaptercfg.Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.Port != 7070 {
		t.Errorf("expected env port 7070 to override YAML 9090, got %d", cfg.Server.Port)
	}
}

func TestLoad_AnthropicAPIKeyEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key-123")

	cfg, err := adaptercfg.Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.LLM.APIKey != "sk-test-key-123" {
		t.Errorf("expected ANTHROPIC_API_KEY mapping, got %s", cfg.LLM.APIKey)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	os.WriteFile(configPath, []byte("{{invalid yaml"), 0644)

	_, err := adaptercfg.Load(configPath)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestLoad_MissingFileCreatesDefault(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "subdir", "config.yaml")

	_, err := adaptercfg.Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		t.Error("expected default config file to be created")
	}
}
```

- [ ] **Step 10: Run all tests**

```bash
go test ./adapter/config/ -v
```

Expected: all PASS

- [ ] **Step 11: Commit**

```bash
git add go.mod go.sum domain/ adapter/config/
git commit -m "feat: project scaffolding, domain config type, koanf config loader with tests"
```
