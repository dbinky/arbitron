package sqlite

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// promoteLocalCheckpointTx advances only the immediate successor of a
// non-portable home's local recovery base, in the caller's transaction.
func promoteLocalCheckpointTx(ctx context.Context, tx groupQueryer, runID uuid.UUID, localRef string) error {
	local, err := runner.ParseLocalCheckpointRef(localRef)
	if err != nil || local.RunID != runID {
		return fmt.Errorf("%w: local checkpoint %q for run %s", engine.ErrInvalidStepCompletion, localRef, runID)
	}
	var cur, reason string
	if err := tx.QueryRowContext(ctx, `SELECT local_checkpoint, non_portable_reason FROM workspace_homes WHERE run_id = ?`, runID.String()).Scan(&cur, &reason); err != nil {
		return err
	}
	if reason == "" {
		return fmt.Errorf("%w: run %s is portable", engine.ErrInvalidStepCompletion, runID)
	}
	next := int64(1)
	if cur != "" {
		prev, err := runner.ParseLocalCheckpointRef(cur)
		if err != nil {
			return err
		}
		next = prev.Version + 1
	}
	if local.Version != next {
		return fmt.Errorf("%w: local checkpoint %s is not the successor of %q", engine.ErrInvalidStepCompletion, local, cur)
	}
	_, err = tx.ExecContext(ctx, `UPDATE workspace_homes SET local_checkpoint = ?, dirty = 0 WHERE run_id = ?`, localRef, runID.String())
	return err
}
