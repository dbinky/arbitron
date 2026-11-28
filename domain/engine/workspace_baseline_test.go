package engine_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realBaseline is the production initializer over the real store and catalog.
func realBaseline(t *testing.T, opts ...checkpoint.Option) (*runner.BaselineInitializer, *checkpoint.Store) {
	t.Helper()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "baseline.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	store := checkpoint.NewStore(filepath.Join(t.TempDir(), "checkpoints"),
		append([]checkpoint.Option{checkpoint.WithCatalog(adaptsqlite.NewCheckpointCatalogRepo(db))}, opts...)...)
	init := runner.NewWorkspaceBaselineInitializer(runner.BaselineDeps{Checkpoints: store, Ledger: adaptsqlite.NewRunnerLedgerRepo(db),
		Leases: adaptsqlite.NewResourceLeaseRepo(db, "boot-"+uuid.NewString()), Homes: adaptsqlite.NewWorkspaceHomeRepo(db),
		Local: checkpoint.NewLocalStore(filepath.Join(t.TempDir(), "local"))})
	return init, store
}

// seededWorkspaces creates each run directory with an initial file (what a
// clone step or a group fork leaves before the run loop starts).
type seededWorkspaces struct {
	*dirWorkspaces
	seed string
}

func (w seededWorkspaces) EnsureInstanceDir(id uuid.UUID) (string, error) {
	p, err := w.dirWorkspaces.EnsureInstanceDir(id)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(p, "seed.txt")); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(filepath.Join(p, "seed.txt"), []byte(w.seed), 0o644); err != nil {
			return "", err
		}
	}
	return p, nil
}

// Happy path: a real baseline row and the exact initial bytes exist BEFORE the
// first node can dispatch anything.
func TestPrepareWorkspace_InitializesBaselineBeforeAnyDispatch(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	f.engine.SetWorkspaceManager(seededWorkspaces{dirWorkspaces: newDirWorkspaces(t), seed: "initial bytes"})
	init, store := realBaseline(t)
	f.engine.SetWorkspaceBaselineInitializer(init)

	var sawBaseline bool
	var sawBytes string
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		id, _ := engine.InstanceIDFromContext(ctx)
		ref, ok, err := store.Latest(ctx, id)
		if err != nil || !ok {
			return &engine.NodeResult{}, nil
		}
		sawBaseline = true
		dst := t.TempDir()
		if err := store.Restore(ctx, ref, dst); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(filepath.Join(dst, "seed.txt"))
		if err != nil {
			return nil, err
		}
		sawBytes = string(b)
		return &engine.NodeResult{}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.True(t, sawBaseline, "the first node saw a committed baseline")
	assert.Equal(t, "initial bytes", sawBytes)
}

// Failure: a disk failure while creating the baseline fails the run visibly
// and nothing is dispatched.
func TestWorkspaceBaseline_DiskFailurePreventsFirstDispatch(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	f.engine.SetWorkspaceManager(seededWorkspaces{dirWorkspaces: newDirWorkspaces(t), seed: "x"})
	init, store := realBaseline(t, checkpoint.WithFileCreator(func(string, os.FileMode) (io.WriteCloser, error) {
		return nil, errors.New("no space left on device")
	}))
	f.engine.SetWorkspaceBaselineInitializer(init)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Empty(t, f.executor.getCalls(), "no node runs, so no operation is dispatched")
	_, ok, err := store.Latest(context.Background(), id)
	require.NoError(t, err)
	assert.False(t, ok)
}

// Edge: a branch task's baseline is taken AFTER ForkTask and contains the
// forked files; it restores elsewhere with them present.
func TestWorkspaceBaseline_ContainsForkedBranchFiles(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	cfg := glBranchConfig()
	wf := gtParentWorkflow()
	wf.Nodes[0].Config.Group = &cfg
	parent.WorkflowSnapshot = gtSnapshot(t, wf)
	graph, findings := workflow.AnalyzeGroups(&wf)
	require.Empty(t, findings)

	root := t.TempDir()
	ws, err := filesystem.NewWorkspaceManager(root)
	require.NoError(t, err)
	groupWS, err := filesystem.NewGroupWorkspace(root)
	require.NoError(t, err)
	h.engine.SetWorkspaceManager(ws)
	init, store := realBaseline(t)
	h.engine.SetWorkspaceBaselineInitializer(init)
	parentDir, err := ws.EnsureInstanceDir(parent.ID)
	require.NoError(t, err)
	content := "branch input for " + parent.ID.String()
	require.NoError(t, os.WriteFile(filepath.Join(parentDir, "source.txt"), []byte(content), 0o600))

	identities := &fakeIdentityResolver{byUser: map[uuid.UUID]identity.Identity{
		owner: {UserID: owner, Role: identity.RoleUser, Departments: []string{"engineering"}},
	}}
	l := engine.NewGroupLauncher(newMemGroupRuns(), h.engine, h.engine, ws, groupWS, identities, h.engine, h.bus)
	run, err := l.Launch(ctxAs(uuid.New()), engine.GroupLaunchRequest{
		Parent: parent, Node: wf.Nodes[0], Config: cfg, Ownership: graph.Groups["g"], Vars: glStore(t, parentDir),
	})
	require.NoError(t, err)
	child := run.Tasks[0].ChildInstanceID

	// ForkTask finished inside Launch, before StartGroupTask; the child run now prepares its workspace.
	require.NoError(t, h.engine.StartInstance(context.Background(), child))
	var ref runner.CheckpointRef
	require.Eventually(t, func() bool {
		r, ok, err := store.Latest(context.Background(), child)
		ref = r
		return err == nil && ok
	}, 5*time.Second, 10*time.Millisecond)

	elsewhere := t.TempDir()
	require.NoError(t, store.Restore(context.Background(), ref, elsewhere))
	got, err := os.ReadFile(filepath.Join(elsewhere, "source.txt"))
	require.NoError(t, err)
	assert.Equal(t, content, string(got), "the child's baseline carries the forked branch files")
}
