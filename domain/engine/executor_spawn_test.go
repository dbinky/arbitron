package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fakes. Each one HONOURS the argument that decides its behaviour: a fake that
// ignored the user id, the workflow name, or the spawn link would make these
// tests unfailable.
// ---------------------------------------------------------------------------

// fakeIdentityResolver resolves per user id and records what it was asked for.
type fakeIdentityResolver struct {
	byUser map[uuid.UUID]identity.Identity
	err    error
	asked  []uuid.UUID
}

func (r *fakeIdentityResolver) IdentityForUser(_ context.Context, userID uuid.UUID) (identity.Identity, error) {
	r.asked = append(r.asked, userID)
	if r.err != nil {
		return identity.Identity{}, r.err
	}
	id, ok := r.byUser[userID]
	if !ok {
		return identity.Identity{}, errors.New("no such user: " + userID.String())
	}
	return id, nil
}

var _ engine.IdentityResolver = (*fakeIdentityResolver)(nil)

// fakeChildSpawner records every call and honours the workflow name it is given.
type fakeChildSpawner struct {
	inputsByWorkflow map[string][]workflow.InputField
	inputsErr        error
	spawnErr         error
	spawned          []engine.SpawnRequest
	spawnedIdentity  []identity.Identity
	inputsIdentity   []identity.Identity
	nextID           uuid.UUID
	// onSpawn, when set, is invoked at the start of Spawn. Test-only ordering probe.
	onSpawn func()
}

// ChildInputs records the IDENTITY carried on ctx, not just the workflow name:
// production's Engine.ChildInputs derives its owner and department-visibility
// filter from ctx (domain/engine/engine_spawn.go:28-32), so a fake that
// ignored ctx here would let a regression that passed the ambient caller's
// context instead of the reconstructed owner's context go undetected.
func (s *fakeChildSpawner) ChildInputs(ctx context.Context, workflowName string) ([]workflow.InputField, error) {
	id, _ := identity.FromContext(ctx)
	s.inputsIdentity = append(s.inputsIdentity, id)
	if s.inputsErr != nil {
		return nil, s.inputsErr
	}
	f, ok := s.inputsByWorkflow[workflowName]
	if !ok {
		return nil, errors.New("workflow not found: " + workflowName)
	}
	return f, nil
}

func (s *fakeChildSpawner) Spawn(ctx context.Context, req engine.SpawnRequest) (uuid.UUID, error) {
	if s.onSpawn != nil {
		s.onSpawn()
	}
	if s.spawnErr != nil {
		return uuid.Nil, s.spawnErr
	}
	s.spawned = append(s.spawned, req)
	id, _ := identity.FromContext(ctx)
	s.spawnedIdentity = append(s.spawnedIdentity, id)
	if s.nextID == uuid.Nil {
		s.nextID = uuid.New()
	}
	return s.nextID, nil
}

var _ engine.ChildSpawner = (*fakeChildSpawner)(nil)

// fakePreparer records the request and returns canned values. It HONOURS the
// declared-inputs argument: with no declared inputs it returns an empty map.
type fakePreparer struct {
	values map[string]any
	err    error
	gotReq engine.ChildInputRequest
	calls  int
}

func (p *fakePreparer) Prepare(_ context.Context, req engine.ChildInputRequest) (map[string]any, error) {
	p.calls++
	p.gotReq = req
	if p.err != nil {
		return nil, p.err
	}
	if len(req.Inputs) == 0 {
		return map[string]any{}, nil
	}
	return p.values, nil
}

var _ engine.ChildInputPreparer = (*fakePreparer)(nil)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type spawnExecFixture struct {
	instances  *mockInstanceRepo
	identities *fakeIdentityResolver
	spawner    *fakeChildSpawner
	preparer   *fakePreparer
	fileReader *mockFileReader
	exec       *engine.SpawnWorkflowExecutor
	parentID   uuid.UUID
	ownerID    uuid.UUID
}

