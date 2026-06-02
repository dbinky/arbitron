package main

import (
	"log/slog"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// Security wiring: mcpDeps threads the logger and the vendor-abstraction
// security fields (Connections, ConnChecker, ProfileChecker) from the domain
// stack into the MCP Deps struct. A nil field on the stack must produce a nil
// field in Deps (not a non-nil zero-value stub), so callers can detect missing
// dependencies at construction time.
func TestDomainStack_MCPDeps_WiresSecurityFields(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(nil, nil)) // non-nil sentinel

	// A stack with only the logger set; the interface fields (connStore,
	// connChecker, profileChecker) are nil, which is intentional — we verify
	// that nil propagates through rather than being accidentally swapped.
	ds := &domainStack{logger: logger}
	deps := ds.mcpDeps()

	require.NotNil(t, deps)
	assert.Same(t, logger, deps.Logger, "Logger must be threaded from domainStack.logger")
	assert.Nil(t, deps.Connections, "nil connStore must produce nil Connections in MCP deps")
	assert.Nil(t, deps.ConnChecker, "nil connChecker must produce nil ConnChecker in MCP deps")
	assert.Nil(t, deps.ProfileChecker, "nil profileChecker must produce nil ProfileChecker in MCP deps")
}
