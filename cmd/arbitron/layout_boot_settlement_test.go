package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func relocationIntentPath(t *testing.T, e *layoutE2EEnv, id string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(e.workspaceDir, ".arbitron-layout-journal", "relocations", id+".*.*.json"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	return matches[0]
}

// One destination's unresolvable or damaged recovery state must not take the
// whole instance down: the real boot path serves the healthy workflow, while
// the affected endpoints stay blocked.
func TestLayoutE2E_BootSurvivesConfinedSettlementFailures(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, close := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "healthy", queryE2EWorkflow("healthy", "Healthy graph")))
	require.NoError(t, ds.workflowSvc.Create(ctx, "orphan-src", queryE2EWorkflow("orphan-src", "Relocating")))
	require.NoError(t, ds.workflowSvc.Create(ctx, "damaged-src", queryE2EWorkflow("damaged-src", "Relocating")))
	owner := workflow.Private(localDefaultUser)
	// Valid intent whose workflow the registry has never heard of: unresolvable.
	unresolvable, err := ds.layout.journal.BeginRelocation(ctx, owner, "orphan-src", owner, "orphan-dst", uuid.New())
	require.NoError(t, err)
	malformed, err := ds.layout.journal.BeginRelocation(ctx, owner, "damaged-src", owner, "damaged-dst", uuid.New())
	require.NoError(t, err)
	close()
	damagedPath := relocationIntentPath(t, e, malformed.ID)
	require.NoError(t, os.WriteFile(damagedPath, []byte(`{"ID":"`), 0600))

	ds, _ = e.boot(t)
	ctx = adminCtxForLocalUser(t, ds)
	before, err := ds.workflowSvc.Load(ctx, "healthy")
	require.NoError(t, err)
	require.NoError(t, ds.workflowSvc.Update(ctx, "healthy", queryE2EWorkflow("healthy", "Saved after boot")))
	after, err := ds.workflowSvc.Load(ctx, "healthy")
	require.NoError(t, err)
	require.NotEqual(t, before.SavedRevision(), after.SavedRevision(), "the save landed")

	for _, name := range []string{"orphan-src", "orphan-dst", "damaged-src", "damaged-dst"} {
		require.ErrorIs(t, ds.layout.commits.Settle(ctx, owner, name), workflow.ErrMutationPending, name)
	}
	// The existing endpoints refuse reads and saves through the service.
	for _, name := range []string{"orphan-src", "damaged-src"} {
		_, err := ds.workflowSvc.Load(ctx, name)
		require.ErrorIs(t, err, workflow.ErrMutationPending, name)
		require.ErrorIs(t, ds.workflowSvc.Update(ctx, name, queryE2EWorkflow(name, "Must not land")), workflow.ErrMutationPending, name)
	}
	require.FileExists(t, damagedPath, "damaged intent is left for operator repair")
	require.FileExists(t, relocationIntentPath(t, e, unresolvable.ID))
}

// A relocation record that cannot be scoped to endpoints is systemic: boot
// still fails closed rather than serving destinations it might protect.
func TestLayoutE2E_UnscopedRelocationRecordAbortsBoot(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	_, close := e.boot(t)
	close()
	dir := filepath.Join(e.workspaceDir, ".arbitron-layout-journal", "relocations")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, uuid.NewString()+".json"), []byte("{}"), 0600))
	ds, err := initDomain(context.Background(), e.configPath)
	require.ErrorContains(t, err, "settling workflow mutations before boot")
	require.ErrorContains(t, err, "unscoped relocation record")
	require.Nil(t, ds)
}

type scriptedCommitted struct {
	block bool
	err   error
}

func (s scriptedCommitted) CommittedRequests(ctx context.Context) ([]workflow.ArrangementRequest, error) {
	if s.block {
		<-ctx.Done()
		return nil, errors.Join(&workflow.DestinationError{Targets: []workflow.MutationTarget{{Scope: workflow.Shared(), Name: "slow"}}, Err: ctx.Err()}, ctx.Err())
	}
	return nil, s.err
}

func TestSettleBeforeBoot_ClassifiesAtSource(t *testing.T) {
	logger := layoutE2EDiscardLogger()
	local := &workflow.DestinationError{Targets: []workflow.MutationTarget{{Scope: workflow.Shared(), Name: "one"}}, Err: errors.New("stuck")}
	systemic := errors.New("journal directory unreadable")

	require.NoError(t, settleBeforeBoot(scriptedCommitted{}, time.Second, logger))
	require.NoError(t, settleBeforeBoot(scriptedCommitted{err: errors.Join(local, &workflow.DestinationError{Record: "r", Err: errors.New("damaged")})}, time.Second, logger))
	// The deadline is not a safety boundary: every destination stays guarded.
	require.NoError(t, settleBeforeBoot(scriptedCommitted{block: true}, 10*time.Millisecond, logger))

	err := settleBeforeBoot(scriptedCommitted{err: errors.Join(local, systemic)}, time.Second, logger)
	require.ErrorIs(t, err, systemic)
	require.NotErrorIs(t, err, local, "confined failures are tolerated, not reported as fatal")
	// A confined failure nested beside a systemic one never masks it.
	err = settleBeforeBoot(scriptedCommitted{err: errors.Join(errors.Join(local, systemic))}, time.Second, logger)
	require.ErrorIs(t, err, systemic)
	// A context deadline that did not come from the boot timeout stays fatal.
	require.ErrorIs(t, settleBeforeBoot(scriptedCommitted{err: context.DeadlineExceeded}, time.Second, logger), context.DeadlineExceeded)
}
