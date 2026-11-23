// Package spooltest is the conformance suite every runneragent.Spool
// implementation must pass (memory spool here, file spool in
// adapter/runneragent/spool).
package spooltest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/runneragent"
)

// Run executes the suite. newSpool must return an EMPTY spool with the given
// unacked-frame capacity.
func Run(t *testing.T, newSpool func(t *testing.T, capacity int) runneragent.Spool) {
	ctx := context.Background()
	a := runneragent.JobRef{JobID: "job-a", Generation: 1}
	b := runneragent.JobRef{JobID: "job-b", Generation: 2}

	t.Run("full spool refuses without storing or dropping", func(t *testing.T) {
		s := newSpool(t, 2)
		require.NoError(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 1, Token: "x"}))
		require.NoError(t, s.AppendOutput(ctx, b, runneragent.OutputFrame{Seq: 1, Token: "y"}))
		err := s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 2, Token: "z"})
		require.ErrorIs(t, err, runneragent.ErrSpoolFull)
		got, ok, err := s.Get(ctx, a)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []runneragent.OutputFrame{{Seq: 1, Token: "x"}}, got.Frames, "the refused frame is not stored and nothing older is dropped")
	})

	t.Run("ack drops acknowledged frames, frees capacity and is monotonic", func(t *testing.T) {
		s := newSpool(t, 3)
		for i := uint64(1); i <= 3; i++ {
			require.NoError(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: i, Token: "t"}))
		}
		require.ErrorIs(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 4, Token: "t"}), runneragent.ErrSpoolFull)
		require.NoError(t, s.Ack(ctx, a, 2))
		require.NoError(t, s.Ack(ctx, a, 1), "a stale lower ack is ignored")
		got, _, err := s.Get(ctx, a)
		require.NoError(t, err)
		assert.Equal(t, uint64(2), got.LastAcked)
		assert.Equal(t, []runneragent.OutputFrame{{Seq: 3, Token: "t"}}, got.Frames)
		require.NoError(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 4, Token: "t"}))
		require.NoError(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 5, Token: "t"}))
	})

	t.Run("sequence must be contiguous after the last frame or ack", func(t *testing.T) {
		s := newSpool(t, 8)
		require.ErrorIs(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 2, Token: "gap"}), runneragent.ErrSpoolSequence)
		require.NoError(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 1, Token: "ok"}))
		require.ErrorIs(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 1, Token: "dup"}), runneragent.ErrSpoolSequence)
		require.NoError(t, s.Ack(ctx, a, 1))
		require.ErrorIs(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 1, Token: "dup"}), runneragent.ErrSpoolSequence)
		require.NoError(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 2, Token: "next"}))
	})

	t.Run("results are retained until AckResult and do not use frame capacity", func(t *testing.T) {
		s := newSpool(t, 1)
		require.NoError(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 1, Token: "x"}))
		res := runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "done", Model: "claude-sonnet-4-5"}}
		require.NoError(t, s.PutResult(ctx, a, res))
		require.NoError(t, s.PutResult(ctx, b, runneragent.Result{OK: false, ErrorText: "boom"}))
		pending, err := s.Pending(ctx)
		require.NoError(t, err)
		require.Len(t, pending, 2)
		assert.Equal(t, a, pending[0].Ref, "pending is sorted by job id then generation")
		require.NotNil(t, pending[0].Result)
		assert.Equal(t, "done", pending[0].Result.Chat.Output)
		require.NoError(t, s.AckResult(ctx, a))
		require.NoError(t, s.AppendOutput(ctx, b, runneragent.OutputFrame{Seq: 1, Token: "y"}), "AckResult freed a's frame")
		_, ok, err := s.Get(ctx, a)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("drop forgets the job and frees capacity", func(t *testing.T) {
		s := newSpool(t, 1)
		require.NoError(t, s.AppendOutput(ctx, a, runneragent.OutputFrame{Seq: 1, Token: "x"}))
		require.NoError(t, s.Drop(ctx, a))
		require.NoError(t, s.AppendOutput(ctx, b, runneragent.OutputFrame{Seq: 1, Token: "y"}))
		pending, err := s.Pending(ctx)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		assert.Equal(t, b, pending[0].Ref)
	})

	t.Run("operations on unknown jobs are no-ops", func(t *testing.T) {
		s := newSpool(t, 1)
		require.NoError(t, s.Ack(ctx, a, 9))
		require.NoError(t, s.AckResult(ctx, a))
		require.NoError(t, s.Drop(ctx, a))
		_, ok, err := s.Get(ctx, a)
		require.NoError(t, err)
		assert.False(t, ok)
	})
}
