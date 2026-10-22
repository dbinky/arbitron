package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queryPausingAccess pauses after real authorization, without replacing its
// name, scope, context or manifest. The second call is the real locked reload.
type queryPausingAccess struct {
	layout.WorkflowAccess
	afterFirst func()
	calls      int
}

func (a *queryPausingAccess) AuthorizeLayoutRetry(ctx context.Context, name string) (*workflow.Workflow, workflow.Scope, error) {
	wf, scope, err := a.WorkflowAccess.AuthorizeLayoutRetry(ctx, name)
	a.calls++
	if a.calls == 1 && err == nil {
		a.afterFirst()
	}
	return wf, scope, err
}

type queryPausingCoordinator struct {
	workflow.MutationCoordinator
	locked func()
}

func (c *queryPausingCoordinator) WithMutations(ctx context.Context, targets []workflow.MutationTarget, fn func(context.Context) error) error {
	return c.MutationCoordinator.WithMutations(ctx, targets, func(owned context.Context) error {
		c.locked()
		return fn(owned)
	})
}

func queryWaitFile(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool { _, err := os.Stat(path); return err == nil }, 20*time.Second, 10*time.Millisecond, "waiting for %s", path)
}

// The subprocess uses a separate WorkflowRepo/coordinator and OS locks. Updates
// run through initDomain's real service/stamper/requester, not a test store.
func TestLayoutQueryE2E_MutationProcess(t *testing.T) {
	configPath := os.Getenv("ARBITRON_QUERY_PROCESS_CONFIG")
	if configPath == "" {
		return
	}
	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	defer ds.Close(context.Background())
	ctx := adminCtxForLocalUser(t, ds)
	syncDir := os.Getenv("ARBITRON_QUERY_PROCESS_SYNC")
	name := "query-race"
	var wf *workflow.Workflow
	if os.Getenv("ARBITRON_QUERY_PROCESS_MODE") == "shadow" {
		wf, err = ds.workflowRepo.Load(ctx, workflow.Shared(), name)
	} else {
		wf, err = ds.workflowSvc.Load(ctx, name)
	}
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(syncDir, "ready"), nil, 0600))
	queryWaitFile(t, filepath.Join(syncDir, "go"))
	require.NoError(t, os.WriteFile(filepath.Join(syncDir, "attempting"), nil, 0600))
	switch os.Getenv("ARBITRON_QUERY_PROCESS_MODE") {
	case "shadow":
		// A legacy/private shadow can carry the same token; revision comparison alone
		// cannot authorize a retry of the formerly resolved shared destination.
		err = ds.workflowRepo.Create(ctx, workflow.Private(localDefaultUser), name, wf)
	case "rename":
		err = ds.workflowSvc.Rename(ctx, name, "query-renamed")
	default:
		wf.Nodes[0].Label = "Saved by the independent process"
		err = ds.workflowSvc.Update(ctx, name, wf)
	}
	require.NoError(t, err)
	if os.Getenv("ARBITRON_QUERY_PROCESS_MODE") == "update-reviewed" {
		saved := layoutE2EManifest(t, filepath.Join(ds.cfg.Workspace.Path, "users", localDefaultUser.String(), name, "workflow.json"))
		rec := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), time.Minute)
		require.Equal(t, layout.StatePassed, rec.State)
		require.Eventually(t, func() bool {
			row, err := ds.layout.store.Get(ctx, saved.SavedRevision())
			return err == nil && row.CacheGeneration == row.ResultGeneration
		}, time.Minute, 10*time.Millisecond, "the independently saved result must reach the real cache sink")
	}
	require.NoError(t, os.WriteFile(filepath.Join(syncDir, "done"), nil, 0600))
}

func queryStartMutation(t *testing.T, e *layoutE2EEnv, mode string) (start func(), wait func(), assertBlocked func()) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLayoutQueryE2E_MutationProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "ARBITRON_QUERY_PROCESS_CONFIG="+e.configPath, "ARBITRON_QUERY_PROCESS_SYNC="+dir, "ARBITRON_QUERY_PROCESS_MODE="+mode)
	log, err := os.Create(filepath.Join(dir, "process.log"))
	require.NoError(t, err)
	t.Cleanup(func() { log.Close() })
	cmd.Stdout, cmd.Stderr = log, log
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	queryWaitFile(t, filepath.Join(dir, "ready"))
	start = func() {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go"), nil, 0600))
		queryWaitFile(t, filepath.Join(dir, "attempting"))
	}
	wait = func() {
		err := cmd.Wait()
		if err != nil {
			b, _ := os.ReadFile(filepath.Join(dir, "process.log"))
			t.Fatalf("mutation process: %v\n%s", err, b)
		}
		require.FileExists(t, filepath.Join(dir, "done"))
	}
	assertBlocked = func() {
		require.Never(t, func() bool { _, err := os.Stat(filepath.Join(dir, "done")); return err == nil }, 100*time.Millisecond, 5*time.Millisecond, "the independent writer must wait while the retry owns the filesystem lock")
	}
	return start, wait, assertBlocked
}

