package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Seam paired:23 — group launch (Area 4) ↔ subtree stream (Area 9). The REAL
// producer (Engine.StartGroupTask → createAndEnqueue) and the REAL bridge share
// one in-process bus and one instance store. A subtree subscription is open on
// the parent BEFORE the task row exists and has already cached a negative
// ancestry entry for the task's reserved id; the task then emits immediately.
// Its instance.created and first event must reach the parent's subscription.
// Delivery rests on the committed parent link alone: a control child created
// WITHOUT any announcement streams too, so a negative lookup is never cached.
func TestGroupLaunchSubtreeSeam_ReservedTaskStreamsDespiteMissBeforeItsRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  func(*instance.Instance) engine.GroupTaskStartRequest
	}{
		{"branch", gtBranchReq},
		{"workflow", gtWorkflowReq},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := eventbus.New()
			h := newGroupTaskHarnessOn(t, bus)
			owner := uuid.New()
			h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
			parent := h.seedParent(t, owner, 0)
			req := tc.req(parent)
			ctx := ctxAs(owner)

			bridge := engine.NewSubtreeEventBridge(bus, h.engine, h.instances)
			ch, unsub, err := bridge.SubscribeSubtree(ctx, parent.ID)
			require.NoError(t, err)
			t.Cleanup(unsub)

			// Before launch: the reserved ids have no rows, so both lookups miss.
			// control is created WITHOUT the announcement.
			control := uuid.New()
			require.False(t, bridge.InSubtree(ctx, parent.ID, req.TaskInstanceID))
			require.False(t, bridge.InSubtree(ctx, parent.ID, control))
			gp, gn := parent.ID, "g"
			require.NoError(t, h.instances.Create(context.Background(), &instance.Instance{
				ModelPins:        map[string]string{},
				ModelPinsVersion: instance.ModelPinsVersionSnapshot,
				ID:               control, OwnerID: owner, ParentInstanceID: &gp, ParentNodeID: &gn,
				State: instance.StateQueued, Visibility: instance.VisibilityPrivate,
			}))
			bus.Publish(control, engine.Event{Type: engine.EventTypeNodeStarted, Timestamp: time.Now()})

			require.NoError(t, h.engine.StartGroupTask(ctx, req))
			// The task emits as soon as it is enqueued.
			bus.Publish(req.TaskInstanceID, engine.Event{Type: engine.EventTypeNodeStarted, Timestamp: time.Now()})
			bus.Publish(parent.ID, engine.Event{Type: engine.EventTypeNodeStarted, Timestamp: time.Now()}) // sentinel

			var got []engine.ScopedEvent
			for {
				ev := stRecv(t, ch)
				if ev.InstanceID == parent.ID {
					break
				}
				got = append(got, ev)
			}
			require.Len(t, got, 3, "the unannounced control and the task's announcement and first event all reach the parent's subscription")
			assert.Equal(t, control, got[0].InstanceID, "an earlier miss is not remembered")
			assert.Equal(t, req.TaskInstanceID, got[1].InstanceID)
			assert.Equal(t, engine.EventTypeInstanceCreated, got[1].Event.Type)
			assert.Equal(t, req.TaskInstanceID, got[2].InstanceID)
			assert.Equal(t, engine.EventTypeNodeStarted, got[2].Event.Type)

			assert.True(t, bridge.InSubtree(ctx, parent.ID, req.TaskInstanceID))
			assert.True(t, bridge.InSubtree(ctx, parent.ID, control))
		})
	}
}

// The other half of BREAK: a task whose group tuple is incomplete must be
// refused before any row exists, never persisted as a parentless root that
// the subtree stream (and graph-state and cancel) would silently lose.
func TestGroupLaunchSubtreeSeam_IncompleteTupleCreatesNoRowAndNoAnnouncement(t *testing.T) {
	bus := eventbus.New()
	h := newGroupTaskHarnessOn(t, bus)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	ctx := ctxAs(owner)

	bridge := engine.NewSubtreeEventBridge(bus, h.engine, h.instances)
	ch, unsub, err := bridge.SubscribeSubtree(ctx, parent.ID)
	require.NoError(t, err)
	t.Cleanup(unsub)

	for name, mutate := range map[string]func(*engine.GroupTaskStartRequest){
		"no group run": func(r *engine.GroupTaskStartRequest) { r.GroupRunID = uuid.Nil },
		"no task key":  func(r *engine.GroupTaskStartRequest) { r.TaskKey = "" },
		"no kind":      func(r *engine.GroupTaskStartRequest) { r.Kind = "" },
		"no node":      func(r *engine.GroupTaskStartRequest) { r.ParentNodeID = "" },
	} {
		req := gtBranchReq(parent)
		mutate(&req)
		require.ErrorIs(t, h.engine.StartGroupTask(ctx, req), engine.ErrInvalidGroupTaskRequest, name)
		_, gerr := h.instances.Get(context.Background(), req.TaskInstanceID)
		require.ErrorIs(t, gerr, instance.ErrNotFound, "%s: no parentless row may be written", name)
	}
	assert.Empty(t, h.enqueuedIDs())
	stNoEvent(t, ch)
}

// The queue evaluator is independent of createAndEnqueue: it may select the
// committed queued row and start it before instance.created is published. The
// task's first event (instance.started) then reaches the bridge while the
// negative entry cached before the row existed is still present. Delivery must
// not depend on the announcement winning that race.
func TestGroupLaunchSubtreeSeam_TaskStartedBeforeAnnouncementStillStreams(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  func(*instance.Instance) engine.GroupTaskStartRequest
	}{
		{"branch", gtBranchReq},
		{"workflow", gtWorkflowReq},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := eventbus.New()
			h := newGroupTaskHarnessOn(t, bus)
			owner := uuid.New()
			h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
			parent := h.seedParent(t, owner, 0)
			req := tc.req(parent)
			ctx := ctxAs(owner)

			bridge := engine.NewSubtreeEventBridge(bus, h.engine, h.instances)
			ch, unsub, err := bridge.SubscribeSubtree(ctx, parent.ID)
			require.NoError(t, err)
			t.Cleanup(unsub)

			// The subscription has already looked the reserved id up and found no row.
			require.False(t, bridge.InSubtree(ctx, parent.ID, req.TaskInstanceID))

			// Evaluator admits the committed row before Create's caller announces it.
			h.instances.mu.Lock()
			h.instances.onCreate = func(inst *instance.Instance) {
				if inst.ID == req.TaskInstanceID {
					bus.Publish(inst.ID, engine.Event{Type: "instance.started", Timestamp: time.Now()})
				}
			}
			h.instances.mu.Unlock()

			require.NoError(t, h.engine.StartGroupTask(ctx, req))
			bus.Publish(parent.ID, engine.Event{Type: engine.EventTypeNodeStarted, Timestamp: time.Now()}) // sentinel

			var types []string
			for {
				ev := stRecv(t, ch)
				if ev.InstanceID == parent.ID {
					break
				}
				require.Equal(t, req.TaskInstanceID, ev.InstanceID)
				types = append(types, ev.Event.Type)
			}
			assert.Equal(t, []string{"instance.started", engine.EventTypeInstanceCreated}, types,
				"the task's first event is delivered even though it precedes its announcement")
		})
	}
}
