package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Dropping ActiveSession before closing its durable row would strand an open
// conversation. Exercise the real bridge and inject failures at its storage seam.
func TestFileGatherer_AbortSessionCleanupIsDurableAndRetryable(t *testing.T) {
	for _, boundary := range []string{"none", "missing handle", "session update", "forget identity", "missing bridge"} {
		t.Run(boundary, func(t *testing.T) {
			f := newReconcileFixture(t, "a", "b")
			active := f.openSession(t)
			f.addSessionRecord(t, active, true, "")
			deps := newBridgeFixture(t)
			bridge := func(nodes instance.NodeExecutionRepository) *engine.GroupResolutionBridge {
				return engine.NewGroupResolutionBridge(f.groups, f.instances, nodes, groupResolverResolver(), deps.selector,
					deps.files, deps.notifier, f.bus, f.waker, f.qm, groupResolverTimeout)
			}
			var starter engine.GroupResolutionStarter = bridge(f.nodeExecs)
			switch boundary {
			case "missing handle":
				f.groups.update(f.runID, func(r *instance.GroupRun) { r.Gather.Handle = nil })
				starter = bridge(resolutionSessionRepo{NodeExecutionRepository: f.nodeExecs, updateErr: assert.AnError})
			case "session update":
				starter = bridge(resolutionSessionRepo{NodeExecutionRepository: f.nodeExecs, updateErr: assert.AnError})
			case "forget identity":
				f.groups.failCall("ReplaceGatherReport", 2, assert.AnError)
			case "missing bridge":
				starter = nil
			}
			f.gatherer = engine.NewGroupGatherer(f.groups, f.area, starter, f.bus)
			ctx := context.Background()
			f.setParent(instance.StateCancelled)
			step, err := f.gatherer.AbortGather(ctx, f.groups.snapshot(f.runID), "cancelled", "parent cancelled")
			if boundary != "none" {
				require.Error(t, err, "cleanup failures must block finalization")
				assert.Nil(t, step)
				assert.Nil(t, f.groups.snapshot(f.runID).FinalOutput)
				assert.Equal(t, &active, f.report().ActiveSession, "retry needs the exact durable identity")
				assert.Equal(t, "failed", f.report().State, "abort intent precedes closing the session")
				// Resume with fresh collaborators and only persisted state.
				f.gatherer = engine.NewGroupGatherer(f.groups, f.area, bridge(f.nodeExecs), f.bus)
				step, err = f.gather()
			}
			require.NoError(t, err)
			require.NotNil(t, step)
			require.True(t, step.Done)
			assert.Equal(t, workflow.GroupPortFailure, step.Port)
			row, err := f.nodeExecs.GetByInstanceAndNode(ctx, f.parent.ID, "g1")
			require.NoError(t, err)
			assert.False(t, engine.IsOpenGroupResolutionSession(row.LLMConversation))
			assert.Equal(t, "cancelled", row.LLMConversation[engine.ConversationKeyEndReason])
			assert.Nil(t, f.report().ActiveSession)
			assert.Equal(t, "cancelled", f.report().Attempts[0].Outcome)
			assert.Equal(t, "parent cancelled", f.report().Error)
			assert.Equal(t, instance.StateCancelled, f.parentState())
			assert.Empty(t, f.waker.wakeCalls())
			assert.Zero(t, f.area.applyCount())
			assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, f.area.parentFiles(gatherParentDir))
			assert.Len(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeInteractiveEnded), 1, "closing again is idempotent")
		})
	}
}
