package runneragent_test

import (
	"context"
	"github.com/arbitron/arbitron/domain/runner"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/require"
)

func TestMemorySpoolResultSnapshotsAreIndependent(t *testing.T) {
	ctx := context.Background()
	s := runneragent.NewMemorySpool(1)
	ref := runneragent.JobRef{JobID: "job", Generation: 1}
	reset := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	headroom, used := 0.75, 25.0
	original := runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "retained"}, Outcome: runner.SourceOutcome{
		ResetAt: &reset, HeadroomHint: &headroom, Windows: []runner.LimitWindow{{UsedPercent: &used, ResetAt: &reset}},
	}}
	wantReset := reset
	require.NoError(t, s.PutResult(ctx, ref, original))
	original.Chat.Output = "caller mutation"
	reset = reset.Add(time.Hour)
	headroom, used = 0, 100
	got, ok, err := s.Get(ctx, ref)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "retained", got.Result.Chat.Output)
	require.Equal(t, wantReset, *got.Result.Outcome.ResetAt)
	require.Equal(t, 0.75, *got.Result.Outcome.HeadroomHint)
	require.Equal(t, 25.0, *got.Result.Outcome.Windows[0].UsedPercent)
	require.Equal(t, wantReset, *got.Result.Outcome.Windows[0].ResetAt)
	*got.Result.Outcome.Windows[0].UsedPercent = 50
	got.Result.Chat.Output = "reader mutation"
	pending, err := s.Pending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "retained", pending[0].Result.Chat.Output)
	require.Equal(t, 25.0, *pending[0].Result.Outcome.Windows[0].UsedPercent)
	pending[0].Result.Chat.Output = "pending mutation"
	got, _, err = s.Get(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "retained", got.Result.Chat.Output)
}
