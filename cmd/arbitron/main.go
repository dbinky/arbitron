// Package main is the entry point for the Arbitron CLI.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/arbitron/arbitron/adapter/anthropic"
	"github.com/arbitron/arbitron/adapter/claudecli"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/adapter/dashboard"
	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/adapter/filesystem"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/adapter/observability"
	"github.com/arbitron/arbitron/adapter/process"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Compile-time assertion that *engine.Engine satisfies the HTTP adapter's EngineService.
var _ adapthttp.EngineService = (*engine.Engine)(nil)

func main() {
	rootCmd := &cobra.Command{
		Use:   "arbitron",
		Short: "Arbitron workflow orchestration engine",
		Long:  "Arbitron is a Go-based workflow orchestration engine with hexagonal architecture.",
	}

	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newMCPCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Arbitron server",
		Long:  "Start the Arbitron HTTP server (with MCP at /mcp), open the SQLite database, and run migrations.",
		RunE:  runServe,
	}
	cmd.Flags().Bool("mcp-stdio", false, "Also start the MCP stdio server alongside the HTTP server (HTTP MCP is always mounted at /mcp)")
	return cmd
}

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start the MCP stdio server only",
		Long:  "Start the Arbitron MCP server over stdin/stdout without the HTTP server.",
		RunE:  runMCP,
	}
}

// domainStack holds all shared domain dependencies initialised by initDomain.
// Both the HTTP serve command and the standalone MCP command reuse this struct
// so that initialisation logic is not duplicated.
type domainStack struct {
	cfg                 *config.Config
	logger              *slog.Logger
	logCloser           io.Closer
	db                  *sql.DB
	eng                 *engine.Engine
	workflowRepo        workflow.WorkflowRepository
	instanceRepo        instance.InstanceRepository
	nodeExecRepo        instance.NodeExecutionRepository
	snapshotRepo        instance.VariableSnapshotRepository
	fileReader          engine.FileReader
	bus                 engine.EventBus
	queueMgr            *queue.Manager
	queueRepo           instance.QueueRepository
	scheduler           *queue.Scheduler
	schedulerCancel     context.CancelFunc
	webhookRegistry     *adapthttp.WebhookRegistry
	notifRepo           notification.Repository
	conversationHandler engine.ConversationHandler
	tracingShutdown     func(ctx context.Context) error
}

// Close tears down the domain stack in the correct order: engine drains first,
// then the scheduler stops, then tracing flushes, then the database closes,
// and finally the log sink is closed.
func (ds *domainStack) Close(ctx context.Context) {
	ds.eng.Shutdown()
	if ds.schedulerCancel != nil {
		ds.schedulerCancel()
	}
	if ds.scheduler != nil {
		ds.scheduler.Stop()
	}
	if ds.tracingShutdown != nil {
		if err := ds.tracingShutdown(ctx); err != nil {
			ds.logger.Error("tracing shutdown error", "error", err)
		}
	}
	if ds.db != nil {
		ds.db.Close()
	}
	if ds.logCloser != nil {
		ds.logCloser.Close()
	}
}

// mcpDeps builds the MCP adapter Deps from the shared domain stack.
func (ds *domainStack) mcpDeps() *adaptmcp.Deps {
	return &adaptmcp.Deps{
		Engine:              ds.eng,
		Workflows:           ds.workflowRepo,
		Instances:           ds.instanceRepo,
		NodeExecs:           ds.nodeExecRepo,
		Snapshots:           ds.snapshotRepo,
		FileReader:          ds.fileReader,
		EventBus:            ds.bus,
		Config:              ds.cfg,
		QueueManager:        ds.queueMgr,
		QueueRepo:           ds.queueRepo,
		Notifications:       ds.notifRepo,
		ConversationHandler: ds.conversationHandler,
	}
}