func newSpawnExecFixture(t *testing.T) *spawnExecFixture {
	t.Helper()
	owner := uuid.New()
	parent := &instance.Instance{
		ID:           uuid.New(),
		OwnerID:      owner,
		WorkflowName: "parent",
		State:        instance.StateRunning,
		Priority:     instance.PriorityHigh,
	}
	instances := newMockInstanceRepo()
	require.NoError(t, instances.Create(context.Background(), parent))

	ids := &fakeIdentityResolver{byUser: map[uuid.UUID]identity.Identity{
		owner: {UserID: owner, Role: "user", Departments: []string{"engineering"}},
	}}
	spawner := &fakeChildSpawner{inputsByWorkflow: map[string][]workflow.InputField{
		"child": {
			{Name: "repo", Type: "string", Required: true},
			{Name: "dryRun", Type: "boolean", Required: false},
		},
		"no-inputs": {},
	}}
	prep := &fakePreparer{values: map[string]any{"repo": "arbitron/arbitron"}}
	fr := newMockFileReader()
	nodeExecs := newMockNodeExecutionRepo()
	sw := newFakeSpawnWaits()
	qm := newMockQueueManager()

	return &spawnExecFixture{
		instances:  instances,
		identities: ids,
		spawner:    spawner,
		preparer:   prep,
		fileReader: fr,
		exec:       engine.NewSpawnWorkflowExecutor(instances, nodeExecs, sw, prep, spawner, ids, fr, qm),
		parentID:   parent.ID,
		ownerID:    owner,
	}
}

func ffNode(child string) workflow.Node {
	return workflow.Node{
		ID:   "spawn-1",
		Type: workflow.NodeTypeSpawnWorkflow,
		Config: workflow.NodeConfig{
			ChildWorkflow: child,
			SpawnMode:     workflow.SpawnModeFireAndForget,
			ModelProfile:  "chat-default",
		},
	}
}

func (f *spawnExecFixture) run(t *testing.T, node workflow.Node) (*engine.NodeResult, error) {
	t.Helper()
	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	return f.exec.Execute(ctx, node, variable.NewStore(), "parent")
}

// ---------------------------------------------------------------------------
// happy path
// ---------------------------------------------------------------------------

func TestSpawnExecutor_FireAndForget_HappyPath(t *testing.T) {
	f := newSpawnExecFixture(t)

	res, err := f.run(t, ffNode("child"))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsWait, "fire-and-forget must return a NON-wait result so the engine advances")
	assert.Empty(t, res.OutputPort, "fire-and-forget routes along the single unnamed edge")

	require.Len(t, f.spawner.spawned, 1)
	req := f.spawner.spawned[0]
	assert.Equal(t, "child", req.ChildWorkflow)
	assert.Equal(t, map[string]any{"repo": "arbitron/arbitron"}, req.Variables)
	assert.Equal(t, f.parentID, req.Link.ParentInstanceID)
	assert.Equal(t, "spawn-1", req.Link.ParentNodeID)
	assert.Equal(t, instance.SpawnModeFireAndForget, req.Link.Mode)

	out, ok := res.Output.(map[string]any)
	require.True(t, ok, "the node output must be a map so downstream nodes can reference its fields")
	assert.Equal(t, f.spawner.nextID.String(), out["childInstanceId"])
	assert.Equal(t, "child", out["childWorkflow"])
	assert.Equal(t, workflow.SpawnModeFireAndForget, out["spawnMode"])
}

// ---------------------------------------------------------------------------
// success
// ---------------------------------------------------------------------------

// The child is created under the PARENT INSTANCE's owner, NOT the ambient
// context identity. This is the spec's owner-inheritance requirement and the
// whole reason the resolver exists — so the test runs with a DIFFERENT identity
// in context.
func TestSpawnExecutor_ChildOwnerIsParentOwnerNotAmbientIdentity(t *testing.T) {
	f := newSpawnExecFixture(t)
	intruder := uuid.New()

	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: intruder, Role: "admin"})
	ctx = engine.ContextWithInstanceID(ctx, f.parentID)

	_, err := f.exec.Execute(ctx, ffNode("child"), variable.NewStore(), "parent")
	require.NoError(t, err)

	require.Len(t, f.identities.asked, 1)
	assert.Equal(t, f.ownerID, f.identities.asked[0],
		"the identity must be reconstructed for the PARENT INSTANCE's owner")
	require.Len(t, f.spawner.spawnedIdentity, 1)
	assert.Equal(t, f.ownerID, f.spawner.spawnedIdentity[0].UserID,
		"the spawn must run under the parent owner's identity, not the ambient one")
	assert.NotEqual(t, intruder, f.spawner.spawnedIdentity[0].UserID)
}

