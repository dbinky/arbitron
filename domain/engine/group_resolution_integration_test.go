package engine_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	gitarea "github.com/arbitron/arbitron/adapter/git"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This exercises the gatherer's actual conflict producer and the bridge's real
// session/notification/wake sinks. The P3-T11 gate additionally boots initDomain;
// this test does not stand in for composition-root coverage.
func TestGroupResolutionBridge_GatherAndConversationReachFileAndPersistenceSinks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for _, mode := range []string{"automatic", "human", "abandon", "automatic-restart", "human-restart", "notification-create-retry", "notification-marker-retry"} {
		t.Run(mode, func(t *testing.T) {
			automatic := strings.HasPrefix(mode, "automatic")
			restart := strings.HasSuffix(mode, "-restart")
			abandon := mode == "abandon"
			ctx := context.Background()
			f := newGatherFixture(t, workflow.GroupRuleAllSucceed, "success", "", "a", "b")
			root := t.TempDir()
			parentDir := filepath.Join(root, "parent")
			require.NoError(t, os.MkdirAll(parentDir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(parentDir, "shared.txt"), []byte("baseline\n"), 0600))
			area, err := gitarea.NewWorkingArea(root)
			require.NoError(t, err)
			snapshot, err := area.SnapshotParent(ctx, f.runID, parentDir)
			require.NoError(t, err)
			refs := map[string]string{}
			for _, key := range []string{"a", "b"} {
				dir := filepath.Join(root, "task-"+key)
				require.NoError(t, area.ForkTask(ctx, snapshot, dir))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("from-"+key+"\n"), 0600))
				refs[key], err = area.FinalizeTask(ctx, snapshot, uuid.New(), dir)
				require.NoError(t, err)
			}
			f.groups.update(f.runID, func(r *instance.GroupRun) {
				r.WorkspaceSnapshot = snapshot
				r.Config["resolverProfile"] = groupResolverProfile
				for i := range r.Tasks {
					r.Tasks[i].FinalizedRef = refs[r.Tasks[i].Key]
				}
			})

			db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "resolution.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			require.NoError(t, adaptsqlite.RunMigrations(db))
			instances := adaptsqlite.NewInstanceRepo(db)
			nodes := adaptsqlite.NewNodeExecutionRepo(db)
			users := adaptsqlite.NewUserRepo(db)
			owner, err := users.GetDefaultUser(ctx)
			require.NoError(t, err)
			node := "g1"
			parent := f.parent
			parent.OwnerID = owner
			parent.Priority = instance.PriorityNormal
			parent.CurrentNodeID = &node
			parent.WorkflowSnapshot = groupWorkflowSnapshot(t)
			parent.WorkflowSnapshot["nodes"] = append(parent.WorkflowSnapshot["nodes"].([]any),
				map[string]any{"id": "after", "type": "script"}, map[string]any{"id": "fail", "type": "script"})
			parent.WorkflowSnapshot["edges"] = []any{
				map[string]any{"source": "g1", "target": "after", "output": "success"},
				map[string]any{"source": "g1", "target": "fail", "output": "failure"},
			}
			parent.Variables = map[string]any{"workspace": parentDir, "before": "kept"}
			require.NoError(t, instances.Create(ctx, parent))
			require.NoError(t, nodes.Create(ctx, &instance.NodeExecution{
				ID: uuid.New(), InstanceID: parent.ID, NodeID: node, Attempt: 1,
				Status: instance.NodeStatusSucceeded, Output: map[string]any{"groupRunId": f.runID.String()}, StartedAt: time.Now(),
			}))
			notifications := adaptsqlite.NewNotificationRepo(db)
			notifier := engine.NewEngine(nil, instances, nodes, adaptsqlite.NewVariableSnapshotRepo(db), users,
				map[workflow.NodeType]engine.NodeExecutor{}, f.bus, newMockQueueManager(), adaptsqlite.NewQueueRepo(db), "")
			faults := &resolutionNotificationRepo{Repository: notifications, failCreate: mode == "notification-create-retry"}
			notifier.SetNotifications(faults)
			t.Cleanup(notifier.Shutdown)
			agent := newMockStreamingChatProvider()
			turns := 0
			agent.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
				turns++
				// The load-bearing WorkDir is used for every read, edit and git command.
				// Empty, parent, or hardcoded directories cannot satisfy this producer.
				before, err := os.ReadFile(filepath.Join(req.WorkDir, "shared.txt"))
				if err != nil {
					return nil, err
				}
				if !strings.Contains(string(before), "<<<<<<<") {
					return nil, fmt.Errorf("resolver did not receive the staged conflict: %s", before)
				}
				if req.Model != groupResolverModel || strings.Join(req.ToolScope, ",") != "Read,Edit" {
					return nil, fmt.Errorf("wrong resolver binding: %+v", req)
				}
				if automatic || turns > 1 {
					if err := os.WriteFile(filepath.Join(req.WorkDir, "shared.txt"), []byte("from-a\nfrom-b\n"), 0600); err != nil {
						return nil, err
					}
					cmd := exec.CommandContext(ctx, "git", "-C", req.WorkDir, "add", "--", "shared.txt")
					if out, err := cmd.CombinedOutput(); err != nil {
						return nil, fmt.Errorf("stage resolution: %s: %w", out, err)
					}
				}
				response := "Please review the conflict."
				if turns > 1 {
					latest, err := nodes.GetByInstanceAndNode(ctx, parent.ID, node)
					if err != nil {
						return nil, err
					}
					nonce, _ := latest.LLMConversation[engine.ConversationKeyNonce].(string)
					response = engine.SentinelOpenTag(nonce) + `{"summary":"combined both sides","resolvedPaths":["shared.txt"]}` + engine.SentinelCloseTag(nonce)
				}
				return cannedStreamWithSession([]string{response}, "real-resolution-session")(ctx, req)
			}
			selector := &fakeStreamSelector{agentProv: agent, chatProv: newMockStreamingChatProvider()}
			qm := newMockQueueManager()
			bridge := engine.NewGroupResolutionBridge(f.groups, instances, nodes, groupResolverResolver(), selector, newOwnerScopedFileReader(), notifier, f.bus, adaptsqlite.NewQueueRepo(db), qm, groupResolverTimeout)
			if mode == "notification-marker-retry" {
				bridge = engine.NewGroupResolutionBridge(f.groups, instances, resolutionSessionRepo{NodeExecutionRepository: nodes, updateErr: assert.AnError}, groupResolverResolver(), selector, newOwnerScopedFileReader(), notifier, f.bus, adaptsqlite.NewQueueRepo(db), qm, groupResolverTimeout)
			}
			var starter engine.GroupResolutionStarter = bridge
			if mode == "automatic-restart" {
				starter = crashAfterSessionClose{bridge, f.groups}
			} else if mode == "human-restart" {
				starter = crashBeforeNotification{bridge}
			}
			gatherer := engine.NewGroupGatherer(f.groups, area, starter, f.bus)
			step, err := gatherer.Gather(ctx, f.groups.snapshot(f.runID), parent)
			if strings.HasPrefix(mode, "notification-") {
				require.ErrorIs(t, err, assert.AnError)
				require.Nil(t, step, "notification storage failure must not finish the gather")
				run := f.groups.snapshot(f.runID)
				require.Equal(t, instance.GroupRunResolvingConflicts, run.State)
				require.NotNil(t, run.Gather.ActiveSession)
				require.Nil(t, run.Gather.Attempts[0].EndedAt)
				latest, err := nodes.GetByInstanceAndNode(ctx, parent.ID, node)
				require.NoError(t, err)
				require.True(t, engine.IsOpenGroupResolutionSession(latest.LLMConversation))
				require.NotEqual(t, true, latest.LLMConversation["notificationSent"])
				notices, err := notifications.List(ctx, notification.ListFilters{})
				require.NoError(t, err)
				if faults.failCreate {
					require.Empty(t, notices)
					require.Empty(t, f.bus.eventsOfType(parent.ID, "notification.created"))
				} else {
					require.Len(t, notices, 1)
					// Acknowledging or dismissing before replay must be preserved.
					require.NoError(t, notifications.MarkAllRead(ctx, nil))
					require.NoError(t, notifications.Dismiss(ctx, notices[0].ID, nil))
				}
				before, err := os.ReadFile(filepath.Join(parentDir, "shared.txt"))
				require.NoError(t, err)
				require.Equal(t, "baseline\n", string(before))
				faults.failCreate = false
				if mode == "notification-create-retry" {
					// Simultaneous recovery callbacks must arbitrate at the real
					// notification primary key, even when no marker can be saved.
					retrying := engine.NewGroupResolutionBridge(f.groups, instances, resolutionSessionRepo{NodeExecutionRepository: nodes, updateErr: assert.AnError}, groupResolverResolver(), selector, newOwnerScopedFileReader(), notifier, f.bus, adaptsqlite.NewQueueRepo(db), qm, groupResolverTimeout)
					var wg sync.WaitGroup
					results := make(chan error, 8)
					for i := 0; i < cap(results); i++ {
						wg.Add(1)
						go func() {
							defer wg.Done()
							results <- retrying.NotifyResolutionRequired(ctx, *run.Gather.ActiveSession)
						}()
					}
					wg.Wait()
					close(results)
					for result := range results {
						require.ErrorIs(t, result, assert.AnError)
					}
				}
				// Reconstruct the bridge so no in-memory deduplication can help.
				bridge = engine.NewGroupResolutionBridge(f.groups, instances, nodes, groupResolverResolver(), selector, newOwnerScopedFileReader(), notifier, f.bus, adaptsqlite.NewQueueRepo(db), qm, groupResolverTimeout)
				gatherer = engine.NewGroupGatherer(f.groups, area, bridge, f.bus)
				step, err = gatherer.Gather(ctx, f.groups.snapshot(f.runID), parent)
				require.NoError(t, err)
				require.False(t, step.Done)
				require.NoError(t, bridge.NotifyResolutionRequired(ctx, *run.Gather.ActiveSession))
				notices, err = notifications.List(ctx, notification.ListFilters{})
				require.NoError(t, err)
				require.Len(t, notices, 1, "one durable notification across create/marker failures and replay")
				require.Len(t, f.bus.eventsOfType(parent.ID, "notification.created"), 1)
				if mode == "notification-marker-retry" {
					assert.True(t, notices[0].Read)
					assert.True(t, notices[0].Dismissed)
				}
				latest, err = nodes.GetByInstanceAndNode(ctx, parent.ID, node)
				require.NoError(t, err)
				require.Equal(t, true, latest.LLMConversation["notificationSent"])
			} else if restart {
				require.ErrorIs(t, err, errRecoveryBoundary)
				require.NoError(t, instances.UpdateState(ctx, parent.ID, instance.StateWaiting))
				workspaces, err := filesystem.NewWorkspaceManager(root)
				require.NoError(t, err)
				pruner := engine.NewGroupResourcePruner(f.groups, instances, instances, workspaces, area, area)
				rec := engine.NewGroupGatherReconciler(f.groups, instances, nodes, gatherer, bridge, bridge,
					adaptsqlite.NewQueueRepo(db), qm, &fakeGatherLocker{}, f.bus, pruner)
				require.NoError(t, rec.RecoverRun(ctx, f.runID))
				persisted, err := instances.Get(ctx, parent.ID)
				require.NoError(t, err)
				gatherer = engine.NewGroupGatherer(f.groups, area, bridge, f.bus)
				if automatic {
					require.Equal(t, instance.StateQueued, persisted.State)
					resumeRecoveredGroupToSink(t, notifier, f.groups, instances, nodes, adaptsqlite.NewQueueRepo(db), qm, f.bus, gatherer, parent.ID)
					step, err = gatherer.Gather(ctx, f.groups.snapshot(f.runID), persisted)
					require.NoError(t, err)
				} else {
					assert.Equal(t, instance.StateWaiting, persisted.State)
				}
			} else {
				require.NoError(t, err)
			}
			if !automatic {
				if !restart {
					require.False(t, step.Done)
				}
				// No parent bytes have changed before human resolution.
				b, err := os.ReadFile(filepath.Join(parentDir, "shared.txt"))
				require.NoError(t, err)
				assert.Equal(t, "baseline\n", string(b))
				notices, err := notifications.List(ctx, notification.ListFilters{})
				require.NoError(t, err)
				require.Len(t, notices, 1)
				assert.Equal(t, notification.TypeConflictResolution, notices[0].Type)
				assert.Equal(t, parent.ID, notices[0].InstanceID)
				require.NoError(t, instances.UpdateState(ctx, parent.ID, instance.StateWaiting))
				handler := engine.NewConversationHandler(instances, nodes, selector, groupResolverResolver(), f.bus, qm)
				handler.(groupResolutionSetter).SetGroupResolution(f.groups, bridge)
				if abandon {
					require.NoError(t, handler.AbandonSession(ctxAs(owner), parent.ID))
				} else {
					require.NoError(t, handler.HandleMessage(ctxAs(owner), parent.ID, "combine both sides and finish"))
				}
				persisted, err := instances.Get(ctx, parent.ID)
				require.NoError(t, err)
				assert.Equal(t, instance.StateQueued, persisted.State, "real node-addressed wake sink")
				assert.Equal(t, "kept", persisted.Variables["before"])
				assert.NotContains(t, persisted.Variables, node, "conversation output never overwrites the group output")
				latest, err := nodes.GetByInstanceAndNode(ctx, parent.ID, node)
				require.NoError(t, err)
				if abandon {
					assert.Equal(t, "abandoned", latest.LLMConversation[engine.ConversationKeyEndReason])
				} else {
					assert.Equal(t, "llm_completed", latest.LLMConversation[engine.ConversationKeyEndReason])
					assert.Equal(t, "combined both sides", latest.Output["summary"])
				}
				require.NoError(t, instances.UpdateState(ctx, parent.ID, instance.StateRunning))
				step, err = gatherer.Gather(ctx, f.groups.snapshot(f.runID), persisted)
				require.NoError(t, err)
			}
			require.True(t, step.Done)
			wantPort, wantBytes, wantOutcome := workflow.GroupPortSuccess, "from-a\nfrom-b\n", instance.ResolutionOutcomeResolved
			if abandon {
				wantPort, wantBytes, wantOutcome = workflow.GroupPortFailure, "baseline\n", instance.ResolutionOutcomeAbandoned
				assert.Equal(t, "rolled_back", gatherSection(t, step)["state"])
			}
			assert.Equal(t, wantPort, step.Port)
			b, err := os.ReadFile(filepath.Join(parentDir, "shared.txt"))
			require.NoError(t, err)
			assert.Equal(t, wantBytes, string(b), "file sink requires actual resolution or rollback in the staged working area")
			info, err := os.Stat(filepath.Join(parentDir, "shared.txt"))
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
			latest, err := nodes.GetByInstanceAndNode(ctx, parent.ID, node)
			require.NoError(t, err)
			assert.False(t, engine.IsOpenGroupResolutionSession(latest.LLMConversation))
			assert.Equal(t, 2, latest.Attempt)
			assert.Equal(t, wantOutcome, f.groups.snapshot(f.runID).Gather.Attempts[0].Outcome)
			notices, err := notifications.List(ctx, notification.ListFilters{})
			require.NoError(t, err)
			if automatic {
				if restart {
					for _, n := range notices {
						assert.Equal(t, notification.TypeCompleted, n.Type, "no conflict notification for automatic resolution")
					}
				} else {
					assert.Empty(t, notices)
				}
				assert.Equal(t, 1, turns)
			} else {
				assert.Len(t, notices, 1)
				if abandon {
					assert.Equal(t, 1, turns, "abandon must not start another provider turn")
				} else {
					assert.Equal(t, 2, turns)
				}
			}
		})
	}
}

