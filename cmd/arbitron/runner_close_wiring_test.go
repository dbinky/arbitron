package main

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rcFakeClaudeScript replaces only the external claude CLI. `auth` blocks while
// `block-auth` exists; a model call always blocks. Both record their shell pid
// (atomically, via rename) and run until `release` exists or they are killed.
const rcFakeClaudeScript = `#!/bin/sh
case "$1" in
  --version) echo "2.1.330 (Claude Code)"; exit 0 ;;
  auth)
    if [ -f '@DIR@/block-auth' ]; then
      echo $$ > '@DIR@/auth.pid.tmp'; mv '@DIR@/auth.pid.tmp' '@DIR@/auth.pid'
      while [ ! -f '@DIR@/release' ]; do sleep 0.05; done
    fi
    echo '{"loggedIn":true}'; exit 0 ;;
esac
cat > /dev/null
echo $$ > '@DIR@/call.pid.tmp'; mv '@DIR@/call.pid.tmp' '@DIR@/call.pid'
echo "$*" >> '@DIR@/calls.log'
while [ ! -f '@DIR@/release' ]; do sleep 0.05; done
m=$(echo "$*" | sed -n 's/.*--model \([^ ]*\).*/\1/p')
echo '{"type":"result","is_error":false,"result":"OK","session_id":"s","modelUsage":{"'"$m"'":{}}}'
`

// rcBoot is rwBoot with the minimum probe interval (a monitor tick every 1.25s)
// so the root's own lifecycle loop drives probes and confirmations quickly.
func rcBoot(t *testing.T) (*domainStack, string) {
	t.Helper()
	configPath, fake := rwWriteRoot(t, 8)
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("  probeintervalseconds: 5\n  probejitterseconds: 0\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	ds, err := initDomainWithModelDependencies(context.Background(), configPath, ModelDependencies{
		FetchTransport: catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errOfflineCatalog }),
	})
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	require.NotNil(t, ds.runners)
	// Release any blocked leaf before the root cleanup above (cleanups are LIFO).
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(fake, "release"), nil, 0o600) })
	// Boot-time inventory already ran; swap the leaf for the blocking variant.
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(strings.ReplaceAll(rcFakeClaudeScript, "@DIR@", fake)), 0o700))
	return ds, fake
}

// rcGoroutines maps goroutine id -> stack for every goroutine whose stack
// mentions one of the markers (the actual workers, not a global count).
func rcGoroutines(markers ...string) map[string]string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n == len(buf) {
			buf = make([]byte, len(buf)*2)
			continue
		}
		found := map[string]string{}
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			for _, m := range markers {
				if strings.Contains(stack, m) {
					found[strings.Fields(stack)[1]] = stack
					break
				}
			}
		}
		return found
	}
}

func rcNew(before, now map[string]string) map[string]string {
	out := map[string]string{}
	for id, s := range now {
		if _, ok := before[id]; !ok {
			out[id] = s
		}
	}
	return out
}

func rcAnyContains(gs map[string]string, s string) bool {
	for _, st := range gs {
		if strings.Contains(st, s) {
			return true
		}
	}
	return false
}

func rcPid(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	require.NoError(t, err)
	return pid
}

func rcAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// rcCloseJoins closes the root while a worker is blocked in the external leaf
// (which is never released), then proves cancellation reached the leaf, every
// worker joined before Close returned, the DB is closed and Close is repeatable.
func rcCloseJoins(t *testing.T, ds *domainStack, fake string, pid int, before map[string]string, markers []string, within time.Duration) {
	t.Helper()
	owned := rcNew(before, rcGoroutines(markers...))
	start := time.Now()
	closed := make(chan struct{})
	go func() { ds.Close(context.Background()); close(closed) }()
	select {
	case <-closed:
	case <-time.After(within):
		t.Fatalf("Close did not cancel and join the in-flight worker within %s", within)
	}
	_, err := os.Stat(filepath.Join(fake, "release"))
	require.True(t, os.IsNotExist(err), "the leaf was never released: only cancellation can end it")
	t.Logf("Close returned after %s", time.Since(start))
	// Joined, not merely signalled: none of the worker goroutines observed in
	// flight outlives Close, so none can issue SQL against the closed handle.
	after := rcGoroutines(markers...)
	for id := range owned {
		_, alive := after[id]
		assert.False(t, alive, "worker goroutine %s survived Close:\n%s", id, after[id])
	}
	assert.Empty(t, rcNew(before, after), "no monitor/confirmer/dispatcher goroutine remains after Close")
	assert.False(t, rcAlive(pid), "cancellation must kill the external leaf (pid %d)", pid)
	require.ErrorContains(t, ds.db.Ping(), "closed", "the database closes only after the workers join")
	done := make(chan struct{})
	go func() { ds.Close(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a repeated Close must return")
	}
	assert.Empty(t, rcNew(before, rcGoroutines(markers...)))
}

// Alignment row "Lifecycle monitor, confirmer and wake forwarding": the
// blocked-dispatch case is TestRunnerWiring_DispatcherShutdownDrainsAndJoins;
// these cover Close during a read-only probe and during a token-spending
// confirmation, both started by the root's own lifecycle loop.
func TestRunnerWiring_CloseJoinsPoolWorkers(t *testing.T) {
	markers := []string{"(*HealthMonitor)", "(*ClaudeAuthProber)",
		"(*DispatchConfirmer)", "(*StepDispatcher)", "runner.NewStepDispatcher"}

	t.Run("read-only probe", func(t *testing.T) {
		before := rcGoroutines(markers...)
		ds, fake := rcBoot(t)
		rwTouch(t, fake, "block-auth")
		require.NoError(t, ds.Start(context.Background()))
		pidFile := filepath.Join(fake, "auth.pid")
		require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 20*time.Second, 20*time.Millisecond,
			"the root's monitor loop must run the claude auth probe")
		pid := rcPid(t, pidFile)
		require.True(t, rcAlive(pid))
		inFlight := rcNew(before, rcGoroutines(markers...))
		require.True(t, rcAnyContains(inFlight, "(*ClaudeAuthProber).Probe("), "probe goroutine is in flight")
		require.True(t, rcAnyContains(inFlight, "(*HealthMonitor).Start."), "monitor loop is running")
		assert.Equal(t, 0, rwCountJobs(ds.db, "1 = 1"), "a read-only probe holds no slot")
		// The probe would time out on its own after 10s; Close must cancel it sooner.
		rcCloseJoins(t, ds, fake, pid, before, markers, 6*time.Second)
	})

	t.Run("token-spending confirmation", func(t *testing.T) {
		before := rcGoroutines(markers...)
		ds, fake := rcBoot(t)
		ctx := adminCtxForLocalUser(t, ds)
		rwChatProfile(t, ctx, ds)
		require.NoError(t, ds.Start(context.Background()))
		past := time.Now().Add(-time.Hour)
		require.NoError(t, ds.runners.monitor.ApplyOutcome(ctx, rwAnthropic, runner.SourceOutcome{
			Class: runner.OutcomeExhausted, Model: "claude-opus-4-1", ResetAt: &past,
		}))
		// The lifecycle loop (not a test-driven Tick) moves the source to probation
		// and starts the confirmation under the root's lifecycle context.
		require.Eventually(t, func() bool {
			return rwCalls(fake) == 1 && rwCountJobs(ds.db, "operation_id = 'probe/confirm' AND state = 'running'") == 1
		}, 30*time.Second, 20*time.Millisecond, "the root loop must dispatch one confirmation")
		pid := rcPid(t, filepath.Join(fake, "call.pid"))
		require.True(t, rcAlive(pid))
		inFlight := rcNew(before, rcGoroutines(markers...))
		require.True(t, rcAnyContains(inFlight, "(*DispatchConfirmer).Confirm("), "confirmation goroutine is in flight")
		require.True(t, rcAnyContains(inFlight, "(*HealthMonitor).advance."), "monitor-owned confirmation worker")
		var jobID string
		require.NoError(t, ds.db.QueryRow(`SELECT id FROM runner_jobs WHERE operation_id = 'probe/confirm'`).Scan(&jobID))

		rcCloseJoins(t, ds, fake, pid, before, markers, 20*time.Second)

		// Durable sink: the confirmation's ledger row was finished before the DB
		// closed, so the slot is not left 'running' across a restart.
		db, err := adaptsqlite.Open(filepath.Join(filepath.Dir(fake), "arbitron.db"))
		require.NoError(t, err)
		defer db.Close()
		var state string
		require.NoError(t, db.QueryRow(`SELECT state FROM runner_jobs WHERE id = ?`, jobID).Scan(&state))
		assert.NotEqual(t, "running", state, "the cancelled confirmation must be terminal in the ledger")
		assert.NotEqual(t, "completed", state, "a cancelled confirmation never completes")
		var attempts, open int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*), COUNT(*) - COUNT(ended_at) FROM runner_job_attempts WHERE job_id = ?`, jobID).Scan(&attempts, &open))
		assert.Equal(t, 1, attempts, "one token-spending attempt")
		assert.Zero(t, open, "the attempt was closed before the DB closed")
		assert.Equal(t, 1, rwCalls(fake), "Close never retries the confirmation")
	})
}

// rcSnapshot dumps every row of a table as text, so "unchanged" means unchanged.
func rcSnapshot(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	var out []string
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		parts := make([]string, len(cols))
		for i, v := range vals {
			parts[i] = cols[i] + "=" + string(v)
		}
		out = append(out, strings.Join(parts, "|"))
	}
	require.NoError(t, rows.Err())
	return out
}

type rcState struct{ Runners, Audit, Pool []string }

func rcPoolState(t *testing.T, db *sql.DB) rcState {
	t.Helper()
	return rcState{rcSnapshot(t, db, "runners"), rcSnapshot(t, db, "audit_events"), rcSnapshot(t, db, "pool_events")}
}

// Alignment row "Service/authz/audit dependency": a non-admin caller is refused
// by the root-installed service on MCP and REST, and nothing is written.
func TestRunnerAdminRoot_NonAdminMutationRefusedWithZeroChanges(t *testing.T) {
	ds, _ := rwBoot(t, 8)
	admin := adminCtxForLocalUser(t, ds)
	viewer := identity.Identity{UserID: uuid.New(), Role: identity.RoleUser}
	ctx := identity.WithIdentity(context.Background(), viewer)
	deps := ds.httpDeps()
	deps.Auth = nil // trusted identity at the boundary; keep the root service
	router := adapthttp.NewRouterFromDeps(deps)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(identity.WithIdentity(r.Context(), viewer)))
	}))
	defer srv.Close()
	asViewer := mcpToolCallerAs(t, ds, ctx)
	rest := func(method, path, body string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "%s %s: %s", method, path, raw)
		assert.Contains(t, string(raw), "FORBIDDEN")
	}
	mcp := func(tool string, args map[string]any) {
		t.Helper()
		isErr, text := asViewer(tool, args)
		require.True(t, isErr, "%s must be refused: %s", tool, text)
		assert.Equal(t, "admin only", text, tool)
	}
	unchanged := func(want rcState, step string) {
		t.Helper()
		got := rcPoolState(t, ds.db)
		assert.Equal(t, want.Runners, got.Runners, "%s: runners row changed", step)
		assert.Equal(t, want.Audit, got.Audit, "%s: audit_events changed", step)
		assert.Equal(t, want.Pool, got.Pool, "%s: pool_events changed", step)
	}

	s0 := rcPoolState(t, ds.db)
	require.Len(t, s0.Runners, 1)
	require.Contains(t, s0.Runners[0], "|cordoned=0|")
	require.Contains(t, s0.Runners[0], "|step_limit=8|")
	mcp("runner_cordon", map[string]any{"id": "builtin"})
	unchanged(s0, "MCP runner_cordon")
	mcp("runner_update", map[string]any{"id": "builtin", "stepLimit": 3})
	unchanged(s0, "MCP runner_update")
	rest(http.MethodPatch, "/api/v1/runners/builtin", `{"stepLimit":3}`)
	unchanged(s0, "REST step limit")
	rest(http.MethodPost, "/api/v1/runners/builtin/cordon", "")
	unchanged(s0, "REST cordon")

	// Uncordon is only a change on a cordoned runner: cordon as admin first,
	// which proves the same sinks DO move for an authorized caller.
	isErr, text := mcpToolCallerAs(t, ds, admin)("runner_cordon", map[string]any{"id": "builtin"})
	require.False(t, isErr, text)
	s1 := rcPoolState(t, ds.db)
	require.Contains(t, s1.Runners[0], "|cordoned=1|")
	require.Len(t, s1.Audit, len(s0.Audit)+1, "admin cordon is audited")
	require.Len(t, s1.Pool, len(s0.Pool)+1, "admin cordon records a pool event")
	rest(http.MethodPost, "/api/v1/runners/builtin/uncordon", "")
	unchanged(s1, "REST uncordon")
	mcp("runner_uncordon", map[string]any{"id": "builtin"})
	unchanged(s1, "MCP runner_uncordon")

	r, _, err := ds.runners.registry.GetRunner(admin, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.True(t, r.Cordoned)
	assert.Equal(t, 8, r.StepLimit)
}