// ChildInputs is called BEFORE Spawn in prepareAndSpawn, and it is the call
// that decides whether the parent's owner can even see the child workflow
// (Engine.ChildInputs gates on ctx's identity — area 13). A regression that
// passed the executor's raw incoming ctx (the AMBIENT caller identity) instead
// of the reconstructed childCtx to spawner.ChildInputs would silently widen or
// narrow which child workflows resolve, with no other test catching it, since
// TestSpawnExecutor_ChildOwnerIsParentOwnerNotAmbientIdentity only inspects the
// identity reaching Spawn.
func TestSpawnExecutor_ChildInputsCallCarriesOwnerIdentityNotAmbient(t *testing.T) {
	f := newSpawnExecFixture(t)
	intruder := uuid.New()

	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: intruder, Role: "admin"})
	ctx = engine.ContextWithInstanceID(ctx, f.parentID)

	_, err := f.exec.Execute(ctx, ffNode("child"), variable.NewStore(), "parent")
	require.NoError(t, err)

	require.Len(t, f.spawner.inputsIdentity, 1)
	assert.Equal(t, f.ownerID, f.spawner.inputsIdentity[0].UserID,
		"ChildInputs must be resolved under the PARENT INSTANCE's owner identity")
	assert.NotEqual(t, intruder, f.spawner.inputsIdentity[0].UserID)
}

// The reconstructed identity carries the owner's DEPARTMENTS so a
// department-scoped child workflow resolves.
func TestSpawnExecutor_ReconstructedIdentityCarriesDepartments(t *testing.T) {
	f := newSpawnExecFixture(t)

	_, err := f.run(t, ffNode("child"))
	require.NoError(t, err)

	require.Len(t, f.spawner.spawnedIdentity, 1)
	assert.Equal(t, []string{"engineering"}, f.spawner.spawnedIdentity[0].Departments)
}

func TestSpawnExecutor_ChildInheritsParentPriority(t *testing.T) {
	f := newSpawnExecFixture(t)

	_, err := f.run(t, ffNode("child"))
	require.NoError(t, err)
	require.Len(t, f.spawner.spawned, 1)
	assert.Equal(t, string(instance.PriorityHigh), f.spawner.spawned[0].Priority)
}

func TestSpawnExecutor_PassesChildContractAndParentContextToPreparer(t *testing.T) {
	f := newSpawnExecFixture(t)

	vars := variable.NewStore()
	require.NoError(t, vars.SetInput("topic", "quantum"))
	vars.SetNodeOutput("analyzer", map[string]any{"branch": "release/2.0"})

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	_, err := f.exec.Execute(ctx, ffNode("child"), vars, "parent")
	require.NoError(t, err)

	got := f.preparer.gotReq
	assert.Equal(t, "spawn-1", got.NodeID)
	assert.Equal(t, "child", got.ChildWorkflow)
	assert.Equal(t, "chat-default", got.ProfileAlias)
	require.Len(t, got.Inputs, 2, "the CHILD's declared inputs are the contract")
	assert.Equal(t, "quantum", got.ParentContext["topic"])
	assert.NotNil(t, got.ParentContext["analyzer"],
		"the parent's prior NODE OUTPUTS must reach the preparer, not just its inputs")
}

