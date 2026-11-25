package runner_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// storeSpy wraps the REAL P3 T2 checkpoint store over the real SQLite catalog.
// It only counts calls and, when asked, injects the RR-08 promisor miss or
// flags a bootstrap made after a job was claimed.
type storeSpy struct {
	runner.CheckpointStore
	t                         *testing.T
	mu                        sync.Mutex
	commits, stages, discards int
	forbidCommit, promisor    bool
}

var errPromisor = errors.Join(runner.ErrUnsupportedGit, runner.ErrPromisorObjectsMissing)

func (s *storeSpy) Commit(ctx context.Context, run uuid.UUID, dir string) (runner.CheckpointRef, runner.Manifest, error) {
	s.mu.Lock()
	s.commits++
	forbid, promisor := s.forbidCommit, s.promisor
	s.mu.Unlock()
	if forbid {
		s.t.Errorf("bootstrap Commit for run %s after a job was reserved and claimed", run)
		return runner.CheckpointRef{}, runner.Manifest{}, errors.New("storeSpy: bootstrap after claim")
	}
	if promisor {
		return runner.CheckpointRef{}, runner.Manifest{}, errPromisor
	}
	return s.CheckpointStore.Commit(ctx, run, dir)
}

func (s *storeSpy) StageBuiltIn(ctx context.Context, run uuid.UUID, dir string, f runner.Fence, res engine.ResourceFenceRef) (runner.CheckpointRef, runner.Manifest, error) {
	s.mu.Lock()
	s.stages++
	promisor := s.promisor
	s.mu.Unlock()
	if promisor {
		return runner.CheckpointRef{}, runner.Manifest{}, errPromisor
	}
	return s.CheckpointStore.StageBuiltIn(ctx, run, dir, f, res)
}

func (s *storeSpy) Discard(ctx context.Context, staged runner.StagedCheckpoint) error {
	s.mu.Lock()
	s.discards++
	s.mu.Unlock()
	return s.CheckpointStore.Discard(ctx, staged)
}

func (s *storeSpy) set(fn func(*storeSpy)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func (s *storeSpy) counts() (commits, stages, discards int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits, s.stages, s.discards
}

// wsTokens records every issued scope (the real issuer is P3 T7).
type wsTokens struct {
	mu     sync.Mutex
	scopes []runner.TransferScope
}

func (t *wsTokens) Issue(_ context.Context, sc runner.TransferScope, ttl time.Duration) (runner.TransferToken, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scopes = append(t.scopes, sc)
	return runner.TransferToken{Token: fmt.Sprintf("art_%s_%d", sc.Direction, len(t.scopes)), MaintenanceOperationID: sc.MaintenanceOperationID,
		JobID: sc.Fence.JobID, Generation: sc.Fence.Generation, RunnerID: sc.Fence.RunnerID, RunID: sc.RunID, Direction: sc.Direction,
		BaseRef: sc.BaseRef, Resource: sc.Resource, BundleHash: sc.BundleHash, ExpiresAt: time.Now().Add(ttl)}, nil
}

func (t *wsTokens) Validate(context.Context, string, runner.RunnerID) (runner.TransferToken, error) {
	return runner.TransferToken{}, errors.New("wsTokens: validation belongs to P3 T7")
}

func (t *wsTokens) issued() []runner.TransferScope {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]runner.TransferScope(nil), t.scopes...)
}

// wsSizer is the server-local sizer (engine.WorkspaceManager in production).
type wsSizer struct {
	mu    sync.Mutex
	bytes map[uuid.UUID]int64
	err   error
}

func (s *wsSizer) InstanceDirStats(id uuid.UUID) (int64, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, 0, s.err
	}
	return s.bytes[id], 1, nil
}

