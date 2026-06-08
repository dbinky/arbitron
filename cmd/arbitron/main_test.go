package main

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptoidc "github.com/arbitron/arbitron/adapter/oidc"
	"github.com/arbitron/arbitron/domain/engine"
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

// Robustness: domainStack.Close() must not panic when all optional fields
// (schedulerCancel, scheduler, tracingShutdown, db, logCloser) are nil. Only
// the engine is non-nil (Shutdown() only waits on a zero-value WaitGroup).
func TestDomainStack_Close_NilOptionalsDoNotPanic(t *testing.T) {
	eng := engine.NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ds := &domainStack{eng: eng}
	// All optional fields (scheduler, tracing, db, logCloser) are nil.
	// Close must complete without panicking.
	assert.NotPanics(t, func() { ds.Close(context.Background()) })
}

// ---------------------------------------------------------------------------
// Tests: connection subcommand (feat/llm-providers)
// ---------------------------------------------------------------------------

// Happy path: the "connection" subcommand is registered on the root command,
// parallel to "serve" and "mcp".
func TestRootCommand_HasConnectionSubcommand(t *testing.T) {
	rootCmd := &cobra.Command{Use: "arbitron"}
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newMCPCmd())
	rootCmd.AddCommand(newConnectionCmd())

	connCmd, _, err := rootCmd.Find([]string{"connection"})
	assert.NoError(t, err)
	assert.Equal(t, "connection", connCmd.Name())
}

// Happy path: the "validate" subcommand is registered under "connection".
func TestConnectionCmd_HasValidateSubcommand(t *testing.T) {
	rootCmd := &cobra.Command{Use: "arbitron"}
	rootCmd.AddCommand(newConnectionCmd())

	validateCmd, _, err := rootCmd.Find([]string{"connection", "validate"})
	assert.NoError(t, err)
	assert.Equal(t, "validate", validateCmd.Name())
}

// Error path: connection validate RunE is wired (not accidentally left nil).
func TestConnectionValidateCmd_HasRunE(t *testing.T) {
	connCmd := newConnectionCmd()
	var validateCmd *cobra.Command
	for _, sub := range connCmd.Commands() {
		if sub.Name() == "validate" {
			validateCmd = sub
			break
		}
	}
	require.NotNil(t, validateCmd, "validate subcommand must be registered on connection")
	assert.NotNil(t, validateCmd.RunE, "connection validate must have a RunE handler wired")
}

// The org-mode session sweep must be cancelled on Close so its goroutine stops
// cleanly on shutdown (no leaked ticker writing to a closed DB).
func TestDomainStack_Close_CancelsSessionSweep(t *testing.T) {
	eng := engine.NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, nil)
	cancelled := false
	ds := &domainStack{eng: eng, sweepCancel: func() { cancelled = true }}
	assert.NotPanics(t, func() { ds.Close(context.Background()) })
	assert.True(t, cancelled, "Close must cancel the org-mode session sweep")
}

// Org-mode wiring fails fast when the OIDC issuer is unreachable: main.go calls
// adaptoidc.New with the configured issuer and aborts initDomain on error. This
// exercises that exact construction call (without the heavyweight full init).
func TestOrgModeOIDC_FailsFastOnUnreachableIssuer(t *testing.T) {
	_, err := adaptoidc.New(context.Background(), adaptoidc.Config{
		IssuerURL: "http://127.0.0.1:1/unreachable",
		ClientID:  "c",
	})
	require.Error(t, err, "org mode with an unreachable issuer must fail fast")
}

// The root command exposes a persistent --config flag (default empty), used to
// override the config-file path for system/service installs.
func TestRootCommand_HasConfigFlag(t *testing.T) {
	f := newRootCmd().PersistentFlags().Lookup("config")
	require.NotNil(t, f, "--config persistent flag must be registered on root")
	assert.Equal(t, "", f.DefValue, "--config default must be empty")
}

// Cross-seam contract lock for FA27: the Windows Service BinaryPathName is
//   "arbitron.exe" serve --config "%ProgramData%\Arbitron\config.yaml"
// The --config flag is a PersistentFlag on root (not a LocalFlag on serve), so
// it must be visible to the serve command's RunE via cmd.Flags(). If it were
// ever moved to a non-persistent flag or omitted from root, the service would
// silently fall back to ~/.arbitron/config.yaml, booting from the wrong (or
// absent) config file with no error.
func TestServeCmd_ConfigFlagReachesConfigPathFromCmd(t *testing.T) {
	const want = "/programdata/arbitron/config.yaml"
	var got string
	var captureErr error

	root := newRootCmd()
	serveCmd, _, err := root.Find([]string{"serve"})
	require.NoError(t, err, "serve subcommand must be findable in the root command tree")
	serveCmd.RunE = func(cmd *cobra.Command, _ []string) error {
		got, captureErr = configPathFromCmd(cmd)
		return captureErr
	}

	root.SetArgs([]string{"serve", "--config", want})
	require.NoError(t, root.Execute())
	require.NoError(t, captureErr)
	assert.Equal(t, want, got,
		"--config must flow from 'arbitron serve --config path' "+
			"through cobra persistent-flag inheritance into configPathFromCmd; "+
			"the Windows Service BinaryPathName depends on this seam")
}

// SECURITY (defense-in-depth) — the shared outbound HTTP client threaded into
// every credential-bearing LLM chat adapter (openrouter/azurefoundry/
// anthropicapi/bedrock) pins an explicit TLS floor of 1.2. The Go stdlib default
// is already TLS 1.2+, so this makes the floor explicit and regression-proof.
// (PR #23 Consider #1.)
func TestNewOutboundHTTPClient_PinsTLS12(t *testing.T) {
	c := newOutboundHTTPClient(30 * time.Second)
	require.NotNil(t, c)
	assert.Equal(t, 30*time.Second, c.Timeout, "the per-call backstop timeout is preserved")

	tr, ok := c.Transport.(*http.Transport)
	require.True(t, ok, "outbound client must use an *http.Transport")
	require.NotNil(t, tr.TLSClientConfig, "transport must carry an explicit TLS config")
	assert.Equal(t, uint16(tls.VersionTLS12), tr.TLSClientConfig.MinVersion,
		"outbound TLS floor must be pinned to TLS 1.2")
}