func TestLayoutQueryE2E_RetryCrossProcessSaveFence(t *testing.T) {
	for _, ordering := range []string{"save-first", "retry-first", "private-shadow", "shadow-retry-first", "rename-first", "shared-only"} {
		t.Run(ordering, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
			ds, _ := e.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			name := "query-race"
			if ordering == "private-shadow" || ordering == "shadow-retry-first" || ordering == "shared-only" {
				require.NoError(t, ds.workflowSvc.Create(ctx, "source", queryE2EWorkflow("source", "Start")))
				require.NoError(t, ds.workflowSvc.Publish(ctx, "source", name, workflow.VisibilityPublic, ""))
			} else {
				require.NoError(t, ds.workflowSvc.Create(ctx, name, queryE2EWorkflow(name, "Start")))
			}
			wf, scope, err := ds.workflowSvc.AuthorizeLayoutRetry(ctx, name)
			require.NoError(t, err)
			before := layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
			require.Equal(t, layout.StateFailed, before.State)
			mode := "update"
			if ordering == "private-shadow" || ordering == "shadow-retry-first" {
				mode = "shadow"
			} else if ordering == "rename-first" {
				mode = "rename"
			}
			if ordering == "shared-only" {
				require.Equal(t, workflow.Shared(), scope, "the published copy resolves to the shared destination")
				ack, err := ds.layout.query.RetryWorkflow(ctx, name, wf.SavedRevision())
				require.NoError(t, err, "a shared retry must lock its whole resolution set through the real filesystem coordinator")
				after, err := ds.layout.store.Get(ctx, wf.SavedRevision())
				require.NoError(t, err)
				assert.Equal(t, before.JobGeneration+1, after.JobGeneration)
				assert.Equal(t, after.JobGeneration, ack.JobGeneration)
				return
			}
			start, wait, assertBlocked := queryStartMutation(t, e, mode)
			if ordering == "retry-first" || ordering == "shadow-retry-first" {
				coordinator := &queryPausingCoordinator{MutationCoordinator: ds.layout.mutations, locked: func() { start(); assertBlocked() }}
				q := layout.NewQueryService(ds.layout.reader, ds.layout.requester, ds.workflowSvc, ds.eng, coordinator)
				ack, err := q.RetryWorkflow(ctx, name, wf.SavedRevision())
				require.NoError(t, err)
				assert.Equal(t, before.JobGeneration+1, ack.JobGeneration)
				wait()
				if ordering == "shadow-retry-first" {
					current, resolved, err := ds.workflowSvc.AuthorizeLayoutRetry(ctx, name)
					require.NoError(t, err)
					require.Equal(t, workflow.Private(localDefaultUser), resolved)
					require.Equal(t, wf.SavedRevision(), current.SavedRevision())
					row, err := ds.layout.store.Get(ctx, wf.SavedRevision())
					require.NoError(t, err)
					assert.Equal(t, workflow.Shared(), row.Destination.Scope)
					assert.Equal(t, ack.JobGeneration, row.JobGeneration)
					assert.Equal(t, before.Manifest, row.Manifest)
					return
				}
				saved := manifestRevisionOnDisk(t, ds, name)
				require.NotEqual(t, wf.SavedRevision(), saved)
				// The short-lived peer may claim its new job before shutdown, so
				// assert durable independent scheduling, not lease-expiry timing.
				newer, err := ds.layout.store.Get(ctx, saved)
				require.NoError(t, err)
				assert.NotEqual(t, layout.StateSuperseded, newer.State)
				var captured workflow.Workflow
				require.NoError(t, json.Unmarshal(newer.Manifest, &captured))
				assert.Equal(t, "Saved by the independent process", captured.Nodes[0].Label)
				assert.Equal(t, saved, captured.SavedRevision())
				older, err := ds.layout.store.Get(ctx, wf.SavedRevision())
				require.NoError(t, err)
				assert.NotEqual(t, layout.StateSuperseded, older.State)
				assert.Equal(t, ack.JobGeneration, older.JobGeneration)
			} else {
				access := &queryPausingAccess{WorkflowAccess: ds.workflowSvc, afterFirst: func() { start(); wait() }}
				q := layout.NewQueryService(ds.layout.reader, ds.layout.requester, access, ds.eng, ds.layout.mutations)
				_, err := q.RetryWorkflow(ctx, name, wf.SavedRevision())
				// A moved name is no longer accessible; the other orderings
				// change scope or token. None may bump the old generation.
				if ordering == "rename-first" {
					require.ErrorIs(t, err, workflow.ErrNotFound)
					moved := layoutE2EManifest(t, e.privatePath("query-renamed"))
					require.NotEqual(t, wf.SavedRevision(), moved.SavedRevision())
					assert.Equal(t, wf.Nodes, moved.Nodes)
					require.NoDirExists(t, filepath.Dir(e.privatePath(name)))
				} else {
					require.ErrorIs(t, err, layout.ErrRevisionConflict)
				}
				after, err := ds.layout.store.Get(ctx, wf.SavedRevision())
				require.NoError(t, err)
				assert.Equal(t, before.JobGeneration, after.JobGeneration)
				if ordering == "private-shadow" {
					current, resolved, err := ds.workflowSvc.AuthorizeLayoutRetry(ctx, name)
					require.NoError(t, err)
					require.Equal(t, wf.SavedRevision(), current.SavedRevision(), "scope, not token, changed")
					require.NotEqual(t, scope, resolved)
				}
			}
			// Confirm the production coordinator is the actual filesystem lock owner.
			_, ok := ds.layout.mutations.(*filesystem.WorkflowRepo)
			require.True(t, ok)
		})
	}
}
