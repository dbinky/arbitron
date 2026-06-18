package engine_test

import (
	"context"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeResolverByName is an in-memory engine.WorkflowResolver keyed by name<->id.
// A rename is simulated by reassigning the name that maps to an id. A miss
// returns workflow.ErrNotFound (the registry's sentinel) so the engine's
// best-effort fallback paths are exercised.
type fakeResolverByName struct {
	mu       sync.Mutex
	nameToID map[string]uuid.UUID
	idToName map[uuid.UUID]string
}

func newFakeResolverByName() *fakeResolverByName {
	return &fakeResolverByName{
		nameToID: map[string]uuid.UUID{},
		idToName: map[uuid.UUID]string{},
	}
}

func (r *fakeResolverByName) register(name string, id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nameToID[name] = id
	r.idToName[id] = name
}

// rename simulates a workflow rename: the same id now resolves to newName, and
// newName resolves to the id. The OLD name is removed so a stale name lookup
// misses (the registry enforces global name uniqueness).
func (r *fakeResolverByName) rename(id uuid.UUID, oldName, newName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nameToID, oldName)
	r.nameToID[newName] = id
	r.idToName[id] = newName
}

func (r *fakeResolverByName) ResolveID(_ context.Context, name string) (uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.nameToID[name]
	if !ok {
		return uuid.Nil, workflow.ErrNotFound
	}
	return id, nil
}

func (r *fakeResolverByName) ResolveName(_ context.Context, id uuid.UUID) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.idToName[id]
	if !ok {
		return "", workflow.ErrNotFound
	}
	return name, nil
}

// --- Step 1/2 (happy): Trigger resolves name->id and stamps WorkflowID. ---

func TestTrigger_StampsWorkflowID(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	idA := uuid.New()
	res := newFakeResolverByName()
	res.register("alpha", idA)
	h.engine.SetWorkflowResolver(res)
	h.repo.put(workflow.Private(caller), sharedWf("alpha"))

	id, _, err := h.engine.Trigger(ctxAs(caller), "alpha", nil, "normal")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, idA, inst.WorkflowID, "trigger must stamp the resolved stable workflow id")
	assert.Equal(t, "alpha", inst.WorkflowName, "the display label is retained")
	require.NotNil(t, inst.WorkflowSnapshot, "the execution snapshot is retained")
}

// No resolver wired (local mode): the instance carries a Nil WorkflowID and the
// trigger still succeeds, preserving today's name-only behavior.
func TestTrigger_NoResolver_NilWorkflowID(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), sharedWf("alpha"))

	id, _, err := h.engine.Trigger(ctxAs(caller), "alpha", nil, "normal")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, inst.WorkflowID, "no resolver => Nil id, name-only behavior preserved")
}

// --- Step 6 (success): a rerun after a rename resolves by id to the new name. ---

func TestRerun_AfterRename_ResolvesByID(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	idA := uuid.New()
	res := newFakeResolverByName()
	res.register("alpha", idA)
	h.engine.SetWorkflowResolver(res)
	h.repo.put(workflow.Private(caller), sharedWf("alpha"))

	// Original run, stamped with idA + name "alpha".
	origID, _, err := h.engine.Trigger(ctxAs(caller), "alpha", map[string]any{"k": "v"}, "normal")
	require.NoError(t, err)
	// Drive it to a terminal state so it is rerunnable.
	require.NoError(t, h.instances.UpdateState(context.Background(), origID, instance.StateCompleted))

	// Simulate a rename: the workflow folder is now "beta" (idA unchanged). The
	// old "alpha" name no longer resolves.
	h.repo.put(workflow.Private(caller), sharedWf("beta"))
	res.rename(idA, "alpha", "beta")

	newID, err := h.engine.Rerun(ctxAs(caller), origID)
	require.NoError(t, err)

	newInst, err := h.instances.Get(context.Background(), newID)
	require.NoError(t, err)
	assert.Equal(t, "beta", newInst.WorkflowName,
		"rerun must resolve the stable id to the CURRENT name (renamed definition)")
	assert.Equal(t, idA, newInst.WorkflowID, "the rerun carries the same stable id")
}

// --- Step 6 (edge): a legacy Nil-id instance reruns via its WorkflowName label. ---

func TestRerun_LegacyNilID_FallsBackToName(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	res := newFakeResolverByName()
	h.engine.SetWorkflowResolver(res)
	h.repo.put(workflow.Private(caller), sharedWf("legacy"))

	// A legacy completed instance with NO WorkflowID (pre-id-references).
	legacyID := uuid.New()
	require.NoError(t, h.instances.Create(context.Background(), &instance.Instance{
		ID:                legacyID,
		OwnerID:           caller,
		WorkflowName:      "legacy",
		State:             instance.StateCompleted,
		Priority:          instance.PriorityNormal,
		OriginalVariables: map[string]any{},
	}))

	newID, err := h.engine.Rerun(ctxAs(caller), legacyID)
	require.NoError(t, err)

	newInst, err := h.instances.Get(context.Background(), newID)
	require.NoError(t, err)
	assert.Equal(t, "legacy", newInst.WorkflowName,
		"a legacy Nil-id instance must rerun via its retained WorkflowName label")
}
