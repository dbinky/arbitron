package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type launchFixture struct {
	groups     *memGroupRuns
	starter    *fakeGroupTaskStarter
	contracts  *fakeChildContracts
	workspaces *dirWorkspaces
	groupWS    *copyGroupWorkspace
	identities *fakeIdentityResolver
	canceller  *recordingCanceller
	bus        *mockEventBus
	instances  *mockInstanceRepo
	launcher   *engine.GroupLauncher
	parent     *instance.Instance
	owner      uuid.UUID
	parentWS   string
}

func newLaunchFixture(t *testing.T) *launchFixture {
	t.Helper()
	owner := uuid.New()
	instances := newMockInstanceRepo()
	ws := newDirWorkspaces(t)
	parentID := uuid.New()
	parentWS := ws.path(parentID)
	require.NoError(t, os.MkdirAll(filepath.Join(parentWS, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(parentWS, "README.md"), []byte("hello group"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(parentWS, "src", "main.go"), []byte("package main\n"), 0o644))
	g := "g"
	parent := &instance.Instance{
		ID: parentID, OwnerID: owner, WorkflowName: "parent-wf", State: instance.StateRunning,
		Priority: instance.PriorityHigh, CurrentNodeID: &g,
	}
	require.NoError(t, instances.Create(context.Background(), parent))

	f := &launchFixture{
		groups:  newMemGroupRuns(),
		starter: &fakeGroupTaskStarter{instances: instances},
		contracts: &fakeChildContracts{byName: map[string][]workflow.InputField{
			"child-wf": {
				{Name: "repo", Type: "string", Required: true},
				{Name: "ticket", Type: "string", Required: true},
				{Name: "count", Type: "number"},
			},
		}},
		workspaces: ws,
		groupWS:    newCopyGroupWorkspace(t),
		identities: &fakeIdentityResolver{byUser: map[uuid.UUID]identity.Identity{
			owner: {UserID: owner, Role: identity.RoleUser, Departments: []string{"engineering"}},
		}},
		canceller: &recordingCanceller{instances: instances},
		bus:       newMockEventBus(),
		instances: instances,
		parent:    parent,
		owner:     owner,
		parentWS:  parentWS,
	}
	f.launcher = engine.NewGroupLauncher(f.groups, f.starter, f.contracts, f.workspaces, f.groupWS, f.identities, f.canceller, f.bus)
	return f
}

func glStore(t *testing.T, parentWS string) *variable.VariableStore {
	t.Helper()
	s := variable.NewStore()
	require.NoError(t, s.SetInput("repo", "acme/widgets"))
	require.NoError(t, s.SetInput("count", float64(3)))
	require.NoError(t, s.SetInput("cfg", map[string]any{"depth": map[string]any{"leaf": "v"}, "list": []any{"a"}}))
	s.SetNodeOutput("init", map[string]any{"ticket": "T-1"})
	if parentWS != "" {
		s.SetReserved("workspace", parentWS)
	}
	return s
}

func glExpectedStart() map[string]any {
	return map[string]any{
		"repo":  "acme/widgets",
		"count": float64(3),
		"cfg":   map[string]any{"depth": map[string]any{"leaf": "v"}, "list": []any{"a"}},
		"init":  map[string]any{"ticket": "T-1"},
	}
}

func glBranchConfig() workflow.GroupConfig {
	return workflow.GroupConfig{
		Form: workflow.GroupFormBranches,
		Tasks: []workflow.GroupTask{
			{Key: "a", Entry: "a1", Inputs: map[string]any{
				"n": "{{count}}", "cfg_copy": "{{cfg}}", "work_dir": "{{workspace}}/repo",
				"label": "task-{{repo}}", "flag": true,
			}},
			{Key: "b", Entry: "b1"},
		},
		CompletionRule:     workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedCancel,
		GatherMode:         workflow.GroupGatherResults,
	}
}

func glWorkflowsConfig() workflow.GroupConfig {
	return workflow.GroupConfig{
		Form: workflow.GroupFormWorkflows,
		Tasks: []workflow.GroupTask{
			{Key: "x", Workflow: "child-wf", Inputs: map[string]any{"ticket": "{{init.ticket}}"}},
			{Key: "y", Workflow: "child-wf", Inputs: map[string]any{"ticket": "T-2", "count": float64(7)}},
		},
		CompletionRule:     workflow.GroupRuleFirstSuccess,
		UnfinishedBehavior: workflow.GroupUnfinishedFinishIndependently,
		GatherMode:         workflow.GroupGatherResults,
	}
}

func glScope(cfg workflow.GroupConfig) *workflow.GroupScope {
	bodies := map[string][]string{}
	for _, task := range cfg.Tasks {
		if task.Entry != "" {
			bodies[task.Key] = []string{task.Entry}
		}
	}
	return &workflow.GroupScope{
		NodeID: "g", Config: cfg, Bodies: bodies, Nested: []string{},
		SuccessTarget: "after", FailureTarget: "fail",
	}
}

func (f *launchFixture) requestWith(t *testing.T, cfg workflow.GroupConfig, store *variable.VariableStore) engine.GroupLaunchRequest {
	t.Helper()
	c := cfg
	return engine.GroupLaunchRequest{
		Parent:    f.parent,
		Node:      workflow.Node{ID: "g", Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: &c}},
		Config:    cfg,
		Ownership: glScope(cfg),
		Vars:      store,
	}
}

func (f *launchFixture) request(t *testing.T, cfg workflow.GroupConfig) engine.GroupLaunchRequest {
	t.Helper()
	return f.requestWith(t, cfg, glStore(t, f.parentWS))
}

func (f *launchFixture) assertNothingPersisted(t *testing.T) {
	t.Helper()
	assert.Equal(t, 0, f.groups.count(), "no group run may be persisted by a pre-launch failure")
	assert.Empty(t, f.starter.requests(), "no task may be started by a pre-launch failure")
	for _, id := range f.workspaces.ensuredIDs() {
		assert.NoDirExists(t, f.workspaces.path(id), "prepared task directories are removed")
	}
	assert.ElementsMatch(t, f.groupWS.snapshotHandles(), f.groupWS.releasedHandles(), "a snapshot taken is released")
}

// ---------- happy path ----------

func TestGroupLauncher_PersistsTheRosterBeforeLaunchingAndOpensTheBarrierLast(t *testing.T) {
	f := newLaunchFixture(t)
	cfg := glBranchConfig()
	var atFirst, atSecond *instance.GroupRun
	f.starter.onStart = func(req engine.GroupTaskStartRequest) {
		snap := f.groups.get(req.GroupRunID)
		if req.TaskKey == "a" {
			atFirst = snap
		} else {
			atSecond = snap
		}
	}

	run, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.NoError(t, err)
	require.NotNil(t, run)

	require.NotNil(t, atFirst, "the roster must be persisted BEFORE the first task is started")
	assert.Equal(t, instance.GroupRunLaunching, atFirst.State)
	require.Len(t, atFirst.Tasks, 2)
	for _, task := range atFirst.Tasks {
		assert.Equal(t, instance.GroupTaskPending, task.LaunchState)
		assert.NotEqual(t, uuid.Nil, task.ChildInstanceID, "child ids are reserved in the roster before enqueue")
	}
	require.NotNil(t, atSecond)
	assert.Equal(t, instance.GroupRunLaunching, atSecond.State, "decisions stay disabled until every task is launched")
	assert.Equal(t, instance.GroupTaskLaunched, atSecond.Tasks[0].LaunchState)

	assert.Equal(t, instance.GroupRunRunning, run.State)
	assert.Equal(t, f.parent.ID, run.ParentInstanceID)
	assert.Equal(t, "g", run.NodeID)
	assert.Equal(t, 1, run.Attempt)
	assert.Equal(t, workflow.GroupFormBranches, run.Form)
	assert.Equal(t, workflow.GroupRuleAllSucceed, run.CompletionRule)
	assert.Equal(t, workflow.GroupUnfinishedCancel, run.UnfinishedBehavior)
	assert.Equal(t, workflow.GroupGatherResults, run.GatherMode)
	assert.Equal(t, "branches", run.Config["form"])
	assert.Equal(t, map[string][]string{"a": {"a1"}, "b": {"b1"}}, run.BodyOwnership)
	assert.Equal(t, glExpectedStart(), run.StartVariables, "start variables are captured once, minus reserved names")
	assert.NotEmpty(t, run.WorkspaceSnapshot)

	reqs := f.starter.requests()
	require.Len(t, reqs, 2)
	idents := f.starter.identities()
	for i, key := range []string{"a", "b"} {
		task := run.Tasks[i]
		assert.Equal(t, key, task.Key)
		assert.Equal(t, i, task.Position)
		assert.Equal(t, instance.GroupTaskKindBranch, task.Kind)
		assert.Equal(t, key+"1", task.Origin)
		assert.Equal(t, instance.GroupTaskLaunched, task.LaunchState)
		assert.True(t, task.Blocking)
		assert.Equal(t, task.ChildInstanceID, reqs[i].TaskInstanceID, "the started child uses the id the roster reserved")
		assert.Equal(t, key, reqs[i].TaskKey)
		assert.Equal(t, instance.GroupTaskKindBranch, reqs[i].Kind)
		assert.Equal(t, key+"1", reqs[i].BranchEntry)
		assert.Empty(t, reqs[i].ChildWorkflow)
		assert.Equal(t, f.parent.ID, reqs[i].ParentInstanceID)
		assert.Equal(t, "g", reqs[i].ParentNodeID)
		assert.Equal(t, run.ID, reqs[i].GroupRunID)
		assert.Equal(t, "high", reqs[i].Priority, "tasks inherit the parent's priority")
		assert.Equal(t, f.owner, idents[i].UserID, "tasks start under the reconstructed OWNER identity")
		assert.Equal(t, []string{"engineering"}, idents[i].Departments)
	}

	events := f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupLaunched)
	require.Len(t, events, 1)
	data := events[0].Data.(map[string]any)
	assert.Equal(t, run.ID.String(), data["groupRunId"])
	assert.Equal(t, "g", data["nodeId"])
	assert.Equal(t, 2, data["tasks"])
}

