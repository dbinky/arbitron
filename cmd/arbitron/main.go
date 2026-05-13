// Package main is the entry point for the Arbitron CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/arbitron/arbitron/adapter/anthropic"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/adapter/dashboard"
	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/adapter/filesystem"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/adapter/observability"
	"github.com/arbitron/arbitron/adapter/postgres"
	"github.com/arbitron/arbitron/adapter/process"
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

// redactDatabaseURL replaces the password in a database URL with "***".
// Returns the original string on parse error so startup is never blocked.
// Uses manual string reconstruction instead of url.String() to prevent
// url.String() from percent-encoding "***" to "%2A%2A%2A" in log output.
func redactDatabaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	_, hasPassword := u.User.Password()
	if !hasPassword {
		return raw
	}
	username := u.User.Username()
	u.User = nil
	// Reconstruct with literal *** by prepending user:***@ after the scheme.
	base := u.String()
	scheme := u.Scheme + "://"
	after := strings.TrimPrefix(base, scheme)
	return scheme + username + ":***@" + after
}

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

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start the MCP stdio server",
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
	pool                *pgxpool.Pool
	eng                 *engine.Engine
	workflowRepo        workflow.WorkflowRepository
	instanceRepo        instance.InstanceRepository
	nodeExecRepo        instance.NodeExecutionRepository
	snapshotRepo        instance.VariableSnapshotRepository
	fileReader          engine.FileReader
	bus                 engine.EventBus
	queueMgr            *queue.Manager
	queueRepo           instance.QueueRepository
	notifRepo           notification.Repository
	conversationHandler engine.ConversationHandler
	tracingShutdown     func(ctx context.Context) error
}

// Close tears down the domain stack in the correct order: engine first, then
// tracing flush, then the database pool, and finally the log sink.
func (ds *domainStack) Close(ctx context.Context) {
	ds.eng.Shutdown()
	if ds.tracingShutdown != nil {
		if err := ds.tracingShutdown(ctx); err != nil {
			ds.logger.Error("tracing shutdown error", "error", err)
		}
	}
	ds.pool.Close()
	ds.logCloser.Close()
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
func initDomain(ctx context.Context) (*domainStack, error) {
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

	// 3. Connect to Postgres.
	logger.Info("connecting to database", "url", redactDatabaseURL(cfg.Database.URL))
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		logCloser.Close()
		return nil, fmt.Errorf("creating database pool: %w", err)
	}

	// Ping database to verify connectivity.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		logCloser.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	logger.Info("database connection established")

	// 4. Run migrations.
	logger.Info("running database migrations")
	if err := postgres.RunMigrations(cfg.Database.URL); err != nil {
		pool.Close()
		logCloser.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	logger.Info("database migrations complete")

	// 5. Initialize adapters.
	instanceRepo := postgres.NewInstanceRepo(pool)
	nodeExecRepo := postgres.NewNodeExecutionRepo(pool)
	snapshotRepo := postgres.NewVariableSnapshotRepo(pool)
	userRepo := postgres.NewUserRepo(pool)

	// 6. Create filesystem-based repos.
	workflowRepo := filesystem.NewWorkflowRepo(cfg.Workspace.Path)
	fileReader := filesystem.NewFileReader(cfg.Workspace.Path)

	// 7. Create Anthropic LLM provider.
	llmProvider := anthropic.New(cfg.LLM.APIKey)

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
	queueRepo := postgres.NewQueueRepo(pool)

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

	// 12. Create the queue manager and wire it to the engine.
	queueMgr := queue.NewManager(queueRepo, instanceRepo, cfg, eng)
	eng.SetQueueManager(queueMgr)

	// 12a. Create notification repo and wire it to the engine.
	notifRepo := postgres.NewNotificationRepo(pool)
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
		pool.Close()
		logCloser.Close()
		return nil, fmt.Errorf("setting up tracing: %w", err)
	}

	// 14. Recover any instances that were running when the process last crashed.
	logger.Info("recovering crashed instances")
	if err := eng.RecoverCrashedInstances(); err != nil {
		if tracingShutdown != nil {
			_ = tracingShutdown(ctx)
		}
		pool.Close()
		logCloser.Close()
		return nil, fmt.Errorf("recovering crashed instances: %w", err)
	}

	return &domainStack{
		cfg:                 cfg,
		logger:              logger,
		logCloser:           logCloser,
		pool:                pool,
		eng:                 eng,
		workflowRepo:        workflowRepo,
		instanceRepo:        instanceRepo,
		nodeExecRepo:        nodeExecRepo,
		snapshotRepo:        snapshotRepo,
		fileReader:          fileReader,
		bus:                 bus,
		queueMgr:            queueMgr,
		queueRepo:           queueRepo,
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

	// 15. Create the MCP server and full API router with embedded dashboard.
	mcpSrv := adaptmcp.NewServerWithDeps("arbitron", "0.1.0", ds.mcpDeps())
	adaptmcp.RegisterAllTools(mcpSrv)

	dashHandler := dashboard.Handler()
	notifHandler := adapthttp.NewNotificationHandler(ds.notifRepo)
	notifHandler.SetEventBus(ds.bus)
	router := adapthttp.NewRouter(
		ds.eng,
		ds.workflowRepo,
		ds.instanceRepo,
		ds.nodeExecRepo,
		ds.snapshotRepo,
		ds.fileReader,
		ds.bus,
		ds.cfg,
		ds.queueMgr,
		ds.queueRepo,
		dashHandler,
		notifHandler,
		ds.conversationHandler,
		mcpSrv.HTTPHandler(),
	)

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
	// The mcpSrv is already created above and its HTTP handler is mounted
	// at /mcp on the router; this only adds the stdio transport.
	// Uses a cancellable context derived from the shutdown signal so the
	// stdio server stops accepting requests during graceful shutdown.
	mcpCtx, mcpCancel := context.WithCancel(ctx)
	defer mcpCancel()

	mcpEnabled, _ := cmd.Flags().GetBool("mcp")
	if mcpEnabled {
		go func() {
			ds.logger.Info("MCP stdio server starting (alongside HTTP)")
			if err := mcpSrv.ServeStdio(mcpCtx); err != nil && mcpCtx.Err() == nil {
				ds.logger.Error("MCP server error", "error", err)
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

	// Wait for in-flight workflow goroutines to finish before closing the pool.
	ds.logger.Info("waiting for in-flight workflows to complete")

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
