// Package config defines the domain configuration types for Arbitron.
package config

// Config is the top-level configuration structure for the Arbitron system.
type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Workspace WorkspaceConfig
	LLM      LLMConfig
	Logging  LoggingConfig
	Tracing  TracingConfig
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host string
	Port int
}

// DatabaseConfig holds database connection settings.
type DatabaseConfig struct {
	URL string
}

// WorkspaceConfig holds workspace directory settings.
type WorkspaceConfig struct {
	Path string
}

// LLMConfig holds LLM provider settings.
type LLMConfig struct {
	Provider string
	APIKey   string
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