// initDomain performs all shared initialisation: configuration, logging,
// database, adapters, engine, queue manager, notifications, tracing, and
// crash recovery. The returned domainStack must be closed by the caller.
func initDomain(_ context.Context) (*domainStack, error) {
	// 1. Load configuration.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("determining home directory: %w", err)
	}
	configPath := filepath.Join(homeDir, ".arbitron", "config.yaml")

	slog.Info("loading configuration", "path", configPath)
	cfg, err := configadapter.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	// 2. Set up structured logging.
	logger, logCloser, err := observability.SetupLogging(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("setting up logging: %w", err)
	}

	// 3. Open SQLite database.
	logger.Info("opening database", "path", cfg.Database.Path)
	db, err := adaptsqlite.Open(cfg.Database.Path)
	if err != nil {
		logCloser.Close()
		return nil, fmt.Errorf("opening database: %w", err)
	}
	logger.Info("database connection established")

	// 4. Run migrations.
	logger.Info("running database migrations")
	if err := adaptsqlite.RunMigrations(db); err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	logger.Info("database migrations complete")

	// 5. Initialize adapters.
	instanceRepo := adaptsqlite.NewInstanceRepo(db)
	nodeExecRepo := adaptsqlite.NewNodeExecutionRepo(db)
	snapshotRepo := adaptsqlite.NewVariableSnapshotRepo(db)
	userRepo := adaptsqlite.NewUserRepo(db)

	// 6. Create filesystem-based repos.
	workflowRepo := filesystem.NewWorkflowRepo(cfg.Workspace.Path)
	fileReader := filesystem.NewFileReader(cfg.Workspace.Path)

	// 7. Create the LLM provider based on config.
	var llmProvider engine.LLMProvider
	switch cfg.LLM.Provider {
	case "claude-cli":
		// SECURITY: WorkspaceRoot is the absolute workspace path; the provider
		// uses it to pin every per-request WorkDir under this subtree.
		wsRoot, err := filepath.Abs(cfg.Workspace.Path)
		if err != nil {
			db.Close()
			logCloser.Close()
			return nil, fmt.Errorf("resolving workspace path: %w", err)
		}
		llmProvider = claudecli.New(claudecli.Config{
			BinaryPath:       cfg.LLM.BinaryPath,
			DefaultModel:     cfg.LLM.Model,
			WorkspaceRoot:    wsRoot,
			Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
			AllowedTools:     cfg.LLM.AllowedTools,
			SkipPermissions:  cfg.LLM.SkipPermissions,
			MaxResponseBytes: cfg.LLM.MaxResponseBytes,
			Runner:           nil, // production os/exec runner
		})
	default:
		llmProvider = anthropic.New(cfg.LLM.APIKey)
	}

	// 8. Create in-process event bus.
	bus := eventbus.New()

	// 9. Create all node executors and build the executor map.
	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeLLMCall:     engine.NewLLMCallExecutor(fileReader, llmProvider),
		workflow.NodeTypeScript:      process.NewScriptExecutor(fileReader),
		workflow.NodeTypeToolCall:    process.NewToolCallExecutor(),
		workflow.NodeTypeConditional: engine.NewConditionalExecutor(),
		workflow.NodeTypeTransform:   engine.NewTransformExecutor(),
	}

	// 10. Create the queue repository.
	queueRepo := adaptsqlite.NewQueueRepo(db)

	// 11. Create the workflow engine (queue manager wired below).
	eng := engine.NewEngine(
		workflowRepo,
		instanceRepo,
		nodeExecRepo,
		snapshotRepo,
		userRepo,
		executors,
		bus,
		nil, // queueManager set below after creation
		queueRepo,
	)

	// 11a. Wire the per-instance workspace manager.
	wsMgr, err := filesystem.NewWorkspaceManager(cfg.Workspace.Path)
	if err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("workspace manager: %w", err)
	}
	eng.SetWorkspaceManager(wsMgr)

	// 12. Create the queue manager and wire it to the engine.
	queueMgr := queue.NewManager(queueRepo, instanceRepo, cfg, eng)
	eng.SetQueueManager(queueMgr)

	// 12-sched. Create the timer scheduler, wire it to the queue manager and engine.
	scheduler := queue.NewScheduler(queueMgr)
	queueMgr.SetScheduler(scheduler)
	eng.SetScheduler(scheduler)

	// 12-wh. Create the webhook registry, wire it to the queue manager and engine.
	webhookRegistry := adapthttp.NewWebhookRegistry()
	queueMgr.SetWebhookRegistry(webhookRegistry)
	eng.SetWebhookRegistry(webhookRegistry)

	// 12-exec. Wire timer_delay and webhook_wait executors now that all deps are
	// available. These executors are added after engine construction because they
	// depend on the scheduler and webhook registry, which in turn depend on the
	// queue manager, creating a circular dependency. This works because the
	// engine stores a reference to the executors map (not a copy), so mutations
	// here are visible to the engine at execution time.
	executors[workflow.NodeTypeTimerDelay] = engine.NewTimerDelayExecutor(
		instanceRepo, nodeExecRepo, scheduler, queueMgr,
	)
	executors[workflow.NodeTypeWebhookWait] = engine.NewWebhookWaitExecutor(
		instanceRepo, nodeExecRepo, webhookRegistry, scheduler, queueMgr,
	)

	// 12a. Create notification repo and wire it to the engine.
	notifRepo := adaptsqlite.NewNotificationRepo(db)
	eng.SetNotifications(notifRepo)

	// 12b. Create external notifier (logging-only) and wire it to the engine.
	logNotifier := adaptnotif.NewLogNotifier(logger)
	eng.SetExternalNotifier(logNotifier)

	// 12c. Register the interactive executor. Notification creation is handled
	// by the engine when it processes the IsWait result.
	executors[workflow.NodeTypeInteractive] = engine.NewInteractiveExecutor(
		instanceRepo, nodeExecRepo, llmProvider, fileReader, bus, queueMgr,
	)

	// 12d. Create the conversation handler for interactive sessions.
	conversationHandler := engine.NewConversationHandler(
		instanceRepo, nodeExecRepo, llmProvider, bus, queueMgr,
	)

	// 13. Set up OpenTelemetry tracing before crash recovery so that any spans
	// emitted during recovery are captured by the real provider.
	tracingShutdown, err := observability.SetupTracing(cfg.Tracing)
	if err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("setting up tracing: %w", err)
	}

	// 13a. Start the timer scheduler so recovery can register timers.
	schedulerCtx, schedulerCancel := context.WithCancel(context.Background())
	scheduler.Start(schedulerCtx)

	// 14. Recover any instances that were running when the process last crashed.
	logger.Info("recovering crashed instances")
	if err := eng.RecoverCrashedInstances(); err != nil {
		schedulerCancel()
		scheduler.Stop()
		if tracingShutdown != nil {
			_ = tracingShutdown(context.Background())
		}
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("recovering crashed instances: %w", err)
	}

	// 14a. Recover pending timers and webhook endpoints from the database.
	logger.Info("recovering pending timers")
	if err := queueMgr.RecoverTimers(context.Background()); err != nil {
		schedulerCancel()
		scheduler.Stop()
		if tracingShutdown != nil {
			_ = tracingShutdown(context.Background())
		}
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("recovering timers: %w", err)
	}
	logger.Info("recovering webhook endpoints")
	if err := queueMgr.RecoverWebhooks(context.Background()); err != nil {
		schedulerCancel()
		scheduler.Stop()
		if tracingShutdown != nil {
			_ = tracingShutdown(context.Background())
		}
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("recovering webhooks: %w", err)
	}

	return &domainStack{
		cfg:                 cfg,
		logger:              logger,
		logCloser:           logCloser,
		db:                  db,
		eng:                 eng,
		workflowRepo:        workflowRepo,
		instanceRepo:        instanceRepo,
		nodeExecRepo:        nodeExecRepo,
		snapshotRepo:        snapshotRepo,
		fileReader:          fileReader,
		bus:                 bus,
		queueMgr:            queueMgr,
		queueRepo:           queueRepo,
		scheduler:           scheduler,
		schedulerCancel:     schedulerCancel,
		webhookRegistry:     webhookRegistry,
		notifRepo:           notifRepo,
		conversationHandler: conversationHandler,
		tracingShutdown:     tracingShutdown,
	}, nil
}

