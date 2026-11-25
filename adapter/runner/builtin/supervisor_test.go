package builtin_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/adapter/runner/builtin"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// supFixture is a file-backed database (so a "restarted" supervisor can open a
// second handle) plus a scratch directory for worker side effects.
type supFixture struct {
	path  string
	db    *sql.DB
	store runner.BuiltInProcessStore
	dir   string
}

func newSupFixture(t *testing.T) *supFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "arbitron.db")
	db, err := sqlite.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))
	return &supFixture{path: path, db: db, store: sqlite.NewBuiltInProcessRepo(db), dir: dir}
}

// reopen is a second handle on the same file, as a restarted server opens it.
func (f *supFixture) reopen(t *testing.T) runner.BuiltInProcessStore {
	t.Helper()
	db, err := sqlite.Open(f.path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return sqlite.NewBuiltInProcessRepo(db)
}

// assign seeds a live built-in ledger job and returns a context stamped with
// its exact assignment.
func (f *supFixture) assign(t *testing.T) (context.Context, runner.Assignment) {
	t.Helper()
	id := uuid.New()
	_, err := f.db.Exec(`INSERT INTO runner_jobs (id, run_id, invocation_id, operation_id, attempt, node_id, op, group_kind, runner_id, generation, state, created_at, updated_at)
		VALUES (?, ?, ?, 'node', 1, 'n', 'process', 'none', 'builtin', 1, 'running', '2026-11-24T00:00:00Z', '2026-11-24T00:00:00Z')`,
		id.String(), uuid.NewString(), uuid.NewString())
	require.NoError(t, err)
	a := runner.Assignment{
		Job:   runner.Job{ID: id, RunnerID: runner.BuiltInRunnerID, Generation: 1, State: runner.JobRunning},
		Fence: runner.Fence{JobID: id, RunnerID: runner.BuiltInRunnerID, Generation: 1},
	}
	return runner.ContextWithAssignment(context.Background(), a), a
}

// fence is recovery's first step: the ledger no longer authorizes the job.
func (f *supFixture) fence(t *testing.T, a runner.Assignment) {
	t.Helper()
	_, err := f.db.Exec(`UPDATE runner_jobs SET state = 'failed' WHERE id = ?`, a.Job.ID.String())
	require.NoError(t, err)
}

// row reads the raw 044 row; state "" means no row.
func (f *supFixture) row(t *testing.T, fence runner.Fence) (pid int, marker, state string) {
	t.Helper()
	err := f.db.QueryRow(`SELECT guardian_pid, process_start_marker, state FROM builtin_processes WHERE job_id = ? AND generation = ?`,
		fence.JobID.String(), fence.Generation).Scan(&pid, &marker, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ""
	}
	require.NoError(t, err)
	return pid, marker, state
}

func (f *supFixture) file(name string) string { return filepath.Join(f.dir, name) }

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func readPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 1
	}, 5*time.Second, 10*time.Millisecond)
	return pid
}

// hookedStore injects checks or failures in front of the real SQLite store.
type hookedStore struct {
	runner.BuiltInProcessStore
	mu           sync.Mutex
	beforeRecord func() error
	beforeStop   func(from runner.BuiltInProcessState) error
}

func (h *hookedStore) Record(ctx context.Context, f runner.Fence, pid int, marker string) error {
	if h.beforeRecord != nil {
		if err := h.beforeRecord(); err != nil {
			return err
		}
	}
	return h.BuiltInProcessStore.Record(ctx, f, pid, marker)
}

