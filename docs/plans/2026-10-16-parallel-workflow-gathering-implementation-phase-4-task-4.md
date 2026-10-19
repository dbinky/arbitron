# Parallel Workflow Gathering — Phase 4 Task 4: SubtreeEventBridge (ancestry-routed, per-source authorized)

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Let a live view subscribed to ANY run (root or intermediate parent) receive exactly the events of that run and its descendants — never siblings outside the subtree — with every forwarded source authorized for the subscriber, including interactive token/history-bearing events.
**Architecture:** A new domain bridge `engine.SubtreeEventBridge` (`domain/engine/subtree_events.go`). Each `SubscribeSubtree` call authorizes the root, opens its own `EventBus.SubscribeAll` channel (so the bus's per-event origin, `ScopedEvent.InstanceID`, is preserved untouched) and runs one goroutine that routes each event: membership by walking `Instance.ParentInstanceID` through `InstanceRepository.Get` with a bridge-wide bounded LRU ancestry cache (invalidated on `instance.created` for that instance), then authorization of the SOURCE via `InstanceViewer.GetInstance` under the subscriber's context identity, rechecked for every event (visibility may change during a subscription). Missing/purged ancestors and cycles degrade to "not in subtree"; storage errors drop the event without caching. No change to the bus, the per-instance stream or the system stream.
**Tech Stack:** Go 1.25.5, standard library (`container/list`, `sync`), testify, `adapter/eventbus` (tests only).
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("The queue's live run view reflects every concurrently active node across all fanned-out paths"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` §3 "Descendant events and reliable refresh" ("The selected instance and every ancestor view must receive relevant descendant changes; a root-only subscription would miss an intermediate parent's view or send it sibling activity", "Deduplicate delivery and keep `SubscribeAll`'s origin identity intact", "Missing/purged ancestors must degrade without broadening scope", "Authorize each forwarded source via the instance service … This includes interactive tokens/history/results") and Data Provenance row "Ancestor routing, authorized subtree subscription" (PLANNED DEPENDENCY P4-B — resolved here; the interface/cache/invalidation choices below are that resolution).

## Required authorization-change TDD cycle

Before implementing the routing change, add `TestSubtreeBridge_VisibilityChangesDuringSubscription`:
subscribe as a non-owner to a public root and public child; forward one child token,
make that child private, then publish a second token and assert it never reaches the
subscriber. Make the child public again and assert the next token arrives on the SAME
subscription. Repeat with the subscribed root as source. Inject a transient viewer error
and verify one event is dropped and the following event is rechecked. Observe red against
the cached implementation, implement per-event authorization, then run the subtree and
stream suites with `-race`. Keep the existing success, hidden-child, ancestry, cancellation,
cache-bound and error tests. P4-T5's real-router test must repeat the private/public change
and assert the WebSocket sink receives no private token payload.


**Interfaces**
- Consumes (existing code):
  - `domain/engine/ports.go:260-285` `Event`, `ScopedEvent{InstanceID, Event}`, `EventBus{Publish, Subscribe, SubscribeAll}` — "The unsubscribe function … MUST close the returned channel".
  - `adapter/eventbus/eventbus.go:51-84` `InProcessEventBus.Publish` (non-blocking fan-out; drops on a full 256 buffer), `:129-159` `SubscribeAll` (unsubscribe idempotent, closes channel).
  - `domain/engine/engine.go:574-578` publishes `"instance.created"` on the new run's id right after `instances.Create`; `:658-672` `Engine.GetInstance` (authorized read; `ErrNotFound` for absent or unviewable).
  - `domain/instance/ports.go:14-29` `InstanceRepository.Get`; `domain/instance/instance.go:137` `Instance.ParentInstanceID`; `domain/instance/spawn_link.go:76` `MaxSpawnDepth = 10`.
  - Test idioms: `domain/engine/engine_test.go:665-690` `newMockEventBus` (package `engine_test`); external engine tests already import adapters (`domain/engine/engine_teams_integration_test.go:18`).
- Consumes (P4-T1): `engine.InstanceViewer` (`GetInstance(ctx, id) (*instance.Instance, error)`).
- Produces (exact):
  - `const EventTypeInstanceCreated = "instance.created"` (names the literal at `engine.go:575`).
  - `type SubtreeEventBridge struct { /* unexported */ }`
  - `func NewSubtreeEventBridge(bus EventBus, viewer InstanceViewer, instances instance.InstanceRepository) *SubtreeEventBridge` (panics on any nil dependency)
  - `func (b *SubtreeEventBridge) SubscribeSubtree(ctx context.Context, rootID uuid.UUID) (<-chan ScopedEvent, func(), error)` — authorizes `rootID` under `ctx` first (returns the viewer's error, wrapping `instance.ErrNotFound`, with nil channel and nil func); the channel carries each in-subtree, authorized bus event exactly once, in bus order, with the bus's `ScopedEvent` unchanged; the returned func is idempotent and closes the channel; cancelling `ctx` also closes it; a bus channel closure closes it.
  - `func (b *SubtreeEventBridge) InSubtree(ctx context.Context, rootID, id uuid.UUID) bool` — **contract refinement (additive):** membership check used by P4-T5 to authorize the explicit target of interactive controls on a subtree stream.

**Wiring & data provenance**
- Composition root: P4-T5 constructs `engine.NewSubtreeEventBridge(bus, eng, instanceRepo)` in `cmd/arbitron/main.go:initDomain` and injects it into `StreamHandler` via `RouterDeps.SubtreeEvents` (`?subtree=1` mode).
- Producers:
  - `bus` ← `cmd/arbitron/main.go` `bus` (the process `*eventbus.InProcessEventBus` already passed to `NewEngine` at `main.go:800-811`); events published by `Engine` (`engine.go` node/instance lifecycle), `conversationHandler` (`conversation.go:111,189,355,388` interactive message/token/ended), `executor_interactive.go:169` (tokens), and P2-T13/P3 group emitters on the parent id.
  - `viewer` ← `eng` (`main.go:800`), the same authorization `InstanceHandler.Get` uses.
  - `instances` ← `instanceRepo` (`main.go:373`); `parent_instance_id` written at creation (`createInstanceSQL`, P2-T7), immutable afterwards — which is why caching parents is safe and only `instance.created` needs to invalidate (it clears a negative entry cached before a row became readable).
- Provenance probes: `TestSubtreeBridge_IntermediateParentGetsDescendantsNotSiblings` (routing comes from persisted parent links, not from the subscription root alone); `TestSubtreeBridge_UnauthorizedDescendantEventsDropped` (per-source viewer decision, not root ownership); `TestNewSubtreeEventBridge_PanicsOnNilDependency`.

### Task 4: SubtreeEventBridge
**Files:**
- Create: `domain/engine/subtree_events.go`
- Test:   `domain/engine/subtree_events_test.go` (package `engine_test`, real `adapter/eventbus` bus)
- Test:   `domain/engine/subtree_events_internal_test.go` (package `engine`, cache bound / transient errors)

(No existing file is modified. External test helpers are prefixed `st`, internal ones `stb`.)

- [x] Step 1: Write the failing behaviour tests. Create `domain/engine/subtree_events_test.go`:

```go
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
	inner engine.InstanceViewer
	mu    sync.Mutex
	calls map[uuid.UUID]int
}

func (v *stViewer) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	v.mu.Lock()
	v.calls[id]++
	v.mu.Unlock()
	return v.inner.GetInstance(ctx, id)
}

