package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayoutStoreContract(t *testing.T) {
	layouttest.RunStoreContract(t, func(t *testing.T) layout.Store { return NewLayoutRepo(setupTestDB(t).DB) })
}

func layoutRequest(t *testing.T, repo *LayoutRepo, rev string) (layout.Record, layout.Fence) {
	t.Helper()
	now := time.Date(2026, 10, 16, 12, 0, 0, 123456789, time.UTC)
	wf := layouttest.SampleWorkflow()
	r, created, err := repo.Request(context.Background(), layout.NewRequest{Revision: rev, Destination: layout.Destination{Scope: workflow.Shared(), Name: "sample"}, Manifest: layouttest.ManifestJSON(t, wf), Retained: workflow.RetainedLayout{Source: workflow.RetainedNone}, AttemptLimit: 5, RequestedAt: now})
	require.NoError(t, err)
	require.True(t, created)
	claimed, err := repo.Claim(context.Background(), rev, "worker", now, now.Add(100*time.Millisecond))
	require.NoError(t, err)
	return r, layout.Fence{Revision: rev, Generation: claimed.JobGeneration}
}

func layoutResult(t *testing.T, f layout.Fence) layout.StoredResult {
	t.Helper()
	return layout.StoredResult{Revision: f.Revision, Generation: f.Generation, Geometry: layouttest.Arranged(t, layouttest.SampleWorkflow()), Verdict: layout.VerdictNeedsAttention, CreatedAt: time.Date(2026, 10, 16, 12, 0, 0, 987654321, time.UTC)}
}

// This drives the real Requester/Reader ports and inspects SQLite's stored bytes.
// The review is synthetic unit evidence, not a browser-acceptance claim.
func TestLayoutRequesterSQLiteSink(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	repo := NewLayoutRepo(db)
	requester := layout.NewRequester(repo, 3, nil)
	wf := layouttest.SampleWorkflow()
	wf.Nodes[0].Label = "保存 résumé 🙂"
	wf.ID = uuid.New()
	revision := uuid.Must(uuid.NewV7()).String()
	now := time.Now().UTC()
	wf.Presentation = &workflow.Presentation{Revision: revision, SavedAt: now, Reason: workflow.SaveReasonCreate, Scope: workflow.ScopeLabelShared, Name: "saved", Retained: workflow.RetainedLayout{Source: workflow.RetainedNone}}
	manifest := layouttest.ManifestJSON(t, wf)
	req := workflow.ArrangementRequest{Revision: revision, Scope: workflow.Shared(), Name: "saved", WorkflowID: wf.ID, Reason: workflow.SaveReasonCreate, Manifest: manifest, Retained: wf.Presentation.Retained, RequestedAt: now}
	require.NoError(t, requester.RequestArrangement(ctx, req))
	var captured []byte
	var budget int
	var id string
	require.NoError(t, db.QueryRow(`SELECT input_manifest,attempt_limit,workflow_id FROM layout_revisions WHERE revision=?`, revision).Scan(&captured, &budget, &id))
	assert.Equal(t, manifest, captured)
	assert.Equal(t, 3, budget)
	assert.Equal(t, wf.ID.String(), id)
	claimed, err := repo.Claim(ctx, revision, "worker", now, now.Add(time.Second))
	require.NoError(t, err)
	geo := layouttest.Arranged(t, wf)
	preflight := []layout.Finding{{Criterion: layout.CriterionCrossings, Severity: layout.SeverityAdvisory, Source: layout.SourcePreflight, Evidence: "preflight-specific finding"}}
	review := &layout.Review{Rendered: true, Findings: []layout.Finding{{Criterion: layout.CriterionTextClipped, Severity: layout.SeverityBlocking, Source: layout.SourceReview, NodeIDs: []string{wf.Nodes[0].ID}, Evidence: "review-specific finding"}}, Evidence: layout.Evidence{GeometryDigest: layout.GeometryDigest(geo), Contract: geo.Contract, RendererVersion: layout.ContractVersion, FontsReady: true, Zoom: geo.Contract.PracticalZoom, Tiles: 1, Artifacts: layouttest.Artifacts(geo), ReviewedAt: now}}
	best := layout.StoredResult{Revision: revision, Generation: claimed.JobGeneration, Geometry: geo, Review: review, Preflight: preflight, Score: layout.ScoreOf(3, append(preflight, review.Findings...), len(geo.Crossings)), Verdict: layout.VerdictNeedsAttention, CreatedAt: now}
	require.NoError(t, repo.Complete(ctx, layout.Fence{Revision: revision, Generation: claimed.JobGeneration}, layout.Completion{State: layout.StateNeedsAttention, AttemptsUsed: 3, Result: &best, ReplaceBest: true}, now))
	view := layout.NewReader(repo, nil, nil).View(ctx, wf)
	require.NotNil(t, view.Geometry)
	assert.Equal(t, geo, *view.Geometry)
	assert.Equal(t, layout.DisplayNeedsAttention, view.DisplayStatus)
	assert.Equal(t, best.UnresolvedFindings(), view.Findings)
	viewed := review.Evidence.ForViewer()
	assert.Equal(t, &viewed, view.Evidence)
	assert.False(t, view.Unreviewed)
	stored, err := repo.Result(ctx, revision, 1)
	require.NoError(t, err)
	assert.Equal(t, best, stored)
	// Mutating or replaying the producer's later graph cannot replace the input.
	wf.Nodes[0].Label = "changed later"
	req.Manifest = layouttest.ManifestJSON(t, wf)
	require.ErrorIs(t, requester.RequestArrangement(ctx, req), layout.ErrConflictingArrangementInput)
	row, err := repo.Get(ctx, revision)
	require.NoError(t, err)
	assert.Equal(t, captured, row.Manifest)
}

