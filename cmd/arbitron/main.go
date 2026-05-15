// Package main is the entry point for the Arbitron CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/arbitron/arbitron/adapter/anthropic"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/adapter/dashboard"
	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/adapter/filesystem"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/adapter/observability"
	"github.com/arbitron/arbitron/adapter/process"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
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

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Arbitron server",
		Long:  "Start the Arbitron HTTP server, open the SQLite database, and run migrations.",
		RunE:  runServe,
	}
}

func runServe(_ *cobra.Command, _ []string) error {
	// 1. Load configuration.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}
	configPath := filepath.Join(homeDir, ".arbitron", "config.yaml")

	slog.Info("loading configuration", "path", configPath)
	cfg, err := configadapter.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// 2. Set up structured logging.
	logger, logCloser, err := observability.SetupLogging(cfg.Logging)
	if err != nil {
		return fmt.Errorf("setting up logging: %w", err)
	}
	defer logCloser.Close()

	// 3. Open SQLite database.
	logger.Info("opening database", "path", cfg.Database.Path)
	db, err := adaptsqlite.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()
	logger.Info("database connection established")

	// 4. Run migrations.
	logger.Info("running database migrations")
	if err := adaptsqlite.RunMigrations(db); err != nil {
		return fmt.Errorf("running migrations: %w", err)
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

	// 12-exec. Wire timer_delay and webhook_wait executors now that all deps are available.
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
	// conversationHandler is passed to the HTTP layer for interactive WebSocket messaging.

	// 13. Set up OpenTelemetry tracing before crash recovery so that any spans
	// emitted during recovery are captured by the real provider.
	tracingShutdown, err := observability.SetupTracing(cfg.Tracing)
	if err != nil {
		return fmt.Errorf("setting up tracing: %w", err)
	}

	// 13a. Start the timer scheduler so recovery can register timers.
	schedulerCtx, schedulerCancel := context.WithCancel(context.Background())
	scheduler.Start(schedulerCtx)

	// 14. Recover any instances that were running when the process last crashed.
	logger.Info("recovering crashed instances")
	if err := eng.RecoverCrashedInstances(); err != nil {
		return fmt.Errorf("recovering crashed instances: %w", err)
	}

	// 14a. Recover pending timers and webhook endpoints from the database.
	logger.Info("recovering pending timers")
	if err := queueMgr.RecoverTimers(context.Background()); err != nil {
		return fmt.Errorf("recovering timers: %w", err)
	}
	logger.Info("recovering webhook endpoints")
	if err := queueMgr.RecoverWebhooks(context.Background()); err != nil {
		return fmt.Errorf("recovering webhooks: %w", err)
	}

	// 15. Create the full API router with embedded dashboard.
	dashHandler := dashboard.Handler()
	notifHandler := adapthttp.NewNotificationHandler(notifRepo)
	notifHandler.SetEventBus(bus)
	webhookHandler := adapthttp.NewWebhookHandler(
		webhookRegistry, instanceRepo, nodeExecRepo, scheduler, queueRepo, queueMgr,
	)
	router := adapthttp.NewRouter(
		eng,
		workflowRepo,
		instanceRepo,
		nodeExecRepo,
		snapshotRepo,
		fileReader,
		bus,
		cfg,
		queueMgr,
		queueRepo,
		dashHandler,
		notifHandler,
		conversationHandler,
		webhookHandler,
	)

	// 16. Add request logging middleware (wraps the entire router).
	handler := adapthttp.RequestLoggingMiddleware(logger)(router)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)

	// Warn if binding to a non-loopback address. The API has no authentication,
	// so exposing it on the network is a security risk.
	if cfg.Server.Host != "127.0.0.1" && cfg.Server.Host != "localhost" && cfg.Server.Host != "::1" {
		logger.Warn("server is binding to a non-loopback address -- the API has no authentication and should not be exposed on the network",
			"host", cfg.Server.Host)
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

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("server error: %w", err)
		}
		close(errCh)
	}()

	select {
	case sig := <-shutdownCh:
		logger.Info("shutdown signal received", "signal", sig)
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	// 18. Shutdown with a 15-second deadline.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	logger.Info("shutting down server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}

	// Stop the timer scheduler before waiting for in-flight workflows.
	schedulerCancel()
	scheduler.Stop()

	// Wait for in-flight workflow goroutines to finish before closing the pool.
	logger.Info("waiting for in-flight workflows to complete")
	eng.Shutdown()

	// Flush OTel spans.
	if err := tracingShutdown(shutdownCtx); err != nil {
		logger.Error("tracing shutdown error", "error", err)
	}

	logger.Info("server stopped gracefully")
	return nil
}
