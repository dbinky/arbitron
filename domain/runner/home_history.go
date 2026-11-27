package runner

import (
	"context"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// WorkspaceHistorySource projects the durable home history without discarding
// operation correlation or associating maintenance transfers with executions.
type WorkspaceHistorySource struct{ homes HomeStore }

func NewWorkspaceHistorySource(homes HomeStore) *WorkspaceHistorySource {
	return &WorkspaceHistorySource{homes: homes}
}

func (s *WorkspaceHistorySource) WorkspaceHistory(ctx context.Context, runID uuid.UUID) ([]instance.WorkspaceMove, error) {
	moves, err := s.homes.History(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]instance.WorkspaceMove, 0, len(moves))
	for _, m := range moves {
		out = append(out, instance.WorkspaceMove{From: string(m.From), To: string(m.To), Reason: m.Reason, Bytes: m.Bytes,
			Generation: m.Generation, At: m.At, TargetJobID: m.TargetJobID, InvocationID: m.InvocationID,
			NodeID: m.NodeID, OperationID: m.OperationID, FromGeneration: m.FromGeneration, ToGeneration: m.ToGeneration})
	}
	return out, nil
}
