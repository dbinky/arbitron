package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpProfileCaller drives the registered MCP tools of the root stack.
func mcpProfileCaller(t *testing.T, ctx context.Context, ds *domainStack) func(string, map[string]any) (bool, string) {
	srv := adaptmcp.NewServerWithDeps("root", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	return func(name string, args map[string]any) (bool, string) {
		t.Helper()
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		require.NoError(t, err)
		data, err := json.Marshal(srv.MCPServer().HandleMessage(ctx, request))
		require.NoError(t, err)
		var response struct {
			Result gomcp.CallToolResult `json:"result"`
			Error  any                  `json:"error"`
		}
		require.NoError(t, json.Unmarshal(data, &response))
		require.Nil(t, response.Error, string(data))
		return response.Result.IsError, fmt.Sprint(response.Result.Content)
	}
}

// hookSink is a model sink whose handler runs onModel once, the first time a
// request for that model arrives, before answering it.
func hookSink(t *testing.T) (*modelSink, func(model string, fn func())) {
	t.Helper()
	var (
		hookMu  sync.Mutex
		hookFor string
		hook    func()
	)
	s := &modelSink{arrived: make(chan struct{}, 64)}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.models = append(s.models, body.Model)
		s.mu.Unlock()
		hookMu.Lock()
		var run func()
		if hook != nil && body.Model == hookFor {
			run, hook = hook, nil
		}
		hookMu.Unlock()
		if run != nil {
			run()
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, body.Model)
	}))
	t.Cleanup(s.srv.Close)
	return s, func(model string, fn func()) {
		hookMu.Lock()
		defer hookMu.Unlock()
		hookFor, hook = model, fn
	}
}