func runServe(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	ds, err := initDomain(ctx)
	if err != nil {
		return err
	}
	defer ds.Close(context.Background())

	// 15. Create the MCP server (mounted at /mcp on the HTTP router) and
	// the full API router with embedded dashboard.
	mcpSrv := adaptmcp.NewServerWithDeps("arbitron", "0.1.0", ds.mcpDeps())
	adaptmcp.RegisterAllTools(mcpSrv)

	dashHandler := dashboard.Handler()
	notifHandler := adapthttp.NewNotificationHandler(ds.notifRepo)
	notifHandler.SetEventBus(ds.bus)
	webhookHandler := adapthttp.NewWebhookHandler(
		ds.webhookRegistry, ds.instanceRepo, ds.nodeExecRepo, ds.scheduler, ds.queueRepo, ds.queueMgr,
	)
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:             ds.eng,
		Workflows:           ds.workflowRepo,
		Instances:           ds.instanceRepo,
		NodeExecs:           ds.nodeExecRepo,
		Snapshots:           ds.snapshotRepo,
		FileReader:          ds.fileReader,
		EventBus:            ds.bus,
		Config:              ds.cfg,
		QueueManager:        ds.queueMgr,
		QueueRepo:           ds.queueRepo,
		DashboardHandler:    dashHandler,
		NotifHandler:        notifHandler,
		ConversationHandler: ds.conversationHandler,
		WebhookHandler:      webhookHandler,
		MCPHandler:          mcpSrv.HTTPHandler(),
	})

	// 16. Add request logging middleware (wraps the entire router).
	handler := adapthttp.RequestLoggingMiddleware(ds.logger)(router)

	addr := fmt.Sprintf("%s:%d", ds.cfg.Server.Host, ds.cfg.Server.Port)

	// Warn if binding to a non-loopback address. The API has no authentication,
	// so exposing it on the network is a security risk.
	if ds.cfg.Server.Host != "127.0.0.1" && ds.cfg.Server.Host != "localhost" && ds.cfg.Server.Host != "::1" {
		ds.logger.Warn("server is binding to a non-loopback address -- the API has no authentication and should not be exposed on the network",
			"host", ds.cfg.Server.Host)
	}

	srv := &http.Server{
		Addr:        addr,
		Handler:     handler,
		ReadTimeout: 10 * time.Second,
		// WriteTimeout is intentionally 0 (no timeout) because WebSocket
		// connections remain open for streaming and a fixed timeout would
		// kill long-lived connections. Per-request deadlines are handled at
		// the handler level instead.
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	// 17. Graceful shutdown on SIGINT/SIGTERM.
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, syscall.SIGINT, syscall.SIGTERM)

	// Optionally start MCP stdio server alongside the HTTP server.
	// HTTP MCP is always available at /mcp; --mcp-stdio adds the stdio transport.
	mcpCtx, mcpCancel := context.WithCancel(ctx)
	defer mcpCancel()

	mcpStdio, _ := cmd.Flags().GetBool("mcp-stdio")
	if mcpStdio {
		go func() {
			ds.logger.Info("MCP stdio server starting (alongside HTTP)")
			if err := mcpSrv.ServeStdio(mcpCtx); err != nil && mcpCtx.Err() == nil {
				ds.logger.Error("MCP stdio server error", "error", err)
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		ds.logger.Info("server starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("server error: %w", err)
		}
		close(errCh)
	}()

	select {
	case sig := <-shutdownCh:
		ds.logger.Info("shutdown signal received", "signal", sig)
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	// Cancel the MCP stdio context so it stops accepting new requests.
	mcpCancel()

	// 18. Shutdown with a 15-second deadline.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ds.logger.Info("shutting down server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}

	ds.logger.Info("server stopped gracefully")
	return nil
}

func runMCP(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	ds, err := initDomain(ctx)
	if err != nil {
		return err
	}
	defer ds.Close(context.Background())

	mcpSrv := adaptmcp.NewServerWithDeps("arbitron", "0.1.0", ds.mcpDeps())
	adaptmcp.RegisterAllTools(mcpSrv)

	ds.logger.Info("MCP stdio server starting")
	return mcpSrv.ServeStdio(ctx)
}
