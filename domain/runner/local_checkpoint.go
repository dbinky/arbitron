package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// LocalCheckpointDeps are the ports the non-portable checkpoint policy needs.
type LocalCheckpointDeps struct {
	Local       LocalCheckpointStore
	Homes       HomeStore
	Checkpoints CheckpointStore
	Leases      ResourceLeaseStore
}

// LocalCheckpoints is the RR-17 decision B policy for non-portable homes:
// every completed step stages a local copy (committed by the step
// transaction), recovery restores the newest committed base, and a missing
// base is ErrNoRecoveryBase.
type LocalCheckpoints struct{ d LocalCheckpointDeps }

func NewLocalCheckpoints(d LocalCheckpointDeps) (*LocalCheckpoints, error) {
	var missing []string
	if d.Local == nil {
		missing = append(missing, "Local")
	}
	if d.Homes == nil {
		missing = append(missing, "Homes")
	}
	if d.Checkpoints == nil {
		missing = append(missing, "Checkpoints")
	}
	if d.Leases == nil {
		missing = append(missing, "Leases")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("runner: local checkpoints: missing %s", strings.Join(missing, ", "))
	}
	return &LocalCheckpoints{d: d}, nil
}

func committedLocal(h Home) (LocalCheckpointRef, error) {
	if h.LocalCheckpoint == "" {
		return LocalCheckpointRef{}, nil
	}
	return ParseLocalCheckpointRef(h.LocalCheckpoint)
}

// Take stages the completed step's working directory as the successor of the
// committed local copy. It returns a PREPARED ref; only the step transaction
// makes it the recovery base. Any error means the step has no receipt.
func (l *LocalCheckpoints) Take(ctx context.Context, runID uuid.UUID, dir string, resource engine.ResourceFenceRef) (LocalCheckpointRef, error) {
	if err := l.d.Leases.Check(ctx, resource); err != nil {
		return LocalCheckpointRef{}, fmt.Errorf("runner: local checkpoint of run %s: %w", runID, err)
	}
	home, err := l.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return LocalCheckpointRef{}, err
	}
	if home.NonPortableReason == "" {
		return LocalCheckpointRef{}, fmt.Errorf("runner: local checkpoint of run %s: home is portable", runID)
	}
	cur, err := committedLocal(home)
	if err != nil {
		return LocalCheckpointRef{}, err
	}
	if err := l.d.Local.PruneLocal(ctx, runID, cur); err != nil {
		return LocalCheckpointRef{}, fmt.Errorf("runner: reclaim local checkpoints of run %s: %w", runID, err)
	}
	ref, err := l.d.Local.StageLocal(ctx, runID, dir, cur.Version+1)
	if err != nil {
		return LocalCheckpointRef{}, fmt.Errorf("runner: local checkpoint of non-portable run %s: %w", runID, err)
	}
	return ref, nil
}

// Bootstrap commits the initial local copy of a run that is non-portable from
// its first preparation (EnsureBaseline, before any job or claim exists).
func (l *LocalCheckpoints) Bootstrap(ctx context.Context, runID uuid.UUID, dir string) error {
	home, err := l.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return err
	}
	if home.LocalCheckpoint != "" {
		return nil // never re-snapshot a committed base
	}
	if home.NonPortableReason == "" {
		return fmt.Errorf("runner: local baseline of run %s: home is portable", runID)
	}
	ref, err := l.d.Local.StageLocal(ctx, runID, dir, 1)
	if err != nil {
		return fmt.Errorf("runner: local baseline of non-portable run %s: %w", runID, err)
	}
	if err := l.d.Homes.BootstrapLocalCheckpoint(ctx, runID, ref.String()); err != nil {
		if errors.Is(err, ErrCheckpointConflict) {
			if again, gerr := l.d.Homes.GetHome(ctx, runID); gerr == nil && again.LocalCheckpoint != "" {
				return nil // a concurrent initializer committed first; its base stands
			}
		}
		return fmt.Errorf("runner: record local baseline of run %s: %w", runID, err)
	}
	return nil
}

// Restore puts the newest committed base of a NON-PORTABLE home into dir:
// its committed local copy, else its initial state (the committed portable
// checkpoint it had before turning non-portable), else ErrNoRecoveryBase.
func (l *LocalCheckpoints) Restore(ctx context.Context, runID uuid.UUID, dir string) error {
	home, err := l.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return err
	}
	cur, err := committedLocal(home)
	if err != nil {
		return fmt.Errorf("%w: run %s: %w", ErrNoRecoveryBase, runID, err)
	}
	if !cur.IsZero() {
		if err := l.d.Local.RestoreLocal(ctx, cur, dir); err != nil {
			if errors.Is(err, ErrCheckpointNotFound) || errors.Is(err, ErrCorruptCheckpoint) {
				return fmt.Errorf("%w: run %s (%s): local checkpoint %s is unusable: %w",
					ErrNoRecoveryBase, runID, home.NonPortableReason, cur, err)
			}
			return fmt.Errorf("runner: restore local checkpoint %s: %w", cur, err)
		}
		// Retention only; the restore already succeeded. A failure here is
		// retried by the next Take.
		_ = l.d.Local.PruneLocal(ctx, runID, cur)
		return nil
	}
	ref, ok, err := l.d.Checkpoints.Latest(ctx, runID)
	if err != nil {
		return err
	}
	if ok {
		return l.d.Checkpoints.Restore(ctx, ref, dir)
	}
	return fmt.Errorf("%w: run %s (%s)", ErrNoRecoveryBase, runID, home.NonPortableReason)
}

// LocalCheckpointRetention decorates the durable step committer: after a
// commit succeeds it keeps only the newly committed local copy (or removes
// every local copy once a portable checkpoint is promoted). Retention is
// best-effort; it never fails a durable commit, and Take reclaims leftovers.
type LocalCheckpointRetention struct {
	inner engine.StepCommitter
	local LocalCheckpointStore
}

var _ engine.StepCommitter = (*LocalCheckpointRetention)(nil)

func NewLocalCheckpointRetention(inner engine.StepCommitter, local LocalCheckpointStore) (*LocalCheckpointRetention, error) {
	if inner == nil || local == nil {
		return nil, errors.New("runner: local checkpoint retention needs a step committer and a local store")
	}
	return &LocalCheckpointRetention{inner: inner, local: local}, nil
}

func (r *LocalCheckpointRetention) CommitStep(ctx context.Context, c engine.StepCommit) error {
	if err := r.inner.CommitStep(ctx, c); err != nil {
		return err
	}
	for _, comp := range c.Completions {
		w := comp.Workspace
		switch {
		case w == nil:
		case w.NonPortable:
			if ref, err := ParseLocalCheckpointRef(w.LocalRef); err == nil {
				_ = r.local.PruneLocal(ctx, ref.RunID, ref)
			}
		case w.Ref != "":
			_ = r.local.Delete(ctx, c.RunID)
		}
	}
	return nil
}

func (r *LocalCheckpointRetention) CommittedResult(ctx context.Context, invocationID uuid.UUID) (*engine.StepCommit, bool, error) {
	return r.inner.CommittedResult(ctx, invocationID)
}