// The parent's workspace directory must reach the preparer as
// ChildInputRequest.WorkDir. Subprocess-backed chat providers (claude-cli, the
// default backend) reject an empty WorkDir outright rather than falling back
// to the engine process's own cwd (ErrWorkDirRequired) — so an empty value
// here is not a harmless default, it is a production outage for every
// claude-cli-backed spawn node.
func TestSpawnExecutor_ParentWorkspaceReachesPreparerAsWorkDir(t *testing.T) {
	f := newSpawnExecFixture(t)

	vars := variable.NewStore()
	vars.SetReserved(engine.ReservedVarWorkspace, "/var/arbitron/workspaces/parent-run-1")

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	_, err := f.exec.Execute(ctx, ffNode("child"), vars, "parent")
	require.NoError(t, err)

	assert.Equal(t, "/var/arbitron/workspaces/parent-run-1", f.preparer.gotReq.WorkDir,
		"the preparer must receive the parent's workspace so its ChatRequest carries a valid WorkDir")
}

func TestSpawnExecutor_GuidanceFileIsReadAndSubstituted(t *testing.T) {
	f := newSpawnExecFixture(t)
	f.fileReader.files["parent/spawn-guidance.md"] = []byte("Prefer branch {{topic}} for the child.")

	node := ffNode("child")
	node.Config.TemplateFile = "spawn-guidance.md"

	vars := variable.NewStore()
	require.NoError(t, vars.SetInput("topic", "release/2.0"))

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	_, err := f.exec.Execute(ctx, node, vars, "parent")
	require.NoError(t, err)

	assert.Equal(t, "Prefer branch release/2.0 for the child.", f.preparer.gotReq.Guidance,
		"guidance prose must be read AND have its {{substitutions}} resolved")
}

func TestSpawnExecutor_NoGuidanceConfiguredIsNotAnError(t *testing.T) {
	f := newSpawnExecFixture(t)

	_, err := f.run(t, ffNode("child"))
	require.NoError(t, err)
	assert.Empty(t, f.preparer.gotReq.Guidance)
}

// ---------------------------------------------------------------------------
// failure
// ---------------------------------------------------------------------------

// A child created from an incomplete payload is the worst failure mode here, so
// this asserts NO child is created.
func TestSpawnExecutor_PreparationFailure_NoChildCreated(t *testing.T) {
	f := newSpawnExecFixture(t)
	f.preparer.err = errors.New("missing required input(s): repo")

	res, err := f.run(t, ffNode("child"))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned, "no child may be created when preparation fails")
	assert.Contains(t, err.Error(), "repo")
}

func TestSpawnExecutor_ChildWorkflowInvisibleOrMissing_Fails(t *testing.T) {
	f := newSpawnExecFixture(t)

	res, err := f.run(t, ffNode("not-visible"))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned)
	assert.Equal(t, 0, f.preparer.calls, "an unresolvable child must fail before any LLM cost")
}

func TestSpawnExecutor_MissingChildWorkflowConfig_Fails(t *testing.T) {
	f := newSpawnExecFixture(t)
	node := ffNode("")

	res, err := f.run(t, node)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "childWorkflow")
}

// ---------------------------------------------------------------------------
// error
// ---------------------------------------------------------------------------

// Identity resolution failure is NOT downgraded to a system identity: falling
// back would silently change which workflows are visible to the spawn and could
// widen reach beyond the owner.
func TestSpawnExecutor_IdentityResolutionFailure_NoChildNoFallback(t *testing.T) {
	f := newSpawnExecFixture(t)
	f.identities.err = errors.New("user store unavailable")

	res, err := f.run(t, ffNode("child"))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned, "no child, and no fallback to a system identity")
}

func TestSpawnExecutor_ParentInstanceLoadFailure_Errors(t *testing.T) {
	f := newSpawnExecFixture(t)
	f.instances.getErr = errors.New("db down")

	res, err := f.run(t, ffNode("child"))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnExecutor_SpawnFailure_Surfaced(t *testing.T) {
	f := newSpawnExecFixture(t)
	sentinel := errors.New("enqueue failed")
	f.spawner.spawnErr = sentinel

	res, err := f.run(t, ffNode("child"))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, sentinel, "an enqueue failure must be surfaced, not swallowed")
}