// Paired seam Area 2 refresh advancement <-> Area 6 admin profile edit. Both
// writers share the profiles row and its revision through full-row CAS
// (commitAdvanceTx versus UpdateExpected/UpdateValidated); neither may
// silently undo the other.
func TestModelRefreshAndAdminProfileEditNeverOverwriteEachOther(t *testing.T) {
	t.Run("admin edit read before the advancement is refused, then lands on Y", func(t *testing.T) {
		sink, _ := hookSink(t)
		ds, _, ctx := bootRefreshPinsRoot(t, sink)
		call := mcpProfileCaller(t, ctx, ds)
		read, err := ds.profileSvc.Get(ctx, "pin-alias")
		require.NoError(t, err)
		require.Equal(t, lifecycleModelX, read.Model)
		n := read.Revision

		refreshAdvancesToY(t, ctx, ds)

		isErr, text := call("profile_update", map[string]any{"alias": "pin-alias", "effort": "high", "revision": n})
		require.True(t, isErr, "an edit based on revision %d must not land on the advanced row: %s", n, text)
		assert.Contains(t, text, "changed concurrently")
		stored, err := ds.profileSvc.Get(ctx, "pin-alias")
		require.NoError(t, err)
		assert.Equal(t, lifecycleModelY, stored.Model, "the refused edit must not revert the advancement")
		assert.Equal(t, n+1, stored.Revision)
		assert.NotEqual(t, provider.Effort("high"), stored.Effort)

		// A sparse edit without a revision merges onto the row it reads: the
		// advanced model survives the admin write.
		isErr, text = call("profile_update", map[string]any{"alias": "pin-alias", "effort": "high"})
		require.False(t, isErr, text)
		_, text = call("profile_get", map[string]any{"alias": "pin-alias"})
		assert.Contains(t, text, lifecycleModelY)
		assert.NotContains(t, text, `"model": "`+lifecycleModelX+`"`)
		stored, err = ds.profileSvc.Get(ctx, "pin-alias")
		require.NoError(t, err)
		assert.Equal(t, lifecycleModelY, stored.Model)
		assert.Equal(t, provider.Effort("high"), stored.Effort)
		assert.Equal(t, n+2, stored.Revision)

		after := sink.mark()
		run, _, err := ds.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
		require.NoError(t, err)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, run).State)
		assert.Equal(t, []string{lifecycleModelY}, after(), "the next new run executes the advanced model")
		assertRefreshPins(t, ds, run, lifecycleModelY)
	})

	t.Run("admin create on another route does not inherit the refresh's proof", func(t *testing.T) {
		sink, _ := hookSink(t)
		ds, _, ctx := bootRefreshPinsRoot(t, sink)
		refreshAdvancesToY(t, ctx, ds) // Y is confirmed on the sink connection
		var mu sync.Mutex
		var probed []string
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			probed = append(probed, body.Model)
			mu.Unlock()
			if body.Model == lifecycleModelY {
				http.Error(w, "model not served on this route", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
		}))
		defer other.Close()
		crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
		require.NoError(t, err)
		require.NoError(t, adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy).Create(ctx, provider.Connection{Name: "other", Provider: "openrouter", BaseURL: other.URL}, "k"))

		isErr, text := mcpProfileCaller(t, ctx, ds)("profile_create", map[string]any{
			"alias": "fresh", "kind": "chat", "connection_ref": "other",
			"selection_mode": "following",
			"follow_target":  map[string]any{"provider": "openrouter", "tier": "flash"},
		})
		require.False(t, isErr, text)
		stored, err := ds.profileSvc.Get(ctx, "fresh")
		require.NoError(t, err)
		assert.Equal(t, lifecycleModelX, stored.Model, "Y is confirmed only on the sink route, so the new route starts on X")
		mu.Lock()
		// Probes run on concurrent workers, so only the set of probed models is fixed.
		assert.ElementsMatch(t, []string{lifecycleModelY, lifecycleModelX}, probed, "the create probes its own route rather than reusing the sink's proof")
		mu.Unlock()
		binding, err := ds.profileResolver.Resolve(ctx, "fresh")
		require.NoError(t, err)
		assert.Equal(t, lifecycleModelX, binding.Model)
	})

	// An admin edit that commits while the refresh is probing Y, after it listed
	// the profile at revision n. Whether the edit comes from this process or
	// from another one sharing the database (the separate `arbitron mcp` stdio
	// server, which shares no in-memory gate), RefreshService.commit's
	// ExecutionGate.WithCurrent rescan sees the changed profile row and drops the
	// proposals. The SQL full-row CAS in commitAdvanceTx is the last line for an
	// edit between that rescan and the transaction (profile_revision_test.go).
	for _, tc := range []struct {
		name, guard, status string
		edit                func(t *testing.T, ctx context.Context, ds *domainStack, read provider.Profile) error
	}{
		{name: "in-process MCP edit", guard: "refresh", status: "advance_conflict",
			edit: func(t *testing.T, ctx context.Context, ds *domainStack, read provider.Profile) error {
				isErr, text := mcpProfileCaller(t, ctx, ds)("profile_update", map[string]any{"alias": "pin-alias", "effort": "high", "revision": read.Revision})
				if isErr {
					return fmt.Errorf("profile_update: %s", text)
				}
				return nil
			}},
		{name: "edit from another process", guard: "refresh", status: "advance_conflict",
			edit: func(t *testing.T, ctx context.Context, ds *domainStack, read provider.Profile) error {
				var seq int
				var name, path string
				if err := ds.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
					return err
				}
				other, err := adaptsqlite.Open(path)
				if err != nil {
					return err
				}
				defer other.Close()
				next := read.Clone()
				next.Effort = provider.Effort("high")
				_, err = adaptsqlite.NewProfileRepo(other).UpdateExpected(ctx, read, next)
				return err
			}},
	} {
		t.Run("admin edit landing during discovery wins: "+tc.name, func(t *testing.T) {
			sink, onModel := hookSink(t)
			ds, _, ctx := bootRefreshPinsRoot(t, sink)
			deps := ds.refreshDeps
			fetcher := &fixtureFetcher{body: refreshCatalogOnlyX}
			deps.Fetcher = fetcher
			serveRootCatalog(t, ds, fetcher)
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			// A first cycle catalogs X only, so the admin edit can re-prove X.
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			require.Empty(t, report.Changes)
			read, err := ds.profileSvc.Get(ctx, "pin-alias")
			require.NoError(t, err)
			require.Equal(t, lifecycleModelX, read.Model)
			n := read.Revision

			edited := make(chan error, 1)
			onModel(lifecycleModelY, func() {
				done := make(chan error, 1)
				go func() { done <- tc.edit(t, ctx, ds, read) }()
				select {
				case err := <-done:
					edited <- err
				case <-time.After(10 * time.Second):
					edited <- fmt.Errorf("admin edit blocked behind the refresh probe")
				}
			})
			fetcher.mu.Lock()
			fetcher.body = refreshCatalogFixture
			fetcher.mu.Unlock()
			report, err = refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			select {
			case err := <-edited:
				require.NoError(t, err)
			default:
				t.Fatal("the refresh never probed Y, so the admin edit did not interleave")
			}
			assert.Empty(t, report.Changes, "the proposal read at revision %d must not commit over the admin edit", n)
			assert.Equal(t, tc.status, report.PathStatus[tc.guard], "guard: %v", report.PathStatus)
			stored, err := ds.profileSvc.Get(ctx, "pin-alias")
			require.NoError(t, err)
			assert.Equal(t, lifecycleModelX, stored.Model)
			assert.Equal(t, provider.Effort("high"), stored.Effort, "the admin edit survives the refresh")
			assert.Equal(t, n+1, stored.Revision)

			// The next cycle reads the edited row and advances it, keeping the edit.
			report, err = refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			require.Len(t, report.Changes, 1)
			assert.Equal(t, n+2, report.Changes[0].Revision)
			stored, err = ds.profileSvc.Get(ctx, "pin-alias")
			require.NoError(t, err)
			assert.Equal(t, lifecycleModelY, stored.Model)
			assert.Equal(t, provider.Effort("high"), stored.Effort)

			after := sink.mark()
			run, _, err := ds.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
			require.NoError(t, err)
			require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, run).State)
			assert.Equal(t, []string{lifecycleModelY}, after(), "the next new run executes Y")
		})
	}
}

