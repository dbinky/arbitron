package engine

import (
	"container/list"
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// EventTypeInstanceCreated is published by Engine.createAndEnqueue right after
// a run row is created. The subtree bridge does not depend on it: the queue
// evaluator can start a committed row before the announcement is published.
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
// created, so only rows that were found are cached: a "missing" result is
// never remembered, because the row may be committed (and the run started by
// the queue evaluator) before instance.created is published. A missing/purged
// ancestor, a cycle, or a storage error degrades to "not in subtree": scope is
// never broadened by guessing.
type SubtreeEventBridge struct {
	bus       EventBus
	viewer    InstanceViewer
	instances instance.InstanceRepository
	groups    instance.GroupRunRepository

	mu          sync.Mutex
	maxAncestry int
	ancestry    map[uuid.UUID]*list.Element // values are *ancestryEntry
	lru         *list.List                  // front = most recently used
}

// SetGroupRuns supplies persisted task provenance for parent-scoped conflict
// events. Call during composition, before subscribing. Without it conflict
// payloads are withheld because authorizing the parent alone is insufficient.
func (b *SubtreeEventBridge) SetGroupRuns(groups instance.GroupRunRepository) {
	b.groups = groups
}

type ancestryEntry struct {
	id        uuid.UUID
	parent    uuid.UUID
	hasParent bool
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
	if !b.InSubtree(ctx, rootID, sev.InstanceID) {
		return
	}
	if !b.EventVisible(ctx, sev.InstanceID, sev.Event) {
		return
	}

	select {
	case out <- sev:
	default:
		slog.Warn("subtree event bridge dropped event for slow subscriber",
			"rootInstanceID", rootID, "instanceID", sev.InstanceID, "eventType", sev.Event.Type)
	}
}

// EventVisible reauthorizes a source and any contributors to its payload.
// History hydration uses this same boundary despite not passing through the
// bus. Neither positive nor negative authorization decisions are cached.
func (b *SubtreeEventBridge) EventVisible(ctx context.Context, sourceID uuid.UUID, evt Event) bool {
	source, err := b.viewer.GetInstance(ctx, sourceID)
	if err != nil || source == nil || source.ID != sourceID {
		return false
	}
	sev := ScopedEvent{InstanceID: sourceID, Event: evt}
	if evt.Type == EventTypeGroupConflict {
		return b.conflictTaskVisible(ctx, sev)
	}
	if !strings.HasPrefix(evt.Type, "interactive.") {
		return true
	}
	data, _ := evt.Data.(map[string]any)
	_, hasRun := data["groupRunId"]
	if data["sessionKind"] == ConversationKindGroupResolution || hasRun {
		return b.resolverContributorsVisible(ctx, sev)
	}
	// Legacy/malformed parent-scoped resolver events must fail closed. No
	// durable session exists yet during the autonomous turn, so inspect the
	// source's node rather than relying on a post-stream session lookup.
	if source.CurrentNodeID != nil {
		node, err := FindNodeByID(source.WorkflowSnapshot, *source.CurrentNodeID)
		if err == nil && node.Type == workflow.NodeTypeParallelGroup {
			return false
		}
	}
	return true
}

func (b *SubtreeEventBridge) resolverContributorsVisible(ctx context.Context, sev ScopedEvent) bool {
	if b.groups == nil {
		return false
	}
	data, _ := sev.Event.Data.(map[string]any)
	rawID, _ := data["groupRunId"].(string)
	runID, err := uuid.Parse(rawID)
	if err != nil || runID == uuid.Nil {
		return false
	}
	nodeID, _ := data["nodeId"].(string)
	run, err := b.groups.Get(ctx, runID)
	if err != nil || run == nil || run.ID != runID || run.ParentInstanceID != sev.InstanceID || nodeID == "" || run.NodeID != nodeID || len(run.Tasks) == 0 {
		return false
	}
	// Resolver text may describe the entire staged merge, not just the task
	// which conflicted. All contributors must be visible, as for panel text.
	for _, task := range run.Tasks {
		if task.ChildInstanceID == uuid.Nil {
			return false
		}
		child, err := b.viewer.GetInstance(ctx, task.ChildInstanceID)
		if err != nil || child == nil || child.ID != task.ChildInstanceID {
			return false
		}
	}
	return true
}

// Conflict paths belong to the roster task, even though the event is emitted
// on its parent. Neither provenance nor task authorization is cached: storage
// failures and missing records withhold this event, and the next event retries.
func (b *SubtreeEventBridge) conflictTaskVisible(ctx context.Context, sev ScopedEvent) bool {
	if b.groups == nil {
		return false
	}
	data, ok := sev.Event.Data.(map[string]any)
	if !ok {
		return false
	}
	rawID, _ := data["groupRunId"].(string)
	runID, err := uuid.Parse(rawID)
	if err != nil || runID == uuid.Nil {
		return false
	}
	nodeID, _ := data["nodeId"].(string)
	taskKey, _ := data["taskKey"].(string)
	if nodeID == "" || taskKey == "" {
		return false
	}
	run, err := b.groups.Get(ctx, runID)
	if err != nil || run == nil || run.ID != runID || run.ParentInstanceID != sev.InstanceID || run.NodeID != nodeID {
		return false
	}
	task, ok := run.TaskByKey(taskKey)
	if !ok || task.ChildInstanceID == uuid.Nil {
		return false
	}
	child, err := b.viewer.GetInstance(ctx, task.ChildInstanceID)
	return err == nil && child != nil && child.ID == task.ChildInstanceID
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
// a storage error. Missing rows and storage errors are never cached.
func (b *SubtreeEventBridge) parentOf(ctx context.Context, id uuid.UUID) (uuid.UUID, bool) {
	b.mu.Lock()
	if el, ok := b.ancestry[id]; ok {
		b.lru.MoveToFront(el)
		e := el.Value.(*ancestryEntry)
		b.mu.Unlock()
		if !e.hasParent {
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

// ancestryLen reports the cache size (tests).
func (b *SubtreeEventBridge) ancestryLen() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lru.Len()
}
