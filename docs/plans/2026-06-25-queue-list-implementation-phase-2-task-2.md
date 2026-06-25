# Phase 2 — Task 2: queue.Manager.QueuePositions (cap-aware simulated rank)

**Goal:** Add the `QueuePositions` read-only method to `queue.Manager` that returns a 1-based priority-aware position for every currently-queued instance, computed by simulating the scheduler's dispatch order (priority desc, FIFO tie-break, per-user-cap deferral) — the same rules `NextEligible` uses — so the position never diverges from real scheduling.

**Architecture:** `queue.Manager` is the single ordering authority. `QueuePositions` reads the full queued set via `QueueRepository.ListQueued` (Phase 2 Task 1), reads running counts (global + per owner) via the existing `CountRunning`/`CountRunningByOwner`, then simulates the dispatch walk honoring the per-user cap: an item whose owner is already at the per-user cap is deferred past later items whose owners still have headroom. It assigns 1-based positions in that simulated dispatch order. Read-only: no mutation, no `StartInstance`, no capacity change. The result is a live snapshot recomputed on each call.

**Tech Stack:** Go, standard library. Tests with `testing` + `testify`, using the existing `domain/queue/manager_test.go` mock patterns (`mockQueueRepo`, `mockInstanceRepo`).

---

### Task 2: QueuePositions

**Files:**
- Create: `domain/queue/positions.go`
- Create: `domain/queue/positions_test.go`

- [ ] Step 1: Write the failing test for the happy path (mixed priorities, distinct owners, no caps)

Create `domain/queue/positions_test.go`:

```go
package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueuePositions_HappyPath_PriorityThenFIFO(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	queued := []instance.Instance{
		{ID: uuid.New(), OwnerID: ownerA, State: instance.StateQueued, Priority: instance.PriorityLow, QueuedAt: &now},
		{ID: uuid.New(), OwnerID: ownerB, State: instance.StateQueued, Priority: instance.PriorityNormal, QueuedAt: ptrTime(now.Add(time.Second))},
		{ID: uuid.New(), OwnerID: ownerA, State: instance.StateQueued, Priority: instance.PriorityHigh, QueuedAt: ptrTime(now.Add(2 * time.Second))},
		{ID: uuid.New(), OwnerID: ownerB, State: instance.StateQueued, Priority: instance.PriorityHigh, QueuedAt: ptrTime(now.Add(3 * time.Second))},
	}
	repo := newMockQueueRepo()
	repo.queuedList = queued
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}}
	mgr := queue.NewManager(repo, newMockInstanceRepo(), cfg, nil)

	got, err := mgr.QueuePositions(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 4)
	// High priority first, FIFO within high.
	assert.Equal(t, 1, got[queued[2].ID], "high earlier QueuedAt -> position 1")
	assert.Equal(t, 2, got[queued[3].ID], "high later QueuedAt -> position 2")
	assert.Equal(t, 3, got[queued[1].ID], "normal -> position 3")
	assert.Equal(t, 4, got[queued[0].ID], "low -> position 4")
}

func TestQueuePositions_PerUserCapDefersOwnerAtCap(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	// ownerA already has 2 running (per-user cap = 2). ownerA's queued items
	// must be deferred past ownerB's queued items, even if ownerA's priority
	// is higher.
	queued := []instance.Instance{
		{ID: uuid.New(), OwnerID: ownerA, State: instance.StateQueued, Priority: instance.PriorityHigh, QueuedAt: &now},
		{ID: uuid.New(), OwnerID: ownerB, State: instance.StateQueued, Priority: instance.PriorityNormal, QueuedAt: ptrTime(now.Add(time.Second))},
		{ID: uuid.New(), OwnerID: ownerA, State: instance.StateQueued, Priority: instance.PriorityNormal, QueuedAt: ptrTime(now.Add(2 * time.Second))},
	}
	repo := newMockQueueRepo()
	repo.queuedList = queued
	repo.runningCount = 2
	repo.ownerCounts = map[uuid.UUID]int{ownerA: 2} // ownerA at cap
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 2}}
	mgr := queue.NewManager(repo, newMockInstanceRepo(), cfg, nil)

	got, err := mgr.QueuePositions(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 3)
	// ownerB's normal-priority item runs first (ownerA deferred).
	assert.Equal(t, 1, got[queued[1].ID], "ownerB (under cap) -> position 1 despite lower priority")
	// ownerA's high-priority item runs second (deferred past ownerB).
	assert.Equal(t, 2, got[queued[0].ID], "ownerA (at cap) high -> position 2 after deferral")
	// ownerA's normal-priority item runs third.
	assert.Equal(t, 3, got[queued[2].ID])
}

func TestQueuePositions_EmptyQueue(t *testing.T) {
	repo := newMockQueueRepo()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}}
	mgr := queue.NewManager(repo, newMockInstanceRepo(), cfg, nil)
	got, err := mgr.QueuePositions(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "empty queue -> empty map, not error")
}

func TestQueuePositions_SingleItem(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	repo := newMockQueueRepo()
	repo.queuedList = []instance.Instance{{ID: id, OwnerID: uuid.New(), State: instance.StateQueued, Priority: instance.PriorityNormal, QueuedAt: &now}}
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}}
	mgr := queue.NewManager(repo, newMockInstanceRepo(), cfg, nil)
	got, err := mgr.QueuePositions(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, got[id])
}

func TestQueuePositions_ZeroCaps_PurePriorityFIFO(t *testing.T) {
	ownerA := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	// All items owned by one user, per-user cap = 0 (unlimited) -> no deferral.
	queued := []instance.Instance{
		{ID: uuid.New(), OwnerID: ownerA, State: instance.StateQueued, Priority: instance.PriorityNormal, QueuedAt: &now},
		{ID: uuid.New(), OwnerID: ownerA, State: instance.StateQueued, Priority: instance.PriorityHigh, QueuedAt: ptrTime(now.Add(time.Second))},
		{ID: uuid.New(), OwnerID: ownerA, State: instance.StateQueued, Priority: instance.PriorityLow, QueuedAt: ptrTime(now.Add(2 * time.Second))},
	}
	repo := newMockQueueRepo()
	repo.queuedList = queued
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}}
	mgr := queue.NewManager(repo, newMockInstanceRepo(), cfg, nil)
	got, err := mgr.QueuePositions(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, got[queued[1].ID], "high -> 1")
	assert.Equal(t, 2, got[queued[0].ID], "normal -> 2")
	assert.Equal(t, 3, got[queued[2].ID], "low -> 3")
}

func TestQueuePositions_ListError_Propagates(t *testing.T) {
	repo := newMockQueueRepo()
	repo.listQueuedErr = errors.New("boom")
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}}
	mgr := queue.NewManager(repo, newMockInstanceRepo(), cfg, nil)
	_, err := mgr.QueuePositions(context.Background())
	require.Error(t, err, "underlying read error surfaces")
}

func TestQueuePositions_GlobalCapSaturated_NoDeferralByGlobalCap(t *testing.T) {
	// Global cap affects whether work STARTS, not the simulated dispatch ORDER
	// (the per-user cap is what reorders items). Position is still assigned 1..N
	// by the simulated order; a saturated global cap does not change the order,
	// only whether the head item would actually start. So positions are pure
	// priority-FIFO here.
	ownerA := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	queued := []instance.Instance{
		{ID: uuid.New(), OwnerID: ownerA, State: instance.StateQueued, Priority: instance.PriorityHigh, QueuedAt: &now},
		{ID: uuid.New(), OwnerID: ownerA, State: instance.StateQueued, Priority: instance.PriorityNormal, QueuedAt: ptrTime(now.Add(time.Second))},
	}
	repo := newMockQueueRepo()
	repo.queuedList = queued
	repo.runningCount = 100 // global cap saturated
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 5, PerUserCap: 0}}
	mgr := queue.NewManager(repo, newMockInstanceRepo(), cfg, nil)
	got, err := mgr.QueuePositions(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, got[queued[0].ID])
	assert.Equal(t, 2, got[queued[1].ID])
}

func ptrTime(t time.Time) *time.Time { return &t }
```