const refreshCatalogOnlyX = `{"openrouter":{"id":"openrouter","models":{
 "deepseek/deepseek-v4-flash":{"id":"deepseek/deepseek-v4-flash","release_date":"2026-01-01"}}}}`

// An edit from another process (the `arbitron mcp` stdio server shares the
// database but not the in-memory gate) can land after RefreshService.commit's
// WithCurrent rescan has passed and before CommitCycle opens its transaction.
// There the full-row CAS in profile_advance.go:commitAdvanceTx is the only
// guard: the proposal must lose per profile, never overwrite the edit.
func TestModelRefreshCASRefusesEditLandingAfterGateRescan(t *testing.T) {
	sink, _ := hookSink(t)
	ds, _, ctx := bootRefreshPinsRoot(t, sink)
	deps := ds.refreshDeps
	fetcher := &fixtureFetcher{body: refreshCatalogOnlyX}
	deps.Fetcher = fetcher
	serveRootCatalog(t, ds, fetcher)
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Empty(t, report.Changes)
	read, err := ds.profileSvc.Get(ctx, "pin-alias")
	require.NoError(t, err)
	require.Equal(t, lifecycleModelX, read.Model)
	n := read.Revision

	var seq int
	var name, path string
	require.NoError(t, ds.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	other, err := adaptsqlite.Open(path)
	require.NoError(t, err)
	defer other.Close()

	// Wrap the production revision source: the rescan computes its answer
	// first, then the other process commits its edit before the write runs.
	var once sync.Once
	edited := make(chan error, 1)
	ds.executionGate.SetCurrentRevision(func(ctx context.Context) (string, error) {
		rev, err := ds.pathInventory.Revision(ctx)
		once.Do(func() {
			next := read.Clone()
			next.Effort = provider.Effort("high")
			_, editErr := adaptsqlite.NewProfileRepo(other).UpdateExpected(ctx, read, next)
			edited <- editErr
		})
		return rev, err
	})
	defer ds.executionGate.SetCurrentRevision(ds.pathInventory.Revision)

	fetcher.mu.Lock()
	fetcher.body = refreshCatalogFixture
	fetcher.mu.Unlock()
	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	select {
	case err := <-edited:
		require.NoError(t, err)
	default:
		t.Fatal("the refresh never rescanned the gate, so the edit did not interleave")
	}
	assert.Empty(t, report.Changes, "the proposal read at revision %d must not commit over the edit", n)
	assert.NotEqual(t, "advance_conflict", report.PathStatus["refresh"], "the gate rescan must pass so only the SQL CAS guards: %v", report.PathStatus)
	assert.Equal(t, "advance_conflict", report.PathStatus["profile:pin-alias"], "%v", report.PathStatus)
	stored, err := ds.profileSvc.Get(ctx, "pin-alias")
	require.NoError(t, err)
	assert.Equal(t, lifecycleModelX, stored.Model)
	assert.Equal(t, provider.Effort("high"), stored.Effort, "the edit survives the refresh CAS")
	assert.Equal(t, n+1, stored.Revision)

	// The next cycle reads the edited row and advances it, keeping the edit.
	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	assert.Equal(t, n+2, report.Changes[0].Revision)
	stored, err = ds.profileSvc.Get(ctx, "pin-alias")
	require.NoError(t, err)
	assert.Equal(t, lifecycleModelY, stored.Model)
	assert.Equal(t, provider.Effort("high"), stored.Effort)

	after := sink.mark()
	run, _, err := ds.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, run).State)
	assert.Equal(t, []string{lifecycleModelY}, after(), "the next new run executes Y")
}