func TestLayoutStoreDecodeAndUnavailable(t *testing.T) {
	ctx := context.Background()
	for _, column := range []string{"retained", "findings", "workflow_id", "owner_id"} {
		t.Run(column, func(t *testing.T) {
			db := setupTestDB(t).DB
			repo := NewLayoutRepo(db)
			layoutRequest(t, repo, "r")
			if column == "owner_id" {
				_, err := db.Exec(`UPDATE layout_revisions SET scope_kind='private' WHERE revision='r'`)
				require.NoError(t, err)
			}
			_, err := db.Exec(`UPDATE layout_revisions SET ` + column + `='bad' WHERE revision='r'`)
			require.NoError(t, err)
			_, err = repo.Get(ctx, "r")
			require.ErrorIs(t, err, layout.ErrStoreCorrupt)
		})
	}
	for _, column := range []string{"geometry", "review", "preflight", "score"} {
		t.Run(column, func(t *testing.T) {
			db := setupTestDB(t).DB
			repo := NewLayoutRepo(db)
			_, f := layoutRequest(t, repo, "r")
			result := layoutResult(t, f)
			require.NoError(t, repo.Checkpoint(ctx, f, result, true, result.CreatedAt))
			got, err := repo.Result(ctx, "r", 1)
			require.NoError(t, err)
			assert.Nil(t, got.Review)
			assert.Equal(t, result, got)
			_, err = db.Exec(`UPDATE layout_results SET ` + column + `='bad' WHERE revision='r'`)
			require.NoError(t, err)
			_, err = repo.Result(ctx, "r", 1)
			require.ErrorIs(t, err, layout.ErrStoreCorrupt)
		})
	}
	db := setupTestDB(t).DB
	repo := NewLayoutRepo(db)
	_, f := layoutRequest(t, repo, "r")
	require.NoError(t, db.Close())
	assertLayoutUnavailable(t, repo, f)
}

