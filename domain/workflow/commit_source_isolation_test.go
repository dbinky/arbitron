package workflow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func isolationService(t *testing.T, f *commitFixture) *workflow.Service {
	t.Helper()
	st, err := workflow.NewRevisionStamper(f.repo, workflow.StamperDeps{Journal: f.journal, Requester: f.requester, Settler: f.source})
	require.NoError(t, err)
	svc := workflow.NewWorkflowService(f.repo, f.pubs)
	svc.SetRegistry(f.registry)
	svc.SetRevisionStamper(st)
	svc.SetMutationCoordinator(f.repo)
	return svc
}

// truncateIntent damages a real intent's body while keeping its keyed filename.
func truncateIntent(t *testing.T, base, id string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(base, ".arbitron-layout-journal", "relocations", id+".*.*.json"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	data, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(matches[0], data[:len(data)/2], 0600))
	return matches[0]
}

// systemicErrors returns every leaf of err that is not confined to a destination.
func systemicErrors(err error) []error {
	if err == nil {
		return nil
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range multi.Unwrap() {
			out = append(out, systemicErrors(e)...)
		}
		return out
	}
	var local *workflow.DestinationError
	if errors.As(err, &local) {
		return nil
	}
	return []error{err}
}

// A malformed intent naming other destinations never affects a healthy one:
// its body is not even read on the healthy destination's load or save path.
func TestCommitSource_UnrelatedMalformedIntentIsIsolated(t *testing.T) {
	f := newCommitFixture(t, true)
	user := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ctx := ctxWith(user, true)
	svc := isolationService(t, f)
	require.NoError(t, svc.Create(ctx, "healthy", stampWorkflow()))
	owner := workflow.Private(user)
	intent, err := f.journal.BeginRelocation(ctx, owner, "ghost-a", owner, "ghost-b", uuid.New())
	require.NoError(t, err)
	damaged := truncateIntent(t, f.base, intent.ID)

	for i := 0; i < 2; i++ {
		_, err = svc.Load(ctx, "healthy")
		require.NoError(t, err)
	}
	require.NoError(t, svc.Update(ctx, "healthy", stampWorkflow()))
	_, err = svc.Load(ctx, "healthy")
	require.NoError(t, err)

	// Global recovery reports the damage as confined, never systemic, and the
	// evidence stays in place: quarantine is operator repair, not settlement.
	_, err = f.source.CommittedRequests(context.Background())
	require.Error(t, err)
	require.Empty(t, systemicErrors(err))
	var local *workflow.DestinationError
	require.ErrorAs(t, err, &local)
	require.Equal(t, intent.ID, local.Record)
	require.FileExists(t, damaged)
	require.ErrorIs(t, f.source.Settle(ctx, owner, "ghost-a"), workflow.ErrMutationPending)
}

// A real in-progress relocation whose body is damaged keeps BOTH endpoints
// blocked for reads and saves, because the filename alone names them.
func TestCommitSource_DamagedInProgressRelocationBlocksBothEndpoints(t *testing.T) {
	f := newCommitFixture(t, true)
	user := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ctx := ctxWith(user, true)
	svc := isolationService(t, f)
	require.NoError(t, svc.Create(ctx, "source", stampWorkflow()))
	require.NoError(t, svc.Create(ctx, "bystander", stampWorkflow()))
	src, err := svc.Load(ctx, "source")
	require.NoError(t, err)
	owner := workflow.Private(user)
	intent, err := f.journal.BeginRelocation(ctx, owner, "source", owner, "target", src.ID)
	require.NoError(t, err)
	require.NoError(t, f.repo.CopyTree(ctx, owner, "source", owner, "target", false))
	damaged := truncateIntent(t, f.base, intent.ID)
	srcPath := filepath.Join(f.base, "users", user.String(), "source", "workflow.json")
	dstPath := filepath.Join(f.base, "users", user.String(), "target", "workflow.json")
	srcBytes, err := os.ReadFile(srcPath)
	require.NoError(t, err)
	dstBytes, err := os.ReadFile(dstPath)
	require.NoError(t, err)

	for _, name := range []string{"source", "target"} {
		_, err = svc.Load(ctx, name)
		require.ErrorIs(t, err, workflow.ErrMutationPending, "read %s", name)
		require.ErrorIs(t, svc.Update(ctx, name, stampWorkflow()), workflow.ErrMutationPending, "save %s", name)
		require.ErrorIs(t, f.source.Settle(ctx, owner, name), workflow.ErrMutationPending)
	}
	got, err := os.ReadFile(srcPath)
	require.NoError(t, err)
	require.Equal(t, srcBytes, got, "source must not be overwritten")
	got, err = os.ReadFile(dstPath)
	require.NoError(t, err)
	require.Equal(t, dstBytes, got, "target must not be overwritten")

	// Recovery cannot settle it either; the endpoints stay blocked afterwards.
	_, err = f.source.CommittedRequests(context.Background())
	require.Error(t, err)
	require.Empty(t, systemicErrors(err))
	require.FileExists(t, damaged)
	_, err = svc.Load(ctx, "source")
	require.ErrorIs(t, err, workflow.ErrMutationPending)

	// Unrelated destinations keep serving throughout.
	_, err = svc.Load(ctx, "bystander")
	require.NoError(t, err)
	require.NoError(t, svc.Update(ctx, "bystander", stampWorkflow()))
}
