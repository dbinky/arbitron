package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// ServerWorkspaceDeps are ServerWorkspaceResource's collaborators (all required).
type ServerWorkspaceDeps struct {
	Leases      ResourceLeaseStore // the ONE shared lease repo (P3 T3; built once in buildWorkspaceMobility)
	Commits     ResourceCommitter  // Cycle 3a ResourceCommitRepo; never an independent Homes.SetCheckpoint
	Homes       HomeStore          // copy-tracking homes (P3 T11) over WorkspaceHomeRepo (P3 T3)
	Checkpoints CheckpointStore    // P3 T2
	Ledger      Ledger             // P1
}

// ServerWorkspaceResource is the engine.WorkspaceResource for server-side group
// mutations (design phase 3 §2 "Group workspaces", RR-11). A mutation holds the
// run's single writer lease for its whole filesystem operation: Acquire claims
// it, brings dir to the committed checkpoint and makes the built-in runner the
// home; Commit stages dir and commits it under the exact claim; Release frees
// an uncommitted claim.
type ServerWorkspaceResource struct{ d ServerWorkspaceDeps }

var _ engine.WorkspaceResource = (*ServerWorkspaceResource)(nil)

// NewServerWorkspaceResource validates deps.
func NewServerWorkspaceResource(d ServerWorkspaceDeps) (*ServerWorkspaceResource, error) {
	var missing []string
	if d.Leases == nil {
		missing = append(missing, "Leases")
	}
	if d.Commits == nil {
		missing = append(missing, "Commits")
	}
	if d.Homes == nil {
		missing = append(missing, "Homes")
	}
	if d.Checkpoints == nil {
		missing = append(missing, "Checkpoints")
	}
	if d.Ledger == nil {
		missing = append(missing, "Ledger")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("runner: NewServerWorkspaceResource requires %s", strings.Join(missing, ", "))
	}
	return &ServerWorkspaceResource{d: d}, nil
}

// GroupHolder is the durable holder identity of a server-side group mutation:
// derived from the run and the mutation kind, never a random process-local
// label. The claim row also records the server boot ID (ResourceLeaseRepo), so
// P3 T10 recovery supersedes it after a restart with SupersedeServerRestart.
func GroupHolder(runID uuid.UUID, reason string) string {
	return "group:" + strings.ReplaceAll(reason, " ", "-") + "/run:" + runID.String()
}

// Acquire claims run:<runID> for this mutation and makes dir the committed
// content. It refuses with engine.ErrWorkspaceBusy while a workspace step of
// the run is active or holds the lease, and fails closed (home untouched) when
// a remote home has no committed checkpoint to restore.
func (s *ServerWorkspaceResource) Acquire(ctx context.Context, runID uuid.UUID, dir, reason string) (engine.ResourceFenceRef, error) {
	if runID == uuid.Nil || dir == "" {
		return engine.ResourceFenceRef{}, fmt.Errorf("runner: group workspace acquire needs a run and a directory")
	}
	jobs, err := s.d.Ledger.ListByRun(ctx, runID)
	if err != nil {
		return engine.ResourceFenceRef{}, fmt.Errorf("runner: list jobs of run %s: %w", runID, err)
	}
	for _, j := range jobs {
		if j.State.HoldsSlot() && j.Op != OpChat && j.Op != OpStream {
			return engine.ResourceFenceRef{}, fmt.Errorf("%w: job %s (%s) on %q", engine.ErrWorkspaceBusy, j.ID, j.NodeID, j.RunnerID)
		}
	}
	holder := GroupHolder(runID, reason)
	claim, err := s.d.Leases.Claim(ctx, runKey(runID), holder, BuiltInRunnerID)
	if errors.Is(err, ErrResourceBusy) {
		return engine.ResourceFenceRef{}, fmt.Errorf("%w: %w", engine.ErrWorkspaceBusy, err)
	}
	if err != nil {
		return engine.ResourceFenceRef{}, fmt.Errorf("runner: claim workspace of run %s: %w", runID, err)
	}
	if err := s.takeHome(ctx, runID, dir, reason, holder); err != nil {
		if rerr := s.d.Leases.Release(context.WithoutCancel(ctx), claim); rerr != nil {
			slog.Error("runner: releasing a failed group acquire's claim", "run", runID, "error", rerr)
		}
		return engine.ResourceFenceRef{}, err
	}
	return claim, nil
}