func assertLayoutUnavailable(t *testing.T, r *LayoutRepo, f layout.Fence) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	dest := layout.Destination{Scope: workflow.Shared(), Name: "sample"}
	_, _, err := r.Request(ctx, layout.NewRequest{Revision: "r", Manifest: []byte("{}"), AttemptLimit: 1})
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = r.Get(ctx, "r")
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = r.ListActive(ctx, dest)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = r.Claimable(ctx, now, 1)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = r.Claim(ctx, "r", "worker", now, now)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	assert.ErrorIs(t, r.Progress(ctx, f, layout.StageRendering, 1, now, now), layout.ErrStoreUnavailable)
	assert.ErrorIs(t, r.Complete(ctx, f, layout.Completion{State: layout.StateFailed}, now), layout.ErrStoreUnavailable)
	assert.ErrorIs(t, r.Checkpoint(ctx, f, layoutResult(t, f), true, now), layout.ErrStoreUnavailable)
	assert.ErrorIs(t, r.Supersede(ctx, "r", now), layout.ErrStoreUnavailable)
	_, err = r.Retry(ctx, "r", 1, now)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = r.Relocate(ctx, dest, dest, now)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = r.Result(ctx, "r", 1)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = r.PendingCache(ctx, 1)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	assert.ErrorIs(t, r.MarkCached(ctx, f, 1, dest), layout.ErrStoreUnavailable)
	assert.ErrorIs(t, r.InvalidateResult(ctx, f, 1, "bad", now), layout.ErrStoreUnavailable)
}

func layoutPeers(t *testing.T) (*LayoutRepo, *LayoutRepo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "layout.db")
	a, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { a.Close() })
	require.NoError(t, RunMigrations(a))
	b, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { b.Close() })
	return NewLayoutRepo(a), NewLayoutRepo(b)
}

func TestLayoutIndependentClaims(t *testing.T) {
	a, b := layoutPeers(t)
	ctx := context.Background()
	original, f := layoutRequest(t, a, "r")
	now := original.RequestedAt.Add(time.Second)
	for generation := int64(2); generation <= 12; generation++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		answers := make(chan layout.Record, 2)
		errs := make(chan error, 2)
		for i, repo := range []*LayoutRepo{a, b} {
			owner := []string{"one", "two"}[i]
			wg.Go(func() {
				<-start
				row, err := repo.Claim(ctx, "r", owner, now, now.Add(100*time.Millisecond))
				if err != nil {
					errs <- err
					return
				}
				assert.Equal(t, owner, row.LeaseOwner)
				answers <- row
			})
		}
		close(start)
		wg.Wait()
		close(answers)
		close(errs)
		require.Len(t, answers, 1)
		require.Len(t, errs, 1)
		for err := range errs {
			require.ErrorIs(t, err, layout.ErrNotClaimable)
		}
		var winner layout.Record
		for row := range answers {
			winner = row
			require.Equal(t, generation, row.JobGeneration)
			assert.Equal(t, now, *row.StartedAt)
			assert.Equal(t, now.Add(100*time.Millisecond), *row.LeaseExpiresAt)
		}
		assert.ErrorIs(t, a.Progress(ctx, f, layout.StageRendering, 99, now, now), layout.ErrStaleGeneration)
		assert.ErrorIs(t, b.Complete(ctx, f, layout.Completion{State: layout.StateFailed}, now), layout.ErrStaleGeneration)
		assert.ErrorIs(t, a.Checkpoint(ctx, f, layoutResult(t, f), true, now), layout.ErrStaleGeneration)
		unchanged, err := b.Get(ctx, "r")
		require.NoError(t, err)
		assert.Equal(t, winner, unchanged, "stale callers must not alter the actual row")
		f.Generation = generation
		now = now.Add(time.Second)
	}
}

