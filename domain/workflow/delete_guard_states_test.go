package workflow_test

// Area 14 (paired): Delete-guard + Instance state vocabulary
//
// Verifies that every instance.State constant is correctly classified as either
// blocking (non-terminal: queued/running/waiting/paused) or non-blocking
// (terminal: completed/failed/cancelled) by the admin Delete guard. Any
// misclassification — a terminal state that blocks Delete, or a non-terminal
// state that permits it — is a correctness bug at the seam between the workflow
// and instance domains.

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeleteGuard_StateVocabulary verifies the complete instance.State
// vocabulary against the delete guard:
//   - Non-terminal states (queued, running, waiting, paused) block Delete with
//     ErrHasRunningInstances.
//   - Terminal states (completed, failed, cancelled) allow Delete to proceed.
//
// This test is the authoritative cross-domain contract between instance.State
// constants and the active-count vocabulary used by CountActiveByWorkflowID and
// HasActiveRun — both of which use state IN ('queued','running','waiting','paused').
func TestDeleteGuard_StateVocabulary(t *testing.T) {
	blocking := []instance.State{
		instance.StateQueued,
		instance.StateRunning,
		instance.StateWaiting,
		instance.StatePaused,
	}
	nonBlocking := []instance.State{
		instance.StateCompleted,
		instance.StateFailed,
		instance.StateCancelled,
	}

	for _, state := range blocking {
		state := state
		t.Run("blocks_"+string(state), func(t *testing.T) {
			svc, repo, reg, _, counter := deleteSvc(t)
			admin := uuid.New()
			idA := seedPrivateRegistered(t, repo, reg, admin, "alpha")
			counter.seed(idA, string(state))

			err := svc.Delete(ctxWith(admin, true), "alpha")
			assert.ErrorIs(t, err, workflow.ErrHasRunningInstances,
				"state %q must block Delete with ErrHasRunningInstances", state)
		})
	}

	for _, state := range nonBlocking {
		state := state
		t.Run("allows_"+string(state), func(t *testing.T) {
			svc, repo, reg, _, counter := deleteSvc(t)
			admin := uuid.New()
			idA := seedPrivateRegistered(t, repo, reg, admin, "alpha")
			counter.seed(idA, string(state))

			err := svc.Delete(ctxWith(admin, true), "alpha")
			require.NoError(t, err, "state %q must not block Delete", state)

			_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
			assert.False(t, folderStill, "folder removed for terminal state %q", state)

			_, regErr := reg.ResolveID(context.Background(), "alpha")
			assert.ErrorIs(t, regErr, workflow.ErrNotFound,
				"registry row removed for terminal state %q", state)
		})
	}
}

// TestDeleteGuard_MultipleInstances_AnyActiveBlocks verifies that even one
// active instance among many terminal ones is sufficient to block Delete.
func TestDeleteGuard_MultipleInstances_AnyActiveBlocks(t *testing.T) {
	svc, repo, reg, _, counter := deleteSvc(t)
	admin := uuid.New()
	idA := seedPrivateRegistered(t, repo, reg, admin, "alpha")

	// Two terminal instances + one active.
	counter.seed(idA, "completed")
	counter.seed(idA, "failed")
	counter.seed(idA, "running") // the one active instance

	err := svc.Delete(ctxWith(admin, true), "alpha")
	assert.ErrorIs(t, err, workflow.ErrHasRunningInstances,
		"one active instance among terminal ones must still block Delete")

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.True(t, folderStill, "folder preserved when any instance is active")
}

// TestDeleteGuard_NoInstances_DeleteProceeds verifies the zero-instance case:
// a workflow with no instance history (completely fresh) is deletable.
func TestDeleteGuard_NoInstances_DeleteProceeds(t *testing.T) {
	svc, repo, reg, _, _ := deleteSvc(t)
	admin := uuid.New()
	seedPrivateRegistered(t, repo, reg, admin, "alpha")
	// No instances seeded — counter returns 0.

	require.NoError(t, svc.Delete(ctxWith(admin, true), "alpha"),
		"a workflow with no instances must be deletable")

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.False(t, folderStill, "folder removed for an instance-free workflow")
}
