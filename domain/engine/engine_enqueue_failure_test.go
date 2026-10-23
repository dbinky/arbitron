package engine_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fail only the queue's reload. Creation, parent lookups, graph execution and
// queue eligibility retain their real SQLite side effects.
type enqueueReloadFailure struct {
	instance.InstanceRepository
	fail atomic.Bool
}

func (r *enqueueReloadFailure) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	if r.fail.Swap(false) {
		return nil, errors.New("injected post-Create reload failure")
	}
	return r.InstanceRepository.Get(ctx, id)
}

type enqueueEvaluationBus struct {
	*mockEventBus
	beforeEnqueue func()
}

func (b *enqueueEvaluationBus) Publish(id uuid.UUID, event engine.Event) {
	b.mockEventBus.Publish(id, event)
	if event.Type == "instance.created" && b.beforeEnqueue != nil {
		b.beforeEnqueue()
	}
}

func TestEngine_ChildEnqueueReloadFailureKeepsCommittedAdmission(t *testing.T) {
	for _, kind := range []string{"spawn", "workflow", "branch"} {
		for _, evaluatorFirst := range []bool{false, true} {
			state := "queued"
			if evaluatorFirst {
				state = "evaluator-started"
			}
			t.Run(kind+"/"+state, func(t *testing.T) {
				ctx := context.Background()
				db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "enqueue.db"))
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close()) })
				require.NoError(t, adaptsqlite.RunMigrations(db))
				instances := adaptsqlite.NewInstanceRepo(db)
				queueRepo := adaptsqlite.NewQueueRepo(db)
				nodes := adaptsqlite.NewNodeExecutionRepo(db)
				bus := &enqueueEvaluationBus{mockEventBus: newMockEventBus()}
				repo := newScopeAwareRepo()
				eng := wireModelPins(engine.NewEngine(repo, instances, nodes, adaptsqlite.NewVariableSnapshotRepo(db),
					adaptsqlite.NewUserRepo(db), map[workflow.NodeType]engine.NodeExecutor{
						workflow.NodeTypeTransform: engine.NewTransformExecutor(),
					}, bus, nil, queueRepo, ""))
				eng.SetGroupSupport(engine.GroupSupportResults)
				reloads := &enqueueReloadFailure{InstanceRepository: instances}
				qm := queue.NewManager(queueRepo, reloads,
					&config.Config{Queue: config.QueueConfig{GlobalCap: 1, PerUserCap: 1}}, eng)
				eng.SetQueueManager(qm)
				// Stop the queue (joining its async evaluations) before draining the
				// engine, as production Close does: an evaluation that launches a
				// walker during Shutdown's Wait would reuse its WaitGroup.
				t.Cleanup(func() { qm.Stop(); eng.Shutdown() })
				owner, err := adaptsqlite.NewUserRepo(db).GetDefaultUser(ctx)
				require.NoError(t, err)
				ctx = ctxAs(owner)
				if evaluatorFirst {
					bus.beforeEnqueue = func() { require.NoError(t, qm.Evaluate(ctx)) }
				}

				// The real transform consumes the persisted payload and counts walks.
				ops := []workflow.Operation{
					{Variable: "walks", Operation: "increment", Value: 1},
					{Variable: "consumed", Operation: "set", Value: "{{payload.nested.0}}"},
				}
				childWF := &workflow.Workflow{Name: "child-wf", Nodes: []workflow.Node{
					{ID: "c1", Type: workflow.NodeTypeTransform, Entrypoint: true, Config: workflow.NodeConfig{Operations: ops}},
				}}
				repo.put(workflow.Private(owner), childWF)
				parentWF := gtParentWorkflow()
				parentWF.Nodes[1].Type = workflow.NodeTypeTransform
				parentWF.Nodes[1].Config = workflow.NodeConfig{Operations: ops}
				if kind == "workflow" {
					parentWF.Nodes[0].Config.Group.Form = workflow.GroupFormWorkflows
					parentWF.Nodes[0].Config.Group.Tasks = []workflow.GroupTask{{Key: "review", Workflow: "child-wf"}}
					parentWF.Edges = parentWF.Edges[2:]
				}
				nodeID := "g"
				parent := &instance.Instance{ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot, ID: uuid.New(), OwnerID: owner, WorkflowName: parentWF.Name,
					WorkflowSnapshot: gtSnapshot(t, parentWF), State: instance.StateRunning,
					Priority: instance.PriorityNormal, CurrentNodeID: &nodeID}
				if evaluatorFirst {
					parent.State = instance.StateWaiting
				}
				require.NoError(t, instances.Create(ctx, parent))
				vars := map[string]any{"walks": 0, "payload": map[string]any{"nested": []any{"real-input"}}}
				childID := uuid.New()
				req := gtWorkflowReq(parent)
				req.TaskInstanceID = childID
				req.Variables = vars
				if kind == "branch" {
					req = gtBranchReq(parent)
					req.TaskInstanceID = childID
					req.Variables = vars
				}
				if kind != "spawn" {
					form, origin := workflow.GroupFormWorkflows, req.ChildWorkflow
					if kind == "branch" {
						form, origin = workflow.GroupFormBranches, req.BranchEntry
					}
					require.NoError(t, adaptsqlite.NewGroupRunRepo(db).CreateWithTasks(ctx, &instance.GroupRun{
						ID: req.GroupRunID, ParentInstanceID: parent.ID, NodeID: "g", Form: form,
						CompletionRule: workflow.GroupRuleAllSucceed, UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
						GatherMode: workflow.GroupGatherResults,
						Tasks:      []instance.GroupTask{{Key: req.TaskKey, Kind: req.Kind, Origin: origin, ChildInstanceID: childID}},
					}))
				}

				reloads.fail.Store(true)
				if kind == "spawn" {
					childID, err = eng.Spawn(ctx, engine.SpawnRequest{
						ChildWorkflow: "child-wf", Variables: vars, Priority: "normal",
						Link: instance.SpawnLink{ParentInstanceID: parent.ID, ParentNodeID: "g", Mode: instance.SpawnModeWaitForChild},
					})
				} else {
					err = eng.StartGroupTask(ctx, req)
				}
				require.NoError(t, err, "a committed child must not be reported as a failed creation")
				require.NotEqual(t, uuid.Nil, childID)
				stored, err := instances.Get(ctx, childID)
				require.NoError(t, err)
				if evaluatorFirst {
					assert.Contains(t, []instance.State{instance.StateRunning, instance.StateCompleted}, stored.State)
					assert.Len(t, bus.eventsOfType(childID, "instance.started"), 1)
				} else {
					assert.Equal(t, instance.StateQueued, stored.State)
					assert.Empty(t, bus.eventsOfType(childID, "instance.started"), "parent still holds the only slot")
				}
				assert.Equal(t, parent.ID, *stored.ParentInstanceID)
				assert.Equal(t, "real-input", stored.Variables["payload"].(map[string]any)["nested"].([]any)[0])
				if kind != "spawn" {
					require.NoError(t, eng.StartGroupTask(ctx, req), "reserved-id replay must keep the same child")
				}

				// Capacity recovery and repeated queue offers must produce one walker.
				require.NoError(t, instances.UpdateState(ctx, parent.ID, instance.StateWaiting))
				require.NoError(t, qm.Evaluate(ctx))
				_, err = qm.Enqueue(ctx, childID, owner)
				require.NoError(t, err)
				qm.Stop()
				eng.Shutdown()
				stored, err = instances.Get(ctx, childID)
				require.NoError(t, err)
				assert.Equal(t, instance.StateCompleted, stored.State)
				assert.Equal(t, float64(1), stored.Variables["walks"])
				assert.Equal(t, "real-input", stored.Variables["consumed"], "nil/empty production variables cannot satisfy the sink")
				assert.Len(t, bus.eventsOfType(childID, "instance.created"), 1)
				assert.Len(t, bus.eventsOfType(childID, "instance.started"), 1)
				var traces int
				require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM node_executions WHERE instance_id = ?", childID.String()).Scan(&traces))
				assert.Equal(t, 1, traces)
			})
		}
	}
}
