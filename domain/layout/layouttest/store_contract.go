package layouttest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunStoreContract is shared, unchanged, by memory and durable Store implementations.
func RunStoreContract(t *testing.T, newStore func(*testing.T) layout.Store) {
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 123456789, time.FixedZone("offset", 3600)).UTC()
	dest := layout.Destination{Scope: workflow.Private(uuid.New()), Name: "sample", WorkflowID: uuid.New()}
	request := func(rev string) layout.NewRequest {
		return layout.NewRequest{Revision: rev, Destination: dest, Reason: workflow.SaveReasonUpdate, Manifest: []byte("captured:" + rev), Retained: workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"start": {X: 12, Y: 34}}}, AttemptLimit: 5, RequestedAt: now}
	}
	result := func(t *testing.T, rev string, gen int64) layout.StoredResult {
		geo := Arranged(t, SampleWorkflow())
		return layout.StoredResult{Revision: rev, Generation: gen, Geometry: geo, Review: &layout.Review{Rendered: true, Evidence: layout.Evidence{GeometryDigest: layout.GeometryDigest(geo), RendererVersion: layout.ContractVersion, Contract: geo.Contract, FontsReady: true, Zoom: geo.Contract.PracticalZoom, Tiles: 1, Artifacts: Artifacts(geo), ReviewedAt: now}}, Verdict: layout.VerdictPassed, CreatedAt: now}
	}
	seed := func(t *testing.T, s layout.Store, rev string) layout.Record {
		t.Helper()
		r, created, err := s.Request(ctx, request(rev))
		require.NoError(t, err)
		require.True(t, created)
		return r
	}
	claim := func(t *testing.T, s layout.Store, rev string) layout.Fence {
		t.Helper()
		r, err := s.Claim(ctx, rev, "worker", now, now.Add(time.Second))
		require.NoError(t, err)
		return layout.Fence{Revision: rev, Generation: r.JobGeneration}
	}
	t.Run("Group JSON round trip and retained retry", func(t *testing.T) {
		for _, name := range []string{"branches", "workflows", "nested", "loop"} {
			t.Run(name, func(t *testing.T) {
				s := newStore(t)
				wf, geo, review := CapturedGroup(t, name)
				req := request("group-" + name)
				wf.Presentation = &workflow.Presentation{Revision: req.Revision, SavedAt: now, Scope: workflow.ScopeLabelShared, Name: "sample", Reason: workflow.SaveReasonUpdate, Retained: req.Retained}
				req.Manifest = ManifestJSON(t, wf)
				_, _, err := s.Request(ctx, req)
				require.NoError(t, err)
				f := claim(t, s, req.Revision)
				best := layout.StoredResult{Revision: f.Revision, Generation: 1, Geometry: geo, Review: review, Verdict: layout.VerdictNeedsAttention, CreatedAt: now, Preflight: []layout.Finding{{Criterion: layout.CriterionCrossings, Severity: layout.SeverityBlocking, Source: layout.SourcePreflight, Evidence: "retained candidate"}}}
				best.Score = layout.ScoreOf(1, best.UnresolvedFindings(), layout.CrossingCount(geo))
				require.NoError(t, s.Complete(ctx, f, layout.Completion{State: layout.StateNeedsAttention, Result: &best, ReplaceBest: true, AttemptsUsed: 1}, now))
				got, err := s.Result(ctx, f.Revision, 1)
				require.NoError(t, err)
				require.Equal(t, best, got)
				view := layout.NewReader(s, nil, nil).View(ctx, wf)
				require.NotNil(t, view.Geometry)
				require.Equal(t, geo, *view.Geometry)
				viewed := review.Evidence.ForViewer()
				require.Equal(t, &viewed, view.Evidence)
				require.Equal(t, best.UnresolvedFindings(), view.Findings)
				_, err = s.Retry(ctx, f.Revision, 5, now)
				require.NoError(t, err)
				f = claim(t, s, f.Revision)
				worse := best
				worse.Generation = 2
				worse.Review = nil
				worse.Verdict = layout.VerdictFailed
				worse.Score = layout.ScoreOf(2, worse.Preflight, layout.CrossingCount(geo))
				require.NoError(t, s.Complete(ctx, f, layout.Completion{State: layout.StateFailed, Result: &worse, ReplaceBest: false, FailureStage: layout.StageRendering, FailureReason: "later attempt failed"}, now))
				row, err := s.Get(ctx, f.Revision)
				require.NoError(t, err)
				require.Equal(t, int64(2), row.JobGeneration)
				require.Equal(t, int64(1), row.ResultGeneration)
				got, err = s.Result(ctx, f.Revision, 1)
				require.NoError(t, err)
				require.Equal(t, best, got)
				after := layout.NewReader(s, nil, nil).View(ctx, wf)
				require.Equal(t, view.Geometry, after.Geometry)
				require.Equal(t, view.Evidence, after.Evidence)
				require.Equal(t, view.Findings, after.Findings)
				geo.GroupElements[0].Texts[0].Text = "response mutation"
				got, err = s.Result(ctx, f.Revision, 1)
				require.NoError(t, err)
				require.NotEqual(t, "response mutation", got.Geometry.GroupElements[0].Texts[0].Text)
			})
		}
	})
	t.Run("request immutable and owned", func(t *testing.T) {
		s := newStore(t)
		req := request("r")
		r, created, err := s.Request(ctx, req)
		require.NoError(t, err)
		require.True(t, created)
		require.Equal(t, req.Manifest, r.Manifest)
		assert.Equal(t, layout.StatePending, r.State)
		assert.Equal(t, layout.StageQueued, r.Stage)
		assert.EqualValues(t, 1, r.JobGeneration)
		assert.Equal(t, 5, r.AttemptLimit)
		assert.Equal(t, now, r.RequestedAt)
		assert.NotNil(t, r.Findings)
		assert.True(t, r.Unreviewed)
		assert.Zero(t, r.ResultGeneration)
		assert.Zero(t, r.CacheGeneration)
		assert.Nil(t, r.LeaseExpiresAt)
		req.Manifest[0] = 'X'
		req.Retained.Positions["start"] = workflow.NodePosition{X: 999}
		r.Manifest[0] = 'Y'
		r.Retained.Positions["start"] = workflow.NodePosition{X: 888}
		got, err := s.Get(ctx, "r")
		require.NoError(t, err)
		assert.Equal(t, request("r"), layout.NewRequest{Revision: got.Revision, Destination: got.Destination, Reason: got.Reason, Manifest: got.Manifest, Retained: got.Retained, AttemptLimit: got.AttemptLimit, RequestedAt: got.RequestedAt})
		dup := request("r")
		dup.Manifest = []byte("new")
		dup.AttemptLimit = 2
		dup.Destination.Name = "other"
		dup.RequestedAt = now.Add(time.Hour)
		again, created, err := s.Request(ctx, dup)
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, got, again)
		other := seed(t, s, "s")
		assert.Equal(t, layout.StatePending, other.State)
		active, err := s.ListActive(ctx, dest)
		require.NoError(t, err)
		require.Len(t, active, 2)
		active[0].Manifest[0] = 'Z'
		fresh, _ := s.Get(ctx, active[0].Revision)
		assert.NotEqual(t, active[0].Manifest, fresh.Manifest)
		_, err = s.Get(ctx, "missing")
		assert.ErrorIs(t, err, layout.ErrRecordNotFound)
	})
	t.Run("lease precision progress reclaim fences", func(t *testing.T) {
		s := newStore(t)
		seed(t, s, "b")
		seed(t, s, "a")
		ids, err := s.Claimable(ctx, now, 1)
		require.NoError(t, err)
		assert.Equal(t, []string{"a"}, ids)
		until := now.Add(100 * time.Millisecond)
		r, err := s.Claim(ctx, "a", "one", now, until)
		require.NoError(t, err)
		assert.EqualValues(t, 1, r.JobGeneration)
		assert.Equal(t, "one", r.LeaseOwner)
		assert.Equal(t, until, *r.LeaseExpiresAt)
		assert.Equal(t, now, *r.StartedAt)
		_, err = s.Claim(ctx, "a", "two", until, until.Add(time.Second))
		assert.ErrorIs(t, err, layout.ErrNotClaimable)
		f := layout.Fence{Revision: "a", Generation: 1}
		renewed := until.Add(100 * time.Millisecond)
		require.NoError(t, s.Progress(ctx, f, layout.StageReviewing, 3, until, renewed))
		ids, err = s.Claimable(ctx, until.Add(time.Nanosecond), 0)
		require.NoError(t, err)
		assert.Equal(t, []string{"b"}, ids)
		r, err = s.Claim(ctx, "a", "two", renewed.Add(time.Nanosecond), renewed.Add(time.Second))
		require.NoError(t, err)
		assert.EqualValues(t, 2, r.JobGeneration)
		assert.Equal(t, "two", r.LeaseOwner)
		assert.Zero(t, r.AttemptsUsed)
		assert.ErrorIs(t, s.Progress(ctx, f, layout.StageRendering, 99, now, until), layout.ErrStaleGeneration)
		assert.ErrorIs(t, s.Complete(ctx, f, layout.Completion{State: layout.StateFailed}, now), layout.ErrStaleGeneration)
		assert.ErrorIs(t, s.Checkpoint(ctx, f, result(t, "a", 1), true, now), layout.ErrStaleGeneration)
		got, _ := s.Get(ctx, "a")
		assert.Equal(t, r, got)
	})
	t.Run("checkpoint complete retry immutable best", func(t *testing.T) {
		s := newStore(t)
		seed(t, s, "r")
		f := claim(t, s, "r")
		best := result(t, "r", 1)
		before, _ := s.Get(ctx, "r")
		require.NoError(t, s.Checkpoint(ctx, f, best, true, now))
		checkpoint, _ := s.Get(ctx, "r")
		assert.Equal(t, before.State, checkpoint.State)
		assert.Equal(t, before.Stage, checkpoint.Stage)
		assert.Equal(t, before.LeaseExpiresAt, checkpoint.LeaseExpiresAt)
		assert.EqualValues(t, 1, checkpoint.ResultGeneration)
		require.NoError(t, s.Checkpoint(ctx, f, best, true, now))
		conflict := best
		conflict.Verdict = layout.VerdictFailed
		require.Error(t, s.Checkpoint(ctx, f, conflict, true, now))
		after, _ := s.Get(ctx, "r")
		assert.Equal(t, checkpoint, after)
		require.Error(t, s.Complete(ctx, f, layout.Completion{State: layout.StateFailed, Result: &conflict, ReplaceBest: true}, now))
		after, _ = s.Get(ctx, "r")
		assert.Equal(t, checkpoint, after)
		require.NoError(t, s.Complete(ctx, f, layout.Completion{State: layout.StateNeedsAttention, AttemptsUsed: 5, Result: &best, ReplaceBest: true}, now))
		got, _ := s.Get(ctx, "r")
		assert.Equal(t, layout.StateNeedsAttention, got.State)
		assert.Equal(t, layout.StageDone, got.Stage)
		assert.Equal(t, 5, got.AttemptsUsed)
		assert.Nil(t, got.LeaseExpiresAt)
		assert.NotNil(t, got.FinishedAt)
		assert.False(t, got.Unreviewed)
		require.NoError(t, s.MarkCached(ctx, f, 1, dest))
		pending, _ := s.PendingCache(ctx, 0)
		assert.Empty(t, pending)
		retried, err := s.Retry(ctx, "r", 3, now.Add(time.Second))
		require.NoError(t, err)
		assert.EqualValues(t, 2, retried.JobGeneration)
		assert.EqualValues(t, 1, retried.ResultGeneration)
		assert.Zero(t, retried.CacheGeneration)
		assert.Equal(t, 3, retried.AttemptLimit)
		assert.Nil(t, retried.StartedAt)
		assert.Nil(t, retried.FinishedAt)
		pending, _ = s.PendingCache(ctx, 0)
		require.Len(t, pending, 1)
		assert.ErrorIs(t, s.MarkCached(ctx, f, 1, dest), layout.ErrStaleGeneration)
		f = claim(t, s, "r")
		worse := result(t, "r", 2)
		worse.Verdict = layout.VerdictNeedsAttention
		require.NoError(t, s.Complete(ctx, f, layout.Completion{State: layout.StateFailed, FailureStage: layout.StageReviewing, FailureReason: "timeout", Result: &worse}, now.Add(2*time.Second)))
		got, _ = s.Get(ctx, "r")
		assert.EqualValues(t, 1, got.ResultGeneration)
		assert.Equal(t, layout.VerdictPassed, got.Verdict)
		assert.Equal(t, "timeout", got.FailureReason)
		stored, err := s.Result(ctx, "r", 1)
		require.NoError(t, err)
		assert.Equal(t, best, stored)
		stored.Geometry.Nodes[0].Text = "mutated"
		stored.Review.Evidence.Artifacts[0].Path = "mutated"
		fresh, _ := s.Result(ctx, "r", 1)
		assert.Equal(t, best, fresh)
		_, err = s.Result(ctx, "r", 99)
		assert.ErrorIs(t, err, layout.ErrRecordNotFound)
	})
	t.Run("relocation full locator cache fences", func(t *testing.T) {
		s := newStore(t)
		original := seed(t, s, "r")
		f := claim(t, s, "r")
		best := result(t, "r", 1)
		require.NoError(t, s.Complete(ctx, f, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true}, now))
		require.NoError(t, s.MarkCached(ctx, f, 1, dest))
		to := layout.Destination{Scope: workflow.Scope{Kind: workflow.ScopeShared, OwnerID: uuid.New()}, Name: "moved"}
		n, err := s.Relocate(ctx, dest, to, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)
		moved, _ := s.Get(ctx, "r")
		assert.Equal(t, workflow.Shared(), moved.Destination.Scope)
		assert.Equal(t, dest.WorkflowID, moved.Destination.WorkflowID)
		assert.Equal(t, original.Manifest, moved.Manifest)
		assert.Equal(t, original.Retained, moved.Retained)
		assert.Zero(t, moved.CacheGeneration)
		pending, _ := s.PendingCache(ctx, 1)
		require.Len(t, pending, 1)
		assert.ErrorIs(t, s.MarkCached(ctx, f, 1, dest), layout.ErrStaleGeneration)
		assert.ErrorIs(t, s.MarkCached(ctx, f, 2, moved.Destination), layout.ErrStaleGeneration)
		require.NoError(t, s.MarkCached(ctx, f, 1, moved.Destination))
		n, err = s.Relocate(ctx, moved.Destination, moved.Destination, now)
		require.NoError(t, err)
		assert.Zero(t, n)
		moved, _ = s.Get(ctx, "r")
		assert.EqualValues(t, 1, moved.CacheGeneration)
		_, _, err = s.Request(ctx, request("r"))
		require.NoError(t, err)
		replay, _ := s.Get(ctx, "r")
		assert.Equal(t, moved, replay)
	})
	// A publication can finish just before identity metadata moves at the same
	// locator. Its acknowledgement must not hide the new destination's work.
	t.Run("cache settlement fences identity at unchanged locator", func(t *testing.T) {
		for _, initialID := range []uuid.UUID{uuid.Nil, dest.WorkflowID} {
			t.Run(initialID.String(), func(t *testing.T) {
				s := newStore(t)
				req := request("r")
				req.Destination.WorkflowID = initialID
				_, _, err := s.Request(ctx, req)
				require.NoError(t, err)
				f := claim(t, s, "r")
				best := result(t, "r", 1)
				require.NoError(t, s.Complete(ctx, f, layout.Completion{State: layout.StatePassed, AttemptsUsed: 1, Result: &best, ReplaceBest: true}, now))
				// Successful publication of the old identity remains acknowledged
				// until Relocate resets it; neither job nor result generation changes.
				require.NoError(t, s.MarkCached(ctx, f, 1, req.Destination))
				moved := req.Destination
				moved.WorkflowID = uuid.New()
				n, err := s.Relocate(ctx, req.Destination, moved, now.Add(time.Second))
				require.NoError(t, err)
				require.EqualValues(t, 1, n)
				before, err := s.Get(ctx, "r")
				require.NoError(t, err)
				require.Zero(t, before.CacheGeneration)
				assert.ErrorIs(t, s.MarkCached(ctx, f, 1, req.Destination), layout.ErrStaleGeneration)
				after, err := s.Get(ctx, "r")
				require.NoError(t, err)
				assert.Equal(t, before, after, "stale acknowledgement must leave the authoritative row unchanged")
				pending, err := s.PendingCache(ctx, 0)
				require.NoError(t, err)
				require.Len(t, pending, 1, "the current destination must still be reconciled")
				assert.Equal(t, moved, pending[0].Destination)
				require.NoError(t, s.MarkCached(ctx, f, 1, moved))
				pending, err = s.PendingCache(ctx, 0)
				require.NoError(t, err)
				assert.Empty(t, pending)
				stored, err := s.Result(ctx, "r", 1)
				require.NoError(t, err)
				assert.Equal(t, best, stored, "publication races preserve immutable evidence")
			})
		}
	})
	t.Run("supersede and invalidation", func(t *testing.T) {
		for _, superseded := range []bool{false, true} {
			t.Run(map[bool]string{false: "passed", true: "superseded pass"}[superseded], func(t *testing.T) {
				s := newStore(t)
				seed(t, s, "r")
				f := claim(t, s, "r")
				best := result(t, "r", 1)
				if superseded {
					require.NoError(t, s.Supersede(ctx, "r", now))
				}
				require.NoError(t, s.Complete(ctx, f, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true}, now))
				got, _ := s.Get(ctx, "r")
				if superseded {
					assert.Equal(t, layout.StateSuperseded, got.State)
				} else {
					assert.Equal(t, layout.StatePassed, got.State)
				}
				_, err := s.Retry(ctx, "r", 5, now)
				assert.ErrorIs(t, err, layout.ErrRetryConflict)
				assert.ErrorIs(t, s.InvalidateResult(ctx, layout.Fence{Revision: "r", Generation: 2}, 1, "bad", now), layout.ErrStaleGeneration)
				assert.ErrorIs(t, s.InvalidateResult(ctx, f, 2, "bad", now), layout.ErrStaleGeneration)
				unchanged, _ := s.Get(ctx, "r")
				assert.Equal(t, got, unchanged)
				require.NoError(t, s.InvalidateResult(ctx, f, 1, "bad evidence", now))
				got, _ = s.Get(ctx, "r")
				assert.Equal(t, layout.StateNeedsAttention, got.State)
				assert.Equal(t, "bad evidence", got.FailureReason)
				retried, err := s.Retry(ctx, "r", 4, now)
				require.NoError(t, err)
				assert.EqualValues(t, 2, retried.JobGeneration)
				assert.EqualValues(t, 1, retried.ResultGeneration)
			})
		}
		s := newStore(t)
		seed(t, s, "p")
		require.NoError(t, s.Supersede(ctx, "p", now))
		r, err := s.Retry(ctx, "p", 1, now)
		require.NoError(t, err)
		assert.EqualValues(t, 2, r.JobGeneration)
		assert.ErrorIs(t, s.InvalidateResult(ctx, layout.Fence{Revision: "missing", Generation: 1}, 0, "bad", now), layout.ErrRecordNotFound)
	})
	t.Run("missing and cancelled every method", func(t *testing.T) {
		s := newStore(t)
		f := layout.Fence{Revision: "missing", Generation: 1}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		for _, c := range []context.Context{ctx, cancelled} {
			want := error(layout.ErrRecordNotFound)
			if c == cancelled {
				want = context.Canceled
			}
			_, err := s.Get(c, "missing")
			assert.ErrorIs(t, err, want)
			_, err = s.Claim(c, "missing", "worker", now, now.Add(time.Second))
			assert.ErrorIs(t, err, want)
			assert.ErrorIs(t, s.Progress(c, f, layout.StageRendering, 1, now, now), want)
			assert.ErrorIs(t, s.Complete(c, f, layout.Completion{State: layout.StateFailed}, now), want)
			assert.ErrorIs(t, s.Checkpoint(c, f, result(t, "missing", 1), true, now), want)
			assert.ErrorIs(t, s.Supersede(c, "missing", now), want)
			_, err = s.Retry(c, "missing", 1, now)
			assert.ErrorIs(t, err, want)
			_, err = s.Result(c, "missing", 1)
			assert.ErrorIs(t, err, want)
			assert.ErrorIs(t, s.MarkCached(c, f, 1, dest), want)
			assert.ErrorIs(t, s.InvalidateResult(c, f, 1, "bad", now), want)
		}
		_, _, err := s.Request(cancelled, request("r"))
		assert.ErrorIs(t, err, context.Canceled)
		_, err = s.ListActive(cancelled, dest)
		assert.ErrorIs(t, err, context.Canceled)
		_, err = s.Claimable(cancelled, now, 0)
		assert.ErrorIs(t, err, context.Canceled)
		_, err = s.PendingCache(cancelled, 0)
		assert.ErrorIs(t, err, context.Canceled)
		_, err = s.Relocate(cancelled, dest, dest, now)
		assert.ErrorIs(t, err, context.Canceled)
	})
	t.Run("UTC precision and cancelled writes preserve actual state", func(t *testing.T) {
		s := newStore(t)
		req := request("r")
		req.RequestedAt = now.In(time.FixedZone("offset", 3600))
		row, _, err := s.Request(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.UTC, row.RequestedAt.Location())
		assert.Equal(t, now.UnixNano(), row.RequestedAt.UnixNano())
		f := claim(t, s, "r")
		before, _ := s.Get(ctx, "r")
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		best := result(t, "r", 1)
		assert.ErrorIs(t, s.Complete(cancelled, f, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true}, now), context.Canceled)
		assert.ErrorIs(t, s.Checkpoint(cancelled, f, best, true, now), context.Canceled)
		after, _ := s.Get(ctx, "r")
		assert.Equal(t, before, after)
		_, err = s.Result(ctx, "r", 1)
		assert.ErrorIs(t, err, layout.ErrRecordNotFound)
	})
	t.Run("failed insertion leaves no result and result input is owned", func(t *testing.T) {
		s := newStore(t)
		seed(t, s, "r")
		f := claim(t, s, "r")
		best := result(t, "r", 1)
		before, _ := s.Get(ctx, "r")
		bad := best
		bad.Generation = 2
		require.Error(t, s.Complete(ctx, f, layout.Completion{State: layout.StatePassed, Result: &bad, ReplaceBest: true}, now))
		require.Error(t, s.Checkpoint(ctx, f, bad, true, now))
		after, _ := s.Get(ctx, "r")
		assert.Equal(t, before, after)
		_, err := s.Result(ctx, "r", 1)
		assert.ErrorIs(t, err, layout.ErrRecordNotFound)
		require.NoError(t, s.Checkpoint(ctx, f, best, true, now))
		best.Geometry.Nodes[0].Text = "mutated"
		best.Review.Evidence.Artifacts[0].Path = "mutated"
		got, err := s.Result(ctx, "r", 1)
		require.NoError(t, err)
		assert.NotEqual(t, "mutated", got.Geometry.Nodes[0].Text)
		assert.NotEqual(t, "mutated", got.Review.Evidence.Artifacts[0].Path)
		// Reclaim after a checkpoint preserves its immutable best but writes to a new key.
		r, err := s.Claim(ctx, "r", "reclaimed", now.Add(2*time.Second), now.Add(3*time.Second))
		require.NoError(t, err)
		assert.EqualValues(t, 2, r.JobGeneration)
		assert.EqualValues(t, 1, r.ResultGeneration)
		newer := result(t, "r", 2)
		require.NoError(t, s.Complete(ctx, layout.Fence{Revision: "r", Generation: 2}, layout.Completion{State: layout.StatePassed, Result: &newer, ReplaceBest: true}, now.Add(3*time.Second)))
		got, err = s.Result(ctx, "r", 1)
		require.NoError(t, err)
		assert.NotEqual(t, "mutated", got.Geometry.Nodes[0].Text)
		terminal, _ := s.Get(ctx, "r")
		require.NoError(t, s.Supersede(ctx, "r", now))
		same, _ := s.Get(ctx, "r")
		assert.Equal(t, terminal, same)
		assert.ErrorIs(t, s.Progress(ctx, layout.Fence{Revision: "r", Generation: 2}, layout.StageRendering, 1, now, now), layout.ErrStaleGeneration)
		_, err = s.Claim(ctx, "r", "late", now, now.Add(time.Hour))
		assert.ErrorIs(t, err, layout.ErrNotClaimable)
	})
	t.Run("locator isolation and pending cache order", func(t *testing.T) {
		s := newStore(t)
		for _, rev := range []string{"b", "a"} {
			seed(t, s, rev)
			f := claim(t, s, rev)
			best := result(t, rev, 1)
			require.NoError(t, s.Complete(ctx, f, layout.Completion{State: layout.StateFailed, Result: &best, ReplaceBest: true}, now))
		}
		pending, err := s.PendingCache(ctx, 1)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		assert.Equal(t, "a", pending[0].Revision)
		pending[0].Manifest[0] = 'X'
		fresh, _ := s.Get(ctx, "a")
		assert.Equal(t, request("a").Manifest, fresh.Manifest)
		foreign := dest
		foreign.Scope = workflow.Private(uuid.New())
		active, err := s.ListActive(ctx, foreign)
		require.NoError(t, err)
		assert.Empty(t, active)
		n, err := s.Relocate(ctx, foreign, layout.Destination{Scope: workflow.Shared(), Name: "other"}, now)
		require.NoError(t, err)
		assert.Zero(t, n)
		assert.ErrorIs(t, s.MarkCached(ctx, layout.Fence{Revision: "a", Generation: 1}, 1, foreign), layout.ErrStaleGeneration)
		pending, err = s.PendingCache(ctx, 0)
		require.NoError(t, err)
		require.Len(t, pending, 2)
	})
	t.Run("atomic concurrent claim returns own fence", func(t *testing.T) {
		s := newStore(t)
		seed(t, s, "r")
		var wg sync.WaitGroup
		start := make(chan struct{})
		answers := make(chan layout.Record, 2)
		errs := make(chan error, 2)
		for _, owner := range []string{"one", "two"} {
			wg.Go(func() {
				<-start
				r, err := s.Claim(ctx, "r", owner, now, now.Add(time.Second))
				if err != nil {
					errs <- err
				} else {
					assert.Equal(t, owner, r.LeaseOwner)
					answers <- r
				}
			})
		}
		close(start)
		wg.Wait()
		close(answers)
		close(errs)
		assert.Len(t, answers, 1)
		assert.Len(t, errs, 1)
		for err := range errs {
			assert.ErrorIs(t, err, layout.ErrNotClaimable)
		}
	})
}
