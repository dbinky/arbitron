package main

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	runnerinventory "github.com/arbitron/arbitron/adapter/runner/inventory"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

// builtinSourceSyncDeps configures the built-in runner's registration.
type builtinSourceSyncDeps struct {
	Registry  runner.Registry
	Inventory runnerinventory.Config
	// StepLimit is cfg.Runners.DefaultStepLimit. It applies when the runner row
	// is first created; UpsertRunner keeps an operator's later edits.
	StepLimit     int
	RunnerVersion string
	WorkspaceRoot string
	Logger        *slog.Logger
	Clock         func() time.Time
	// Detect reads the local inventory. Production leaves it nil, which selects
	// runnerinventory.DetectLocal; unit tests inject a fixed inventory.
	Detect func(context.Context, runnerinventory.Config) (runner.InventoryInput, error)
}

// builtinSourceSync registers the built-in runner and replaces its sources from
// harness presence plus the server's connections. It runs at boot and after
// every connection write, so a new connection is placeable immediately.
type builtinSourceSync struct {
	mu sync.Mutex
	d  builtinSourceSyncDeps
}

func newBuiltinSourceSync(d builtinSourceSyncDeps) *builtinSourceSync {
	if d.Registry == nil || d.Inventory.Connections == nil || d.Logger == nil {
		panic("arbitron: the built-in source sync requires a registry, a connection lister and a logger")
	}
	if d.Detect == nil {
		d.Detect = runnerinventory.DetectLocal
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &builtinSourceSync{d: d}
}

// Sync (re)registers the runner row and its sources. A harness detection
// failure degrades to connections only (subscription sources are always
// registered, annotated harness_not_found); registry or connection-list
// failures are returned.
func (s *builtinSourceSync) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, err := s.d.Detect(ctx, s.d.Inventory)
	if err != nil {
		s.d.Logger.Error("runner inventory detection failed; registering built-in sources from connections only", "error", err)
		conns, lerr := s.d.Inventory.Connections.List(ctx)
		if lerr != nil {
			return fmt.Errorf("listing connections for the built-in runner: %w", lerr)
		}
		in.Connections = conns // retain successfully detected harness facts from the partial inventory
	}
	harness := map[string]string{}
	if in.ClaudeCLI != nil && in.ClaudeCLI.Version != "" {
		harness["claude-code"] = in.ClaudeCLI.Version
	}
	if in.Codex != nil && in.Codex.Version != "" {
		harness["codex"] = in.Codex.Version
	}
	now := s.d.Clock().UTC()
	if err := s.d.Registry.UpsertRunner(ctx, runner.Runner{
		ID: runner.BuiltInRunnerID, Name: "server", BuiltIn: true, StepLimit: s.d.StepLimit,
		Compat:   runner.Compatibility{State: runner.CompatCompatible},
		Liveness: runner.LivenessOnline, LastContact: &now,
		OS: runtime.GOOS, RunnerVersion: s.d.RunnerVersion, HarnessVersions: harness,
		WorkspaceRoot: s.d.WorkspaceRoot,
	}); err != nil {
		return fmt.Errorf("registering the built-in runner: %w", err)
	}
	if err := s.d.Registry.ReplaceSources(ctx, runner.BuiltInRunnerID, runner.BuiltInSources(in)); err != nil {
		return fmt.Errorf("registering the built-in runner's sources: %w", err)
	}
	return nil
}

// syncingConnStore is the store behind the connection admin service: after a
// successful create/update/delete it refreshes the built-in runner's sources.
// Reads pass straight through.
type syncingConnStore struct {
	provider.ConnectionStore
	sync func(context.Context) error
}

func newSyncingConnStore(store provider.ConnectionStore, sync func(context.Context) error) syncingConnStore {
	if store == nil || sync == nil {
		panic("arbitron: the syncing connection store requires a store and a sync function")
	}
	return syncingConnStore{ConnectionStore: store, sync: sync}
}

func (s syncingConnStore) Create(ctx context.Context, c provider.Connection, apiKey string) error {
	if err := s.ConnectionStore.Create(ctx, c, apiKey); err != nil {
		return err
	}
	return s.resync(ctx)
}

func (s syncingConnStore) Update(ctx context.Context, c provider.Connection, apiKey *string) error {
	if err := s.ConnectionStore.Update(ctx, c, apiKey); err != nil {
		return err
	}
	return s.resync(ctx)
}

func (s syncingConnStore) Delete(ctx context.Context, name string) error {
	if err := s.ConnectionStore.Delete(ctx, name); err != nil {
		return err
	}
	return s.resync(ctx)
}

func (s syncingConnStore) resync(ctx context.Context) error {
	if err := s.sync(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("connection change saved, but the built-in runner's sources were not refreshed (repeat the change to retry): %w", err)
	}
	return nil
}