// wsFixture is a real Phase 3 workspace stack on one file-backed SQLite DB:
// ledger/registry/events (hubtest), workspace homes, resource leases and the
// P3 T2 checkpoint store + catalog.
type wsFixture struct {
	pool     *hubtest.Pool
	homes    *adaptsqlite.WorkspaceHomeRepo
	leases   *adaptsqlite.ResourceLeaseRepo
	ckptDir  string
	store    *storeSpy
	tokens   *wsTokens
	sizes    *wsSizer
	bus      *recordingBus
	status   *runner.ExecutionStatusProjector
	baseline *runner.BaselineInitializer
	sync     *runner.WorkspaceSync
	root     string
	run      uuid.UUID
}

func newWSFixture(t *testing.T) *wsFixture {
	t.Helper()
	p := hubtest.NewPool(t)
	p.SeedBuiltIn(t, 4, hubtest.BuiltInAnthropic())
	f := &wsFixture{
		pool: p, homes: adaptsqlite.NewWorkspaceHomeRepo(p.DB), leases: adaptsqlite.NewResourceLeaseRepo(p.DB, "boot-"+uuid.NewString()),
		ckptDir: t.TempDir(), tokens: &wsTokens{}, sizes: &wsSizer{bytes: map[uuid.UUID]int64{}}, bus: &recordingBus{}, root: t.TempDir(),
	}
	f.store = &storeSpy{t: t, CheckpointStore: f.openStore()}
	f.status = runner.NewExecutionStatusProjector(p.Ledger)
	f.status.AttachBus(f.bus)
	f.run = p.SeedRun(t)
	require.NoError(t, os.MkdirAll(f.dir(), 0o755))
	f.baseline = runner.NewWorkspaceBaselineInitializer(f.baselineDeps())
	f.sync = runner.NewWorkspaceSync(f.syncDeps())
	return f
}

// openStore builds a fresh P3 T2 store over the SAME archive dir and DB, which
// is exactly what a server restart does.
func (f *wsFixture) openStore() *checkpoint.Store {
	return checkpoint.NewStore(f.ckptDir, checkpoint.WithCatalog(adaptsqlite.NewCheckpointCatalogRepo(f.pool.DB)))
}

// baselineDeps is the ONLY place the fixture builds BaselineDeps (P3 T16
// Cycle 5 adds Local: here).
func (f *wsFixture) baselineDeps() runner.BaselineDeps {
	return runner.BaselineDeps{Checkpoints: f.store, Ledger: f.pool.Ledger, Leases: f.leases, Homes: f.homes}
}

func (f *wsFixture) dir() string { return filepath.Join(f.root, "instances", f.run.String()) }

func (f *wsFixture) write(t *testing.T, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.dir(), name), []byte(body), 0o644))
}

func readIn(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	return string(b)
}

func (f *wsFixture) read(t *testing.T, name string) string { return readIn(t, f.dir(), name) }

func (f *wsFixture) req() runner.ExecRequirements {
	return runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpProcess,
		Affinity: runner.AffinityWorkspace, RunID: f.run, WorkspaceBytes: runner.WorkspaceBytesUnknown}
}

func (f *wsFixture) job() *runner.StepJob {
	return &runner.StepJob{Op: runner.OpProcess, Process: &engine.ProcessSpec{Kind: engine.ProcessScript, Command: "/bin/sh",
		Args: []string{"edit.sh"}, WorkDir: f.dir()}}
}

func (f *wsFixture) addRemote(t *testing.T, id runner.RunnerID, limit int) {
	t.Helper()
	f.pool.SeedRunner(t, id, limit)
	require.NoError(t, f.pool.Registry.SetLiveness(context.Background(), id, runner.LivenessOnline, f.pool.Clock.Now()))
}

func (f *wsFixture) offline(t *testing.T, id runner.RunnerID) {
	t.Helper()
	require.NoError(t, f.pool.Registry.SetLiveness(context.Background(), id, runner.LivenessOffline, f.pool.Clock.Now()))
}