func (v *stViewer) count(id uuid.UUID) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls[id]
}

// stTree:   r ── a ── a1        s ── s1
//               └─ b
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
	eng := engine.NewEngine(nil, &stRowsReader{s: tr.store}, nil, nil, nil, nil, newMockEventBus(), nil, nil, "")
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

// ---------------------------------------------------------------------------
// Ancestry cache, purge and corruption (edge cases)
// ---------------------------------------------------------------------------

func TestSubtreeBridge_AncestryIsCachedAndInvalidatedOnInstanceCreated(t *testing.T) {
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
	assert.Equal(t, 2, tr.store.getCount(tr.a1), "instance.created invalidates that instance's entry")
	assert.Equal(t, 1, tr.store.getCount(tr.a), "other entries survive")
}

func TestSubtreeBridge_NegativeEntryRecoversOnInstanceCreated(t *testing.T) {
	tr := newSTTree(t)
	ch := tr.subscribe(t, tr.ownerCtx(), tr.r)
	late := uuid.New()

	tr.publish(late, engine.EventTypeNodeStarted) // row not readable yet: cached as missing
	lateInst := &instance.Instance{ID: late, OwnerID: tr.owner, State: instance.StateQueued, Visibility: instance.VisibilityPrivate}
	rp := tr.r
	lateInst.ParentInstanceID = &rp
	tr.store.mu.Lock()
	tr.store.rows[late] = lateInst
	tr.store.mu.Unlock()
	tr.publish(late, engine.EventTypeNodeStarted) // still the cached negative entry
	tr.publish(tr.b, engine.EventTypeNodeStarted) // sentinel
	assert.Equal(t, tr.b, stRecv(t, ch).InstanceID, "both events of the unknown instance were dropped")

	tr.publish(late, engine.EventTypeInstanceCreated)
	got := stRecv(t, ch)
	assert.Equal(t, late, got.InstanceID)
	assert.Equal(t, engine.EventTypeInstanceCreated, got.Event.Type)
	tr.publish(late, engine.EventTypeNodeStarted)
	assert.Equal(t, late, stRecv(t, ch).InstanceID)
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
```

- [x] Step 2: Write the failing internal cache tests. Create `domain/engine/subtree_events_internal_test.go`:

```go
package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/instance"
)

