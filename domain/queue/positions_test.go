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