func TestGroupLauncher_StartVariablesAndFilesAreIdenticalInBothForms(t *testing.T) {
	cases := []struct {
		name string
		cfg  workflow.GroupConfig
		want func(taskDir string) []map[string]any
	}{
		{name: "branches", cfg: glBranchConfig(), want: func(dirA string) []map[string]any {
			a := glExpectedStart()
			a["n"] = float64(3)
			a["cfg_copy"] = map[string]any{"depth": map[string]any{"leaf": "v"}, "list": []any{"a"}}
			a["work_dir"] = dirA + "/repo"
			a["label"] = "task-acme/widgets"
			a["flag"] = true
			return []map[string]any{a, glExpectedStart()}
		}},
		{name: "workflows (one child workflow reused with different inputs)", cfg: glWorkflowsConfig(), want: func(string) []map[string]any {
			x := glExpectedStart()
			x["ticket"] = "T-1"
			y := glExpectedStart()
			y["ticket"] = "T-2"
			y["count"] = float64(7)
			return []map[string]any{x, y}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			_, err := f.launcher.Launch(context.Background(), f.request(t, c.cfg))
			require.NoError(t, err)

			reqs := f.starter.requests()
			require.Len(t, reqs, 2)
			want := c.want(f.workspaces.path(reqs[0].TaskInstanceID))
			for i, r := range reqs {
				assert.Equal(t, want[i], r.Variables, "task %s", r.TaskKey)
				assert.NotContains(t, r.Variables, "workspace", "each task's own run seeds its workspace")

				dir := f.workspaces.path(r.TaskInstanceID)
				readme, rerr := os.ReadFile(filepath.Join(dir, "README.md"))
				require.NoError(t, rerr, "task %s must start with the parent's files", r.TaskKey)
				assert.Equal(t, "hello group", string(readme))
				src, serr := os.ReadFile(filepath.Join(dir, "src", "main.go"))
				require.NoError(t, serr, "relative layout is preserved")
				assert.Equal(t, "package main\n", string(src))
			}
		})
	}
}