// takeHome runs under the claim: no job can claim the run meanwhile.
func (s *ServerWorkspaceResource) takeHome(ctx context.Context, runID uuid.UUID, dir, reason, holder string) error {
	home, err := s.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return fmt.Errorf("runner: read workspace home of run %s: %w", runID, err)
	}
	base, ok, err := s.d.Checkpoints.Latest(ctx, runID)
	if err != nil {
		return fmt.Errorf("runner: read committed checkpoint of run %s: %w", runID, err)
	}
	switch {
	case !isServerLocal(home.RunnerID):
		if !ok {
			return fmt.Errorf("%w: run %s is homed on %q with no committed checkpoint to restore", ErrCheckpointNotFound, runID, home.RunnerID)
		}
		cause := HomeMoveCause{OperationID: holder, FromGeneration: home.Generation, ToGeneration: home.Generation + 1}
		if _, err := s.d.Homes.HandOff(ctx, runID, BuiltInRunnerID, "server: "+reason, 0, home.Generation, cause); err != nil {
			return fmt.Errorf("runner: move workspace of run %s to the server: %w", runID, err)
		}
		if err := s.d.Checkpoints.Restore(ctx, base, dir); err != nil {
			return fmt.Errorf("runner: restore committed checkpoint of run %s: %w", runID, err)
		}
	case home.RunnerID == ServerHome:
		if _, err := s.d.Homes.AcquireResource(ctx, runID, BuiltInRunnerID, home.Generation); err != nil {
			return fmt.Errorf("runner: acquire workspace of run %s: %w", runID, err)
		}
		if ok && (home.Dirty || home.CheckpointVersion != base.Version) {
			if err := s.restoreClean(ctx, runID, base, dir); err != nil {
				return err
			}
		}
	default: // built-in home
		if ok && (home.Dirty || home.CheckpointVersion != base.Version) {
			if err := s.restoreClean(ctx, runID, base, dir); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *ServerWorkspaceResource) restoreClean(ctx context.Context, runID uuid.UUID, base CheckpointRef, dir string) error {
	if err := s.d.Checkpoints.Restore(ctx, base, dir); err != nil {
		return fmt.Errorf("runner: restore committed checkpoint of run %s: %w", runID, err)
	}
	if err := s.d.Homes.MarkDirty(ctx, runID, false); err != nil {
		return fmt.Errorf("runner: mark workspace of run %s clean: %w", runID, err)
	}
	return nil
}

// Commit publishes dir as the run's next committed checkpoint under claim, in
// the committer's single transaction (which also releases the claim). A failed
// commit leaves the claim active for the caller's deferred Release.
func (s *ServerWorkspaceResource) Commit(ctx context.Context, claim engine.ResourceFenceRef, runID uuid.UUID, dir string) error {
	if claim.Key != runKey(runID) {
		return fmt.Errorf("%w: claim %s does not cover run %s", ErrResourceFenced, claim.Key, runID)
	}
	if err := s.d.Leases.Check(ctx, claim); err != nil {
		return err
	}
	base, _, err := s.d.Checkpoints.Latest(ctx, runID)
	if err != nil {
		return fmt.Errorf("runner: read committed checkpoint of run %s: %w", runID, err)
	}
	staged, err := s.d.Checkpoints.StageResource(ctx, runID, dir, base)
	if err != nil {
		return fmt.Errorf("runner: stage group workspace of run %s: %w", runID, err)
	}
	staged.Resource = claim
	if err := s.d.Commits.CommitResource(ctx, claim, staged); err != nil {
		return fmt.Errorf("runner: commit group workspace of run %s: %w", runID, err)
	}
	return nil
}

// Release frees an uncommitted claim. A claim already released by a successful
// Commit (or a stale one) is a no-op: the lease store's exact-token release
// can never free a newer holder.
func (s *ServerWorkspaceResource) Release(ctx context.Context, claim engine.ResourceFenceRef) error {
	if err := s.d.Leases.Release(ctx, claim); err != nil && !errors.Is(err, ErrResourceFenced) {
		return err
	}
	return nil
}