func TestSpawnExecutor_MissingInstanceIDInContext_Errors(t *testing.T) {
	f := newSpawnExecFixture(t)

	res, err := f.exec.Execute(context.Background(), ffNode("child"), variable.NewStore(), "parent")
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "instance ID not found in context")
}

func TestSpawnExecutor_GuidanceFileUnreadable_Errors(t *testing.T) {
	f := newSpawnExecFixture(t)
	node := ffNode("child")
	node.Config.TemplateFile = "missing-guidance.md"

	res, err := f.run(t, node)
	require.Error(t, err, "a CONFIGURED template that cannot be read is a misconfiguration, "+
		"not an optional extra")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "missing-guidance.md")
	assert.Empty(t, f.spawner.spawned)
}

// ---------------------------------------------------------------------------
// edge cases
// ---------------------------------------------------------------------------

func TestSpawnExecutor_ChildWithZeroDeclaredInputs_SpawnsWithEmptyPayload(t *testing.T) {
	f := newSpawnExecFixture(t)

	res, err := f.run(t, ffNode("no-inputs"))
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, f.spawner.spawned, 1)
	assert.Empty(t, f.spawner.spawned[0].Variables)
}

// The executor does NOT trust that validation ran: a snapshot predating Phase 1
// can carry an unrecognized mode.
func TestSpawnExecutor_UnrecognizedSnapshotMode_Fails(t *testing.T) {
	f := newSpawnExecFixture(t)
	node := ffNode("child")
	node.Config.SpawnMode = "async"

	res, err := f.run(t, node)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "async")
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnExecutor_EmptySnapshotMode_Fails(t *testing.T) {
	f := newSpawnExecFixture(t)
	node := ffNode("child")
	node.Config.SpawnMode = ""

	res, err := f.run(t, node)
	require.Error(t, err, "an empty mode is an authoring error, never an inheritance request")
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned)
}

// A parent whose owner has NO departments still spawns a private child.
func TestSpawnExecutor_OwnerWithNoDepartments_StillSpawns(t *testing.T) {
	f := newSpawnExecFixture(t)
	f.identities.byUser[f.ownerID] = identity.Identity{UserID: f.ownerID, Role: "user"}

	res, err := f.run(t, ffNode("child"))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Len(t, f.spawner.spawned, 1)
}

// A fire-and-forget node reached TWICE (a graph back-edge, or a node-level
// retry) spawns TWICE. This is pinned deliberately rather than discovered
// later: fire-and-forget has no short-circuit, consistent with the documented
// re-execution semantics of loop revisits.
func TestSpawnExecutor_FireAndForgetSpawnsAgainOnReExecution(t *testing.T) {
	f := newSpawnExecFixture(t)

	_, err := f.run(t, ffNode("child"))
	require.NoError(t, err)
	f.spawner.nextID = uuid.New() // a distinct child on the second pass
	_, err = f.run(t, ffNode("child"))
	require.NoError(t, err)

	assert.Len(t, f.spawner.spawned, 2, "fire-and-forget re-execution creates a SECOND child")
}

// A spawn from a run that itself has a parent produces a correctly linked
// grandchild: the link names the IMMEDIATE parent.
func TestSpawnExecutor_GrandchildLinksToImmediateParent(t *testing.T) {
	f := newSpawnExecFixture(t)
	grandparent := uuid.New()

	mid := &instance.Instance{
		ID: uuid.New(), OwnerID: f.ownerID, WorkflowName: "mid",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		ParentInstanceID: &grandparent,
	}
	require.NoError(t, f.instances.Create(context.Background(), mid))

	ctx := engine.ContextWithInstanceID(context.Background(), mid.ID)
	_, err := f.exec.Execute(ctx, ffNode("child"), variable.NewStore(), "mid")
	require.NoError(t, err)

	require.Len(t, f.spawner.spawned, 1)
	assert.Equal(t, mid.ID, f.spawner.spawned[0].Link.ParentInstanceID)
	assert.NotEqual(t, grandparent, f.spawner.spawned[0].Link.ParentInstanceID)
}
