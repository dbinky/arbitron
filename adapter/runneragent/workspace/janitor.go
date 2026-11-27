package workspace

import (
	"context"
	"fmt"
	"os"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
)

var _ runneragent.CopyJanitor = (*Syncer)(nil)

// HeldCopies reports persisted local copies and their home generations.
func (s *Syncer) HeldCopies(ctx context.Context) ([]runneragent.HeldCopy, error) {
	states, err := s.d.Copies.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]runneragent.HeldCopy, 0, len(states))
	for _, st := range states {
		out = append(out, runneragent.HeldCopy{RunID: st.RunID.String(), HomeGeneration: st.HomeGeneration})
	}
	return out, nil
}

// DeleteCopy keeps any local copy newer than the generation named by the server.
func (s *Syncer) DeleteCopy(ctx context.Context, runID string, upTo int64) (bool, error) {
	id, err := uuid.Parse(runID)
	if err != nil {
		return false, fmt.Errorf("runner workspace: delete copy: %w", err)
	}
	st, ok, err := s.d.Copies.Get(ctx, id)
	if err != nil || !ok {
		return false, err
	}
	if st.HomeGeneration > upTo {
		return false, nil
	}
	local, err := s.LocalPath(st.WorkDir)
	if err != nil {
		return false, err
	}
	if err := os.RemoveAll(local); err != nil {
		return false, fmt.Errorf("runner workspace: removing %s: %w", local, err)
	}
	if err := s.d.Copies.Delete(ctx, id); err != nil {
		return false, err
	}
	return true, nil
}