// paired:7, admin direction. A tools-only MCP profile_update on a following
// profile, sent without a revision, reads the row at revision n (model X) and
// then waits on the mutation lease while the real refresh commits X -> Y under
// its WithCurrent read lease. The admin write then reaches
// UpdateValidated/updateExpectedTx with expected revision n: the full-row CAS
// must refuse it, so Y is never silently reverted to the X the edit read.
func TestAdminToolsEditReadBeforeAdvancementCommitNeverRevertsY(t *testing.T) {
	sink, _ := hookSink(t)
	ds, _, ctx := bootRefreshPinsRoot(t, sink)
	call := mcpProfileCaller(t, ctx, ds)
	deps := ds.refreshDeps
	fetcher := &fixtureFetcher{body: refreshCatalogOnlyX}
	deps.Fetcher = fetcher
	serveRootCatalog(t, ds, fetcher)
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	// A first cycle proves X, so the edit's own X revalidation needs no discovery.
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Empty(t, report.Changes)
	read, err := ds.profileSvc.Get(ctx, "pin-alias")
	require.NoError(t, err)
	require.Equal(t, lifecycleModelX, read.Model)
	n := read.Revision

	type result struct {
		isErr bool
		text  string
	}
	edited := make(chan result, 1)
	blocked := make(chan bool, 1)
	var once sync.Once
	ds.executionGate.SetCurrentRevision(func(ctx context.Context) (string, error) {
		once.Do(func() {
			// Runs under the refresh's read lease, before CommitCycle.
			go func() {
				isErr, text := call("profile_update", map[string]any{"alias": "pin-alias", "allowed_tools": []any{"Glob"}})
				edited <- result{isErr, text}
			}()
			blocked <- waitForStack(5*time.Second, "provider.(*ProfileService).", "provider.(*ExecutionGate).WithMutation")
		})
		return ds.pathInventory.Revision(ctx)
	})
	defer ds.executionGate.SetCurrentRevision(ds.pathInventory.Revision)

	fetcher.mu.Lock()
	fetcher.body = refreshCatalogFixture
	fetcher.mu.Unlock()
	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	select {
	case ok := <-blocked:
		require.True(t, ok, "the admin edit must have read the row and be waiting on the mutation lease")
	default:
		t.Fatal("the refresh never rescanned under its lease, so the edit did not interleave")
	}
	require.Len(t, report.Changes, 1, "the advancement commits first: %v", report.PathStatus)
	assert.Equal(t, n+1, report.Changes[0].Revision)

	var got result
	select {
	case got = <-edited:
	case <-time.After(10 * time.Second):
		t.Fatal("admin edit never finished")
	}
	require.True(t, got.isErr, "an edit read at revision %d must not land on the advanced row: %s", n, got.text)
	assert.Contains(t, got.text, "changed concurrently")
	stored, err := ds.profileSvc.Get(ctx, "pin-alias")
	require.NoError(t, err)
	assert.Equal(t, lifecycleModelY, stored.Model, "the refused edit must not revert Y")
	assert.Equal(t, read.AllowedTools, stored.AllowedTools)
	assert.Equal(t, n+1, stored.Revision)

	// The retry merges onto the advanced row and keeps Y.
	isErr, text := call("profile_update", map[string]any{"alias": "pin-alias", "allowed_tools": []any{"Glob"}})
	require.False(t, isErr, text)
	_, text = call("profile_get", map[string]any{"alias": "pin-alias"})
	assert.Contains(t, text, lifecycleModelY)
	stored, err = ds.profileSvc.Get(ctx, "pin-alias")
	require.NoError(t, err)
	assert.Equal(t, lifecycleModelY, stored.Model)
	assert.Equal(t, []string{"Glob"}, stored.AllowedTools)
	assert.Equal(t, n+2, stored.Revision)

	after := sink.mark()
	run, _, err := ds.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, run).State)
	assert.Equal(t, []string{lifecycleModelY}, after(), "the next new run executes Y")
}

// waitForStack reports whether some goroutine's stack contains every frame
// fragment within d.
func waitForStack(d time.Duration, frames ...string) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		for _, g := range strings.Split(string(stack[:n]), "\n\n") {
			all := true
			for _, f := range frames {
				all = all && strings.Contains(g, f)
			}
			if all {
				return true
			}
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
