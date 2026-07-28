package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compile-time proof that *Engine satisfies the port the executor depends on.
var _ engine.ChildSpawner = (*engine.Engine)(nil)

func spawnerFixture(t *testing.T) *testFixtures {
	t.Helper()
	f := newTestFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name: "child",
		Inputs: []workflow.InputField{
			{Name: "repo", Type: "string", Description: "target repo", Required: true},
			{Name: "dryRun", Type: "boolean", Required: false},
		},
		Nodes: []workflow.Node{
			{ID: "t1", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{
					{Variable: "x", Operation: "set", Value: 1},
				}}},
		},
	})
	return f
}

// spawnParent creates a real parent ROW at the given spawn depth. Spawn derives
// the child's depth from the parent's PERSISTED value, so a synthetic parent id
// with no row behind it is no longer a valid stand-in: an unverifiable depth
// fails closed rather than defaulting to zero.
func spawnParent(t *testing.T, f *testFixtures, depth int) uuid.UUID {
	t.Helper()
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "parent",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		SpawnDepth: depth,
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

// ---------- ChildInputs ----------

func TestEngineChildInputs_ReturnsDeclaredContract(t *testing.T) {
	f := spawnerFixture(t)

	got, err := f.engine.ChildInputs(context.Background(), "child")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "repo", got[0].Name)
	assert.True(t, got[0].Required)
	assert.Equal(t, "dryRun", got[1].Name)
	assert.False(t, got[1].Required)
}

func TestEngineChildInputs_UnknownWorkflowErrors(t *testing.T) {
	f := spawnerFixture(t)

	_, err := f.engine.ChildInputs(context.Background(), "does-not-exist")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound,
		"a workflow the caller cannot see is indistinguishable from one that does not exist")
}

// ChildInputs resolves the owner exactly as TriggerWithDepartment does: the
// context identity when present, otherwise the default user. A system caller
// with no context identity and an unresolvable default user must fail closed
// rather than proceed with a nil owner, which would silently bypass
// owner-scoped visibility. TestTrigger_DefaultUserUnavailable_FailsClosed pins
// this same triggerOwner branch for Trigger; ChildInputs has its own call to
// triggerOwner and needs its own pin.
func TestEngineChildInputs_DefaultUserUnavailable_FailsClosed(t *testing.T) {
	f := spawnerFixture(t)
	f.userRepo.defaultUserErr = errors.New("db unavailable")

	_, err := f.engine.ChildInputs(context.Background(), "child")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to resolve owner")
}

func TestEngineChildInputs_ZeroDeclaredInputs(t *testing.T) {
	f := spawnerFixture(t)
	f.workflowRepo.add(&workflow.Workflow{
		Name: "no-inputs",
		Nodes: []workflow.Node{
			{ID: "t1", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{
					{Variable: "x", Operation: "set", Value: 1},
				}}},
		},
	})

	got, err := f.engine.ChildInputs(context.Background(), "no-inputs")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// The lookup is scoped to the identity on the context, exactly as the trigger
// path is: a private workflow of user A is not visible to user B.
func TestEngineChildInputs_ScopedToContextIdentity(t *testing.T) {
	f := spawnerFixture(t)
	other := uuid.New()
	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: other, Role: "user", Departments: []string{"default"},
	})

	// The mock workflow repo is scope-agnostic, so this asserts the CALL SHAPE:
	// ChildInputs resolves through the same owner-scoped LoadVisibleFiltered the
	// trigger path uses, with the owner taken from the context identity.
	got, err := f.engine.ChildInputs(ctx, "child")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// ChildInputs shares the exact sharedVisibilityFilter gate Trigger uses, but
// TestEngineChildInputs_ScopedToContextIdentity above only proves the CALL
// SHAPE against a scope-agnostic mock — it cannot catch a regression that
// drops or bypasses the gate. newTriggerHarness's scope-aware repo is what
// TestEngine_Trigger_DepartmentWorkflow_HiddenFromOutsider uses to pin this
// same gate for Trigger; ChildInputs needs its own pin against real
// department-scoped visibility since it is ChildInputs's own call into
// LoadVisibleFiltered, not a re-export of Trigger's.
func TestEngineChildInputs_DepartmentWorkflow_HiddenFromOutsider(t *testing.T) {
	h := newTriggerHarness()
	publisher := uuid.New()
	outsider := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("eng-wf"))
	h.engine.SetPublisherLookup(&fakePubLookup{pubs: map[string]workflow.Publication{
		"eng-wf": {Publisher: publisher, Visibility: workflow.VisibilityDepartment, Department: "engineering"},
	}})

	_, err := h.engine.ChildInputs(ctxWithDept(outsider, "sales"), "eng-wf")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound,
		"a spawn node must never learn that a department workflow it cannot see exists")
}