func TestGroupLauncher_EveryTaskStartsFromAnIsolatedCopy(t *testing.T) {
	f := newLaunchFixture(t)
	store := glStore(t, f.parentWS)
	_, err := f.launcher.Launch(context.Background(), f.requestWith(t, glBranchConfig(), store))
	require.NoError(t, err)
	reqs := f.starter.requests()
	require.Len(t, reqs, 2)

	// Variables: task A's nested write reaches neither task B nor the stored start snapshot.
	reqs[0].Variables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"] = "task-a"
	assert.Equal(t, "v", reqs[1].Variables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"])
	require.Len(t, f.groups.createdRaw, 1)
	assert.Equal(t, "v", f.groups.createdRaw[0].StartVariables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"])

	// A later write to the parent's live store reaches nothing that was captured.
	live, ok := store.Get("cfg")
	require.True(t, ok)
	live.(map[string]any)["depth"].(map[string]any)["leaf"] = "parent-later"
	assert.Equal(t, "v", reqs[1].Variables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"])
	assert.Equal(t, "v", f.groups.createdRaw[0].StartVariables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"])

	// Files: task A's writes reach neither task B nor the parent.
	dirA := f.workspaces.path(reqs[0].TaskInstanceID)
	dirB := f.workspaces.path(reqs[1].TaskInstanceID)
	require.NoError(t, os.WriteFile(filepath.Join(dirA, "new.txt"), []byte("a only"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dirA, "README.md"), []byte("changed by a"), 0o644))
	assert.NoFileExists(t, filepath.Join(dirB, "new.txt"))
	assert.NoFileExists(t, filepath.Join(f.parentWS, "new.txt"))
	b, err := os.ReadFile(filepath.Join(dirB, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "hello group", string(b))
	p, err := os.ReadFile(filepath.Join(f.parentWS, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "hello group", string(p))
}

func TestGroupLauncher_TypedInputsArePreserved(t *testing.T) {
	f := newLaunchFixture(t)
	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)
	a := f.starter.requests()[0].Variables

	assert.IsType(t, float64(0), a["n"], "a number stays a number")
	assert.IsType(t, map[string]any{}, a["cfg_copy"], "a map stays a map")
	assert.IsType(t, true, a["flag"], "a literal bool stays a bool")
}

func TestGroupLauncher_WorkflowKind_RequiredInputFromBaseAccepted(t *testing.T) {
	f := newLaunchFixture(t)
	run, err := f.launcher.Launch(context.Background(), f.request(t, glWorkflowsConfig()))
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunRunning, run.State)
	assert.Equal(t, instance.GroupTaskKindWorkflow, run.Tasks[0].Kind)
	assert.Equal(t, "child-wf", run.Tasks[0].Origin)
	reqs := f.starter.requests()
	assert.Equal(t, "child-wf", reqs[0].ChildWorkflow)
	assert.Empty(t, reqs[0].BranchEntry)
	assert.Equal(t, "acme/widgets", reqs[0].Variables["repo"], "the required input comes from the inherited base")

	assert.Equal(t, []string{"child-wf", "child-wf"}, f.contracts.names)
	for _, who := range f.contracts.asked {
		assert.Equal(t, f.owner, who.UserID, "the child contract is read under the OWNER's identity, never the ambient one")
	}
}

// ---------- error: pre-launch, nothing persisted ----------

func TestGroupLauncher_MissingTemplateReferenceIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	cfg := glBranchConfig()
	cfg.Tasks[1].Inputs = map[string]any{"x": "{{nope.path}}"}

	run, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.Error(t, err)
	assert.Nil(t, run)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference)
	assert.Contains(t, err.Error(), `task "b"`)
	assert.Len(t, f.workspaces.ensuredIDs(), 2)
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_SharedPathIsRejectedPreLaunch(t *testing.T) {
	t.Run("inherited work_dir inside the parent's area", func(t *testing.T) {
		f := newLaunchFixture(t)
		store := glStore(t, f.parentWS)
		require.NoError(t, store.SetInput("work_dir", filepath.Join(f.parentWS, "repo")))

		_, err := f.launcher.Launch(context.Background(), f.requestWith(t, glBranchConfig(), store))
		require.ErrorIs(t, err, engine.ErrGroupSharedPath, "task b inherits work_dir unchanged")
		assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
		assert.Contains(t, err.Error(), `"work_dir"`)
		f.assertNothingPersisted(t)
	})
	t.Run("explicit nested input inside the parent's area", func(t *testing.T) {
		f := newLaunchFixture(t)
		cfg := glBranchConfig()
		cfg.Tasks[0].Inputs["out"] = map[string]any{"dir": filepath.Join(f.parentWS, "x")}

		_, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
		require.ErrorIs(t, err, engine.ErrGroupSharedPath)
		assert.Contains(t, err.Error(), `"out.dir"`)
		f.assertNothingPersisted(t)
	})
}

func TestGroupLauncher_WorkflowKind_ContractViolationsArePreLaunch(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(f *launchFixture, cfg *workflow.GroupConfig)
		wantIs  error
		wantMsg string
	}{
		{name: "missing required input", wantIs: engine.ErrChildContractUnsatisfied, wantMsg: "approver",
			mutate: func(f *launchFixture, _ *workflow.GroupConfig) {
				f.contracts.byName["child-wf"] = append(f.contracts.byName["child-wf"],
					workflow.InputField{Name: "approver", Type: "string", Required: true})
			}},
		{name: "declared type mismatch", wantIs: engine.ErrChildContractUnsatisfied, wantMsg: `"count"`,
			mutate: func(_ *launchFixture, cfg *workflow.GroupConfig) { cfg.Tasks[1].Inputs["count"] = "seven" }},
		{name: "invisible or absent child workflow", wantIs: workflow.ErrNotFound, wantMsg: "secret-wf",
			mutate: func(_ *launchFixture, cfg *workflow.GroupConfig) { cfg.Tasks[0].Workflow = "secret-wf" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			cfg := glWorkflowsConfig()
			c.mutate(f, &cfg)

			_, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
			require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
			assert.ErrorIs(t, err, c.wantIs)
			assert.Contains(t, err.Error(), c.wantMsg)
			f.assertNothingPersisted(t)
		})
	}
}

func TestGroupLauncher_SnapshotFailureIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	f.groupWS.snapErr = errors.New("disk full")

	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "snapshot")
	assert.Contains(t, err.Error(), "disk full")
	assert.Empty(t, f.workspaces.ensuredIDs(), "no task directory is prepared once the snapshot fails")
	f.assertNothingPersisted(t)
}

