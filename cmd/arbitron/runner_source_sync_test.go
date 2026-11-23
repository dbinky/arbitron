package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	runnerinventory "github.com/arbitron/arbitron/adapter/runner/inventory"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rsRegistry(t *testing.T) *adaptsqlite.RunnerRegistryRepo {
	t.Helper()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "rs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	return adaptsqlite.NewRunnerRegistryRepo(db)
}

type rsConns struct {
	conns []provider.Connection
	err   error
}

func (c rsConns) List(context.Context) ([]provider.Connection, error) { return c.conns, c.err }

func rsSourceIDs(t *testing.T, reg runner.Registry) []runner.SourceID {
	t.Helper()
	_, srcs, err := reg.GetRunner(context.Background(), runner.BuiltInRunnerID)
	require.NoError(t, err)
	ids := make([]runner.SourceID, 0, len(srcs))
	for _, s := range srcs {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestBuiltinSourceSync_RegistersRunnerAndSources(t *testing.T) {
	ctx := context.Background()
	reg := rsRegistry(t)
	var got runnerinventory.Config
	sync := newBuiltinSourceSync(builtinSourceSyncDeps{
		Registry:      reg,
		Inventory:     runnerinventory.Config{ClaudeBinary: "/opt/claude", Connections: rsConns{conns: []provider.Connection{{Name: "acme", Provider: "openrouter"}}}},
		StepLimit:     8,
		RunnerVersion: "1.2.3",
		WorkspaceRoot: "/srv/ws",
		Logger:        slog.Default(),
		Detect: func(ctx context.Context, cfg runnerinventory.Config) (runner.InventoryInput, error) {
			got = cfg
			conns, err := cfg.Connections.List(ctx)
			return runner.InventoryInput{ClaudeCLI: &runner.HarnessPresence{Path: "/opt/claude", Version: "2.1.330", Supported: true}, Connections: conns}, err
		},
	})
	require.NoError(t, sync.Sync(ctx))
	assert.Equal(t, "/opt/claude", got.ClaudeBinary, "detection reads the configured binary")

	rn, _, err := reg.GetRunner(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.True(t, rn.BuiltIn)
	assert.Equal(t, 8, rn.StepLimit)
	assert.Equal(t, "1.2.3", rn.RunnerVersion)
	assert.Equal(t, map[string]string{"claude-code": "2.1.330"}, rn.HarnessVersions)
	assert.Equal(t, runner.LivenessOnline, rn.Liveness)
	ids := rsSourceIDs(t, reg)
	assert.Contains(t, ids, runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, ""))
	assert.Contains(t, ids, runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredServerConnection, "acme"))
	assert.Contains(t, ids, runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredNone, ""))

	twelve := 12
	_, err = reg.UpdateConfig(ctx, runner.BuiltInRunnerID, runner.RunnerConfigPatch{StepLimit: &twelve})
	require.NoError(t, err)
	require.NoError(t, sync.Sync(ctx))
	rn, _, err = reg.GetRunner(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.Equal(t, 12, rn.StepLimit, "a resync never overwrites an operator's step limit")
}

func TestBuiltinSourceSync_DetectFailureFallsBackToConnections(t *testing.T) {
	reg := rsRegistry(t)
	sync := newBuiltinSourceSync(builtinSourceSyncDeps{
		Registry:  reg,
		Inventory: runnerinventory.Config{Connections: rsConns{conns: []provider.Connection{{Name: "acme", Provider: "openrouter"}}}},
		StepLimit: 8, Logger: slog.Default(),
		Detect: func(context.Context, runnerinventory.Config) (runner.InventoryInput, error) {
			return runner.InventoryInput{}, errors.New("codex --version: exit 2")
		},
	})
	require.NoError(t, sync.Sync(context.Background()))
	ids := rsSourceIDs(t, reg)
	assert.Contains(t, ids, runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredServerConnection, "acme"))
	assert.Contains(t, ids, runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, ""))
}

func TestBuiltinSourceSync_ConnectionListFailureFailsSync(t *testing.T) {
	sync := newBuiltinSourceSync(builtinSourceSyncDeps{
		Registry:  rsRegistry(t),
		Inventory: runnerinventory.Config{Connections: rsConns{err: errors.New("db locked")}},
		StepLimit: 8, Logger: slog.Default(),
		Detect: func(context.Context, runnerinventory.Config) (runner.InventoryInput, error) {
			return runner.InventoryInput{}, errors.New("detect failed")
		},
	})
	assert.ErrorContains(t, sync.Sync(context.Background()), "db locked")
	assert.Panics(t, func() { newBuiltinSourceSync(builtinSourceSyncDeps{Logger: slog.Default()}) })
}

type rsStore struct {
	provider.ConnectionStore
	writes []string
	err    error
}

func (s *rsStore) Create(_ context.Context, c provider.Connection, _ string) error {
	if s.err != nil {
		return s.err
	}
	s.writes = append(s.writes, "create "+c.Name)
	return nil
}
func (s *rsStore) Update(_ context.Context, c provider.Connection, _ *string) error {
	if s.err != nil {
		return s.err
	}
	s.writes = append(s.writes, "update "+c.Name)
	return nil
}
func (s *rsStore) Delete(_ context.Context, name string) error {
	if s.err != nil {
		return s.err
	}
	s.writes = append(s.writes, "delete "+name)
	return nil
}

func TestSyncingConnStore_ResyncsAfterEveryWrite(t *testing.T) {
	ctx := context.Background()
	inner := &rsStore{}
	syncs := 0
	store := newSyncingConnStore(inner, func(context.Context) error { syncs++; return nil })
	require.NoError(t, store.Create(ctx, provider.Connection{Name: "a"}, "k"))
	require.NoError(t, store.Update(ctx, provider.Connection{Name: "a"}, nil))
	require.NoError(t, store.Delete(ctx, "a"))
	assert.Equal(t, []string{"create a", "update a", "delete a"}, inner.writes)
	assert.Equal(t, 3, syncs)

	inner.err = provider.ErrConnectionNotFound
	assert.ErrorIs(t, store.Delete(ctx, "a"), provider.ErrConnectionNotFound)
	assert.Equal(t, 3, syncs, "a failed write does not resync")

	inner.err = nil
	failing := newSyncingConnStore(inner, func(context.Context) error { return errors.New("registry down") })
	assert.ErrorContains(t, failing.Create(ctx, provider.Connection{Name: "b"}, "k"), "registry down")
	assert.Panics(t, func() { newSyncingConnStore(inner, nil) })
}
