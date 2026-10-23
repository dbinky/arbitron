package engine_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// stStore is the bridge's ancestry source. It counts Get calls per id so the
// cache and its invalidation are observable. Only Get is exercised.
type stStore struct {
	instance.InstanceRepository
	mu   sync.Mutex
	rows map[uuid.UUID]*instance.Instance
	gets map[uuid.UUID]int
}

func newSTStore() *stStore {
	return &stStore{rows: map[uuid.UUID]*instance.Instance{}, gets: map[uuid.UUID]int{}}
}

func (s *stStore) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets[id]++
	return s.read(id)
}

func (s *stStore) read(id uuid.UUID) (*instance.Instance, error) {
	inst, ok := s.rows[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	cp := *inst
	return &cp, nil
}

func (s *stStore) getCount(id uuid.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets[id]
}

// stRowsReader gives the engine (the viewer) its own, uncounted view of the
// same rows, so only the bridge's ancestry reads are counted.
type stRowsReader struct {
	instance.InstanceRepository
	s *stStore
}

func (r *stRowsReader) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return r.s.read(id)
}

// stViewer wraps the REAL engine authorization and counts decisions per id.
type stViewer struct {
	inner    engine.InstanceViewer
	mu       sync.Mutex
	calls    map[uuid.UUID]int
	failNext map[uuid.UUID]error
}

func (v *stViewer) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	v.mu.Lock()
	v.calls[id]++
	err := v.failNext[id]
	delete(v.failNext, id)
	v.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return v.inner.GetInstance(ctx, id)
}

func (v *stViewer) count(id uuid.UUID) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls[id]
}

// stTree has two roots: r has children a (with child a1) and b;
// the unrelated root s has child s1.
type stTree struct {
	store  *stStore
	viewer *stViewer
	bus    *eventbus.InProcessEventBus
	bridge *engine.SubtreeEventBridge
	owner  uuid.UUID

	r, a, a1, b, s, s1 uuid.UUID
}

func newSTTree(t *testing.T) *stTree {
	t.Helper()
	tr := &stTree{store: newSTStore(), bus: eventbus.New(), owner: uuid.New()}
	eng := wireModelPins(engine.NewEngine(nil, &stRowsReader{s: tr.store}, nil, nil, nil, nil, newMockEventBus(), nil, nil, ""))
	tr.viewer = &stViewer{inner: eng, calls: map[uuid.UUID]int{}}
	tr.bridge = engine.NewSubtreeEventBridge(tr.bus, tr.viewer, tr.store)
	tr.r = tr.put(nil, instance.VisibilityPrivate)
	tr.a = tr.put(&tr.r, instance.VisibilityPrivate)
	tr.a1 = tr.put(&tr.a, instance.VisibilityPrivate)
	tr.b = tr.put(&tr.r, instance.VisibilityPrivate)
	tr.s = tr.put(nil, instance.VisibilityPrivate)
	tr.s1 = tr.put(&tr.s, instance.VisibilityPrivate)
	return tr
}

func (tr *stTree) put(parent *uuid.UUID, vis instance.Visibility) uuid.UUID {
	inst := &instance.Instance{ID: uuid.New(), OwnerID: tr.owner, State: instance.StateRunning, Visibility: vis}
	if parent != nil {
		p := *parent
		inst.ParentInstanceID = &p
	}
	tr.store.mu.Lock()
	tr.store.rows[inst.ID] = inst
	tr.store.mu.Unlock()
	return inst.ID
}

func (tr *stTree) setVisibility(id uuid.UUID, vis instance.Visibility) {
	tr.store.mu.Lock()
	defer tr.store.mu.Unlock()
	tr.store.rows[id].Visibility = vis
}

func (tr *stTree) as(user uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: user, Username: "u", Role: identity.RoleUser})
}

func (tr *stTree) ownerCtx() context.Context { return tr.as(tr.owner) }

func (tr *stTree) subscribe(t *testing.T, ctx context.Context, root uuid.UUID) <-chan engine.ScopedEvent {
	t.Helper()
	ch, unsub, err := tr.bridge.SubscribeSubtree(ctx, root)
	require.NoError(t, err)
	t.Cleanup(unsub)
	return ch
}