func TestEngineChildInputs_DepartmentWorkflow_AllowedForMember(t *testing.T) {
	h := newTriggerHarness()
	publisher := uuid.New()
	member := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("eng-wf"))
	h.engine.SetPublisherLookup(&fakePubLookup{pubs: map[string]workflow.Publication{
		"eng-wf": {Publisher: publisher, Visibility: workflow.VisibilityDepartment, Department: "engineering"},
	}})

	_, err := h.engine.ChildInputs(ctxWithDept(member, "engineering"), "eng-wf")
	require.NoError(t, err)
}

// A PublicationOf lookup error must fail ChildInputs closed, not silently
// treat the child workflow as invisible or as open. sharedVisibilityFilter's
// error-propagation branch is already pinned through Trigger
// (TestEngine_Trigger_PublicationLookupError_FailsClosed), but ChildInputs
// builds its OWN closure via its OWN call to e.sharedVisibilityFilter(ctx) —
// same shared logic, a distinct call site. This is the same shape of gap
// already fixed for areas 12/13: a shared helper's behavior proven at one
// call site and silently assumed at a sibling one.
func TestEngineChildInputs_PublicationLookupError_FailsClosed(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	lookupErr := errors.New("publication lookup unavailable")
	h.repo.put(workflow.Shared(), sharedWf("flaky-wf"))
	h.engine.SetPublisherLookup(&fakePubLookup{errs: map[string]error{"flaky-wf": lookupErr}})

	_, err := h.engine.ChildInputs(ctxAs(caller), "flaky-wf")
	require.Error(t, err)
	assert.ErrorIs(t, err, lookupErr)
}

// ---------- Spawn ----------

func TestEngineSpawn_CreatesLinkedChildUnderRequestedOwner(t *testing.T) {
	f := spawnerFixture(t)
	owner := uuid.New()
	parentID := spawnParent(t, f, 0)

	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: owner, Role: "user", Departments: []string{"default"},
	})

	childID, err := f.engine.Spawn(ctx, engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{"repo": "arbitron/arbitron"},
		Priority:      string(instance.PriorityHigh),
		Link: instance.SpawnLink{
			ParentInstanceID: parentID,
			ParentNodeID:     "spawn-1",
			Mode:             instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, childID)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	assert.Equal(t, owner, got.OwnerID, "the child inherits the identity injected by the caller")
	require.NotNil(t, got.ParentInstanceID)
	assert.Equal(t, parentID, *got.ParentInstanceID)
	require.NotNil(t, got.ParentNodeID)
	assert.Equal(t, "spawn-1", *got.ParentNodeID)
	assert.Equal(t, instance.SpawnModeFireAndForget, got.SpawnMode)
	assert.Equal(t, instance.PriorityHigh, got.Priority,
		"a child of a high-priority run competes on equal footing with its parent's siblings")
}

func TestEngineSpawn_PropagatesMissingRequiredInput(t *testing.T) {
	f := spawnerFixture(t)

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{}, // "repo" is required
		Priority:      string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: spawnParent(t, f, 0), ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo")
}

func TestEngineSpawn_UnknownChildWorkflowErrors(t *testing.T) {
	f := spawnerFixture(t)

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "nope",
		Variables:     map[string]any{},
		Priority:      string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: uuid.New(), ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

// An INVALID link is rejected before any instance is created: a child that
// cannot be matched by the wake would strand its parent forever, which is worse
// than a failed spawn.
func TestEngineSpawn_RejectsInvalidLinkBeforeCreating(t *testing.T) {
	f := spawnerFixture(t)
	before := len(f.instanceRepo.all())

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{"repo": "r"},
		Priority:      string(instance.PriorityNormal),
		Link:          instance.SpawnLink{ParentNodeID: "spawn-1"}, // no parent id, no mode
	})
	require.Error(t, err)
	assert.Equal(t, before, len(f.instanceRepo.all()), "no child may be created for an invalid link")
}

