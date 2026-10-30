package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stageContract() []workflow.InputField {
	return []workflow.InputField{
		{Name: "repo_url", Type: "string", Required: true},
		{Name: "branch", Type: "string", Required: true},
		{Name: "paths", Type: "array", Required: true},
		{Name: "in_place", Type: "boolean", Required: false},
		{Name: "base", Type: "string", Required: true, Default: "main"},
		{Name: "topic", Type: "string", Required: true},
	}
}

func mappedNode(child, mode string, inputs map[string]any) workflow.Node {
	n := ffNode(child)
	n.Config.SpawnMode = mode
	n.Config.Inputs = inputs
	return n
}

// fullStageInputs maps every required no-default input of "stage" plus a literal.
func fullStageInputs() map[string]any {
	return map[string]any{
		"repo_url": "{{intake.repo_url}}",
		"branch":   "{{intake.branch}}",
		"paths":    "{{prior.paths}}",
		"topic":    "{{intake.topic}}",
		"in_place": true,
	}
}

func stageVars() *variable.VariableStore {
	v := variable.NewStore()
	v.SetNodeOutput("intake", map[string]any{
		"repo_url": "https://example.test/r.git",
		"branch":   "feat/x",
		"topic":    "t-intake",
	})
	v.SetNodeOutput("prior", map[string]any{"paths": []any{"a", "b"}})
	return v
}

func (f *spawnExecFixture) withStage() *spawnExecFixture {
	f.spawner.inputsByWorkflow["stage"] = stageContract()
	return f
}

func (f *spawnExecFixture) runVars(t *testing.T, node workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error) {
	t.Helper()
	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	return f.exec.Execute(ctx, node, vars, "parent")
}

func TestSpawnInputs_FullyMappedFireAndForget_ZeroPreparerCalls(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()

	res, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, fullStageInputs()), stageVars())
	require.NoError(t, err)

	assert.Equal(t, 0, f.preparer.calls)
	require.Len(t, f.spawner.spawned, 1)
	assert.Equal(t, map[string]any{
		"repo_url": "https://example.test/r.git",
		"branch":   "feat/x",
		"paths":    []any{"a", "b"},
		"topic":    "t-intake",
		"in_place": true,
		"base":     "main",
	}, f.spawner.spawned[0].Variables)

	out := res.Output.(map[string]any)
	assert.Equal(t, map[string]any{
		"repo_url": "mapped", "branch": "mapped", "paths": "mapped",
		"topic": "mapped", "in_place": "mapped", "base": "default",
	}, out["inputSources"])
}

// inputSources must be a map[string]any so variable.walk can traverse it: a
// downstream template reads it from the live store.
func TestSpawnInputs_FireAndForget_InputSourcesReadableDownstream(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	vars := stageVars()

	res, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, fullStageInputs()), vars)
	require.NoError(t, err)
	vars.SetNodeOutput("spawn-1", res.Output)

	got, ok := vars.Get("spawn-1.inputSources.topic")
	require.True(t, ok, "{{spawn-1.inputSources.topic}} must resolve")
	assert.Equal(t, "mapped", got)
	got, ok = vars.Get("spawn-1.inputSources.base")
	require.True(t, ok)
	assert.Equal(t, "default", got)
}

func TestSpawnInputs_FullyMappedWaitForChild_RecordCarriesInputSources(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	f.spawner.inputsByWorkflow["stage"] = stageContract()

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	res, err := f.exec.Execute(ctx, mappedNode("stage", workflow.SpawnModeWaitForChild, fullStageInputs()), stageVars(), "parent")
	require.NoError(t, err)
	require.True(t, res.IsWait)

	want := map[string]any{
		"repo_url": "mapped", "branch": "mapped", "paths": "mapped",
		"topic": "mapped", "in_place": "mapped", "base": "default",
	}
	assert.Equal(t, want, res.Output.(map[string]any)["inputSources"])
	require.Len(t, f.nodeExecs.executions, 1)
	rec := f.nodeExecs.executions[0].Output
	assert.Equal(t, want, rec["inputSources"])
	assert.Equal(t, 0, f.preparer.calls)
}

func TestSpawnInputs_PartialMapping_PreparerAskedOnlyForUnmapped(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	f.preparer.values = map[string]any{"topic": "t"}
	in := fullStageInputs()
	delete(in, "topic")

	res, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, in), stageVars())
	require.NoError(t, err)

	assert.Equal(t, 1, f.preparer.calls)
	require.Len(t, f.preparer.gotReq.Inputs, 1)
	assert.Equal(t, "topic", f.preparer.gotReq.Inputs[0].Name)
	assert.Equal(t, "t", f.spawner.spawned[0].Variables["topic"])
	assert.Equal(t, "prepared", res.Output.(map[string]any)["inputSources"].(map[string]any)["topic"])
}

func TestSpawnInputs_FullyMappedNeedsNoModelProfile(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	node := mappedNode("stage", workflow.SpawnModeFireAndForget, fullStageInputs())
	node.Config.ModelProfile = ""

	_, err := f.runVars(t, node, stageVars())
	require.NoError(t, err)
	assert.Len(t, f.spawner.spawned, 1)
}