// A workspace adapter can fail after creating its directory (for example,
// while tightening permissions). That directory must be cleaned up too.
type partiallyFailingWorkspaces struct{ *dirWorkspaces }

func (w partiallyFailingWorkspaces) EnsureInstanceDir(id uuid.UUID) (string, error) {
	_, err := w.dirWorkspaces.EnsureInstanceDir(id)
	if err != nil {
		return "", err
	}
	return "", errors.New("chmod failed")
}

func TestGroupLauncher_DirectoryCreationFailureIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	l := engine.NewGroupLauncher(f.groups, f.starter, f.contracts,
		partiallyFailingWorkspaces{f.workspaces}, f.groupWS, f.identities, f.canceller, f.bus)

	_, err := l.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "chmod failed")
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_ReservedInputIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	cfg := glBranchConfig()
	cfg.Tasks[0].Inputs["workspace"] = "/shared"

	_, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, workflow.ErrGroupReservedInput)
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_ForkFailureIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	f.groupWS.forkErr = errors.New("permission denied")

	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "fork")
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_RosterPersistFailureIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	f.groups.failOnce("CreateWithTasks", errors.New("disk I/O error"))

	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "disk I/O error")
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_IdentityResolutionFailureIsPreLaunchWithNoFallback(t *testing.T) {
	f := newLaunchFixture(t)
	f.identities.err = errors.New("user deleted")

	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "identity")
	assert.Empty(t, f.groupWS.snapshotHandles())
	f.assertNothingPersisted(t)
}

