package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An upgraded install can hold a NEW parent beside an OLD published child (the
// seeder never overwrites a workflow it already seeded). The parent's spawn node
// maps inputs the old child does not declare; without a preflight the run only
// fails at that node, after every upstream stage has been paid for. Trigger must
// refuse before anything is created or enqueued.

func preflightParent(name, child string, inputs map[string]any) *workflow.Workflow {
	return &workflow.Workflow{Name: name, Nodes: []workflow.Node{
		{ID: "early", Type: workflow.NodeTypeScript, Entrypoint: true},
		{ID: "refine", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
			ChildWorkflow: child, SpawnMode: workflow.SpawnModeWaitForChild, Inputs: inputs,
		}},
	}, Edges: []workflow.Edge{{Source: "early", Target: "refine"}}}
}

func preflightChild(name string, inputs ...string) *workflow.Workflow {
	wf := sharedWf(name)
	for _, n := range inputs {
		wf.Inputs = append(wf.Inputs, workflow.InputField{Name: n, Type: "string"})
	}
	return wf
}

var newParentInputs = map[string]any{"repo_url": "{{repo_url}}", "in_place": true, "focus_seed": "{{seed}}", "baseline_path": "{{bp}}"}

func instanceCount(h *triggerHarness) int {
	h.instances.mu.Lock()
	defer h.instances.mu.Unlock()
	return len(h.instances.instances)
}

func TestTrigger_SpawnPreflight_OldPublishedChildRejectsNewParentBeforeEnqueue(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	// Old ralph-loop (shared/published) declares none of the new inputs.
	h.repo.put(workflow.Shared(), preflightChild("ralph-loop", "repo_url"))
	h.repo.put(workflow.Shared(), preflightParent("fp-next", "ralph-loop", newParentInputs))

	id, started, err := h.engine.Trigger(ctxAs(caller), "fp-next", nil, "normal")
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrSpawnUnknownChildInput)
	assert.Equal(t, uuid.Nil, id)
	assert.False(t, started)
	for _, want := range []string{"baseline_path", "focus_seed", "in_place", `"ralph-loop"`, "update and publish"} {
		assert.Contains(t, err.Error(), want, "the error must say what to fix")
	}
	assert.NotContains(t, err.Error(), "repo_url,", "declared inputs are not reported as unknown")
	assert.Equal(t, 0, instanceCount(h), "no parent instance may be created, so zero parent nodes execute")
	assert.Equal(t, uuid.Nil, h.enqueuedOwner, "nothing may be enqueued")
}

func TestTrigger_SpawnPreflight_UpdatedChildAllowsTheParent(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Shared(), preflightChild("ralph-loop", "repo_url", "in_place", "focus_seed", "baseline_path"))
	h.repo.put(workflow.Shared(), preflightParent("fp-next", "ralph-loop", newParentInputs))

	_, _, err := h.engine.Trigger(ctxAs(caller), "fp-next", nil, "normal")
	require.NoError(t, err)
	assert.Equal(t, 1, instanceCount(h))
}

// The child is resolved as the RUN OWNER sees it: a private copy shadows the
// shared one, in both directions.
func TestTrigger_SpawnPreflight_ResolvesTheChildAsTheOwnerSeesIt(t *testing.T) {
	t.Run("a private up-to-date child shadows an old shared one", func(t *testing.T) {
		h := newTriggerHarness()
		caller := uuid.New()
		h.repo.put(workflow.Shared(), preflightChild("ralph-loop", "repo_url"))
		h.repo.put(workflow.Private(caller), preflightChild("ralph-loop", "repo_url", "in_place", "focus_seed", "baseline_path"))
		h.repo.put(workflow.Shared(), preflightParent("fp-next", "ralph-loop", newParentInputs))
		_, _, err := h.engine.Trigger(ctxAs(caller), "fp-next", nil, "normal")
		require.NoError(t, err)
	})
	t.Run("a private old child shadows an up-to-date shared one", func(t *testing.T) {
		h := newTriggerHarness()
		caller := uuid.New()
		h.repo.put(workflow.Shared(), preflightChild("ralph-loop", "repo_url", "in_place", "focus_seed", "baseline_path"))
		h.repo.put(workflow.Private(caller), preflightChild("ralph-loop", "repo_url"))
		h.repo.put(workflow.Shared(), preflightParent("fp-next", "ralph-loop", newParentInputs))
		_, _, err := h.engine.Trigger(ctxAs(caller), "fp-next", nil, "normal")
		require.Error(t, err)
		assert.ErrorIs(t, err, engine.ErrSpawnUnknownChildInput)
	})
	t.Run("another user's private copy is invisible", func(t *testing.T) {
		h := newTriggerHarness()
		caller, other := uuid.New(), uuid.New()
		h.repo.put(workflow.Shared(), preflightChild("ralph-loop", "repo_url", "in_place", "focus_seed", "baseline_path"))
		h.repo.put(workflow.Private(other), preflightChild("ralph-loop", "repo_url"))
		h.repo.put(workflow.Shared(), preflightParent("fp-next", "ralph-loop", newParentInputs))
		_, _, err := h.engine.Trigger(ctxAs(caller), "fp-next", nil, "normal")
		require.NoError(t, err)
	})
}

// A child that cannot be resolved is left to the spawn-time guard: children can
// appear or change later and a lookup failure must not block a valid run.
func TestTrigger_SpawnPreflight_UnresolvableChildIsLeftToTheSpawnTimeGuard(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Shared(), preflightParent("fp-next", "not-installed-yet", newParentInputs))
	_, _, err := h.engine.Trigger(ctxAs(caller), "fp-next", nil, "normal")
	require.NoError(t, err)
}

func TestTrigger_SpawnPreflight_UnmappedSpawnAndPlainWorkflowsAreUntouched(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Shared(), preflightChild("kid"))
	h.repo.put(workflow.Shared(), preflightParent("no-inputs", "kid", nil))
	h.repo.put(workflow.Shared(), sharedWf("plain"))
	for _, name := range []string{"no-inputs", "plain"} {
		_, _, err := h.engine.Trigger(ctxAs(caller), name, nil, "normal")
		require.NoError(t, err, name)
	}
}

// The reserved "workspace" key is skipped by the plan, exactly as at spawn time.
func TestTrigger_SpawnPreflight_ReservedWorkspaceKeyIsNotUnknown(t *testing.T) {
	h := newTriggerHarness()
	h.repo.put(workflow.Shared(), preflightChild("kid", "a"))
	h.repo.put(workflow.Shared(), preflightParent("p", "kid", map[string]any{"a": "x", "workspace": "y"}))
	_, _, err := h.engine.Trigger(context.Background(), "p", nil, "normal")
	require.NoError(t, err)
}
