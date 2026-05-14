# Phase 1, Task 5: CLI Commands (arbitron mcp, arbitron serve --mcp)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the `arbitron mcp` subcommand for stdio-only MCP server mode and the `--mcp` flag to `arbitron serve` for combined HTTP + MCP mode.

**Architecture:** Both modes boot the full domain layer (database, engine, event bus, queue manager) — same initialization as the existing `runServe`. The difference: `arbitron mcp` runs only the MCP stdio server (no HTTP), while `arbitron serve --mcp` runs both. The MCP server receives the same domain dependencies via the `Deps` struct.

**Tech Stack:** Go, cobra, os, os/signal

---

## Prerequisites

- Tasks 1-4 complete (MCP server with all tools registered)

## Files to Modify

### Modify: `cmd/arbitron/main.go`

- Add `newMCPCmd()` function returning a cobra.Command for `arbitron mcp`
- Add `--mcp` flag to the existing `serve` command
- Extract shared domain initialization into a helper function to avoid duplication

### Test: `cmd/arbitron/main_test.go`

---

### Task 5.1: Extract Domain Initialization

- [ ] **Step 1: Extract `initDomain` helper**

The existing `runServe` function in `cmd/arbitron/main.go` initializes database, repos, engine, queue manager, etc. Extract the reusable parts into a struct and helper:

```go
// domainStack holds all initialized domain components.
type domainStack struct {
	cfg             *config.Config
	logger          *slog.Logger
	logCloser       io.Closer
	pool            *pgxpool.Pool
	eng             *engine.Engine
	workflowRepo    *filesystem.WorkflowRepo
	fileReader      *filesystem.FileReader
	instanceRepo    *postgres.InstanceRepo
	nodeExecRepo    *postgres.NodeExecutionRepo
	snapshotRepo    *postgres.VariableSnapshotRepo
	userRepo        *postgres.UserRepo
	queueRepo       *postgres.QueueRepo
	queueMgr        *queue.Manager
	bus             *eventbus.EventBus
	notifRepo       *postgres.NotificationRepo
	convHandler     *engine.ConversationHandler
	tracingShutdown func(context.Context) error
}

// initDomain boots the full domain stack: config, logging, database, engine,
// queue manager, and all repositories. Caller is responsible for cleanup via
// the returned stack's Close method.
func initDomain(ctx context.Context, configPath string) (*domainStack, error) {
	// ... (same initialization logic currently in runServe, through step 12d)
}

// Close releases all resources held by the domain stack.
func (ds *domainStack) Close(ctx context.Context) {
	ds.eng.Shutdown()
	if ds.tracingShutdown != nil {
		_ = ds.tracingShutdown(ctx)
	}
	ds.pool.Close()
	ds.logCloser.Close()
}
```

- [ ] **Step 2: Refactor runServe to use initDomain**

```go
func runServe(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	configPath := filepath.Join(mustHomeDir(), ".arbitron", "config.yaml")

	ds, err := initDomain(ctx, configPath)
	if err != nil {
		return err
	}
	defer ds.Close(context.Background())

	// ... existing HTTP server setup using ds fields ...
}
```

- [ ] **Step 3: Verify all existing tests pass**

Run: `go test ./... -count=1`
Expected: ALL PASS (refactoring only, no behavior change)

- [ ] **Step 4: Commit**

```bash
git add cmd/arbitron/main.go
git commit -m "refactor: extract initDomain helper for shared domain initialization"
```

---

### Task 5.2: Add `arbitron mcp` Subcommand

- [ ] **Step 1: Write failing test**

Add to `cmd/arbitron/main_test.go`:

```go
func TestMCPCommand_Exists(t *testing.T) {
	rootCmd := &cobra.Command{Use: "arbitron"}
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newMCPCmd())

	// Verify the mcp subcommand is registered
	mcpCmd, _, err := rootCmd.Find([]string{"mcp"})
	require.NoError(t, err)
	assert.Equal(t, "mcp", mcpCmd.Use)
}
```

- [ ] **Step 2: Implement newMCPCmd**