// Provenance: the fork capability is a required input whenever the parent has
// a working area. Without it the launcher refuses rather than letting tasks
// share the parent's mutable directory.
func TestGroupLauncher_NilGroupWorkspaceWithAParentWorkspaceRefuses(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(f *launchFixture) *engine.GroupLauncher
	}{
		{"nil GroupWorkspace", func(f *launchFixture) *engine.GroupLauncher {
			return engine.NewGroupLauncher(f.groups, f.starter, f.contracts, f.workspaces, nil, f.identities, f.canceller, f.bus)
		}},
		{"nil WorkspaceManager", func(f *launchFixture) *engine.GroupLauncher {
			return engine.NewGroupLauncher(f.groups, f.starter, f.contracts, nil, f.groupWS, f.identities, f.canceller, f.bus)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			_, err := c.build(f).Launch(context.Background(), f.request(t, glBranchConfig()))
			require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
			assert.Contains(t, err.Error(), "fork")
			assert.Empty(t, f.workspaces.ensuredIDs())
			f.assertNothingPersisted(t)
		})
	}
}

func TestGroupLauncher_WorkflowFormWithoutAContractReaderRefuses(t *testing.T) {
	f := newLaunchFixture(t)
	l := engine.NewGroupLauncher(f.groups, f.starter, nil, f.workspaces, f.groupWS, f.identities, f.canceller, f.bus)

	_, err := l.Launch(context.Background(), f.request(t, glWorkflowsConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "child-contract reader")
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_IncompleteRequestIsPreLaunch(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *engine.GroupLaunchRequest)
	}{
		{"no parent", func(r *engine.GroupLaunchRequest) { r.Parent = nil }},
		{"no parent id", func(r *engine.GroupLaunchRequest) { r.Parent.ID = uuid.Nil }},
		{"no node id", func(r *engine.GroupLaunchRequest) { r.Node.ID = ""; r.Ownership.NodeID = "" }},
		{"no variable store", func(r *engine.GroupLaunchRequest) { r.Vars = nil }},
		{"no ownership", func(r *engine.GroupLaunchRequest) { r.Ownership = nil }},
		{"ownership of another group", func(r *engine.GroupLaunchRequest) { r.Ownership.NodeID = "other" }},
		{"no tasks", func(r *engine.GroupLaunchRequest) { r.Config.Tasks = nil }},
		{"entry outside the validated body", func(r *engine.GroupLaunchRequest) { r.Ownership.Bodies["b"] = []string{"elsewhere"} }},
		{"branches task naming a workflow", func(r *engine.GroupLaunchRequest) { r.Config.Tasks[1].Workflow = "child-wf" }},
		{"unknown form", func(r *engine.GroupLaunchRequest) { r.Config.Form = "matrix" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			req := f.request(t, glBranchConfig())
			c.mutate(&req)

			_, err := f.launcher.Launch(context.Background(), req)
			require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
			f.assertNothingPersisted(t)
		})
	}
}

