package queue

import (
	"context"
	"sort"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// QueuePositions returns a 1-based priority-aware position for every
// currently-queued instance, computed by simulating the scheduler's dispatch
// order: priority desc (high -> normal -> low), FIFO within a priority, and
// per-user-cap deferral (an item whose owner is already at the per-user cap
// is deferred past later items whose owners still have headroom -- mirroring
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

	src, err := m.queue.ListQueued(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int, len(src))
	if len(src) == 0 {
		return out, nil
	}

	// Copy before sorting so we never mutate the caller's slice (the
	// repository may return its own backing array).
	queued := make([]instance.Instance, len(src))
	copy(queued, src)

	// Sort by priority (high -> normal -> low), then QueuedAt FIFO.
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
// ordering: priority desc (high -> normal -> low), then QueuedAt FIFO.
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