```go
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start the MCP stdio server",
		Long:  "Start the Arbitron MCP server communicating over stdin/stdout. Boots the full domain layer but does not start the HTTP server.",
		RunE:  runMCP,
	}
}

func runMCP(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	configPath := filepath.Join(mustHomeDir(), ".arbitron", "config.yaml")

	ds, err := initDomain(ctx, configPath)
	if err != nil {
		return err
	}
	defer ds.Close(context.Background())

	// Build MCP server with domain dependencies
	mcpDeps := &adaptmcp.Deps{
		Engine:       ds.eng,
		Workflows:    ds.workflowRepo,
		Instances:    ds.instanceRepo,
		NodeExecs:    ds.nodeExecRepo,
		Snapshots:    ds.snapshotRepo,
		FileReader:   ds.fileReader,
		EventBus:     ds.bus,
		Config:       ds.cfg,
		QueueRepo:    ds.queueRepo,
		Notifications: ds.notifRepo,
	}

	mcpSrv := adaptmcp.NewServerWithDeps("arbitron", version, mcpDeps)
	adaptmcp.RegisterAllTools(mcpSrv)

	// Run stdio server — blocks until stdin closes or context is cancelled
	ds.logger.Info("MCP stdio server starting")

	// Handle SIGINT/SIGTERM
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return mcpSrv.ServeStdio(ctx, os.Stdin, os.Stdout)
}

func mustHomeDir() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Sprintf("cannot determine home directory: %v", err))
	}
	return dir
}
```

- [ ] **Step 3: Register in main()**

```go
rootCmd.AddCommand(newMCPCmd())
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/arbitron/ -run TestMCPCommand -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/arbitron/main.go cmd/arbitron/main_test.go
git commit -m "feat: add arbitron mcp subcommand for stdio MCP server"
```

---

### Task 5.3: Add `--mcp` Flag to Serve Command

- [ ] **Step 1: Write failing test**

```go
func TestServeCommand_MCPFlag(t *testing.T) {
	cmd := newServeCmd()
	flag := cmd.Flags().Lookup("mcp")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}
```

- [ ] **Step 2: Add the --mcp flag**

In `newServeCmd()`:

```go
func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Arbitron server",
		Long:  "Start the Arbitron HTTP server, connect to Postgres, and run migrations.",
		RunE:  runServe,
	}
	cmd.Flags().Bool("mcp", false, "Also start the MCP stdio server alongside the HTTP server")
	return cmd
}
```

- [ ] **Step 3: Wire MCP startup in runServe**

At the end of `runServe`, after the HTTP server starts but before the shutdown select:

```go
	// Optionally start MCP stdio server
	mcpEnabled, _ := cmd.Flags().GetBool("mcp")
	if mcpEnabled {
		mcpDeps := &adaptmcp.Deps{
			Engine:       ds.eng,
			Workflows:    ds.workflowRepo,
			Instances:    ds.instanceRepo,
			NodeExecs:    ds.nodeExecRepo,
			Snapshots:    ds.snapshotRepo,
			FileReader:   ds.fileReader,
			EventBus:     ds.bus,
			Config:       ds.cfg,
			QueueRepo:    ds.queueRepo,
			Notifications: ds.notifRepo,
		}
		mcpSrv := adaptmcp.NewServerWithDeps("arbitron", version, mcpDeps)
		adaptmcp.RegisterAllTools(mcpSrv)

		mcpCtx, mcpCancel := context.WithCancel(context.Background())
		defer mcpCancel()

		go func() {
			ds.logger.Info("MCP stdio server starting (alongside HTTP)")
			if err := mcpSrv.ServeStdio(mcpCtx, os.Stdin, os.Stdout); err != nil {
				ds.logger.Error("MCP server error", "error", err)
			}
		}()
	}
```

In the shutdown section, `mcpCancel()` will be called by the deferred cancel, stopping the MCP server.

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/arbitron/ -v`
Expected: ALL PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./... -count=1`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/arbitron/main.go cmd/arbitron/main_test.go
git commit -m "feat: add --mcp flag to arbitron serve for combined HTTP + MCP mode"
```

---

## Acceptance Criteria

- `arbitron mcp` boots the domain layer and runs the MCP stdio server
- `arbitron mcp` exits cleanly on SIGINT/SIGTERM and stdin EOF
- `arbitron serve --mcp` starts both the HTTP server and MCP stdio server in the same process
- Both modes share the same domain state (create workflow via MCP, list via HTTP)
- `arbitron serve` (without `--mcp`) works exactly as before — no MCP server started
- Domain initialization is shared code, not duplicated
- All existing tests continue to pass