// ---------- edge: no parent working area ----------

func TestGroupLauncher_NoParentWorkspaceRunsWithoutAFork(t *testing.T) {
	f := newLaunchFixture(t)
	l := engine.NewGroupLauncher(f.groups, f.starter, f.contracts, nil, nil, f.identities, f.canceller, f.bus)
	cfg := glBranchConfig()
	delete(cfg.Tasks[0].Inputs, "work_dir")

	run, err := l.Launch(context.Background(), f.requestWith(t, cfg, glStore(t, "")))
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunRunning, run.State)
	assert.Empty(t, run.WorkspaceSnapshot, "no parent area: no snapshot handle")
	assert.Len(t, f.starter.requests(), 2)

	cfg.Tasks[0].Inputs["work_dir"] = "{{workspace}}/repo"
	f2 := newLaunchFixture(t)
	l2 := engine.NewGroupLauncher(f2.groups, f2.starter, f2.contracts, nil, nil, f2.identities, f2.canceller, f2.bus)
	_, err = l2.Launch(context.Background(), f2.requestWith(t, cfg, glStore(t, "")))
	require.ErrorIs(t, err, variable.ErrUnresolvedReference, "without a task area {{workspace}} cannot resolve")
	f2.assertNothingPersisted(t)
}

func TestGroupLauncher_ResultsModeReleasesTheSnapshotOnceLaunched(t *testing.T) {
	f := newLaunchFixture(t)
	run, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)
	assert.Equal(t, []string{run.WorkspaceSnapshot}, f.groupWS.releasedHandles(),
		"a results-only group needs its baseline only to fork the tasks")
}

// ---------- failure: post-persist partial launch ----------

func TestGroupLauncher_PartialLaunchFailureDecidesFailureAndCancelsLaunchedTasks(t *testing.T) {
	f := newLaunchFixture(t)
	cfg := glBranchConfig()
	cfg.Tasks = append(cfg.Tasks, workflow.GroupTask{Key: "c", Entry: "c1"})
	f.starter.errByKey = map[string]error{"b": errors.New("queue full")}

	run, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.NoError(t, err, "a post-persist launch failure is DECIDED, not returned")
	require.NotNil(t, run)

	assert.Equal(t, instance.GroupRunDecided, run.State)
	require.NotNil(t, run.Decision)
	assert.Equal(t, "failure", run.Decision.Outcome)
	assert.Equal(t, `launch failed: task "b": queue full`, run.Decision.Reason)

	a, b, c := run.Tasks[0], run.Tasks[1], run.Tasks[2]
	assert.Equal(t, instance.GroupTaskLaunched, a.LaunchState)
	assert.True(t, a.CancelRequested)
	assert.Equal(t, instance.GroupTaskLaunchFailed, b.LaunchState)
	assert.Equal(t, `task "b": queue full`, b.LaunchError)
	assert.Equal(t, instance.GroupTaskPending, c.LaunchState, "a task after the failure is never started")

	frozen := run.Decision.Tasks
	assert.Equal(t, instance.GroupTaskStatusUnfinished, frozen[0].Status)
	assert.True(t, frozen[0].CancelRequested)
	assert.Equal(t, instance.GroupTaskStatusUnfinished, frozen[1].Status)
	assert.Empty(t, frozen[1].ChildInstanceID)
	assert.Equal(t, `not launched: task "b": queue full`, frozen[1].Failure["error"])
	assert.Equal(t, `not launched: launch failed: task "b": queue full`, frozen[2].Failure["error"])

	keys := []string{}
	for _, r := range f.starter.requests() {
		keys = append(keys, r.TaskKey)
	}
	assert.Equal(t, []string{"a", "b"}, keys)

	calls := f.canceller.callsFor(a.ChildInstanceID)
	require.Len(t, calls, 1, "the already-launched task is cancelled")
	assert.Equal(t, f.owner, calls[0].Identity.UserID, "under the OWNER's identity")
	assert.Equal(t, instance.StateCancelled, f.instances.get(a.ChildInstanceID).State)
	assert.Len(t, f.canceller.callsFor(b.ChildInstanceID), 1, "the failed task's reserved child is stopped if it exists")
	assert.NoDirExists(t, f.workspaces.path(c.ChildInstanceID), "the never-started task's prepared area is removed")
	assert.Len(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupDecided), 1)
	assert.Empty(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupLaunched))

	// Re-call (retry / re-entry): the existing run is returned and no child is
	// started twice.
	again, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.NoError(t, err)
	assert.Equal(t, run.ID, again.ID)
	assert.Len(t, f.starter.requests(), 2, "no duplicate children")
	assert.Equal(t, 1, f.groups.count())
}

