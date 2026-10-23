package engine_test

import (
	"context"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// paired:2 — the launch barrier. A Pause then Resume while a launch is still
// starting tasks gives the parent a second walker, which re-enters the group
// node and parks the parent waiting at it. The coordinator then sees a
// launching run whose parent is no longer running at the node and decides an
// interrupted launch, cancelling what the roster shows as started. The ORIGINAL
// launcher goroutine is still running: every task it starts after that
// decision must be stopped by the launcher itself, or it runs uncancelled under
// a group that has already routed failure.
func TestGroupLauncher_ForeignDecisionDuringLaunchStopsLateTasks(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		for _, timing := range []string{"before_insert", "after_insert"} {
			t.Run(form+"/"+timing, func(t *testing.T) {
				ctx := context.Background()
				f := newBarrierFixture(t, form)
				instances, groups, coord, repo, parent := f.instances, f.groups, f.coord, f.repo, f.parent

				// The second walker parks the parent and a terminal/sweep evaluates
				// the still-launching run.
				var runID uuid.UUID
				interrupt := func() {
					require.NoError(t, instances.UpdateState(ctx, parent.ID, instance.StateWaiting))
					require.NoError(t, coord.Evaluate(ctx, runID))
					decided, err := groups.Get(ctx, runID)
					require.NoError(t, err)
					require.NotNil(t, decided.Decision, "fixture: the coordinator must decide the interrupted launch")
					require.True(t, decided.ActionsApplied, "fixture: the coordinator applied its cancellations")
				}
				starts := 0
				repo.create = func(createCtx context.Context, child *instance.Instance) error {
					starts++
					if starts == 2 {
						run, err := groups.GetByChild(ctx, child.ID)
						require.NoError(t, err)
						require.NotNil(t, run)
						runID = run.ID
						if timing == "before_insert" {
							interrupt()
						}
					}
					err := instances.Create(createCtx, child)
					if starts == 2 && timing == "after_insert" {
						interrupt()
					}
					return err
				}

				run, err := f.launch()
				require.NoError(t, err)
				require.NotNil(t, run.Decision)
				assert.Equal(t, "failure", run.Decision.Outcome)
				assert.Equal(t, 2, starts, "no task may be started after a decision was applied")
				for _, task := range run.Tasks {
					child, err := instances.Get(ctx, task.ChildInstanceID)
					if task.Key == "c" {
						assert.ErrorIs(t, err, instance.ErrNotFound, "the task after the decision never starts")
						continue
					}
					require.NoError(t, err)
					assert.Equal(t, instance.StateCancelled, child.State, "task %q runs uncancelled under a decided group", task.Key)
				}
				// The coordinator has nothing left to stop and still routes failure.
				require.NoError(t, coord.Evaluate(ctx, run.ID))
				settled, err := groups.Get(ctx, run.ID)
				require.NoError(t, err)
				assert.Equal(t, instance.GroupRunDone, settled.State)
				assert.Equal(t, workflow.GroupPortFailure, settled.ContinuationPort)
			})
		}
	}
}

type barrierFixture struct {
	instances *adaptsqlite.InstanceRepo
	groups    *adaptsqlite.GroupRunRepo
	cfg       *workflow.GroupConfig
	// launched, when set, runs after the launcher's MarkTaskLaunched commits
	// and before the launcher sees the write return.
	launched func(runID uuid.UUID, key string)
	// decided, when set, runs after the coordinator's Decide commits.
	decided func(runID uuid.UUID)
	coord   *engine.GroupCoordinator
	repo    *launchCancelRepo
	parent  *instance.Instance
	launch  func() (*instance.GroupRun, error)
}

