package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errLaunchAreaUnused = errors.New("not used by the launcher")

// launchFileArea is a file WorkingArea whose snapshot/fork really copy
// directories (P2-T8's copyGroupWorkspace), so the tests assert the SINK: the
// bytes in each task directory. The gather half is never reached by Launch.
type launchFileArea struct {
	*copyGroupWorkspace
}

var _ engine.WorkingArea = launchFileArea{}

func (launchFileArea) FinalizeTask(context.Context, string, uuid.UUID, string) (string, error) {
	return "", errLaunchAreaUnused
}
func (launchFileArea) BeginGather(context.Context, string, uuid.UUID, string) (engine.GatherHandle, error) {
	return engine.GatherHandle{}, errLaunchAreaUnused
}
func (launchFileArea) MergeTask(context.Context, engine.GatherHandle, string) (engine.MergeOutcome, error) {
	return engine.MergeOutcome{}, errLaunchAreaUnused
}
func (launchFileArea) ConflictPaths(context.Context, engine.GatherHandle) ([]string, error) {
	return nil, errLaunchAreaUnused
}
func (launchFileArea) CommitResolution(context.Context, engine.GatherHandle, string) error {
	return errLaunchAreaUnused
}
func (launchFileArea) Apply(context.Context, engine.GatherHandle) error { return errLaunchAreaUnused }
func (launchFileArea) Rollback(context.Context, engine.GatherHandle) error {
	return errLaunchAreaUnused
}
func (launchFileArea) ReleaseRef(context.Context, string, string) error { return errLaunchAreaUnused }

type preflightCall struct {
	parentDir string
	repoRel   string
}

// preflightFileArea additionally records every preflight with the arguments it
// was handed (the load-bearing parent directory and resolved hint).
type preflightFileArea struct {
	launchFileArea
	mu    sync.Mutex
	calls []preflightCall
	err   error
}

var _ engine.GatherPreflighter = (*preflightFileArea)(nil)

func (a *preflightFileArea) PreflightGather(_ context.Context, parentDir, repoRel string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, preflightCall{parentDir: parentDir, repoRel: repoRel})
	return a.err
}

func (a *preflightFileArea) preflights() []preflightCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]preflightCall(nil), a.calls...)
}

type fileLaunchFixture struct {
	groups    *memGroupRuns
	instances *mockInstanceRepo
	starter   *fakeGroupTaskStarter
	ws        *dirWorkspaces
	resultsWS *copyGroupWorkspace
	area      *preflightFileArea
	launcher  *engine.GroupLauncher
	parent    *instance.Instance
	parentWS  string
}

// newFileLaunchFixture: a running parent whose working area holds seed.txt and
// a src/ directory; results groups snapshot through resultsWS, file groups
// through area.
func newFileLaunchFixture(t *testing.T) *fileLaunchFixture {
	t.Helper()
	instances := newMockInstanceRepo()
	groups := newMemGroupRuns()
	starter := &fakeGroupTaskStarter{instances: instances}
	ws := newDirWorkspaces(t)
	resultsWS := newCopyGroupWorkspace(t)
	area := &preflightFileArea{launchFileArea: launchFileArea{newCopyGroupWorkspace(t)}}
	launcher := engine.NewGroupLauncher(groups, starter, nil, ws, resultsWS, fixedIdentityResolver{},
		&recordingCanceller{instances: instances}, newMockEventBus())
	launcher.SetFileWorkingArea(area)

	parent := &instance.Instance{ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "wf", State: instance.StateRunning, Priority: instance.PriorityNormal}
	require.NoError(t, instances.Create(context.Background(), parent))
	parentWS, err := ws.EnsureInstanceDir(parent.ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(parentWS, "seed.txt"), []byte("seed"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(parentWS, "src"), 0o755))

	return &fileLaunchFixture{groups: groups, instances: instances, starter: starter, ws: ws, resultsWS: resultsWS,
		area: area, launcher: launcher, parent: parent, parentWS: parentWS}
}

func fileLaunchConfig(gatherMode, repoPath string) workflow.GroupConfig {
	return workflow.GroupConfig{
		Form:               workflow.GroupFormBranches,
		Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
		CompletionRule:     workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedCancel,
		GatherMode:         gatherMode,
		RepoPath:           repoPath,
		ResolverProfile:    "resolver",
	}
}

func (f *fileLaunchFixture) vars(t *testing.T, withWorkspace bool, inputs map[string]any) *variable.VariableStore {
	t.Helper()
	ws := ""
	if withWorkspace {
		ws = f.parentWS
	}
	return repoVars(t, ws, inputs)
}

func (f *fileLaunchFixture) request(cfg workflow.GroupConfig, vars *variable.VariableStore) engine.GroupLaunchRequest {
	c := cfg
	return engine.GroupLaunchRequest{
		Parent:    f.parent,
		Node:      workflow.Node{ID: "g", Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: &c}},
		Config:    cfg,
		Ownership: &workflow.GroupScope{NodeID: "g", Config: cfg, Bodies: map[string][]string{"a": {"a1"}, "b": {"b1"}}},
		Vars:      vars,
	}
}

func (f *fileLaunchFixture) assertNothingStarted(t *testing.T) {
	t.Helper()
	run, err := f.groups.GetByParentNode(context.Background(), f.parent.ID, "g")
	require.NoError(t, err)
	assert.Nil(t, run, "nothing is persisted before the preflight passes")
	assert.Empty(t, f.starter.requests(), "no task starts")
	assert.Equal(t, []uuid.UUID{f.parent.ID}, f.ws.ensuredIDs(), "no task directory is prepared")
}