func TestGroupLauncher_UnrecordedLaunchIsTreatedAsALaunchFailure(t *testing.T) {
	f := newLaunchFixture(t)
	f.groups.failOnce("MarkTaskLaunched", errors.New("database is locked"))

	run, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunDecided, run.State)
	assert.Contains(t, run.Decision.Reason, "started but its launch could not be recorded")
	a := run.Tasks[0]
	assert.Equal(t, instance.StateCancelled, f.instances.get(a.ChildInstanceID).State,
		"the child that DID start is stopped even though its launch was never recorded")
	assert.Len(t, f.starter.requests(), 1)
}

func TestGroupLauncher_ExistingRunIsReturnedWithoutRelaunching(t *testing.T) {
	f := newLaunchFixture(t)
	first, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)

	second, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Len(t, f.starter.requests(), 2)
	for _, id := range f.workspaces.ensuredIDs()[2:] {
		assert.NoDirExists(t, f.workspaces.path(id), "the losing preparation is cleaned up")
	}
}

func TestGroupLauncher_ExistingRunReadFailureIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)
	readErr := errors.New("database unavailable")
	f.groups.failOnce("GetByParentNode", readErr)

	_, err = f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, readErr)
	assert.Len(t, f.starter.requests(), 2)
	assert.Equal(t, 1, f.groups.count())
	for _, id := range f.workspaces.ensuredIDs()[2:] {
		assert.NoDirExists(t, f.workspaces.path(id))
	}
	assert.ElementsMatch(t, f.groupWS.snapshotHandles(), f.groupWS.releasedHandles())
}

func TestGroupLauncher_FilesModeKeepsTheBaseline(t *testing.T) {
	f := newLaunchFixture(t)
	area := launchFileArea{newCopyGroupWorkspace(t)}
	f.launcher.SetFileWorkingArea(area)
	cfg := glBranchConfig()
	cfg.GatherMode = workflow.GroupGatherResultsAndFiles

	run, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.NoError(t, err)
	assert.DirExists(t, run.WorkspaceSnapshot)
	assert.Equal(t, []string{run.WorkspaceSnapshot}, area.snapshotHandles())
	assert.Empty(t, area.releasedHandles())
	assert.Empty(t, f.groupWS.snapshotHandles())
}

func TestNewGroupLauncher_PanicsOnAMissingRequiredCollaborator(t *testing.T) {
	f := newLaunchFixture(t)
	assert.Panics(t, func() {
		engine.NewGroupLauncher(nil, f.starter, f.contracts, f.workspaces, f.groupWS, f.identities, f.canceller, f.bus)
	})
	assert.Panics(t, func() {
		engine.NewGroupLauncher(f.groups, nil, f.contracts, f.workspaces, f.groupWS, f.identities, f.canceller, f.bus)
	})
	assert.Panics(t, func() {
		engine.NewGroupLauncher(f.groups, f.starter, f.contracts, f.workspaces, f.groupWS, nil, f.canceller, f.bus)
	})
	assert.Panics(t, func() {
		engine.NewGroupLauncher(f.groups, f.starter, f.contracts, f.workspaces, f.groupWS, f.identities, nil, f.bus)
	})
	assert.Panics(t, func() {
		engine.NewGroupLauncher(f.groups, f.starter, f.contracts, f.workspaces, f.groupWS, f.identities, f.canceller, nil)
	})
}