// newBarrierFixture builds a three-task first_success/cancel_unfinished group
// (a, b, c) over real SQLite, the real Engine as starter and canceller, and the
// real coordinator wired as the terminal handler.
func newBarrierFixture(t *testing.T, form string) *barrierFixture {
	t.Helper()
	ctx := context.Background()
	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	instances := adaptsqlite.NewInstanceRepo(db)
	groups := adaptsqlite.NewGroupRunRepo(db)
	users := adaptsqlite.NewUserRepo(db)
	owner, err := users.GetDefaultUser(ctx)
	require.NoError(t, err)
	ownerCtx := identity.WithIdentity(ctx, identity.Identity{UserID: owner, Role: identity.RoleUser})

	wf := gtParentWorkflow()
	cfg := *wf.Nodes[0].Config.Group
	cfg.CompletionRule = workflow.GroupRuleFirstSuccess
	cfg.UnfinishedBehavior = workflow.GroupUnfinishedCancel
	if form == workflow.GroupFormWorkflows {
		cfg.Form = form
		cfg.Tasks = []workflow.GroupTask{{Key: "a", Workflow: "child-wf"}, {Key: "b", Workflow: "child-wf"}, {Key: "c", Workflow: "child-wf"}}
		wf.Edges = wf.Edges[2:]
	} else {
		cfg.Tasks = append(cfg.Tasks, workflow.GroupTask{Key: "c", Entry: "stray"})
		wf.Edges = append(wf.Edges, workflow.Edge{Source: "g", Target: "stray", Output: workflow.GroupPortTask, Label: "c"})
	}
	wf.Nodes[0].Config.Group = &cfg
	graph, findings := workflow.AnalyzeGroups(&wf)
	require.Empty(t, findings)
	node := "g"
	parent := &instance.Instance{ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot, ID: uuid.New(), OwnerID: owner, WorkflowName: wf.Name,
		WorkflowSnapshot: gtSnapshot(t, wf), State: instance.StateRunning, Priority: instance.PriorityNormal, CurrentNodeID: &node}
	require.NoError(t, instances.Create(ctx, parent))

	workflows := newScopeAwareRepo()
	workflows.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
	repo := &launchCancelRepo{InstanceRepository: instances}
	bus, qm := newMockEventBus(), newMockQueueManager()
	eng := wireModelPins(engine.NewEngine(workflows, repo, adaptsqlite.NewNodeExecutionRepo(db), nil, users, nil, bus, qm, nil, ""))
	eng.SetSpawnLinks(instances)
	eng.SetGroupSupport(engine.GroupSupportResults)
	identities := &fakeIdentityResolver{byUser: map[uuid.UUID]identity.Identity{
		owner: {UserID: owner, Role: identity.RoleUser},
	}}
	f := &barrierFixture{instances: instances, groups: groups, cfg: &cfg, repo: repo, parent: parent}
	coord := engine.NewGroupCoordinator(&decideHookRepo{GroupRunRepository: groups, f: f}, instances, adaptsqlite.NewQueueRepo(db), eng, identities, bus, qm)
	eng.SetGroupTerminalHandler(coord)
	qm.enqueueFn = func(queueCtx context.Context, id, _ uuid.UUID) (bool, error) {
		child, err := instances.Get(queueCtx, id)
		if err != nil || child.State != instance.StateQueued {
			return false, err
		}
		return true, instances.UpdateState(queueCtx, id, instance.StateRunning)
	}

	f.coord = coord
	launcher := engine.NewGroupLauncher(&launchHookRepo{GroupRunRepository: groups, f: f}, eng, eng, nil, nil, identities, eng, bus)
	vars := variable.NewStore()
	require.NoError(t, vars.SetInput("repo", "acme/widgets"))
	f.launch = func() (*instance.GroupRun, error) {
		return launcher.Launch(ownerCtx, engine.GroupLaunchRequest{
			Parent: parent, Node: wf.Nodes[0], Config: cfg, Ownership: graph.Groups["g"], Vars: vars,
		})
	}
	return f
}

type launchHookRepo struct {
	instance.GroupRunRepository
	f *barrierFixture
}

func (r *launchHookRepo) MarkTaskLaunched(ctx context.Context, runID uuid.UUID, key string) error {
	err := r.GroupRunRepository.MarkTaskLaunched(ctx, runID, key)
	if err == nil && r.f.launched != nil {
		r.f.launched(runID, key)
	}
	return err
}

type decideHookRepo struct {
	instance.GroupRunRepository
	f *barrierFixture
}

func (r *decideHookRepo) Decide(ctx context.Context, runID uuid.UUID, d instance.GroupDecision) (bool, error) {
	won, err := r.GroupRunRepository.Decide(ctx, runID, d)
	if err == nil && won && r.f.decided != nil {
		r.f.decided(runID)
	}
	return won, err
}

// paired:2 — the coordinator side of the same handoff: an interrupted-launch
// decision frozen while c was pending, with c's launch record landing between
// the coordinator's Decide and its reload. The live roster is then complete,
// but the decision is still a launch failure: no task may be marked as
// continuing independently while the launcher cancels it.
func TestGroupCoordinator_InterruptedLaunchDecisionIgnoresFinishIndependently(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		t.Run(form, func(t *testing.T) {
			ctx := context.Background()
			f := newBarrierFixture(t, form)
			f.cfg.UnfinishedBehavior = workflow.GroupUnfinishedFinishIndependently
			starts := 0
			f.repo.create = func(createCtx context.Context, child *instance.Instance) error {
				starts++
				err := f.instances.Create(createCtx, child)
				if starts == 3 {
					run, gerr := f.groups.GetByChild(ctx, child.ID)
					require.NoError(t, gerr)
					f.decided = func(runID uuid.UUID) {
						f.decided = nil
						require.NoError(t, f.groups.MarkTaskLaunched(ctx, runID, "c"))
					}
					require.NoError(t, f.instances.UpdateState(ctx, f.parent.ID, instance.StateWaiting))
					require.NoError(t, f.coord.Evaluate(ctx, run.ID))
				}
				return err
			}

			run, err := f.launch()
			require.NoError(t, err)
			require.NotNil(t, run.Decision)
			assert.Equal(t, "failure", run.Decision.Outcome)
			for _, task := range run.Tasks {
				child, err := f.instances.Get(ctx, task.ChildInstanceID)
				require.NoError(t, err)
				assert.Equal(t, instance.StateCancelled, child.State, "task %q", task.Key)
				assert.True(t, task.CancelRequested, "task %q", task.Key)
				assert.False(t, task.ContinuingIndependently, "task %q is not independent work after a launch failure", task.Key)
			}
		})
	}
}