func TestLayoutResultTransactionRollback(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "checkpoint"}[checkpoint], func(t *testing.T) {
			a, b := layoutPeers(t)
			ctx := context.Background()
			_, f := layoutRequest(t, a, "r")
			best := layoutResult(t, f)
			before, err := b.Get(ctx, "r")
			require.NoError(t, err)
			_, err = a.db.Exec(`CREATE TRIGGER reject_best BEFORE UPDATE OF result_generation ON layout_revisions BEGIN SELECT RAISE(ABORT,'injected best failure'); END`)
			require.NoError(t, err)
			persist := func() error {
				if checkpoint {
					return a.Checkpoint(ctx, f, best, true, best.CreatedAt)
				}
				return a.Complete(ctx, f, layout.Completion{State: layout.StateNeedsAttention, Result: &best, ReplaceBest: true}, best.CreatedAt)
			}
			require.ErrorIs(t, persist(), layout.ErrStoreUnavailable)
			after, err := b.Get(ctx, "r")
			require.NoError(t, err)
			assert.Equal(t, before, after)
			_, err = b.Result(ctx, "r", 1)
			require.ErrorIs(t, err, layout.ErrRecordNotFound)
			_, err = a.db.Exec(`DROP TRIGGER reject_best`)
			require.NoError(t, err)
			require.NoError(t, persist())
			if checkpoint {
				require.NoError(t, persist())
				row, err := b.Get(ctx, "r")
				require.NoError(t, err)
				assert.Equal(t, before.Stage, row.Stage)
				assert.Equal(t, before.LeaseExpiresAt, row.LeaseExpiresAt)
				assert.Equal(t, layout.StateInProgress, row.State)
				renewed, err := b.Claim(ctx, "r", "peer", before.LeaseExpiresAt.Add(time.Nanosecond), best.CreatedAt.Add(time.Second))
				require.NoError(t, err)
				assert.EqualValues(t, 2, renewed.JobGeneration)
				assert.ErrorIs(t, a.Checkpoint(ctx, f, best, true, best.CreatedAt), layout.ErrStaleGeneration)
			}
			got, err := b.Result(ctx, "r", 1)
			require.NoError(t, err)
			assert.Equal(t, best, got)
		})
	}
}

func TestLayoutDisplayedFallback(t *testing.T) {
	ctx := context.Background()
	a, b := layoutPeers(t)
	original, f := layoutRequest(t, a, "r")
	best := layoutResult(t, f)
	now := best.CreatedAt
	_, ok, err := a.DisplayedLayout(ctx, "r")
	require.NoError(t, err)
	assert.False(t, ok)
	require.NoError(t, a.Complete(ctx, f, layout.Completion{State: layout.StateNeedsAttention, Result: &best, ReplaceBest: true}, now))
	expected := workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: "r", FromGeneration: 1, Positions: map[string]workflow.NodePosition{}}
	for _, node := range best.Geometry.Nodes {
		expected.Positions[node.ID] = workflow.NodePosition{X: node.Box.X, Y: node.Box.Y}
	}
	assertDisplayed := func() {
		t.Helper()
		got, ok, err := b.DisplayedLayout(ctx, "r")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, expected, got)
	}
	assertDisplayed()
	_, err = a.Retry(ctx, "r", 5, now)
	require.NoError(t, err)
	_, err = a.Claim(ctx, "r", "retry", now, now.Add(time.Second))
	require.NoError(t, err)
	f.Generation = 2
	worse := layoutResult(t, f)
	worse.Geometry.Nodes[0].Box.X += 1
	require.NoError(t, a.Complete(ctx, f, layout.Completion{State: layout.StateFailed, Result: &worse}, now))
	_, err = a.Relocate(ctx, original.Destination, layout.Destination{Scope: workflow.Shared(), Name: "moved"}, now)
	require.NoError(t, err)
	assertDisplayed()
	// No source tree, identity, publication or cache exists in this database.
	got, ok, err := b.DisplayedLayout(ctx, "r")
	require.NoError(t, err)
	require.True(t, ok)
	got.Positions[best.Geometry.Nodes[0].ID] = workflow.NodePosition{X: 999}
	assertDisplayed()
	_, ok, err = b.DisplayedLayout(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, ok)
	_, err = a.db.Exec(`DELETE FROM layout_results WHERE revision='r' AND generation=1`)
	require.NoError(t, err)
	_, ok, err = b.DisplayedLayout(ctx, "r")
	assert.False(t, ok)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
}

