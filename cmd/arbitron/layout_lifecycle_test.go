package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/stretchr/testify/require"
)

// This gate uses argument-bound unit envelopes solely for lifecycle testing.
// It is never installed by the production factory and proves no browser acceptance.
type layoutCheckpointReviewer struct {
	reviewer layout.Reviewer
	entered  chan struct{}
}

func (r *layoutCheckpointReviewer) Review(ctx context.Context, q layout.ReviewRequest) (layout.Review, error) {
	if q.Attempt == 2 {
		close(r.entered)
		<-ctx.Done()
		return layout.Review{}, ctx.Err()
	}
	return r.reviewer.Review(ctx, q)
}

func TestLayoutE2E_LifecycleProcess(t *testing.T) {
	action := os.Getenv("ARBITRON_LAYOUT_E2E_PROCESS")
	if action == "" {
		return
	}
	configPath := os.Getenv("ARBITRON_LAYOUT_E2E_CONFIG")
	if action == "checkpoint" {
		reviewer := &layoutCheckpointReviewer{reviewer: layouttest.NewFakeReviewer(layouttest.ReviewScript{Findings: []layout.Finding{{Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking, Source: layout.SourceReview, Evidence: "checkpoint completed review"}}}), entered: make(chan struct{})}
		layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
			return layouttest.NewFakeRenderer(), reviewer, nil, nil
		}
		ds, err := initDomain(context.Background(), configPath)
		require.NoError(t, err)
		require.NoError(t, ds.workflowSvc.Create(adminCtxForLocalUser(t, ds), "checkpoint", layoutE2EWorkflow("checkpoint", "Retained best")))
		select {
		case <-reviewer.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("second review never received actual saved candidate")
		}
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(configPath), "review-ready"), []byte("ready"), 0600))
		var b [1]byte
		_, err = os.Stdin.Read(b[:])
		require.NoError(t, err)
		ds.Close(context.Background())
	} else {
		ds, err := initDomain(context.Background(), configPath)
		require.NoError(t, err)
		defer ds.Close(context.Background())
		path := filepath.Join(ds.cfg.Workspace.Path, "users", localDefaultUser.String(), "checkpoint", "workflow.json")
		rec := layoutE2EWaitTerminal(t, ds, layoutE2ESavedRevision(t, path))
		require.Greater(t, rec.JobGeneration, int64(1))
		require.Equal(t, int64(1), rec.ResultGeneration)
		best, err := ds.layout.store.Result(context.Background(), rec.Revision, 1)
		require.NoError(t, err)
		require.Equal(t, "checkpoint completed review", best.Review.Findings[0].Evidence)
	}
}

func layoutLifecycleChild(t *testing.T, e *layoutE2EEnv, action string) (*exec.Cmd, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLayoutE2E_LifecycleProcess$")
	cmd.Env = append(os.Environ(), "ARBITRON_LAYOUT_E2E_PROCESS="+action, "ARBITRON_LAYOUT_E2E_CONFIG="+e.configPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		cancel()
		stdin.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return cmd, func() { _, err := stdin.Write([]byte("s")); require.NoError(t, err); require.NoError(t, stdin.Close()) }
}

func TestLayoutE2E_CloseCheckpointsAndNewProcessReclaims(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	cmd, stop := layoutLifecycleChild(t, e, "checkpoint")
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "review-ready")); return err == nil }, 8*time.Second, 10*time.Millisecond)
	stop()
	require.NoError(t, cmd.Wait())
	db, err := adaptsqlite.Open(e.dbPath)
	require.NoError(t, err)
	store := adaptsqlite.NewLayoutRepo(db)
	rev := layoutE2ESavedRevision(t, e.privatePath("checkpoint"))
	row, err := store.Get(context.Background(), rev)
	require.NoError(t, err)
	require.Equal(t, layout.StateInProgress, row.State)
	require.Nil(t, row.FinishedAt)
	require.Equal(t, int64(1), row.ResultGeneration)
	require.NotNil(t, row.LeaseExpiresAt)
	best, err := store.Result(context.Background(), rev, 1)
	require.NoError(t, err)
	require.True(t, layout.HasRenderedEvidence(best.Geometry, best.Review))
	require.Equal(t, "checkpoint completed review", best.Review.Findings[0].Evidence)
	captured, err := json.Marshal(layoutE2EManifest(t, e.privatePath("checkpoint")))
	require.NoError(t, err)
	require.JSONEq(t, string(captured), string(row.Manifest))
	// Expire the durable test lease rather than sleeping through the production five-minute lease.
	_, err = db.Exec("UPDATE layout_revisions SET lease_expires_at = ? WHERE revision = ?", time.Now().Add(-time.Minute).UnixNano(), rev)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	restarted, _ := layoutLifecycleChild(t, e, "recover")
	require.NoError(t, restarted.Wait())
	db, err = adaptsqlite.Open(e.dbPath)
	require.NoError(t, err)
	defer db.Close()
	store = adaptsqlite.NewLayoutRepo(db)
	row, err = store.Get(context.Background(), rev)
	require.NoError(t, err)
	require.Equal(t, layout.StateFailed, row.State)
	require.Equal(t, int64(2), row.JobGeneration)
	require.Equal(t, int64(1), row.ResultGeneration)
	require.ErrorIs(t, store.Complete(context.Background(), layout.Fence{Revision: rev, Generation: 1}, layout.Completion{State: layout.StatePassed}, time.Now()), layout.ErrStaleGeneration)
	retained, err := store.Result(context.Background(), rev, 1)
	require.NoError(t, err)
	require.Equal(t, best, retained)
}
