package engine

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// RecoveryNoticeSource reports the interrupted attempts of a run (the runner
// ledger implements it; the engine never imports the runner package).
type RecoveryNoticeSource interface {
	RecoveryNotices(ctx context.Context, runID uuid.UUID) ([]instance.RecoveryNotice, error)
}

// SetRecoveryNoticeSource installs the notice source. Call before Start.
func (e *Engine) SetRecoveryNoticeSource(s RecoveryNoticeSource) { e.recoveryNotices = s }

func (e *Engine) attachRecovery(ctx context.Context, inst *instance.Instance) error {
	if e.recoveryNotices == nil || inst == nil {
		return nil
	}
	n, err := e.recoveryNotices.RecoveryNotices(ctx, inst.ID)
	if err != nil {
		return fmt.Errorf("reading recovery notices for %s: %w", inst.ID, err)
	}
	inst.Recovery = n
	return nil
}

func noticesFor(all []instance.RecoveryNotice, nodeID string) []instance.RecoveryNotice {
	var out []instance.RecoveryNotice
	for _, n := range all {
		if n.NodeID == nodeID {
			out = append(out, n)
		}
	}
	return out
}