func (h *hookedStore) MarkStopped(ctx context.Context, f runner.Fence, from runner.BuiltInProcessState) error {
	if h.beforeStop != nil {
		h.mu.Lock()
		err := h.beforeStop(from)
		h.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return h.BuiltInProcessStore.MarkStopped(ctx, f, from)
}

func TestProcessSupervisor_PermitIsDurableBeforeTheGuardianDoesRealWork(t *testing.T) {
	f := newSupFixture(t)
	ctx, a := f.assign(t)
	worked, pidFile := f.file("worked"), f.file("pid")
	var sawPermitOnly bool
	store := &hookedStore{BuiltInProcessStore: f.store, beforeRecord: func() error {
		_, _, state := f.row(t, a.Fence)
		sawPermitOnly = state == "permitted" && !exists(worked)
		return nil
	}}
	sup := builtin.NewProcessSupervisor(store, builtin.SupervisorOptions{})

	res, err := process.NewSupervisedLocalProcessRunner(sup).RunProcess(ctx, engine.ProcessSpec{
		Command: "/bin/sh", Args: []string{"-c", `echo $$ > "$0"; touch "$1"; echo done`, pidFile, worked}})
	require.NoError(t, err)
	assert.Equal(t, "done\n", res.Stdout)
	assert.True(t, sawPermitOnly, "when the guardian is recorded, the permit row exists and no real work has run")
	pid, marker, state := f.row(t, a.Fence)
	assert.Equal(t, readPID(t, pidFile), pid, "the recorded guardian is the process that did the work")
	assert.True(t, strings.HasPrefix(marker, runtime.GOOS+":"), "start marker %q", marker)
	assert.Equal(t, "stopped", state, "a normal exit with no surviving group member is confirmed stopped")
}

func TestProcessSupervisor_FailureBeforeRecordLeavesNoWriter(t *testing.T) {
	f := newSupFixture(t)
	ctx, a := f.assign(t)
	worked := f.file("worked")
	boom := errors.New("disk I/O error")
	sup := builtin.NewProcessSupervisor(&hookedStore{BuiltInProcessStore: f.store, beforeRecord: func() error { return boom }}, builtin.SupervisorOptions{})

	_, err := process.NewSupervisedLocalProcessRunner(sup).RunProcess(ctx, engine.ProcessSpec{Command: "/bin/sh", Args: []string{"-c", `touch "$0"`, worked}})
	require.ErrorIs(t, err, boom)
	time.Sleep(200 * time.Millisecond)
	assert.False(t, exists(worked), "the unrecorded guardian exited before any real work")
	pid, _, state := f.row(t, a.Fence)
	assert.Equal(t, []any{0, "stopped"}, []any{pid, state}, "the abandoned permit is durably closed")
}

func TestProcessSupervisor_FencedJobCannotLaunch(t *testing.T) {
	f := newSupFixture(t)
	ctx, a := f.assign(t)
	f.fence(t, a)
	worked := f.file("worked")
	sup := builtin.NewProcessSupervisor(f.store, builtin.SupervisorOptions{})

	_, err := process.NewSupervisedLocalProcessRunner(sup).RunProcess(ctx, engine.ProcessSpec{Command: "/bin/sh", Args: []string{"-c", `touch "$0"`, worked}})
	require.ErrorIs(t, err, runner.ErrFenced)
	assert.False(t, exists(worked))
	_, _, state := f.row(t, a.Fence)
	assert.Empty(t, state, "no permit for a fenced job")
}

func TestProcessSupervisor_OneLiveProcessPerJobGeneration(t *testing.T) {
	f := newSupFixture(t)
	ctx, a := f.assign(t)
	sup := builtin.NewProcessSupervisor(f.store, builtin.SupervisorOptions{})
	runCtx, cancel := context.WithCancel(ctx)
	lines, wait, err := claudecli.NewSupervisedExecRunner(sup).Run(runCtx, claudecli.RunSpec{Bin: "/bin/sh", Args: []string{"-c", "echo up; sleep 30"}})
	require.NoError(t, err)
	require.Equal(t, "up", <-lines)

	_, err = process.NewSupervisedLocalProcessRunner(sup).RunProcess(ctx, engine.ProcessSpec{Command: "/bin/sh", Args: []string{"-c", "true"}})
	require.ErrorIs(t, err, runner.ErrProcessActive, "a second writer for the same job generation is refused")

	cancel()
	for range lines {
	}
	_ = wait()
	require.Eventually(t, func() bool { _, _, s := f.row(t, a.Fence); return s == "stopped" }, 5*time.Second, 20*time.Millisecond,
		"the cancelled group is reaped and confirmed stopped")
}

// Named by the alignment prose ("Supervisor identity producer").
func TestBuiltInSupervisor_MissingAssignmentCannotLaunch(t *testing.T) {
	f := newSupFixture(t)
	sup := builtin.NewProcessSupervisor(f.store, builtin.SupervisorOptions{})
	worked := f.file("worked")
	touch := engine.ProcessSpec{Kind: engine.ProcessToolCall, Command: "/bin/sh", Args: []string{"-c", `touch "$0"`, worked}}

	_, err := process.NewSupervisedLocalProcessRunner(sup).RunProcess(context.Background(), touch)
	require.ErrorIs(t, err, builtin.ErrNoAssignment, "process launch without an assignment")
	_, _, err = claudecli.NewSupervisedExecRunner(sup).Run(context.Background(), claudecli.RunSpec{Bin: "/bin/sh", Args: touch.Args})
	require.ErrorIs(t, err, builtin.ErrNoAssignment, "harness launch without an assignment")
	r := builtin.New(builtin.Deps{Chats: &fakeSelectors{}, Agents: &fakeSelectors{}, Streams: &fakeStreams{},
		Process: process.NewSupervisedLocalProcessRunner(sup), Confirmations: &fakeConfirmations{}})
	_, err = r.Run(context.Background(), runner.Assignment{}, runner.StepJob{Op: runner.OpProcess, Process: &touch})
	require.ErrorIs(t, err, builtin.ErrNoAssignment, "an empty dispatcher assignment is not an identity")

	assert.False(t, exists(worked))
	var n int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM builtin_processes`).Scan(&n))
	assert.Zero(t, n, "no permit, guardian or row is invented")
}