// reserve records a NEW operation intent (new invocation) for the run and
// reserves it on `on`, exactly as the dispatcher does before BeginAttempt.
func (f *wsFixture) reserve(t *testing.T, on runner.RunnerID) runner.Assignment {
	t.Helper()
	key := runner.JobKey{RunID: f.run, InvocationID: uuid.New(), OperationID: "edit", Attempt: 1}
	job, err := f.pool.Ledger.EnsureIntent(context.Background(), key, "edit", f.req())
	require.NoError(t, err)
	return f.reserveJob(t, job.ID, on)
}

// reserveJob re-places an existing queued job (a requeued operation keeps its id).
func (f *wsFixture) reserveJob(t *testing.T, jobID uuid.UUID, on runner.RunnerID) runner.Assignment {
	t.Helper()
	ctx := context.Background()
	r, _, err := f.pool.Registry.GetRunner(ctx, on)
	require.NoError(t, err)
	job, err := f.pool.Ledger.Reserve(ctx, jobID, runner.Decision{Placed: true, Runner: on, Reason: "test"}, r.StepLimit, f.pool.Clock.Now().Add(2*time.Minute))
	require.NoError(t, err)
	require.NoError(t, f.pool.Ledger.MarkRunning(ctx, job.Fence()))
	job.State = runner.JobRunning
	return runner.Assignment{Job: job, Fence: job.Fence()}
}

func (f *wsFixture) seedBaseline(t *testing.T, files map[string]string) runner.CheckpointRef {
	t.Helper()
	for name, body := range files {
		f.write(t, name, body)
	}
	require.NoError(t, f.baseline.EnsureBaseline(context.Background(), f.run, f.dir()))
	return f.latest(t)
}

func (f *wsFixture) latest(t *testing.T) runner.CheckpointRef {
	t.Helper()
	ref, ok, err := f.store.Latest(context.Background(), f.run)
	require.NoError(t, err)
	require.True(t, ok, "the run has a committed checkpoint")
	return ref
}

func (f *wsFixture) hasLatest(t *testing.T) bool {
	t.Helper()
	_, ok, err := f.store.Latest(context.Background(), f.run)
	require.NoError(t, err)
	return ok
}

func (f *wsFixture) home(t *testing.T) runner.Home {
	t.Helper()
	h, err := f.homes.GetHome(context.Background(), f.run)
	require.NoError(t, err)
	return h
}

func (f *wsFixture) active(t *testing.T) (runner.LeaseRecord, bool) {
	t.Helper()
	rec, ok, err := f.leases.Active(context.Background(), "run:"+f.run.String())
	require.NoError(t, err)
	return rec, ok
}

// restoreTo materializes ref into a fresh directory ("elsewhere").
func (f *wsFixture) restoreTo(t *testing.T, ref runner.CheckpointRef) string {
	t.Helper()
	dst := t.TempDir()
	require.NoError(t, f.openStore().Restore(context.Background(), ref, dst))
	return dst
}

// preparedRow reads the catalog row for (run, version) — the real sink.
func (f *wsFixture) preparedRow(t *testing.T, version int64) (state, jobID string, jobGen int64, token string, found bool) {
	t.Helper()
	err := f.pool.DB.QueryRowContext(context.Background(), `SELECT state, COALESCE(job_id,''), COALESCE(job_generation,0), resource_token
		FROM workspace_checkpoints WHERE run_id = ? AND version = ?`, f.run.String(), version).Scan(&state, &jobID, &jobGen, &token)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, "", false
	}
	require.NoError(t, err)
	return state, jobID, jobGen, token, true
}

// syncDeps is the ONLY place the fixture builds SyncDeps (P3 T16 Cycle 5 adds Local: here).
func (f *wsFixture) syncDeps() runner.SyncDeps {
	return runner.SyncDeps{Checkpoints: f.store, Homes: f.homes, Leases: f.leases, Ledger: f.pool.Ledger, Tokens: f.tokens,
		Events: f.pool.Events, Status: f.status, Sizes: f.sizes, Clock: f.pool.Clock.Now}
}