- [ ] Step 2: Run it, confirm it fails

```bash
go test ./domain/queue/ -run TestQueuePositions
```

Expected: build failure — `undefined: queue.Manager.QueuePositions`.

- [ ] Step 3: Implement `QueuePositions`

Create `domain/queue/positions.go`:

```go
package queue

import (
	"context"
	"sort"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// QueuePositions returns a 1-based priority-aware position for every
// currently-queued instance, computed by simulating the scheduler's dispatch
// order: priority desc (high → normal → low), FIFO within a priority, and
// per-user-cap deferral (an item whose owner is already at the per-user cap
// is deferred past later items whose owners still have headroom — mirroring
// what the manager does at dispatch time). Read-only: it does not mutate
// queue state, start work, or change capacity.
//
// The result is a live snapshot recomputed on each call. An empty queue
// returns an empty map. The global cap does NOT reorder items (it gates
// whether the head would start, not the relative order); only the per-user
// cap reorders.
func (m *Manager) QueuePositions(ctx context.Context) (map[uuid.UUID]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	queued, err := m.queue.ListQueued(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int, len(queued))
	if len(queued) == 0 {
		return out, nil
	}

	// Sort by priority (high → normal → low), then QueuedAt FIFO.
	sort.SliceStable(queued, func(i, j int) bool {
		return queueLess(queued[i], queued[j])
	})

	perUserCap := m.config.Queue.PerUserCap
	// runningByOwner counts running instances per owner, used for the per-user
	// cap deferral. Lazily computed per owner on first encounter.
	runningByOwner := map[uuid.UUID]int{}

	pos := 1
	// deferred items (owner at cap) are collected and appended after the
	// non-deferred items, preserving their relative order.
	var deferred []instance.Instance
	for i := range queued {
		item := &queued[i]
		if perUserCap > 0 {
			running, ok := runningByOwner[item.OwnerID]
			if !ok {
				count, err := m.queue.CountRunningByOwner(ctx, item.OwnerID)
				if err != nil {
					return nil, err
				}
				running = count
				runningByOwner[item.OwnerID] = running
			}
			if running >= perUserCap {
				deferred = append(deferred, *item)
				continue
			}
			runningByOwner[item.OwnerID] = running + 1
		}
		out[item.ID] = pos
		pos++
	}

	// Assign positions to deferred items in their preserved order. They do not
	// consume further cap headroom in this snapshot (the simulation is a single
	// pass; a deferred item does not "start" during it).
	for i := range deferred {
		out[deferred[i].ID] = pos
		pos++
	}

	return out, nil
}

// queueLess reports whether a should sort before b by the scheduler's
// ordering: priority desc (high → normal → low), then QueuedAt FIFO.
func queueLess(a, b instance.Instance) bool {
	pa := priorityRank(a.Priority)
	pb := priorityRank(b.Priority)
	if pa != pb {
		return pa < pb
	}
	// FIFO by QueuedAt (nil = epoch, so a nil QueuedAt sorts first).
	var ta, tb int64
	if a.QueuedAt != nil {
		ta = a.QueuedAt.UnixNano()
	}
	if b.QueuedAt != nil {
		tb = b.QueuedAt.UnixNano()
	}
	return ta < tb
}

func priorityRank(p instance.Priority) int {
	switch p {
	case instance.PriorityHigh:
		return 1
	case instance.PriorityNormal:
		return 2
	case instance.PriorityLow:
		return 3
	}
	return 4
}
```

- [ ] Step 4: Run the tests, confirm they pass

```bash
go test ./domain/queue/ -run TestQueuePositions
```

Expected:

```
ok  	github.com/arbitron/arbitron/domain/queue	0.00s
```

- [ ] Step 5: Confirm the whole module builds

```bash
go build ./... && go test ./domain/queue/
```

Expected: no build errors; all queue tests pass.

- [ ] Step 6: Commit

```bash
git add domain/queue/positions.go domain/queue/positions_test.go
git commit -m "feat(queue): add Manager.QueuePositions cap-aware simulated rank"
```