// paired:2 — the decision a coordinator legitimately takes after the LAST
// launch record (barrier opened by completeLaunch, roster complete) is the
// group's own decision, not an interrupted launch: the launcher's re-read must
// leave its unfinished-task policy alone. With finish_independently, b and c
// keep running independently and are never cancel-requested.
func TestGroupLauncher_LastTaskDecisionPreservesFinishIndependently(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		t.Run(form, func(t *testing.T) {
			ctx := context.Background()
			f := newBarrierFixture(t, form)
			f.cfg.UnfinishedBehavior = workflow.GroupUnfinishedFinishIndependently
			f.launched = func(runID uuid.UUID, key string) {
				if key != "c" {
					return
				}
				run, err := f.groups.Get(ctx, runID)
				require.NoError(t, err)
				require.NoError(t, f.instances.SetCompleted(ctx, run.Tasks[0].ChildInstanceID))
				require.NoError(t, f.coord.Evaluate(ctx, runID))
				decided, err := f.groups.Get(ctx, runID)
				require.NoError(t, err)
				require.NotNil(t, decided.Decision, "fixture: the coordinator decides once every task is launched")
				require.True(t, decided.ActionsApplied)
			}
			f.repo.create = f.instances.Create

			run, err := f.launch()
			require.NoError(t, err)
			require.NotNil(t, run.Decision)
			assert.Equal(t, "success", run.Decision.Outcome)
			for _, task := range run.Tasks[1:] {
				child, err := f.instances.Get(ctx, task.ChildInstanceID)
				require.NoError(t, err)
				assert.Equal(t, instance.StateRunning, child.State, "task %q must keep running independently", task.Key)
				assert.True(t, task.ContinuingIndependently, "task %q", task.Key)
				assert.False(t, task.CancelRequested, "task %q must not be cancel-requested", task.Key)
			}
		})
	}
}

// paired:2 — TRACE of the row: task a succeeds while task c is still being
// launched. The coordinator must not decide a launching run whose launcher is
// alive (completeLaunch keeps the barrier closed), so no cancellation is
// applied before c exists and the roster is complete when the decision is
// frozen. The executor's post-Launch Evaluate then decides a as the winner and
// cancels both b and c.
func TestGroupCoordinator_EarlyWinnerIsDecidedOnlyAfterTheBarrierOpens(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		t.Run(form, func(t *testing.T) {
			ctx := context.Background()
			f := newBarrierFixture(t, form)
			starts := 0
			f.repo.create = func(createCtx context.Context, child *instance.Instance) error {
				starts++
				if starts == 3 {
					run, err := f.groups.GetByChild(ctx, child.ID)
					require.NoError(t, err)
					winner := run.Tasks[0].ChildInstanceID
					require.NoError(t, f.instances.SetCompleted(ctx, winner))
					require.NoError(t, f.coord.Evaluate(ctx, run.ID))
					held, err := f.groups.Get(ctx, run.ID)
					require.NoError(t, err)
					assert.Equal(t, instance.GroupRunLaunching, held.State, "the barrier must stay closed while c starts")
					assert.Nil(t, held.Decision, "no decision before every task is registered")
					for _, task := range held.Tasks[1:2] {
						sibling, err := f.instances.Get(ctx, task.ChildInstanceID)
						require.NoError(t, err)
						assert.NotEqual(t, instance.StateCancelled, sibling.State, "no cancellation before the barrier opens")
					}
				}
				return f.instances.Create(createCtx, child)
			}
			run, err := f.launch()
			require.NoError(t, err)
			require.Equal(t, 3, starts)
			assert.Equal(t, instance.GroupRunRunning, run.State)
			assert.Nil(t, run.Decision)

			// firstEntry's Evaluate after Launch: the early success is not lost.
			require.NoError(t, f.coord.Evaluate(ctx, run.ID))
			decided, err := f.groups.Get(ctx, run.ID)
			require.NoError(t, err)
			require.NotNil(t, decided.Decision)
			assert.Equal(t, "success", decided.Decision.Outcome)
			assert.Equal(t, decided.Tasks[0].Key, decided.Decision.WinnerKey)
			assert.Len(t, decided.Tasks, 3)
			for _, task := range decided.Tasks[1:] {
				child, err := f.instances.Get(ctx, task.ChildInstanceID)
				require.NoError(t, err)
				assert.Equal(t, instance.StateCancelled, child.State, "task %q launched before the decision is cancelled", task.Key)
				assert.True(t, task.CancelRequested)
			}
			assert.Equal(t, instance.GroupRunDone, decided.State)
			assert.Equal(t, workflow.GroupPortSuccess, decided.ContinuationPort)
		})
	}
}