// PROVENANCE: the resolved hint and the parent's {{workspace}} reach the
// preflight, and the baseline is taken by the FILE area (not the results one).
func TestGroupLauncher_FilesMode_PreflightsThenSnapshotsThroughTheFileArea(t *testing.T) {
	f := newFileLaunchFixture(t)
	run, err := f.launcher.Launch(context.Background(),
		f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, "{{repo_dir}}"), f.vars(t, true, map[string]any{"repo_dir": "src"})))
	require.NoError(t, err)
	require.NotNil(t, run)

	assert.Equal(t, []preflightCall{{parentDir: f.parentWS, repoRel: "src"}}, f.area.preflights())
	require.Len(t, f.area.snapshotHandles(), 1)
	assert.Equal(t, f.area.snapshotHandles()[0], run.WorkspaceSnapshot)
	assert.Empty(t, f.resultsWS.snapshotHandles(), "a file group never snapshots through the results workspace")
	assert.Empty(t, f.area.releasedHandles(), "the files baseline is kept for the gather")

	stored, err := f.groups.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, "src", stored.Config[engine.GroupConfigKeyResolvedRepoPath])
	for _, task := range stored.Tasks {
		got, rerr := os.ReadFile(filepath.Join(f.ws.path(task.ChildInstanceID), "seed.txt"))
		require.NoError(t, rerr, "task %s starts from the parent's files", task.Key)
		assert.Equal(t, "seed", string(got))
	}
}

func TestGroupLauncher_ResultsMode_KeepsTheResultsWorkspace(t *testing.T) {
	f := newFileLaunchFixture(t)
	run, err := f.launcher.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResults, ""), f.vars(t, true, nil)))
	require.NoError(t, err)
	require.NotNil(t, run)

	assert.Len(t, f.resultsWS.snapshotHandles(), 1)
	assert.Len(t, f.resultsWS.releasedHandles(), 1, "results snapshots are released once launching ends")
	assert.Empty(t, f.area.snapshotHandles(), "results-only groups never need the file area (or git)")
	assert.Empty(t, f.area.preflights())
	_, present := run.Config[engine.GroupConfigKeyResolvedRepoPath]
	assert.False(t, present)
}

func TestGroupLauncher_FilesMode_PreflightFailureIsPreLaunch(t *testing.T) {
	f := newFileLaunchFixture(t)
	errNoGit := errors.New("git executable is not available on this host")
	f.area.err = errNoGit

	_, err := f.launcher.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, ""), f.vars(t, true, nil)))
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, errNoGit)
	assert.Empty(t, f.area.snapshotHandles(), "the preflight runs before the snapshot")
	f.assertNothingStarted(t)
}

func TestGroupLauncher_FilesMode_InvalidRepoPathIsRejectedBeforeThePreflight(t *testing.T) {
	f := newFileLaunchFixture(t)
	_, err := f.launcher.Launch(context.Background(),
		f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, "{{up}}/x"), f.vars(t, true, map[string]any{"up": ".."})))
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
	assert.Empty(t, f.area.preflights())
	f.assertNothingStarted(t)
}

func TestGroupLauncher_FilesMode_UnresolvedRepoPathTemplateIsPreLaunch(t *testing.T) {
	f := newFileLaunchFixture(t)
	_, err := f.launcher.Launch(context.Background(),
		f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, "{{nope}}"), f.vars(t, true, nil)))
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference)
	f.assertNothingStarted(t)
}

// PROVENANCE: without the composition-root setter a file group is refused —
// it is never silently snapshotted through the results workspace.
func TestGroupLauncher_FilesMode_WithoutAFileAreaRefuses(t *testing.T) {
	f := newFileLaunchFixture(t)
	bare := engine.NewGroupLauncher(f.groups, f.starter, nil, f.ws, f.resultsWS, fixedIdentityResolver{},
		&recordingCanceller{instances: f.instances}, newMockEventBus())

	_, err := bare.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, ""), f.vars(t, true, nil)))
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "file-gathering working area")
	assert.Empty(t, f.resultsWS.snapshotHandles())
	f.assertNothingStarted(t)
}

func TestGroupLauncher_FilesMode_WithoutAParentWorkspaceRefuses(t *testing.T) {
	f := newFileLaunchFixture(t)
	_, err := f.launcher.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, ""), f.vars(t, false, nil)))
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "{{workspace}}")
	assert.Empty(t, f.area.preflights())
}

func TestGroupLauncher_FilesMode_ForkFailureReleasesTheFileSnapshot(t *testing.T) {
	f := newFileLaunchFixture(t)
	f.area.forkErr = errors.New("disk full")

	_, err := f.launcher.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, ""), f.vars(t, true, nil)))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	require.Len(t, f.area.snapshotHandles(), 1)
	assert.Equal(t, f.area.snapshotHandles(), f.area.releasedHandles(), "the unused baseline is released by the area that took it")
	assert.Empty(t, f.resultsWS.releasedHandles())
}

func TestGroupLauncher_FilesMode_AreaWithoutPreflightStillLaunches(t *testing.T) {
	f := newFileLaunchFixture(t)
	plain := launchFileArea{newCopyGroupWorkspace(t)}
	f.launcher.SetFileWorkingArea(plain)

	run, err := f.launcher.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, ""), f.vars(t, true, nil)))
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Len(t, plain.snapshotHandles(), 1)
	_, present := run.Config[engine.GroupConfigKeyResolvedRepoPath]
	assert.False(t, present, "no repoPath declared, nothing recorded")
}
