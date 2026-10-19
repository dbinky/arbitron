package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Paired seam 6 (file gather ↔ conflict resolution): gather_json has two
// writers. The gatherer writes under the gather gate; the session-end callback
// (OnResolutionSessionEnded) writes from the conversation, outside it. Each
// direction below interleaves one writer between the other's read and write.

// Callback → gatherer: the gatherer committed the resolution and moved on
// after the callback read the report. The callback must not write its stale
// copy back (which would rewind MergeIndex and re-open the finished attempt).
func TestSeamGatherResolution_SessionEndNeverRewindsGathererProgress(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "g1")
	injected := false
	f.groups.onCall = func(method string) {
		if method != "ReplaceGatherReport" || injected {
			return
		}
		injected = true
		f.groups.update(f.runID, func(r *instance.GroupRun) {
			now := time.Now().UTC()
			r.Gather.Attempts[0].EndedAt = &now
			r.Gather.Attempts[0].Outcome = engine.ResolutionOutcomeResolved
			r.Gather.Tasks[1].Merged = true
			r.Gather.MergeIndex = 2
			r.Gather.ActiveSession = nil
			r.Gather.State = "merging"
		})
	}

	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.activeRef(), true))

	rep := f.groups.snapshot(f.runID).Gather
	assert.Equal(t, 2, rep.MergeIndex, "the gatherer's merge progress survives")
	assert.Equal(t, engine.ResolutionOutcomeResolved, rep.Attempts[0].Outcome, "the verified outcome is not overwritten by abandon")
	assert.True(t, rep.Tasks[1].Merged)
	assert.Nil(t, rep.ActiveSession)
	assert.Empty(t, f.waker.wakeCalls(), "a session the gatherer already finished wakes nothing")
}

// Callback → gatherer: an end for an earlier merge's session must not end the
// session the gatherer has opened since for the next merge.
func TestSeamGatherResolution_StaleSessionEndIsIgnored(t *testing.T) {
	f := newBridgeFixture(t)
	f.setParentState(instance.StateWaiting, "g1")
	stale := f.activeRef()
	f.groups.update(f.runID, func(r *instance.GroupRun) {
		now := time.Now().UTC()
		r.Gather.Attempts[0].EndedAt = &now
		r.Gather.Attempts[0].Outcome = engine.ResolutionOutcomeResolved
		r.Gather.MergeIndex = 2
		r.Gather.Tasks = append(r.Gather.Tasks, instance.GatherTaskReport{Key: "c", ConflictPaths: []string{"other.txt"}})
		r.Gather.Attempts = append(r.Gather.Attempts, instance.ResolutionAttempt{
			MergeIndex: 2, Attempt: 1, TaskKey: "c", ConflictPaths: []string{"other.txt"}, StartedAt: now,
		})
		next := stale
		next.MergeIndex = 2
		r.Gather.ActiveSession = &next
	})
	before := f.groups.snapshot(f.runID).Gather

	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), stale, true))
	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), instanceRefWithAttempt(f.activeRef(), 2, 2), true))

	after := f.groups.snapshot(f.runID).Gather
	assert.Equal(t, before, after, "merge 2's open attempt is untouched")
	assert.Nil(t, after.Attempts[1].EndedAt)
	assert.Zero(t, f.groups.callCount("ReplaceGatherReport"))
	assert.Empty(t, f.waker.wakeCalls())
}

func instanceRefWithAttempt(ref instance.ResolutionSessionRef, mergeIndex, attempt int) instance.ResolutionSessionRef {
	ref.MergeIndex, ref.Attempt = mergeIndex, attempt
	return ref
}

// Gatherer → callback: the operator abandons while the re-entered gatherer is
// verifying a (now clean) stage. The gatherer's persist of "resolved" must not
// erase the abandon: the pass fails stale before committing anything, and the
// next pass honours the abandon by rolling back to failure.
func TestSeamGatherResolution_GathererNeverErasesARecordedAbandon(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	step, err := f.gather()
	require.NoError(t, err)
	require.False(t, step.Done)
	f.area.resolveStagedIn(f.starter.calls()[0].req.Handle, "shared.txt", "repaired")

	injected := false
	f.groups.onCall = func(method string) {
		if method == "ReplaceGatherReport" && !injected {
			injected = true
			f.endOpenAttempts(engine.ResolutionOutcomeAbandoned)
		}
	}
	_, err = f.gather()
	require.True(t, injected, "the abandon landed inside the gatherer's pass")
	require.Error(t, err)
	assert.True(t, errors.Is(err, instance.ErrGatherReportStale), "got %v", err)
	assert.Equal(t, engine.ResolutionOutcomeAbandoned, f.report().Attempts[0].Outcome)
	assert.Zero(t, f.area.commits, "nothing was committed over the abandon")
	assert.Zero(t, f.area.applyCount())

	f.groups.onCall = nil
	step, err = f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, f.area.parentFiles(gatherParentDir),
		"SINK: the abandoned gather leaves the parent's area as it was")
	rep := f.report()
	assert.Equal(t, engine.ResolutionOutcomeAbandoned, rep.Attempts[0].Outcome)
	assert.Contains(t, rep.Error, "abandoned")
}
