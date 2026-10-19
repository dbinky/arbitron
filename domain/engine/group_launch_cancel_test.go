package engine_test

import (
	"context"
	"fmt"
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

// The hook schedules cancellation after StartGroupTask has read its parent,
// immediately before/after the real child INSERT. No cancellation or receipt
// writes are faked. A committed insert may win a race with context cancellation.
type launchCancelRepo struct {
	instance.InstanceRepository
	create func(context.Context, *instance.Instance) error
}

func (r *launchCancelRepo) Create(ctx context.Context, inst *instance.Instance) error {
	return r.create(ctx, inst)
}

// Removing the post-create parent check strands task two after Cancel already
// listed the children. Removing detached failLaunch cleanup loses the durable
// failure decision when the parent run's context has also been cancelled.
func TestGroupLauncher_CancelBetweenStartsClosesLateCreationWindow(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		for _, callbacks := range []bool{false, true} {
			for _, timing := range []string{"before_insert", "after_insert"} {
				for _, cancelContext := range []bool{false, true} {
					name := string(form) + "/" + timing + "/live_context"
					if cancelContext {
						name = string(form) + "/" + timing + "/cancelled_context"
					}
					name = fmt.Sprintf("%s/callbacks_%t", name, callbacks)
					t.Run(name, func(t *testing.T) {
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
						launchCtx, cancel := context.WithCancel(ownerCtx)
						defer cancel()
						wf := gtParentWorkflow()
						cfg := *wf.Nodes[0].Config.Group
						cfg.UnfinishedBehavior = workflow.GroupUnfinishedCancel
						if form == workflow.GroupFormWorkflows {
							cfg.Form = form
							cfg.Tasks = []workflow.GroupTask{{Key: "a", Workflow: "child-wf"}, {Key: "b", Workflow: "child-wf"}, {Key: "never", Workflow: "child-wf"}}
							wf.Edges = wf.Edges[2:]
						} else {
							cfg.Tasks = append(cfg.Tasks, workflow.GroupTask{Key: "never", Entry: "stray"})
							wf.Edges = append(wf.Edges, workflow.Edge{Source: "g", Target: "stray", Output: workflow.GroupPortTask, Label: "never"})
						}
						// Also cancel during the LAST start: there is no later start
						// whose pre-check could accidentally compensate the leak.
						if callbacks {
							cfg.Tasks = cfg.Tasks[:2]
							if form == workflow.GroupFormBranches {
								wf.Edges = wf.Edges[:len(wf.Edges)-1]
							}
						}
						wf.Nodes[0].Config.Group = &cfg
						graph, findings := workflow.AnalyzeGroups(&wf)
						require.Empty(t, findings)
						node := "g"
						parent := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: wf.Name,
							WorkflowSnapshot: gtSnapshot(t, wf), State: instance.StateRunning, Priority: instance.PriorityNormal, CurrentNodeID: &node}
						require.NoError(t, instances.Create(ctx, parent))
						workflows := newScopeAwareRepo()
						workflows.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
						repo := &launchCancelRepo{InstanceRepository: instances}
						bus, qm := newMockEventBus(), newMockQueueManager()
						eng := engine.NewEngine(workflows, repo, adaptsqlite.NewNodeExecutionRepo(db), nil, users, nil, bus, qm, nil, "")
						eng.SetSpawnLinks(instances)
						eng.SetGroupSupport(engine.GroupSupportResults)
						identities := &fakeIdentityResolver{byUser: map[uuid.UUID]identity.Identity{
							owner: {UserID: owner, Role: identity.RoleUser},
						}}
						coord := engine.NewGroupCoordinator(groups, instances, adaptsqlite.NewQueueRepo(db), eng, identities, bus, qm)
						if callbacks {
							eng.SetGroupTerminalHandler(coord)
						}
						qm.enqueueFn = func(queueCtx context.Context, id, queuedOwner uuid.UUID) (bool, error) {
							if err := queueCtx.Err(); err != nil {
								return false, err
							}
							child, err := instances.Get(queueCtx, id)
							if err != nil {
								return false, err
							}
							if child.OwnerID != queuedOwner {
								return false, identity.ErrForbidden
							}
							if child.State != instance.StateQueued {
								return false, nil
							}
							return true, instances.UpdateState(queueCtx, id, instance.StateRunning)
						}
						starts := 0
						var lateID uuid.UUID
						cancelParent := func() {
							require.NoError(t, eng.Cancel(ownerCtx, parent.ID)) // synchronous full walk
							if cancelContext {
								cancel() // the live Engine also cancels its parent's run context
							}
						}
						repo.create = func(createCtx context.Context, child *instance.Instance) error {
							starts++
							if starts != 2 {
								return instances.Create(createCtx, child)
							}
							lateID = child.ID
							if timing == "before_insert" {
								cancelParent()
							}
							// Force the INSERT to commit even when cancellation won just
							// before its return; the real engine must account for that row.
							err := instances.Create(context.WithoutCancel(createCtx), child)
							if timing == "after_insert" {
								cancelParent()
							}
							return err
						}
						launcher := engine.NewGroupLauncher(groups, eng, eng, nil, nil, identities, eng, bus)
						vars := variable.NewStore()
						require.NoError(t, vars.SetInput("repo", "acme/widgets"))
						run, err := launcher.Launch(launchCtx, engine.GroupLaunchRequest{
							Parent: parent, Node: wf.Nodes[0], Config: cfg, Ownership: graph.Groups["g"], Vars: vars,
						})
						require.NoError(t, err)
						require.NotEqual(t, uuid.Nil, lateID)
						late, err := instances.Get(ctx, lateID)
						require.NoError(t, err)
						assert.Equal(t, instance.StateCancelled, late.State, "late task must stop before Launch returns, without a sweep")
						assert.Equal(t, 2, starts, "the remaining roster must never be started")
						require.NotNil(t, run.Decision, "failure must be persisted even on a cancelled launch context")
						assert.Equal(t, "failure", run.Decision.Outcome)
						assert.Equal(t, instance.GroupTaskLaunchFailed, run.Tasks[1].LaunchState)
						assert.NotEmpty(t, run.LaunchError, "even live callbacks must not mask lost launch-failure bookkeeping")
						for _, task := range run.Tasks[:2] {
							child, err := instances.Get(ctx, task.ChildInstanceID)
							require.NoError(t, err)
							assert.Equal(t, instance.StateCancelled, child.State)
							assert.Equal(t, instance.GroupTaskStatusCancelled, task.LiveStatus)
							assert.NotNil(t, task.TerminalSeq, "the terminal receipt comes from the real SQLite trigger")
						}
						if len(run.Tasks) == 3 {
							_, err = instances.Get(ctx, run.Tasks[2].ChildInstanceID)
							assert.ErrorIs(t, err, instance.ErrNotFound)
						}
						// Recovery may settle bookkeeping, but must have no work left to
						// cancel and must never revive the parent through success.
						require.NoError(t, coord.Evaluate(ctx, run.ID))
						settled, err := groups.Get(ctx, run.ID)
						require.NoError(t, err)
						assert.Equal(t, workflow.GroupPortFailure, settled.ContinuationPort)
						assert.True(t, settled.ContinuationDelivered)
						unchanged, err := instances.Get(ctx, parent.ID)
						require.NoError(t, err)
						assert.Equal(t, instance.StateCancelled, unchanged.State)
					})
				}
			}
		}
	}
}

func TestStartGroupTask_TerminalParentRefusesNewTasks(t *testing.T) {
	for _, state := range []instance.State{instance.StateCancelled, instance.StateFailed, instance.StateCompleted} {
		for _, branch := range []bool{false, true} {
			t.Run(string(state)+"/"+map[bool]string{true: "branch", false: "workflow"}[branch], func(t *testing.T) {
				h := newGroupTaskHarness(t)
				owner := uuid.New()
				h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
				parent := h.seedParent(t, owner, 0)
				require.NoError(t, h.instances.UpdateState(context.Background(), parent.ID, state))
				req := gtWorkflowReq(parent)
				if branch {
					req = gtBranchReq(parent)
				}
				err := h.engine.StartGroupTask(ctxAs(owner), req)
				assert.Error(t, err)
				_, err = h.lookups.Get(context.Background(), req.TaskInstanceID)
				assert.ErrorIs(t, err, instance.ErrNotFound)
				assert.Empty(t, h.enqueuedIDs())
			})
		}
	}
}