func TestSpawnInputs_DefaultIsDeepCopied(t *testing.T) {
	f := newSpawnExecFixture(t)
	def := []any{"x"}
	f.spawner.inputsByWorkflow["stage"] = []workflow.InputField{
		{Name: "base", Type: "array", Required: true, Default: def},
	}

	_, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, nil), variable.NewStore())
	require.NoError(t, err)

	got := f.spawner.spawned[0].Variables["base"].([]any)
	got[0] = "mutated"
	assert.Equal(t, []any{"x"}, def)
}

func TestSpawnInputs_PreparerExtrasAndCollisionsDropped(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	f.preparer.values = map[string]any{"topic": "t", "branch": "EVIL", "extra": 1}
	in := fullStageInputs()
	delete(in, "topic")

	_, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, in), stageVars())
	require.NoError(t, err)

	vars := f.spawner.spawned[0].Variables
	assert.Equal(t, "feat/x", vars["branch"])
	assert.NotContains(t, vars, "extra")
	assert.Equal(t, "t", vars["topic"])
}

func TestSpawnInputs_FireAndForgetReResolvesEachExecution(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	node := mappedNode("stage", workflow.SpawnModeFireAndForget, fullStageInputs())

	v1 := stageVars()
	v1.SetNodeOutput("intake", map[string]any{"repo_url": "r", "branch": "one", "topic": "t"})
	_, err := f.runVars(t, node, v1)
	require.NoError(t, err)

	f.spawner.nextID = uuid.New()
	v2 := stageVars()
	v2.SetNodeOutput("intake", map[string]any{"repo_url": "r", "branch": "two", "topic": "t"})
	_, err = f.runVars(t, node, v2)
	require.NoError(t, err)

	require.Len(t, f.spawner.spawned, 2)
	assert.Equal(t, "one", f.spawner.spawned[0].Variables["branch"])
	assert.Equal(t, "two", f.spawner.spawned[1].Variables["branch"])
}

func TestSpawnInputs_UnresolvedReference_FailsClosed(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	in := fullStageInputs()
	in["branch"] = "{{nope.branch}}"

	res, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, in), stageVars())
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference)
	assert.Contains(t, err.Error(), "nope.branch")
	assert.Equal(t, 0, f.preparer.calls)
	assert.Empty(t, f.spawner.spawned)
	parent, gerr := f.instances.Get(context.Background(), f.parentID)
	require.NoError(t, gerr)
	assert.Equal(t, instance.StateRunning, parent.State)
}

func TestSpawnInputs_UnknownMappedKey_FailsBeforeResolve(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()

	_, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget,
		map[string]any{"brnch": "{{nope}}"}), stageVars())
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrSpawnUnknownChildInput)
	assert.NotErrorIs(t, err, variable.ErrUnresolvedReference)
	assert.Contains(t, err.Error(), "brnch")
	for _, name := range []string{"repo_url", "branch", "paths", "in_place", "base", "topic"} {
		assert.Contains(t, err.Error(), name)
	}
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnInputs_MappedRequiredResolvesToNull_Fails(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	vars := stageVars()
	vars.SetNodeOutput("intake", map[string]any{"repo_url": "r", "branch": nil, "topic": "t"})

	_, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, fullStageInputs()), vars)
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrChildContractUnsatisfied)
	assert.Contains(t, err.Error(), "branch")
	assert.Equal(t, 0, f.preparer.calls)
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnInputs_PrepareNeededWithoutProfile_Fails(t *testing.T) {
	// Use the real preparer so its message is what is asserted.
	f := newSpawnExecFixture(t).withStage()
	real := engine.NewChildInputPreparer(nil, nil)
	exec := engine.NewSpawnWorkflowExecutor(f.instances, newMockNodeExecutionRepo(), newFakeSpawnWaits(),
		real, f.spawner, f.identities, f.fileReader, newMockQueueManager())
	in := fullStageInputs()
	delete(in, "topic")
	node := mappedNode("stage", workflow.SpawnModeFireAndForget, in)
	node.Config.ModelProfile = ""

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	_, err := exec.Execute(ctx, node, stageVars(), "parent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmapped required input")
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnInputs_PreparerOmitsRequested_Fails(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	f.preparer.values = map[string]any{"unrelated": "x"}
	in := fullStageInputs()
	delete(in, "topic")

	_, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, in), stageVars())
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrChildContractUnsatisfied)
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnInputs_SnapshotWithReservedInput_Fails(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	in := fullStageInputs()
	in["workspace"] = "/etc"

	_, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, in), stageVars())
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrSpawnReservedInput)
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnInputs_WholeReferenceWhitespaceRules(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()

	in := fullStageInputs()
	in["paths"] = "{{ prior.paths }}"
	_, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, in), stageVars())
	require.NoError(t, err)
	assert.Equal(t, []any{"a", "b"}, f.spawner.spawned[0].Variables["paths"])

	// A reference with surrounding text is INTERPOLATED to a string, so the child
	// must declare a string for it. Retype the input; the array-declared contract
	// above (rightly) refuses a string there.
	f.spawner.inputsByWorkflow["stage"] = stageContractWithPathsType("string")
	f.spawner.nextID = uuid.New()
	in["paths"] = " {{prior.paths}}"
	_, err = f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, in), stageVars())
	require.NoError(t, err)
	assert.IsType(t, "", f.spawner.spawned[1].Variables["paths"])
}