// Spawn is deliberately THIN: it derives a context carrying the link and
// delegates entirely to TriggerWithDepartment, which refuses an archived
// workflow (engine.go:330-348) before creating any instance. Design Phase 3's
// own testing strategy lists "the child workflow is archived -> node fails" as
// a required failure-category case for the spawn path, but every existing
// archived-refusal test (TestTrigger_Archived_Refused and siblings,
// engine_archived_trigger_test.go) drives the check through Trigger, never
// through Spawn. This is the same shape of gap already fixed repeatedly in
// this loop (areas 12, 13, 14): a shared helper's behavior proven at one call
// site and silently assumed at a sibling one that reaches it one line away
// through the identical delegation. Spawn inherits the check "for free" by
// construction today, so this is not a live bug — but an untested inheritance
// claim is exactly the gap this loop exists to close, and a future refactor
// that gave Spawn its own bypass of TriggerWithDepartment (or reordered the
// archive check past the link/instance creation) would ship undetected.
func TestEngineSpawn_ArchivedChildWorkflow_Refused(t *testing.T) {
	f := spawnerFixture(t)
	res := newArchiveResolver()
	res.register("child", uuid.New())
	res.markArchived("child")
	f.engine.SetWorkflowResolver(res)
	parentID := spawnParent(t, f, 0)
	before := len(f.instanceRepo.all())

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{"repo": "r"},
		Priority:      string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: parentID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.ErrorIs(t, err, workflow.ErrArchived)
	assert.Equal(t, before, len(f.instanceRepo.all()), "no child may be created for an archived workflow")
}

// A grandchild is linked to ITS parent, not to the root.
func TestEngineSpawn_GrandchildLinksToItsOwnParent(t *testing.T) {
	f := spawnerFixture(t)
	root := spawnParent(t, f, 0)

	childID, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: root, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)

	grandID, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: childID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)

	grand := f.instanceRepo.get(grandID)
	require.NotNil(t, grand)
	require.NotNil(t, grand.ParentInstanceID)
	assert.Equal(t, childID, *grand.ParentInstanceID)
	assert.NotEqual(t, root, *grand.ParentInstanceID)
}

// ---------- spawn depth bound ----------

// The depth counter is DERIVED from the parent's persisted value and stamped
// onto the child in the SAME insert as the link, so a grandchild reading it can
// never see a zero that has not yet been written.
func TestEngineSpawn_DerivesChildDepthFromTheParentsPersistedValue(t *testing.T) {
	f := spawnerFixture(t)
	parentID := spawnParent(t, f, 3)

	childID, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: parentID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	assert.Equal(t, 4, got.SpawnDepth)
}

// A root run is depth 0, so its child is depth 1.
func TestEngineSpawn_ChildOfARootIsDepthOne(t *testing.T) {
	f := spawnerFixture(t)

	childID, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: spawnParent(t, f, 0), ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, f.instanceRepo.get(childID).SpawnDepth)
}

// THE RESOURCE-EXHAUSTION GUARD. Without it, a cycle (direct or indirect)
// spends a row, a workspace, and a billed provider call per generation,
// forever — and a shared workflow bills the triggering tenant, not the author.
func TestEngineSpawn_RefusesToExceedTheDepthCeiling(t *testing.T) {
	f := spawnerFixture(t)
	parentID := spawnParent(t, f, instance.MaxSpawnDepth)
	before := len(f.instanceRepo.all())

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: parentID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.ErrorIs(t, err, instance.ErrSpawnDepthExceeded)
	assert.Equal(t, before, len(f.instanceRepo.all()),
		"the refusal must land BEFORE the row, the workspace, and the billed call")
}

// The LAST allowed generation still succeeds: the ceiling is a bound on the
// tree, not an off-by-one that costs an author a level.
func TestEngineSpawn_AllowsTheDeepestPermittedGeneration(t *testing.T) {
	f := spawnerFixture(t)
	parentID := spawnParent(t, f, instance.MaxSpawnDepth-1)

	childID, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: parentID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, instance.MaxSpawnDepth, f.instanceRepo.get(childID).SpawnDepth)
}

// A caller-supplied depth is OVERWRITTEN, never trusted: the parent's stored
// value is the only authority, so no request can inflate its own budget and no
// executor can forget to set one.
func TestEngineSpawn_IgnoresACallerSuppliedDepth(t *testing.T) {
	f := spawnerFixture(t)
	parentID := spawnParent(t, f, 5)

	childID, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: parentID, ParentNodeID: "spawn-1",
			Mode: instance.SpawnModeFireAndForget, SpawnDepth: 0,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 6, f.instanceRepo.get(childID).SpawnDepth)
}

// An unreadable parent fails the spawn: the bound cannot be verified, and
// defaulting to zero would reset the budget on every hop, which is exactly the
// unbounded recursion the counter exists to stop.
func TestEngineSpawn_FailsClosedWhenTheParentCannotBeRead(t *testing.T) {
	f := spawnerFixture(t)
	before := len(f.instanceRepo.all())

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: uuid.New(), // no row behind it
			ParentNodeID:     "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.Error(t, err)
	assert.Equal(t, before, len(f.instanceRepo.all()))
}