func (tr *stTree) publish(id uuid.UUID, typ string) engine.Event {
	evt := engine.Event{Type: typ, Timestamp: time.Now().UTC(), Data: map[string]any{"marker": typ + "@" + id.String()}}
	tr.bus.Publish(id, evt)
	return evt
}

func stRecv(t *testing.T, ch <-chan engine.ScopedEvent) engine.ScopedEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		require.True(t, ok, "subscription closed unexpectedly")
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a subtree event")
	}
	return engine.ScopedEvent{}
}

func stNoEvent(t *testing.T, ch <-chan engine.ScopedEvent) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("unexpected event %s from %s", ev.Event.Type, ev.InstanceID)
		}
	case <-time.After(150 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func TestSubtreeBridge_IntermediateParentGetsDescendantsNotSiblings(t *testing.T) {
	tr := newSTTree(t)
	ch := tr.subscribe(t, tr.ownerCtx(), tr.a)

	tr.publish(tr.r, engine.EventTypeNodeStarted)  // ancestor: outside the subtree
	tr.publish(tr.b, engine.EventTypeNodeStarted)  // sibling
	tr.publish(tr.s1, engine.EventTypeNodeStarted) // unrelated tree
	tr.publish(tr.a1, engine.EventTypeNodeCompleted)
	tr.publish(tr.a, engine.EventTypeNodeStarted)

	first := stRecv(t, ch)
	assert.Equal(t, tr.a1, first.InstanceID, "events are delivered in bus order; everything before was outside the subtree")
	assert.Equal(t, engine.EventTypeNodeCompleted, first.Event.Type)
	assert.Equal(t, tr.a, stRecv(t, ch).InstanceID)
	stNoEvent(t, ch)
}

func TestSubtreeBridge_RootSubscriptionGetsWholeSubtreeOnly(t *testing.T) {
	tr := newSTTree(t)
	ch := tr.subscribe(t, tr.ownerCtx(), tr.r)

	tr.publish(tr.s1, engine.EventTypeNodeStarted)
	tr.publish(tr.s, engine.EventTypeNodeStarted)
	tr.publish(tr.a1, engine.EventTypeNodeStarted)
	tr.publish(tr.b, engine.EventTypeInstanceCompleted)
	tr.publish(tr.r, engine.EventTypeNodeStarted)

	assert.Equal(t, tr.a1, stRecv(t, ch).InstanceID)
	assert.Equal(t, tr.b, stRecv(t, ch).InstanceID)
	assert.Equal(t, tr.r, stRecv(t, ch).InstanceID)
	stNoEvent(t, ch)
}

func TestSubtreeBridge_EventsKeepOriginIdentity(t *testing.T) {
	tr := newSTTree(t)
	ch := tr.subscribe(t, tr.ownerCtx(), tr.r)

	sent := tr.publish(tr.a1, engine.EventTypeInteractiveToken)

	got := stRecv(t, ch)
	assert.Equal(t, tr.a1, got.InstanceID, "the source instance is the bus's, never the subscription root")
	assert.Equal(t, sent, got.Event)
}

func TestSubtreeBridge_DeliversEachEventExactlyOnce(t *testing.T) {
	tr := newSTTree(t)
	ch1 := tr.subscribe(t, tr.ownerCtx(), tr.r)
	ch2 := tr.subscribe(t, tr.ownerCtx(), tr.r)

	tr.publish(tr.r, engine.EventTypeNodeStarted) // the root matches as source AND as its own ancestor
	tr.publish(tr.a1, engine.EventTypeNodeStarted)

	for _, ch := range []<-chan engine.ScopedEvent{ch1, ch2} {
		assert.Equal(t, tr.r, stRecv(t, ch).InstanceID)
		assert.Equal(t, tr.a1, stRecv(t, ch).InstanceID)
		stNoEvent(t, ch)
	}
}

func TestSubtreeBridge_InSubtree(t *testing.T) {
	tr := newSTTree(t)
	ctx := tr.ownerCtx()
	assert.True(t, tr.bridge.InSubtree(ctx, tr.r, tr.r))
	assert.True(t, tr.bridge.InSubtree(ctx, tr.r, tr.a1))
	assert.True(t, tr.bridge.InSubtree(ctx, tr.a, tr.a1))
	assert.False(t, tr.bridge.InSubtree(ctx, tr.a, tr.b), "a sibling is not in an intermediate parent's subtree")
	assert.False(t, tr.bridge.InSubtree(ctx, tr.a, tr.r), "an ancestor is not in its descendant's subtree")
	assert.False(t, tr.bridge.InSubtree(ctx, tr.r, uuid.New()))
}

// ---------------------------------------------------------------------------
// Authorization (error cases)
// ---------------------------------------------------------------------------

func TestSubtreeBridge_UnauthorizedRootRejected(t *testing.T) {
	tr := newSTTree(t)

	ch, unsub, err := tr.bridge.SubscribeSubtree(tr.as(uuid.New()), tr.r)

	require.Error(t, err)
	assert.True(t, errors.Is(err, instance.ErrNotFound), "an unviewable root is indistinguishable from an absent one")
	assert.Nil(t, ch)
	assert.Nil(t, unsub)
}

func TestSubtreeBridge_UnauthorizedDescendantEventsDropped(t *testing.T) {
	tr := newSTTree(t)
	viewer := uuid.New()
	// The viewer may see r and a (public) but not a1, whose child workflow is
	// private even though the owner is the same.
	tr.setVisibility(tr.r, instance.VisibilityPublic)
	tr.setVisibility(tr.a, instance.VisibilityPublic)
	ch := tr.subscribe(t, tr.as(viewer), tr.r)

	tr.publish(tr.a1, engine.EventTypeInteractiveToken) // token stream of an unviewable session
	tr.publish(tr.a1, engine.EventTypeInteractiveMessage)
	tr.publish(tr.a1, engine.EventTypeNodeStarted)
	tr.publish(tr.a, engine.EventTypeInteractiveToken)

	got := stRecv(t, ch)
	assert.Equal(t, tr.a, got.InstanceID, "only the authorized source's token reaches the subscriber")
	assert.Equal(t, engine.EventTypeInteractiveToken, got.Event.Type)
	stNoEvent(t, ch)
	assert.Equal(t, 3, tr.viewer.count(tr.a1), "each source event is authorized independently")

	// The owner, subscribed to the same root, receives a1's events.
	ownerCh := tr.subscribe(t, tr.ownerCtx(), tr.r)
	tr.publish(tr.a1, engine.EventTypeInteractiveToken)
	assert.Equal(t, tr.a1, stRecv(t, ownerCh).InstanceID)
}

func TestSubtreeBridge_VisibilityChangesDuringSubscription(t *testing.T) {
	tr := newSTTree(t)
	for _, id := range []uuid.UUID{tr.r, tr.a1, tr.b} {
		tr.setVisibility(id, instance.VisibilityPublic)
	}
	ch := tr.subscribe(t, tr.as(uuid.New()), tr.r)

	// A public sibling is a barrier: receiving it proves that the preceding
	// sensitive event was processed, not merely delayed by the goroutine.
	assertDropped := func(t *testing.T, source uuid.UUID) {
		t.Helper()
		tr.publish(source, engine.EventTypeInteractiveToken)
		sentinel := tr.publish(tr.b, engine.EventTypeNodeCompleted)
		got := stRecv(t, ch)
		require.Equal(t, tr.b, got.InstanceID, "private/error token must never reach the sink")
		require.Equal(t, sentinel, got.Event)
		stNoEvent(t, ch)
	}
	for _, source := range []uuid.UUID{tr.a1, tr.r} {
		t.Run(source.String(), func(t *testing.T) {
			sent := tr.publish(source, engine.EventTypeInteractiveToken)
			require.Equal(t, engine.ScopedEvent{InstanceID: source, Event: sent}, stRecv(t, ch))

			tr.setVisibility(source, instance.VisibilityPrivate)
			assertDropped(t, source)
			tr.setVisibility(source, instance.VisibilityPublic)
			sent = tr.publish(source, engine.EventTypeInteractiveToken)
			require.Equal(t, engine.ScopedEvent{InstanceID: source, Event: sent}, stRecv(t, ch), "same subscription recovers from denial")

			tr.viewer.mu.Lock()
			tr.viewer.failNext = map[uuid.UUID]error{source: errors.New("temporary viewer failure")}
			tr.viewer.mu.Unlock()
			assertDropped(t, source)
			sent = tr.publish(source, engine.EventTypeInteractiveToken)
			require.Equal(t, engine.ScopedEvent{InstanceID: source, Event: sent}, stRecv(t, ch), "transient errors are rechecked")
		})
	}
}

// ---------------------------------------------------------------------------
// Ancestry cache, purge and corruption (edge cases)
// ---------------------------------------------------------------------------

func TestSubtreeBridge_AncestryIsCached(t *testing.T) {
	tr := newSTTree(t)
	ch := tr.subscribe(t, tr.ownerCtx(), tr.r)

	for i := 0; i < 3; i++ {
		tr.publish(tr.a1, engine.EventTypeNodeStarted)
		stRecv(t, ch)
	}
	assert.Equal(t, 1, tr.store.getCount(tr.a1), "a1's parent is read once")
	assert.Equal(t, 1, tr.store.getCount(tr.a), "the shared ancestor chain is cached")

	tr.publish(tr.a1, engine.EventTypeInstanceCreated)
	assert.Equal(t, engine.EventTypeInstanceCreated, stRecv(t, ch).Event.Type)
	assert.Equal(t, 1, tr.store.getCount(tr.a1), "parent links are immutable: nothing to re-read")
}

// An event from an id with no row yet is dropped, but nothing is remembered:
// once the row is committed, its next event is delivered with NO
// instance.created (the queue evaluator may start it before the announcement).
func TestSubtreeBridge_UnknownInstanceStreamsOnceItsRowExists(t *testing.T) {
	tr := newSTTree(t)
	ch := tr.subscribe(t, tr.ownerCtx(), tr.r)
	late := uuid.New()

	tr.publish(late, engine.EventTypeNodeStarted) // row not readable yet
	tr.publish(tr.b, engine.EventTypeNodeStarted) // sentinel
	require.Equal(t, tr.b, stRecv(t, ch).InstanceID, "the event of the unknown instance was dropped")
	lateInst := &instance.Instance{ID: late, OwnerID: tr.owner, State: instance.StateQueued, Visibility: instance.VisibilityPrivate}
	rp := tr.r
	lateInst.ParentInstanceID = &rp
	tr.store.mu.Lock()
	tr.store.rows[late] = lateInst
	tr.store.mu.Unlock()

	tr.publish(late, engine.EventTypeNodeStarted)
	got := stRecv(t, ch)
	assert.Equal(t, late, got.InstanceID)
	assert.Equal(t, engine.EventTypeNodeStarted, got.Event.Type)
}

func TestSubtreeBridge_PurgedAncestorDegradesToNotInSubtree(t *testing.T) {
	tr := newSTTree(t)
	tr.store.mu.Lock()
	delete(tr.store.rows, tr.a) // a1's parent is purged
	tr.store.mu.Unlock()
	ch := tr.subscribe(t, tr.ownerCtx(), tr.r)

	tr.publish(tr.a1, engine.EventTypeNodeStarted)
	tr.publish(tr.b, engine.EventTypeNodeStarted)

	assert.Equal(t, tr.b, stRecv(t, ch).InstanceID, "a broken chain is never assumed to reach the root")
	stNoEvent(t, ch)

	own := tr.subscribe(t, tr.ownerCtx(), tr.a1)
	tr.publish(tr.a1, engine.EventTypeNodeStarted)
	assert.Equal(t, tr.a1, stRecv(t, own).InstanceID, "the orphan's own subscription still works")
}

func TestSubtreeBridge_CorruptParentCycleTerminates(t *testing.T) {
	tr := newSTTree(t)
	x, y := uuid.New(), uuid.New()
	tr.store.mu.Lock()
	tr.store.rows[x] = &instance.Instance{ID: x, OwnerID: tr.owner, ParentInstanceID: &y}
	tr.store.rows[y] = &instance.Instance{ID: y, OwnerID: tr.owner, ParentInstanceID: &x}
	tr.store.mu.Unlock()
	ch := tr.subscribe(t, tr.ownerCtx(), tr.r)

	tr.publish(x, engine.EventTypeNodeStarted)
	tr.publish(tr.b, engine.EventTypeNodeStarted)

	assert.Equal(t, tr.b, stRecv(t, ch).InstanceID)
	assert.False(t, tr.bridge.InSubtree(tr.ownerCtx(), tr.r, x))
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func TestSubtreeBridge_UnsubscribeAndCancelCloseTheChannel(t *testing.T) {
	tr := newSTTree(t)

	ch, unsub, err := tr.bridge.SubscribeSubtree(tr.ownerCtx(), tr.r)
	require.NoError(t, err)
	unsub()
	unsub() // idempotent
	_, ok := <-ch
	assert.False(t, ok)

	ctx, cancel := context.WithCancel(tr.ownerCtx())
	ch2, unsub2, err := tr.bridge.SubscribeSubtree(ctx, tr.r)
	require.NoError(t, err)
	defer unsub2()
	cancel()
	select {
	case _, ok := <-ch2:
		assert.False(t, ok, "cancelling the subscriber's context closes the channel")
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after context cancellation")
	}
}

// stClosingBus lets a test close the all-subscriber channel (bus shutdown).
type stClosingBus struct {
	ch chan engine.ScopedEvent
}

func (b *stClosingBus) Publish(uuid.UUID, engine.Event) {}
func (b *stClosingBus) Subscribe(uuid.UUID) (<-chan engine.Event, func()) {
	c := make(chan engine.Event)
	close(c)
	return c, func() {}
}
func (b *stClosingBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) { return b.ch, func() {} }

func TestSubtreeBridge_BusShutdownClosesTheChannel(t *testing.T) {
	tr := newSTTree(t)
	bus := &stClosingBus{ch: make(chan engine.ScopedEvent)}
	bridge := engine.NewSubtreeEventBridge(bus, tr.viewer, tr.store)
	ch, unsub, err := bridge.SubscribeSubtree(tr.ownerCtx(), tr.r)
	require.NoError(t, err)
	defer unsub()

	close(bus.ch)

	select {
	case _, ok := <-ch:
		assert.False(t, ok)
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after bus shutdown")
	}
}

func TestNewSubtreeEventBridge_PanicsOnNilDependency(t *testing.T) {
	tr := newSTTree(t)
	assert.Panics(t, func() { engine.NewSubtreeEventBridge(nil, tr.viewer, tr.store) })
	assert.Panics(t, func() { engine.NewSubtreeEventBridge(tr.bus, nil, tr.store) })
	assert.Panics(t, func() { engine.NewSubtreeEventBridge(tr.bus, tr.viewer, nil) })
}

type stGroupReader struct {
	instance.GroupRunRepository
	read func(context.Context, uuid.UUID) (*instance.GroupRun, error)
}

func (r stGroupReader) Get(ctx context.Context, id uuid.UUID) (*instance.GroupRun, error) {
	return r.read(ctx, id)
}

func TestSubtreeBridge_ConflictPayloadFailsClosedWithoutTaskAuthority(t *testing.T) {
	for _, name := range []string{
		"visible task", "missing reader", "nil data", "wrong data type", "missing run id", "invalid run id", "zero run id",
		"missing task key", "unknown task", "missing node id", "wrong node", "wrong parent", "wrong run", "nil run",
		"storage error", "missing child id", "task read error", "hidden task",
	} {
		t.Run(name, func(t *testing.T) {
			tr := newSTTree(t)
			runID := uuid.New()
			run := &instance.GroupRun{ID: runID, ParentInstanceID: tr.r, NodeID: "g",
				Tasks: []instance.GroupTask{{Key: "a", ChildInstanceID: tr.a, Kind: instance.GroupTaskKindWorkflow}}}
			data := map[string]any{"groupRunId": runID.String(), "nodeId": "g", "taskKey": "a", "conflictPaths": []string{"secret.txt"}}
			var payload any = data
			var readErr error
			switch name {
			case "nil data":
				payload = nil
			case "wrong data type":
				payload = "secret.txt"
			case "missing run id":
				delete(data, "groupRunId")
			case "invalid run id":
				data["groupRunId"] = "invalid"
			case "zero run id":
				data["groupRunId"] = uuid.Nil.String()
			case "missing task key":
				delete(data, "taskKey")
			case "unknown task":
				data["taskKey"] = "other"
			case "missing node id":
				delete(data, "nodeId")
			case "wrong node":
				data["nodeId"] = "other"
			case "wrong parent":
				run.ParentInstanceID = tr.s
			case "wrong run":
				run.ID = uuid.New()
			case "nil run":
				run = nil
			case "storage error":
				readErr = errors.New("database unavailable")
			case "missing child id":
				run.Tasks[0].ChildInstanceID = uuid.Nil
			case "task read error":
				tr.viewer.failNext = map[uuid.UUID]error{tr.a: errors.New("database unavailable")}
			case "hidden task":
				tr.store.rows[tr.a].OwnerID = uuid.New()
			}
			if name != "missing reader" {
				tr.bridge.SetGroupRuns(stGroupReader{read: func(_ context.Context, id uuid.UUID) (*instance.GroupRun, error) {
					if id != runID {
						return nil, instance.ErrGroupRunNotFound
					}
					return run, readErr
				}})
			}
			ch := tr.subscribe(t, tr.ownerCtx(), tr.r)
			tr.bus.Publish(tr.r, engine.Event{Type: engine.EventTypeGroupConflict, Data: payload})
			tr.publish(tr.r, engine.EventTypeNodeStarted)
			if name == "visible task" {
				conflict := stRecv(t, ch)
				require.Equal(t, engine.EventTypeGroupConflict, conflict.Event.Type)
				require.Equal(t, payload, conflict.Event.Data)
			}
			require.Equal(t, engine.EventTypeNodeStarted, stRecv(t, ch).Event.Type, "unprovable task visibility must withhold the conflict payload")
		})
	}
}

func TestSubtreeBridge_ResolverPayloadRequiresEveryContributor(t *testing.T) {
	for _, name := range []string{
		"visible", "missing reader", "missing run id", "invalid run id", "zero run id", "missing node id",
		"wrong node", "wrong parent", "wrong run", "nil run", "storage error", "empty roster",
		"missing child id", "missing child", "task read error", "hidden second task", "untagged at group",
	} {
		t.Run(name, func(t *testing.T) {
			tr := newSTTree(t)
			runID := uuid.New()
			run := &instance.GroupRun{ID: runID, ParentInstanceID: tr.r, NodeID: "g",
				Tasks: []instance.GroupTask{{Key: "a", ChildInstanceID: tr.a}, {Key: "b", ChildInstanceID: tr.b}}}
			data := map[string]any{"sessionKind": "group_resolution", "groupRunId": runID.String(), "nodeId": "g", "token": "secret"}
			var readErr error
			switch name {
			case "missing run id":
				delete(data, "groupRunId")
			case "invalid run id":
				data["groupRunId"] = "invalid"
			case "zero run id":
				data["groupRunId"] = uuid.Nil.String()
			case "missing node id":
				delete(data, "nodeId")
			case "wrong node":
				data["nodeId"] = "other"
			case "wrong parent":
				run.ParentInstanceID = tr.s
			case "wrong run":
				run.ID = uuid.New()
			case "nil run":
				run = nil
			case "storage error":
				readErr = errors.New("database unavailable")
			case "empty roster":
				run.Tasks = nil
			case "missing child id":
				run.Tasks[1].ChildInstanceID = uuid.Nil
			case "missing child":
				run.Tasks[1].ChildInstanceID = uuid.New()
			case "task read error":
				tr.viewer.failNext = map[uuid.UUID]error{tr.b: errors.New("database unavailable")}
			case "hidden second task":
				tr.store.rows[tr.b].OwnerID = uuid.New()
			case "untagged at group":
				data = map[string]any{"token": "secret"}
				node := "g"
				tr.store.rows[tr.r].CurrentNodeID = &node
				tr.store.rows[tr.r].WorkflowSnapshot = map[string]any{"nodes": []any{map[string]any{"id": "g", "type": "parallel_group"}}}
			}
			if name != "missing reader" {
				tr.bridge.SetGroupRuns(stGroupReader{read: func(_ context.Context, id uuid.UUID) (*instance.GroupRun, error) {
					require.Equal(t, runID, id)
					return run, readErr
				}})
			}
			ch := tr.subscribe(t, tr.ownerCtx(), tr.r)
			tr.bus.Publish(tr.r, engine.Event{Type: engine.EventTypeInteractiveToken, Data: data})
			tr.publish(tr.r, engine.EventTypeNodeStarted)
			if name == "visible" {
				require.Equal(t, engine.EventTypeInteractiveToken, stRecv(t, ch).Event.Type)
			}
			require.Equal(t, engine.EventTypeNodeStarted, stRecv(t, ch).Event.Type, "unprovable contributor visibility must withhold resolver text")
		})
	}
}
