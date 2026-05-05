// Package main is the entry point for the Arbitron CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/arbitron/arbitron/adapter/anthropic"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/adapter/filesystem"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/adapter/observability"
	"github.com/arbitron/arbitron/adapter/postgres"
	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/engine"
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

// redactDatabaseURL replaces the password in a database URL with "***".
// Returns the original string on parse error so startup is never blocked.
func redactDatabaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	return u.String()
}

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Arbitron server",
		Long:  "Start the Arbitron HTTP server, connect to Postgres, and run migrations.",
		RunE:  runServe,
	}
}

func runServe(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

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

	// 3. Connect to Postgres.
	logger.Info("connecting to database", "url", redactDatabaseURL(cfg.Database.URL))
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("creating database pool: %w", err)
	}
	defer pool.Close()

	// Ping database to verify connectivity.
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("pinging database: %w", err)
	}
	logger.Info("database connection established")

	// 4. Run migrations.
	logger.Info("running database migrations")
	if err := postgres.RunMigrations(cfg.Database.URL); err != nil {
		return fmt.Errorf("running migrations: %w", err)
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

	// 10. Create the workflow engine.
	eng := engine.NewEngine(
		workflowRepo,
		instanceRepo,
		nodeExecRepo,
		snapshotRepo,
		userRepo,
		executors,
		bus,
	)

	// 11. Set up OpenTelemetry tracing before crash recovery so that any spans
	// emitted during recovery are captured by the real provider.
	tracingShutdown, err := observability.SetupTracing(cfg.Tracing)
	if err != nil {
		return fmt.Errorf("setting up tracing: %w", err)
	}

	// 12. Recover any instances that were running when the process last crashed.
	logger.Info("recovering crashed instances")
	if err := eng.RecoverCrashedInstances(); err != nil {
		return fmt.Errorf("recovering crashed instances: %w", err)
	}

	// 13. Create the full API router.
	router := adapthttp.NewRouter(
		eng,
		workflowRepo,
		instanceRepo,
		nodeExecRepo,
		snapshotRepo,
		fileReader,
		bus,
		cfg,
	)

	// 14. Add request logging middleware (wraps the entire router).
	handler := adapthttp.RequestLoggingMiddleware(logger)(router)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
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

	// 15. Graceful shutdown on SIGINT/SIGTERM.
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

	// 16. Shutdown with a 15-second deadline.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	logger.Info("shutting down server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}

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
