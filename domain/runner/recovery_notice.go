// domain/runner/recovery_notice.go
package runner

import (
	"context"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// RecoveryNoticeSource derives instance.RecoveryNotice values from the
// ledger: every attempt that ended runner_lost (restart of a built-in job,
// lost runner, revoked key) was interrupted and repeated.
type RecoveryNoticeSource struct{ ledger Ledger }

func NewRecoveryNoticeSource(l Ledger) *RecoveryNoticeSource { return &RecoveryNoticeSource{ledger: l} }

func (s *RecoveryNoticeSource) RecoveryNotices(ctx context.Context, runID uuid.UUID) ([]instance.RecoveryNotice, error) {
	jobs, err := s.ledger.ListByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	var out []instance.RecoveryNotice
	for _, j := range jobs {
		attempts, err := s.ledger.Attempts(ctx, j.ID)
		if err != nil {
			return nil, err
		}
		for _, a := range attempts {
			if a.Outcome != string(OutcomeRunnerLost) {
				continue
			}
			at := a.StartedAt
			if a.EndedAt != nil {
				at = *a.EndedAt
			}
			out = append(out, instance.RecoveryNotice{NodeID: j.NodeID, JobID: j.ID, RunnerID: string(a.RunnerID),
				Reason: a.Outcome, At: at, Text: instance.RecoveryNoticeText})
		}
	}
	return out, nil
}
