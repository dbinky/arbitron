package provider

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Delay only the repository I/O return, after reading the actual migrated row.
// This reproduces a terminal release racing an already-started cache miss.
type pendingPinRead struct {
	id     uuid.UUID
	resume chan struct{}
}

type delayedPinRepo struct {
	instance.InstanceRepository
	reads chan pendingPinRead
}

func (r *delayedPinRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, err := r.InstanceRepository.Get(ctx, id)
	read := pendingPinRead{id: id, resume: make(chan struct{})}
	select {
	case r.reads <- read:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-read.resume:
		return inst, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type pinReadResult struct {
	set domprovider.PinSet
	err error
}

func startPinRead(ctx context.Context, lookup *PinLookup, id uuid.UUID) <-chan pinReadResult {
	result := make(chan pinReadResult, 1)
	go func() {
		set, err := lookup.Lookup(ctx, id)
		result <- pinReadResult{set: set, err: err}
	}()
	return result
}

func receivePinRead[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pin read")
		var zero T
		return zero
	}
}

func TestPinLookupReleaseDuringRead(t *testing.T) {
	for _, tc := range []struct {
		name                string
		reloadBeforeOldRead bool
	}{
		{name: "reload after old reads"},
		{name: "reload before old reads", reloadBeforeOldRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			db, err := sqlite.Open(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			require.NoError(t, sqlite.RunMigrations(db))
			repo := sqlite.NewInstanceRepo(db)
			owner, err := sqlite.NewUserRepo(db).GetDefaultUser(ctx)
			require.NoError(t, err)
			id, other := uuid.New(), uuid.New()
			for _, runID := range []uuid.UUID{id, other} {
				require.NoError(t, repo.Create(ctx, &instance.Instance{
					ID: runID, OwnerID: owner, WorkflowName: "pin-read",
					State: instance.StateRunning, Priority: instance.PriorityNormal,
					WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
					ModelPinsVersion: instance.ModelPinsVersionSnapshot,
					ModelPins:        map[string]string{"worker": "deepseek/deepseek-v4-flash"},
					ModelPinRoutes:   map[string]string{"worker": "bedrock"},
				}))
			}
			delayed := &delayedPinRepo{InstanceRepository: repo, reads: make(chan pendingPinRead)}
			lookup := NewPinLookup(delayed)
			oldResults := []<-chan pinReadResult{startPinRead(ctx, lookup, id)}
			oldReads := []pendingPinRead{receivePinRead(t, delayed.reads)}
			oldResults = append(oldResults, startPinRead(ctx, lookup, id))
			oldReads = append(oldReads, receivePinRead(t, delayed.reads))
			otherResult := startPinRead(ctx, lookup, other)
			otherRead := receivePinRead(t, delayed.reads)
			require.Equal(t, other, otherRead.id)

			require.NoError(t, repo.SetCompleted(ctx, id))
			lookup.Release(id)
			lookup.Release(id) // repeated terminal notifications are harmless

			reload := func() {
				result := startPinRead(ctx, lookup, id)
				read := receivePinRead(t, delayed.reads)
				require.Equal(t, id, read.id, "released lookup must reach the persisted row")
				close(read.resume)
				got := receivePinRead(t, result)
				require.NoError(t, got.err)
				assert.Equal(t, domprovider.PinSet{Version: 1, Models: map[string]string{"worker": "deepseek/deepseek-v4-flash"}, OpaqueRoutes: map[string]string{"worker": "bedrock"}}, got.set)
				require.True(t, lookup.Cached(id), "a later retry may cache immutable persisted pins again")
			}
			if tc.reloadBeforeOldRead {
				reload()
			}
			for i, read := range oldReads {
				require.Equal(t, id, read.id)
				close(read.resume)
				got := receivePinRead(t, oldResults[i])
				require.NoError(t, got.err)
				assert.Equal(t, "deepseek/deepseek-v4-flash", got.set.Models["worker"])
				assert.Equal(t, "bedrock", got.set.OpaqueRoutes["worker"])
				got.set.Models["worker"] = "mutated"
				got.set.OpaqueRoutes["worker"] = "mutated"
				require.Equal(t, tc.reloadBeforeOldRead, lookup.Cached(id), "pre-release I/O must not repopulate a released cache or remove a later reload")
			}
			close(otherRead.resume)
			require.NoError(t, receivePinRead(t, otherResult).err)
			assert.True(t, lookup.Cached(other), "releasing one run must not invalidate unrelated reads")
			if !tc.reloadBeforeOldRead {
				reload()
			}
			got, err := lookup.Lookup(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, "deepseek/deepseek-v4-flash", got.Models["worker"])
			assert.Equal(t, "bedrock", got.OpaqueRoutes["worker"])
		})
	}
}