// Inject the failure immediately before the durable notification sink. Successful
// retries use the real SQLite repository, including its unique ID constraint.
type resolutionNotificationRepo struct {
	notification.Repository
	failCreate bool
}

func (r *resolutionNotificationRepo) Create(ctx context.Context, n *notification.Notification) error {
	if r.failCreate {
		return assert.AnError
	}
	return r.Repository.Create(ctx, n)
}

// Model queue intake only after the reconciler's real SQL wake. The real
// engine/executor must then gather, route and persist a downstream file read;
// directly invoking Gather regardless of a lost wake would hide absent recovery.
func resumeRecoveredGroupToSink(t *testing.T, eng *engine.Engine, groups *memGroupRuns, instances *adaptsqlite.InstanceRepo,
	nodes *adaptsqlite.NodeExecutionRepo, waker instance.NodeAddressedWaker, qm engine.QueueManager, bus engine.EventBus,
	gatherer engine.GroupGatherer, parentID uuid.UUID) {
	t.Helper()
	ids := fixedIdentityResolver{}
	launcher := engine.NewGroupLauncher(groups, eng, eng, nil, nil, ids, eng, bus)
	coord := engine.NewGroupCoordinator(groups, instances, waker, eng, ids, bus, qm)
	x := engine.NewParallelGroupExecutor(instances, nodes, groups, launcher, coord, instances, qm)
	x.SetGatherer(gatherer)
	eng.RegisterExecutor(workflow.NodeTypeParallelGroup, x)
	sink := newMockExecutor()
	sink.onNode("after", func(_ context.Context, _ workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error) {
		dir, ok := engine.WorkDirFromVars(vars)
		if !ok {
			return nil, fmt.Errorf("downstream node has no parent workspace")
		}
		contents, err := os.ReadFile(filepath.Join(dir, "shared.txt"))
		if err != nil {
			return nil, err
		}
		return &engine.NodeResult{Output: map[string]any{"content": string(contents)}}, nil
	})
	eng.RegisterExecutor(workflow.NodeTypeScript, sink)
	eng.FreezeExecutors()
	require.NoError(t, eng.StartInstance(context.Background(), parentID))
	require.Eventually(t, func() bool {
		p, err := instances.Get(context.Background(), parentID)
		return err == nil && p.State.IsTerminal()
	}, 5*time.Second, 10*time.Millisecond)
	parent, err := instances.Get(context.Background(), parentID)
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, parent.State)
	assert.NotContains(t, parent.Variables, "fail")
	output, ok := parent.Variables["after"].(map[string]any)
	require.True(t, ok, "SINK: the resumed engine persisted its downstream file read")
	assert.Equal(t, "from-a\nfrom-b\n", output["content"])
	group, ok := parent.Variables["g1"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "success", group["outcome"])
	assert.Equal(t, "applied", group["gather"].(map[string]any)["state"])
}
