package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestNewServeCmd(t *testing.T) {
	cmd := newServeCmd()
	assert.Equal(t, "serve", cmd.Use)
}

// ---------------------------------------------------------------------------
// Tests: CLI subcommands and flags
// ---------------------------------------------------------------------------

// Happy path: the "mcp" subcommand is registered on the root command.
func TestRootCommand_HasMCPSubcommand(t *testing.T) {
	rootCmd := &cobra.Command{Use: "arbitron"}
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newMCPCmd())

	mcpCmd, _, err := rootCmd.Find([]string{"mcp"})
	assert.NoError(t, err)
	assert.Equal(t, "mcp", mcpCmd.Name())
}

// Happy path: the --mcp-stdio flag exists on the serve subcommand with default false.
// (HTTP MCP is always mounted at /mcp; this flag adds the stdio transport.)
func TestServeCommand_HasMCPFlag(t *testing.T) {
	cmd := newServeCmd()
	f := cmd.Flags().Lookup("mcp-stdio")
	assert.NotNil(t, f, "--mcp-stdio flag must be registered on serve command")
	assert.Equal(t, "false", f.DefValue, "--mcp-stdio flag default must be false")
}

// Happy path: the "mcp" subcommand has the expected short description.
func TestMCPCommand_ShortDescription(t *testing.T) {
	cmd := newMCPCmd()
	assert.Equal(t, "Start the MCP stdio server only", cmd.Short)
}

// Happy path: the "serve" subcommand is still present alongside "mcp".
func TestRootCommand_HasServeSubcommand(t *testing.T) {
	rootCmd := &cobra.Command{Use: "arbitron"}
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newMCPCmd())

	serveCmd, _, err := rootCmd.Find([]string{"serve"})
	assert.NoError(t, err)
	assert.Equal(t, "serve", serveCmd.Name())
}

// Error path: serve command has RunE wired (not accidentally left nil).
func TestServeCommand_HasRunE(t *testing.T) {
	cmd := newServeCmd()
	assert.NotNil(t, cmd.RunE, "serve command must have a RunE handler wired — a nil RunE silently does nothing")
}

// Error path: mcp command has RunE wired (not accidentally left nil).
func TestMCPCommand_HasRunE(t *testing.T) {
	cmd := newMCPCmd()
	assert.NotNil(t, cmd.RunE, "mcp command must have a RunE handler wired — a nil RunE silently does nothing")
}