type stbBus struct{}

func (stbBus) Publish(uuid.UUID, Event) {}
func (stbBus) Subscribe(uuid.UUID) (<-chan Event, func()) {
	c := make(chan Event)
	close(c)
	return c, func() {}
}
func (stbBus) SubscribeAll() (<-chan ScopedEvent, func()) {
	c := make(chan ScopedEvent)
	close(c)
	return c, func() {}
}

type stbViewer struct{}

func (stbViewer) GetInstance(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	return &instance.Instance{ID: id}, nil
}

type stbRepo struct {
	instance.InstanceRepository
	mu   sync.Mutex
	rows map[uuid.UUID]*instance.Instance
	errs map[uuid.UUID]error
	gets map[uuid.UUID]int
}

func (r *stbRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gets[id]++
	if err := r.errs[id]; err != nil {
		return nil, err
	}
	inst, ok := r.rows[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	cp := *inst
	return &cp, nil
}

func newSTBRepo() *stbRepo {
	return &stbRepo{rows: map[uuid.UUID]*instance.Instance{}, errs: map[uuid.UUID]error{}, gets: map[uuid.UUID]int{}}
}

func TestSubtreeBridge_AncestryCacheIsBounded(t *testing.T) {
	repo := newSTBRepo()
	b := NewSubtreeEventBridge(stbBus{}, stbViewer{}, repo)
	b.maxAncestry = 3
	root := uuid.New()
	ids := make([]uuid.UUID, 5)
	for i := range ids {
		ids[i] = uuid.New()
		repo.rows[ids[i]] = &instance.Instance{ID: ids[i]}
	}
	ctx := context.Background()
	for _, id := range ids {
		assert.False(t, b.InSubtree(ctx, root, id))
	}

	assert.Equal(t, 3, b.ancestryLen(), "the cache never exceeds its bound")
	b.InSubtree(ctx, root, ids[4])
	assert.Equal(t, 1, repo.gets[ids[4]], "the most recent entry is still cached")
	b.InSubtree(ctx, root, ids[0])
	assert.Equal(t, 2, repo.gets[ids[0]], "the least recently used entry was evicted")
}

func TestSubtreeBridge_TransientStorageErrorIsNotCached(t *testing.T) {
	repo := newSTBRepo()
	b := NewSubtreeEventBridge(stbBus{}, stbViewer{}, repo)
	root := uuid.New()
	child := uuid.New()
	repo.rows[root] = &instance.Instance{ID: root}
	repo.rows[child] = &instance.Instance{ID: child, ParentInstanceID: &root}
	repo.errs[child] = errors.New("database is locked")
	ctx := context.Background()

	assert.False(t, b.InSubtree(ctx, root, child), "an unreadable chain degrades to not-in-subtree")
	assert.Equal(t, 0, b.ancestryLen(), "a storage error is not remembered")

	delete(repo.errs, child)
	assert.True(t, b.InSubtree(ctx, root, child), "the next lookup reads storage again")
	assert.Equal(t, 2, repo.gets[child])
}

func TestSubtreeBridge_InvalidateDropsOneEntry(t *testing.T) {
	repo := newSTBRepo()
	b := NewSubtreeEventBridge(stbBus{}, stbViewer{}, repo)
	root, a, c := uuid.New(), uuid.New(), uuid.New()
	repo.rows[a] = &instance.Instance{ID: a, ParentInstanceID: &root}
	repo.rows[c] = &instance.Instance{ID: c, ParentInstanceID: &root}
	ctx := context.Background()
	b.InSubtree(ctx, root, a)
	b.InSubtree(ctx, root, c)

	b.invalidate(a)

	assert.Equal(t, 1, b.ancestryLen())
	b.InSubtree(ctx, root, c)
	assert.Equal(t, 1, repo.gets[c])
	b.InSubtree(ctx, root, a)
	assert.Equal(t, 2, repo.gets[a])
}
```

- [x] Step 3: Run them, confirm they fail.

Confirmed red: the focused command fails to compile with `undefined: NewSubtreeEventBridge` in all three internal cache tests. Behavior coverage includes the required same-subscription visibility/error changes, using public sentinel events to prove dropped tokens reached the routing decision.

```bash
go test ./domain/engine/ -run 'TestSubtreeBridge|TestNewSubtreeEventBridge' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/subtree_events_internal_test.go:62:7: undefined: NewSubtreeEventBridge
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 4: Minimal implementation. Create `domain/engine/subtree_events.go`:

```go
package engine

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/instance"
)

// EventTypeInstanceCreated is published by Engine.TriggerWithDepartment right
// after a run row is created. The subtree bridge invalidates that instance's
// ancestry entry on it.
const EventTypeInstanceCreated = "instance.created"

const (
	// defaultSubtreeAncestryEntries bounds the bridge-wide parent cache.
	defaultSubtreeAncestryEntries = 4096
	// subtreeBufferSize mirrors the bus's per-subscriber buffer.
	subtreeBufferSize = 256
)

// SubtreeEventBridge delivers the events of a run AND its descendants to a
// subscriber, never events from outside that subtree, and only from sources
// the subscriber may view.
//
// The bus has no ancestry: Publish(instanceID, Event) carries only the source.
// Membership is therefore derived from persisted parent links
// (Instance.ParentInstanceID via InstanceRepository.Get), cached in a bounded
// LRU shared by all subscriptions. Parent links are immutable once a row is
// created, so an entry only needs invalidating when instance.created arrives
// for it (clearing a "missing" entry cached before the row was readable).
// A missing/purged ancestor, a cycle, or a storage error degrades to "not in
// subtree": scope is never broadened by guessing.
type SubtreeEventBridge struct {
	bus       EventBus
	viewer    InstanceViewer
	instances instance.InstanceRepository

	mu          sync.Mutex
	maxAncestry int
	ancestry    map[uuid.UUID]*list.Element // values are *ancestryEntry
	lru         *list.List                  // front = most recently used
}

type ancestryEntry struct {
	id        uuid.UUID
	parent    uuid.UUID
	hasParent bool
	missing   bool // the row could not be found (purged or not yet readable)
}

// NewSubtreeEventBridge wires the bridge. A nil dependency is a programming
// error (a nil viewer would forward unauthorized events), so it panics.
func NewSubtreeEventBridge(bus EventBus, viewer InstanceViewer, instances instance.InstanceRepository) *SubtreeEventBridge {
	if bus == nil || viewer == nil || instances == nil {
		panic("engine: NewSubtreeEventBridge requires non-nil bus, viewer and instances")
	}
	return &SubtreeEventBridge{
		bus:         bus,
		viewer:      viewer,
		instances:   instances,
		maxAncestry: defaultSubtreeAncestryEntries,
		ancestry:    map[uuid.UUID]*list.Element{},
		lru:         list.New(),
	}
}

// SubscribeSubtree authorizes rootID under ctx's identity, then streams every
// bus event whose source is rootID or one of its descendants AND which the
// subscriber may view. Each subscription owns one SubscribeAll channel, so the
// bus's ScopedEvent (and its origin InstanceID) reaches the subscriber exactly
// once and unchanged. The returned func is idempotent and closes the channel;
// cancelling ctx or a bus shutdown closes it too.
func (b *SubtreeEventBridge) SubscribeSubtree(ctx context.Context, rootID uuid.UUID) (<-chan ScopedEvent, func(), error) {
	if _, err := b.viewer.GetInstance(ctx, rootID); err != nil {
		return nil, nil, err
	}
	in, busUnsub := b.bus.SubscribeAll()
	var busOnce sync.Once
	releaseBus := func() { busOnce.Do(busUnsub) }

	out := make(chan ScopedEvent, subtreeBufferSize)
	subCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go b.pump(subCtx, rootID, in, out, done, releaseBus)

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	return out, unsub, nil
}

func (b *SubtreeEventBridge) pump(ctx context.Context, rootID uuid.UUID, in <-chan ScopedEvent, out chan<- ScopedEvent, done chan<- struct{}, releaseBus func()) {
	defer close(done)
	defer close(out)
	defer releaseBus()
	for {
		select {
		case <-ctx.Done():
			return
		case sev, ok := <-in:
			if !ok {
				return
			}
			b.route(ctx, rootID, sev, out)
		}
	}
}

func (b *SubtreeEventBridge) route(ctx context.Context, rootID uuid.UUID, sev ScopedEvent, out chan<- ScopedEvent) {
	if sev.Event.Type == EventTypeInstanceCreated {
		b.invalidate(sev.InstanceID)
	}
	if !b.InSubtree(ctx, rootID, sev.InstanceID) {
		return
	}
	// Membership can be cached because ancestry is immutable; authorization
	// cannot. Both a prior allow and a prior deny can change while connected.
	if _, err := b.viewer.GetInstance(ctx, sev.InstanceID); err != nil {
		return
	}

	select {
	case out <- sev:
	default:
		slog.Warn("subtree event bridge dropped event for slow subscriber",
			"rootInstanceID", rootID, "instanceID", sev.InstanceID, "eventType", sev.Event.Type)
	}
}

// InSubtree reports whether id is rootID or one of its descendants, walking
// persisted parent links. Missing ancestors, cycles and storage errors yield
// false; the walk is bounded by instance.MaxSpawnDepth.
func (b *SubtreeEventBridge) InSubtree(ctx context.Context, rootID, id uuid.UUID) bool {
	cur := id
	seen := map[uuid.UUID]bool{}
	for hops := 0; hops <= instance.MaxSpawnDepth+1; hops++ {
		if cur == rootID {
			return true
		}
		if seen[cur] {
			return false
		}
		seen[cur] = true
		parent, ok := b.parentOf(ctx, cur)
		if !ok {
			return false
		}
		cur = parent
	}
	return false
}

// parentOf returns id's parent and true, or false for a root, a missing row or
// a storage error. Storage errors are never cached.
func (b *SubtreeEventBridge) parentOf(ctx context.Context, id uuid.UUID) (uuid.UUID, bool) {
	b.mu.Lock()
	if el, ok := b.ancestry[id]; ok {
		b.lru.MoveToFront(el)
		e := el.Value.(*ancestryEntry)
		b.mu.Unlock()
		if e.missing || !e.hasParent {
			return uuid.Nil, false
		}
		return e.parent, true
	}
	b.mu.Unlock()

	inst, err := b.instances.Get(ctx, id)
	switch {
	case err == nil && inst != nil:
		e := &ancestryEntry{id: id}
		if inst.ParentInstanceID != nil {
			e.parent, e.hasParent = *inst.ParentInstanceID, true
		}
		b.remember(e)
		return e.parent, e.hasParent
	case err == nil || errors.Is(err, instance.ErrNotFound):
		b.remember(&ancestryEntry{id: id, missing: true})
		return uuid.Nil, false
	default:
		return uuid.Nil, false
	}
}

func (b *SubtreeEventBridge) remember(e *ancestryEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if el, ok := b.ancestry[e.id]; ok {
		el.Value = e
		b.lru.MoveToFront(el)
		return
	}
	b.ancestry[e.id] = b.lru.PushFront(e)
	for b.lru.Len() > b.maxAncestry {
		oldest := b.lru.Back()
		b.lru.Remove(oldest)
		delete(b.ancestry, oldest.Value.(*ancestryEntry).id)
	}
}

func (b *SubtreeEventBridge) invalidate(id uuid.UUID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if el, ok := b.ancestry[id]; ok {
		b.lru.Remove(el)
		delete(b.ancestry, id)
	}
}

// ancestryLen reports the cache size (tests).
func (b *SubtreeEventBridge) ancestryLen() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lru.Len()
}
```

Implementation evidence: focused behavior/cache tests pass. The required visibility regression was also run against a temporary subscription-local allow/deny cache: it failed at the channel sink on a private child token. Removed the mutation; per-event authorization passes for both child and root, including recovery after denial and transient viewer errors. The negative-cache test now waits on a public sentinel before inserting the formerly absent row, avoiding scheduler-dependent results.

Design notes the implementer must keep (the tests pin them):
- One `SubscribeAll` per subscription and a single sequential goroutine: order is the bus order and each bus delivery is forwarded at most once (the root matching as both source and "ancestor of itself" is ONE membership decision).
- Authorization is per SOURCE for every event under the subscriber's ctx, including events from the root; any viewer error drops that event. Neither allows nor denies are cached.
- The slow-subscriber policy matches the bus (non-blocking send, warn, drop); P4-T5/P4-T6 recover via periodic refetch.

- [x] Step 5: Run, confirm PASS; then the package with the race detector, gofmt, vet.

Verification: focused tests pass, focused race tests pass with `-count=10`, existing HTTP stream/system-stream tests pass with `-race`, the entire non-race engine package passes, and formatting, vet and `git diff --check` are clean.

Self-review (required order):
1. Spec compliance: reviewed the bridge against this task's routing, per-source authorization, bounded ancestry cache and lifecycle contracts. Kept changes to the three specified Go files and this plan; no stream mode or extra option was added. P4-T5 explicitly owns production composition-root/router wiring and the WebSocket sink proof; that task remains unfinished.
2. Anti-green-theater: the real event bus feeds persisted parent links and real Engine authorization under the subscriber identity; assertions inspect unchanged events at the subscription channel sink. Public sentinel events make private/error token assertions deterministic. Cached authorization was mutation-tested red, then removed; the same subscription recovers after visibility and transient-error changes for root and child sources. Nonempty ancestry, identity and event inputs are load-bearing.
3. Code quality: compared neighboring graph-state and event-bus conventions. Fixed the scheduler-dependent negative-cache setup and a test helper capturing the parent test. Found an in-flight lookup could restore a stale negative cache entry after instance.created invalidation: added a deterministic red regression, then fenced cache insertion with an invalidation generation. This preserves bounded storage without serializing repository reads. Reran all verification commands after fixes, green.

```bash
go test ./domain/engine/ -run 'TestSubtreeBridge|TestNewSubtreeEventBridge' -count=1
go test -race ./domain/engine/ -run 'TestSubtreeBridge' -count=1
go test ./domain/engine/ -count=1
gofmt -w domain/engine/subtree_events.go domain/engine/subtree_events_test.go domain/engine/subtree_events_internal_test.go
gofmt -l domain/engine/subtree_events.go domain/engine/subtree_events_test.go domain/engine/subtree_events_internal_test.go
go vet ./domain/engine/
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	0.XXXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 6: Commit.

Committed the bridge, regression tests and plan evidence with the specified task title; production streaming wiring remains assigned to P4-T5, not implemented in this pass.

```bash
git add domain/engine/subtree_events.go domain/engine/subtree_events_test.go domain/engine/subtree_events_internal_test.go
git commit -m "feat(engine): SubtreeEventBridge — ancestry-routed, per-source authorized subtree subscriptions" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
