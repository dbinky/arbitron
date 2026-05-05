# Phase 1 Task 5: CLI Bootstrap with Cobra

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create the `arbitron serve` CLI command using Cobra that wires all adapters to ports, runs migrations, performs crash recovery, and starts the HTTP server.

**Architecture:** The `cmd/arbitron/main.go` entrypoint imports both domain and adapter packages. It creates all adapter instances, injects them into domain services, and starts the server. This is the composition root of the hexagonal architecture.

**Tech Stack:** Go 1.23+, cobra, pgx v5

---

### Task 1: Cobra CLI Setup

**Files:**
- Create: `cmd/arbitron/main.go`

- [ ] **Step 1: Install cobra**

```bash
go get github.com/spf13/cobra
```

- [ ] **Step 2: Create the main entrypoint with serve command**

Create `cmd/arbitron/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	adaptercfg "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/adapter/postgres"
)

var version = "dev"

func main() {
	rootCmd := &cobra.Command{
		Use:   "arbitron",
		Short: "Arbitron workflow orchestration engine",
	}

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Arbitron API server and execution engine",
		RunE:  runServe,
	}

	rootCmd.AddCommand(serveCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runServe(cmd *cobra.Command, args []string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	configPath := filepath.Join(homeDir, ".arbitron", "config.yaml")

	cfg, err := adaptercfg.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// TODO: Set up slog (Phase 3 Task 5)
	slog.Info("starting arbitron", "version", version)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("pinging database: %w", err)
	}

	if err := postgres.RunMigrations(cfg.Database.URL); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	slog.Info("migrations complete")

	// These TODOs are resolved as each component is built:
	// Phase 1 Task 4: Initialize sqlc querier and Postgres adapters
	// Phase 2 Task 2: Initialize LLM provider (Anthropic adapter)
	// Phase 2 Task 3: Create engine, call engine.RecoverCrashedInstances()
	// Phase 3 Task 1: Create HTTP handlers and router
	// Phase 3 Task 3: Initialize OTel tracer, wire logging middleware

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	server := &http.Server{
		Addr: addr,
		// Handler: router, // TODO: wire router from Phase 3
	}

	slog.Info("arbitron started",
		"address", addr,
		"workspace", cfg.Workspace.Path,
		"database", cfg.Database.URL,
		"llm_provider", cfg.LLM.Provider,
	)

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	}()

	<-stop
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}

	// TODO: Flush OTel tracer
	slog.Info("shutdown complete")
	return nil
}
```

Note: This is a skeleton that compiles and runs but has TODO markers for components built in later tasks. Each subsequent task will fill in these TODOs as the components become available.

- [ ] **Step 3: Verify it compiles**

```bash
go build ./cmd/arbitron/
```

Expected: builds successfully

- [ ] **Step 4: Commit**

```bash
git add cmd/ go.mod go.sum
git commit -m "feat: cobra CLI with serve command skeleton"
```