func TestLayoutDisplayedCorrupt(t *testing.T) {
	for _, corruption := range []string{"manifest", "geometry", "contract", "text"} {
		t.Run(corruption, func(t *testing.T) {
			ctx := context.Background()
			r := NewLayoutRepo(setupTestDB(t).DB)
			_, f := layoutRequest(t, r, "r")
			best := layoutResult(t, f)
			if corruption == "contract" {
				best.Geometry.Contract.NodePaddingX++
			}
			if corruption == "text" {
				best.Geometry.Nodes[0].Text = "wrong graph"
			}
			require.NoError(t, r.Checkpoint(ctx, f, best, true, best.CreatedAt))
			if corruption == "manifest" {
				_, err := r.db.Exec(`UPDATE layout_revisions SET input_manifest=x'626164' WHERE revision='r'`)
				require.NoError(t, err)
			}
			if corruption == "geometry" {
				_, err := r.db.Exec(`UPDATE layout_results SET geometry='bad' WHERE revision='r'`)
				require.NoError(t, err)
			}
			_, ok, err := r.DisplayedLayout(ctx, "r")
			assert.False(t, ok)
			if corruption == "geometry" {
				assert.ErrorIs(t, err, layout.ErrStoreCorrupt)
			} else {
				assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
			}
		})
	}
}

func TestLayoutPeerCacheSettlement(t *testing.T) {
	a, b := layoutPeers(t)
	ctx := context.Background()
	original, f := layoutRequest(t, a, "r")
	best := layoutResult(t, f)
	now := best.CreatedAt
	require.NoError(t, a.Complete(ctx, f, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true}, now))
	require.NoError(t, a.InvalidateResult(ctx, f, 1, "bad evidence", now))
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		err := a.MarkCached(ctx, f, 1, original.Destination)
		if err != nil {
			assert.ErrorIs(t, err, layout.ErrStaleGeneration)
		}
	})
	wg.Go(func() {
		<-start
		row, err := b.Retry(ctx, "r", 3, now)
		assert.NoError(t, err)
		assert.EqualValues(t, 2, row.JobGeneration)
	})
	close(start)
	wg.Wait()
	row, err := a.Get(ctx, "r")
	require.NoError(t, err)
	assert.Zero(t, row.CacheGeneration)
	assert.ErrorIs(t, b.InvalidateResult(ctx, f, 1, "late", now), layout.ErrStaleGeneration)
	assert.ErrorIs(t, a.MarkCached(ctx, f, 1, original.Destination), layout.ErrStaleGeneration)
	f.Generation = 2
	moved := layout.Destination{Scope: workflow.Private(uuid.New()), Name: "moved"}
	start = make(chan struct{})
	wg.Go(func() {
		<-start
		err := a.MarkCached(ctx, f, 1, original.Destination)
		if err != nil {
			assert.ErrorIs(t, err, layout.ErrStaleGeneration)
		}
	})
	wg.Go(func() {
		<-start
		n, err := b.Relocate(ctx, original.Destination, moved, now)
		assert.NoError(t, err)
		assert.EqualValues(t, 1, n)
	})
	close(start)
	wg.Wait()
	row, err = a.Get(ctx, "r")
	require.NoError(t, err)
	assert.Zero(t, row.CacheGeneration)
	assert.Equal(t, moved, row.Destination)
	assert.ErrorIs(t, a.MarkCached(ctx, f, 1, original.Destination), layout.ErrStaleGeneration)
	require.NoError(t, b.MarkCached(ctx, f, 1, moved))
	pending, err := a.PendingCache(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, pending)
}
