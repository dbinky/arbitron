package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRunResource(run uuid.UUID) engine.ResourceFenceRef {
	return engine.ResourceFenceRef{Key: "run:" + run.String(), Owner: "builtin", Generation: 1, Token: uuid.New()}
}

func testCatalogRef(run uuid.UUID, version int) string {
	return fmt.Sprintf("%s@%d:%s", run, version, strings.Repeat("a", 64))
}

func testJobFence(gen int64) engine.JobFenceRef {
	return engine.JobFenceRef{JobID: uuid.New(), RunnerID: "builtin", Generation: gen}
}

func TestStepCompletionSink_RecordsAllSuboperations(t *testing.T) {
	run := uuid.New()
	sink := engine.NewStepCompletionSink()
	prep := engine.StepCompletion{JobFence: testJobFence(1)} // child-input chat: no workspace
	agent := engine.StepCompletion{JobFence: testJobFence(3), Workspace: &engine.WorkspaceCompletion{
		Ref: testCatalogRef(run, 2), Resource: testRunResource(run)}}

	require.NoError(t, sink.Record(prep))
	require.NoError(t, sink.Record(agent))
	require.NoError(t, sink.Record(prep), "an identical receipt is idempotent")

	got := sink.Completions()
	require.Len(t, got, 2, "every suboperation is kept; the last one never stands in for the others")
	assert.Equal(t, prep, got[0])
	assert.Equal(t, agent, got[1])
}

func TestStepCompletionSink_RejectsIncompleteWorkspaceReceipt(t *testing.T) {
	run := uuid.New()
	withRes := func(mod func(*engine.ResourceFenceRef)) *engine.WorkspaceCompletion {
		r := testRunResource(run)
		mod(&r)
		return &engine.WorkspaceCompletion{Ref: testCatalogRef(run, 2), Resource: r}
	}
	cases := map[string]engine.StepCompletion{
		"zero job id":              {JobFence: engine.JobFenceRef{RunnerID: "builtin", Generation: 1}},
		"empty runner":             {JobFence: engine.JobFenceRef{JobID: uuid.New(), Generation: 1}},
		"zero generation":          {JobFence: engine.JobFenceRef{JobID: uuid.New(), RunnerID: "builtin"}},
		"empty workspace ref":      {JobFence: testJobFence(1), Workspace: &engine.WorkspaceCompletion{Resource: testRunResource(run)}},
		"zero resource token":      {JobFence: testJobFence(1), Workspace: withRes(func(r *engine.ResourceFenceRef) { r.Token = uuid.Nil })},
		"empty resource key":       {JobFence: testJobFence(1), Workspace: withRes(func(r *engine.ResourceFenceRef) { r.Key = "" })},
		"empty resource owner":     {JobFence: testJobFence(1), Workspace: withRes(func(r *engine.ResourceFenceRef) { r.Owner = "" })},
		"zero resource generation": {JobFence: testJobFence(1), Workspace: withRes(func(r *engine.ResourceFenceRef) { r.Generation = 0 })},
		"ref of another run": {JobFence: testJobFence(1), Workspace: &engine.WorkspaceCompletion{
			Ref: testCatalogRef(uuid.New(), 2), Resource: testRunResource(run)}},
		"malformed ref": {JobFence: testJobFence(1), Workspace: &engine.WorkspaceCompletion{
			Ref: "latest", Resource: testRunResource(run)}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			sink := engine.NewStepCompletionSink()
			require.ErrorIs(t, sink.Record(c), engine.ErrInvalidStepCompletion)
			assert.Empty(t, sink.Completions(), "a rejected receipt is never kept")
		})
	}
}

func TestStepCompletionSink_ConflictingDuplicateFailsClosed(t *testing.T) {
	run := uuid.New()
	sink := engine.NewStepCompletionSink()
	first := engine.StepCompletion{JobFence: testJobFence(2), Workspace: &engine.WorkspaceCompletion{
		Ref: testCatalogRef(run, 2), Resource: testRunResource(run)}}
	require.NoError(t, sink.Record(first))

	otherGen := first
	otherGen.JobFence.Generation = 3
	require.ErrorIs(t, sink.Record(otherGen), engine.ErrInvalidStepCompletion)

	otherRef := engine.StepCompletion{JobFence: first.JobFence, Workspace: &engine.WorkspaceCompletion{
		Ref: testCatalogRef(run, 3), Resource: first.Workspace.Resource}}
	require.ErrorIs(t, sink.Record(otherRef), engine.ErrInvalidStepCompletion)

	noWorkspace := engine.StepCompletion{JobFence: first.JobFence}
	require.ErrorIs(t, sink.Record(noWorkspace), engine.ErrInvalidStepCompletion)

	got := sink.Completions()
	require.Len(t, got, 1)
	assert.Equal(t, first, got[0], "the original receipt is unchanged by a rejected duplicate")
}