func (f *wsFixture) begin(t *testing.T, a runner.Assignment, dec runner.Decision) runner.WorkspaceAttempt {
	t.Helper()
	att, err := f.sync.BeginAttempt(context.Background(), a, f.req(), dec, f.job())
	require.NoError(t, err)
	return att
}

func stay(on runner.RunnerID) runner.Decision {
	return runner.Decision{Placed: true, Runner: on, Tier: runner.TierStay, Reason: "stay: home=" + string(on)}
}

func moveTo(on runner.RunnerID, tier string) runner.Decision {
	return runner.Decision{Placed: true, Runner: on, Tier: tier, Reason: tier + ": test move"}
}

// stage plays P3 T9 + P3 T7: the remote copy starts at att.Base, gets the
// edits, and its delta is staged on the server; staged.Resource is assigned
// from the (trusted) transfer session, never from archive bytes.
func (f *wsFixture) stage(ctx context.Context, att runner.WorkspaceAttempt, files map[string]string) (runner.StagedCheckpoint, error) {
	dir, err := os.MkdirTemp("", "remote-copy-")
	if err != nil {
		return runner.StagedCheckpoint{}, err
	}
	defer os.RemoveAll(dir)
	if err := f.openStore().Restore(ctx, att.Base, dir); err != nil {
		return runner.StagedCheckpoint{}, err
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return runner.StagedCheckpoint{}, err
		}
	}
	baseM, err := f.store.Manifest(ctx, att.Base)
	if err != nil {
		return runner.StagedCheckpoint{}, err
	}
	entries, err := checkpoint.ScanEntries(ctx, dir)
	if err != nil {
		return runner.StagedCheckpoint{}, err
	}
	changed, deleted := runner.DiffManifests(baseM.Entries, entries)
	paths := make([]string, 0, len(changed))
	for _, e := range changed {
		paths = append(paths, e.Path)
	}
	var buf bytes.Buffer
	if err := checkpoint.WriteDelta(&buf, dir, paths, runner.DeltaControl{Deleted: deleted, Expected: entries}); err != nil {
		return runner.StagedCheckpoint{}, err
	}
	staged, err := f.store.StageDelta(ctx, att.RunID, att.Base, &buf)
	if err != nil {
		return runner.StagedCheckpoint{}, err
	}
	staged.Resource = att.Resource
	return staged, nil
}

func (f *wsFixture) mustStage(t *testing.T, att runner.WorkspaceAttempt, files map[string]string) runner.StagedCheckpoint {
	t.Helper()
	staged, err := f.stage(context.Background(), att, files)
	require.NoError(t, err)
	return staged
}

// restorePreparedTo extracts the exact PREPARED candidate ref into a fresh
// directory for inspection only (authorized fixture, unstick 2026-11-24). It
// reads that candidate's archive through its real catalog row and never
// promotes it: Store.Restore stays committed-only (P3 T2).
func (f *wsFixture) restorePreparedTo(t *testing.T, ref runner.CheckpointRef) string {
	t.Helper()
	var archive, state string
	require.NoError(t, f.pool.DB.QueryRowContext(context.Background(),
		`SELECT archive_path, state FROM workspace_checkpoints WHERE run_id = ? AND version = ? AND manifest_hash = ?`,
		ref.RunID.String(), ref.Version, ref.ManifestHash).Scan(&archive, &state))
	require.Equal(t, "prepared", state, "restorePreparedTo inspects prepared candidates only; committed refs use restoreTo")
	fh, err := os.Open(archive)
	require.NoError(t, err)
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	dst := t.TempDir()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		name, err := runner.CleanRelPath(strings.TrimSuffix(hdr.Name, "/"))
		require.NoError(t, err)
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			require.NoError(t, os.MkdirAll(target, 0o755))
		case tar.TypeReg:
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
			b, err := io.ReadAll(tr)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(target, b, 0o644))
		}
	}
	return dst
}