func stageContractWithPathsType(typ string) []workflow.InputField {
	fields := stageContract()
	for i := range fields {
		if fields[i].Name == "paths" {
			fields[i].Type = typ
		}
	}
	return fields
}

// A resolved mapping of the wrong type must never reach Spawn: the same
// declared-type contract a workflow-kind group task is held to applies here.
func TestSpawnInputs_WrongTypeWholeReferenceNeverReachesSpawn(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		value    any
		declared string
	}{
		{"string into an array input", "paths", "just-a-string", "array"},
		{"array into a string input", "branch", []any{"a"}, "string"},
		{"string into a boolean input", "in_place", "yes", "boolean"},
		{"object into a number input", "retries", map[string]any{"n": 1}, "number"},
	}
	for _, c := range cases {
		for _, mode := range []string{workflow.SpawnModeFireAndForget, workflow.SpawnModeWaitForChild} {
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				contract := append(stageContract(), workflow.InputField{Name: "retries", Type: "number"})
				for i := range contract {
					if contract[i].Name == c.input {
						contract[i].Type = c.declared
					}
				}
				in := fullStageInputs()
				in["retries"] = 3
				in[c.input] = "{{wrong.value}}"
				vars := stageVars()
				vars.SetNodeOutput("wrong", map[string]any{"value": c.value})

				var spawned func() int
				var run func() error
				if mode == workflow.SpawnModeFireAndForget {
					f := newSpawnExecFixture(t)
					f.spawner.inputsByWorkflow["stage"] = contract
					spawned = func() int { return len(f.spawner.spawned) }
					run = func() error {
						_, err := f.runVars(t, mappedNode("stage", mode, in), vars)
						return err
					}
				} else {
					f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
					f.spawner.inputsByWorkflow["stage"] = contract
					spawned = func() int { return len(f.spawner.spawned) }
					run = func() error {
						ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
						_, err := f.exec.Execute(ctx, mappedNode("stage", mode, in), vars, "parent")
						return err
					}
				}
				err := run()
				require.Error(t, err)
				assert.ErrorIs(t, err, engine.ErrChildContractUnsatisfied)
				assert.Contains(t, err.Error(), c.input)
				assert.Equal(t, 0, spawned(), "no child may be created")
			})
		}
	}
}

// Defaults and prepared values pass through the same check.
func TestSpawnInputs_DefaultsOfTheDeclaredTypePass(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	_, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, fullStageInputs()), stageVars())
	require.NoError(t, err)
	assert.Equal(t, "main", f.spawner.spawned[0].Variables["base"])
}

func TestSpawnInputs_EmptyStringLiteralPassedThrough(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	in := fullStageInputs()
	in["branch"] = ""

	_, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, in), stageVars())
	require.NoError(t, err)
	assert.Equal(t, "", f.spawner.spawned[0].Variables["branch"])
}

// Review Focus 2: mapped values bypass the preparer's parent-context
// truncation entirely.
func TestSpawnInputs_LargeMappedArrayIntact(t *testing.T) {
	f := newSpawnExecFixture(t).withStage()
	big := make([]any, 10000)
	for i := range big {
		big[i] = "path/to/file-" + uuid.NewString()
	}
	vars := stageVars()
	vars.SetNodeOutput("prior", map[string]any{"paths": big})

	_, err := f.runVars(t, mappedNode("stage", workflow.SpawnModeFireAndForget, fullStageInputs()), vars)
	require.NoError(t, err)

	got, ok := f.spawner.spawned[0].Variables["paths"].([]any)
	require.True(t, ok)
	assert.Len(t, got, 10000)
	assert.Equal(t, big[9999], got[9999])
	assert.Equal(t, 0, f.preparer.calls)
}

func TestSpawnInputs_AwaitedReentryDoesNotResolveAgain(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	f.spawner.inputsByWorkflow["stage"] = stageContract()
	node := mappedNode("stage", workflow.SpawnModeWaitForChild, fullStageInputs())
	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)

	_, err := f.exec.Execute(ctx, node, stageVars(), "parent")
	require.NoError(t, err)
	require.Len(t, f.spawner.spawned, 1)

	f.spawnWaits.set(f.parentID, "spawn-1", instance.SpawnModeWaitForChild,
		&instance.ChildLink{ChildID: f.spawner.nextID, ChildState: instance.StateRunning})
	changed := stageVars()
	changed.SetNodeOutput("intake", map[string]any{"repo_url": "r", "branch": "changed", "topic": "t"})

	res, err := f.exec.Execute(ctx, node, changed, "parent")
	require.NoError(t, err)
	assert.True(t, res.IsWait)
	assert.Len(t, f.spawner.spawned, 1)
	assert.Equal(t, 0, f.preparer.calls)
	assert.NotContains(t, res.Output.(map[string]any), "inputSources")
}