func TestStepCompletionSink_DefensiveCopies(t *testing.T) {
	run := uuid.New()
	sink := engine.NewStepCompletionSink()
	in := engine.StepCompletion{JobFence: testJobFence(1), Workspace: &engine.WorkspaceCompletion{
		Ref: testCatalogRef(run, 2), Resource: testRunResource(run)}}
	want := engine.StepCompletion{JobFence: in.JobFence, Workspace: &engine.WorkspaceCompletion{
		Ref: in.Workspace.Ref, Resource: in.Workspace.Resource}}
	require.NoError(t, sink.Record(in))

	in.Workspace.Ref = "mutated-after-record"
	out := sink.Completions()
	out[0].Workspace.Ref = "mutated-after-read"
	out[0].JobFence.Generation = 99

	again := sink.Completions()
	require.Len(t, again, 1)
	assert.Equal(t, want, again[0])
}

func TestStepCompletionSink_EmptyRefOnlyWithNonPortable(t *testing.T) {
	run := uuid.New()
	res := testRunResource(run)
	job := testJobFence(1)

	// The RR-08 rule set T5 owns (P3 T16 adds LocalRef in Record, not here).
	nonPortable := engine.StepCompletion{JobFence: job, Workspace: &engine.WorkspaceCompletion{NonPortable: true, Resource: res}}
	require.NoError(t, nonPortable.Validate(), "an empty ref is valid only for a non-portable receipt with a complete fence")

	incomplete := res
	incomplete.Token = uuid.Nil
	for name, c := range map[string]engine.StepCompletion{
		"portable receipt with an empty ref":            {JobFence: job, Workspace: &engine.WorkspaceCompletion{Resource: res}},
		"non-portable receipt with an incomplete fence": {JobFence: job, Workspace: &engine.WorkspaceCompletion{NonPortable: true, Resource: incomplete}},
		"non-portable receipt carrying a catalog ref":   {JobFence: job, Workspace: &engine.WorkspaceCompletion{NonPortable: true, Ref: testCatalogRef(run, 2), Resource: res}},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, c.Validate(), engine.ErrInvalidStepCompletion)
			require.ErrorIs(t, engine.NewStepCompletionSink().Record(c), engine.ErrInvalidStepCompletion)
		})
	}
	portable := engine.StepCompletion{JobFence: job, Workspace: &engine.WorkspaceCompletion{Ref: testCatalogRef(run, 2), Resource: res}}
	require.NoError(t, engine.NewStepCompletionSink().Record(portable))
}

func TestEngine_RetryDoesNotReusePriorAttemptReceipt(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	wf.Nodes[0].ErrorBehavior = workflow.ErrorBehavior{Type: "retry", Retries: 1}
	f.workflowRepo.add(wf)
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	f.engine.SetStepCommitter(newMemCommitter(f, invs))
	f.engine.SetStepCompletionReleaser(&recordingReleaser{})

	var mu sync.Mutex
	var sinks []*engine.StepCompletionSink
	var atEntry []int
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		sink, ok := engine.StepCompletionSinkFromContext(ctx)
		if !ok {
			return nil, errors.New("attempt context carries no completion sink")
		}
		mu.Lock()
		sinks = append(sinks, sink)
		atEntry = append(atEntry, len(sink.Completions()))
		attempt := len(sinks)
		mu.Unlock()
		if err := sink.Record(engine.StepCompletion{JobFence: testJobFence(int64(attempt))}); err != nil {
			return nil, err
		}
		if attempt == 1 {
			return nil, errors.New("transient adapter failure")
		}
		return &engine.NodeResult{Output: map[string]any{"ok": true}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, sinks, 2)
	assert.NotSame(t, sinks[0], sinks[1], "each workflow retry attempt gets its own sink")
	assert.Equal(t, []int{0, 0}, atEntry, "attempt 2 starts with no receipt from attempt 1")
	require.Len(t, sinks[1].Completions(), 1)
	assert.Equal(t, int64(2), sinks[1].Completions()[0].JobFence.Generation)
}

func TestEngine_NoCommitterInstallsNoCompletionSink(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())
	var mu sync.Mutex
	var sawSink, ran bool
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		_, ok := engine.StepCompletionSinkFromContext(ctx)
		mu.Lock()
		sawSink, ran = ok, true
		mu.Unlock()
		return &engine.NodeResult{Output: map[string]any{"ok": true}}, nil
	})
	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	mu.Lock()
	defer mu.Unlock()
	require.True(t, ran)
	assert.False(t, sawSink, "without a StepCommitter nothing would finalize deferred receipts, so dispatch must complete immediately")
}