// Drive the launcher's real starter and contract-reader seams. The sink is
// the linked instance row already present at queue intake, with files copied
// by the production filesystem adapter, not just a recorded start request.
func TestGroupLauncher_RealStarterEnqueuesComposedVariablesAndForkedFiles(t *testing.T) {
	for _, cfg := range []workflow.GroupConfig{glBranchConfig(), glWorkflowsConfig()} {
		t.Run(string(cfg.Form), func(t *testing.T) {
			h := newGroupTaskHarness(t)
			owner := uuid.New()
			parent := h.seedParent(t, owner, 0)
			wf := gtParentWorkflow()
			wf.Nodes[0].Config.Group = &cfg
			if cfg.Form == workflow.GroupFormWorkflows {
				wf.Edges = wf.Edges[2:] // workflows have no branch task edges
			}
			parent.WorkflowSnapshot = gtSnapshot(t, wf)
			graph, findings := workflow.AnalyzeGroups(&wf)
			require.Empty(t, findings)
			childWF := gtChildWorkflow("child-wf")
			childWF.Inputs = append(childWF.Inputs, workflow.InputField{Name: "ticket", Type: "string", Required: true})
			h.repo.put(workflow.Private(owner), childWF)

			root := t.TempDir()
			ws, err := filesystem.NewWorkspaceManager(root)
			require.NoError(t, err)
			groupWS, err := filesystem.NewGroupWorkspace(root)
			require.NoError(t, err)
			h.engine.SetWorkspaceManager(ws)
			parentDir, err := ws.EnsureInstanceDir(parent.ID)
			require.NoError(t, err)
			content := "parent files for " + parent.ID.String()
			require.NoError(t, os.WriteFile(filepath.Join(parentDir, "source.txt"), []byte(content), 0o600))

			groups := newMemGroupRuns()
			identities := &fakeIdentityResolver{byUser: map[uuid.UUID]identity.Identity{
				owner: {UserID: owner, Role: identity.RoleUser, Departments: []string{"engineering"}},
			}}
			l := engine.NewGroupLauncher(groups, h.engine, h.engine, ws, groupWS, identities, h.engine, h.bus)
			// An unrelated ambient identity must not supply the task's access.
			run, err := l.Launch(ctxAs(uuid.New()), engine.GroupLaunchRequest{
				Parent: parent, Node: wf.Nodes[0], Config: cfg, Ownership: graph.Groups["g"], Vars: glStore(t, parentDir),
			})
			require.NoError(t, err)
			require.Equal(t, instance.GroupRunRunning, run.State)
			require.Len(t, h.enqueuedIDs(), len(cfg.Tasks))
			for i, task := range run.Tasks {
				child := h.rowAtEnqueue(task.ChildInstanceID)
				require.NotNil(t, child)
				assert.Equal(t, owner, child.OwnerID)
				require.NotNil(t, child.GroupRunID)
				assert.Equal(t, run.ID, *child.GroupRunID)
				require.NotNil(t, child.GroupTaskKey)
				assert.Equal(t, cfg.Tasks[i].Key, *child.GroupTaskKey)
				assert.Equal(t, "acme/widgets", child.Variables["repo"])
				assert.Equal(t, glExpectedStart()["cfg"], child.Variables["cfg"])
				assert.NotContains(t, child.Variables, "workspace")
				dir, err := ws.EnsureInstanceDir(child.ID)
				require.NoError(t, err)
				if cfg.Form == workflow.GroupFormBranches && i == 0 {
					assert.Equal(t, float64(3), child.Variables["n"])
					assert.Equal(t, filepath.Join(dir, "repo"), child.Variables["work_dir"])
				}
				if cfg.Form == workflow.GroupFormWorkflows {
					assert.Equal(t, []string{"T-1", "T-2"}[i], child.Variables["ticket"])
				}
				data, err := os.ReadFile(filepath.Join(dir, "source.txt"))
				require.NoError(t, err)
				assert.Equal(t, content, string(data))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "source.txt"), []byte(task.Key), 0o600))
			}
			data, err := os.ReadFile(filepath.Join(parentDir, "source.txt"))
			require.NoError(t, err)
			assert.Equal(t, content, string(data), "tasks never write back into the parent's working area")
		})
	}
}

func TestGroupEventTypes_MatchTheContract(t *testing.T) {
	assert.Equal(t, "group.launched", engine.EventTypeGroupLaunched)
	assert.Equal(t, "group.task_updated", engine.EventTypeGroupTaskUpdated)
	assert.Equal(t, "group.decided", engine.EventTypeGroupDecided)
	assert.Equal(t, "group.gathering", engine.EventTypeGroupGathering)
	assert.Equal(t, "group.conflict", engine.EventTypeGroupConflict)
	assert.Equal(t, "group.resolved", engine.EventTypeGroupResolved)
	assert.Equal(t, "group.completed", engine.EventTypeGroupCompleted)
}
