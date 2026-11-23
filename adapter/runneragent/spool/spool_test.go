package spool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/arbitron/arbitron/domain/runneragent/spooltest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const jobA = "6f1c1f0e-8a39-4d0e-9d51-3a4c3c0b6a11"

func TestFileSpoolConformance(t *testing.T) {
	spooltest.Run(t, func(t *testing.T, capacity int) runneragent.Spool {
		s, err := Open(t.TempDir(), capacity)
		require.NoError(t, err)
		return s
	})
}

func TestFileSpoolSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ref := runneragent.JobRef{JobID: jobA, Generation: 2}
	s, err := Open(dir, 16)
	require.NoError(t, err)
	for i, tok := range []string{"a", "b", "c"} {
		require.NoError(t, s.AppendOutput(ctx, ref, runneragent.OutputFrame{Seq: uint64(i + 1), Token: tok}))
	}
	require.NoError(t, s.Ack(ctx, ref, 1))
	res := runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "abc", Model: "claude-sonnet-4-5"}, Outcome: runner.SourceOutcome{}}
	require.NoError(t, s.PutResult(ctx, ref, res))

	re, err := Open(dir, 16)
	require.NoError(t, err)
	got, ok, err := re.Get(ctx, ref)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, uint64(1), got.LastAcked)
	assert.Equal(t, []runneragent.OutputFrame{{Seq: 2, Token: "b"}, {Seq: 3, Token: "c"}}, got.Frames, "no gaps, no duplicates")
	require.NotNil(t, got.Result)
	assert.Equal(t, "abc", got.Result.Chat.Output)

	require.NoError(t, re.AckResult(ctx, ref))
	again, err := Open(dir, 16)
	require.NoError(t, err)
	pending, err := again.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "an acknowledged result is gone for good")
}

func TestFileSpoolDropRemovesLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir, 4)
	require.NoError(t, err)
	ref := runneragent.JobRef{JobID: jobA, Generation: 1}
	require.NoError(t, s.AppendOutput(ctx, ref, runneragent.OutputFrame{Seq: 1, Token: "x"}))
	logs, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	require.Len(t, logs, 1)
	info, err := os.Stat(logs[0])
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.NoError(t, s.Drop(ctx, ref))
	logs, _ = filepath.Glob(filepath.Join(dir, "*.log"))
	assert.Empty(t, logs)
}

func TestFileSpoolIgnoresTruncatedTail(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir, 8)
	require.NoError(t, err)
	ref := runneragent.JobRef{JobID: jobA, Generation: 1}
	require.NoError(t, s.AppendOutput(ctx, ref, runneragent.OutputFrame{Seq: 1, Token: "x"}))
	logs, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	f, err := os.OpenFile(logs[0], os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, _ = f.WriteString(`{"op":"out","seq":2,"to`) // crash mid-write
	require.NoError(t, f.Close())

	re, err := Open(dir, 8)
	require.NoError(t, err)
	got, ok, err := re.Get(ctx, ref)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []runneragent.OutputFrame{{Seq: 1, Token: "x"}}, got.Frames)
}

func TestFileSpoolRejectsNonUUIDJobID(t *testing.T) {
	s, err := Open(t.TempDir(), 4)
	require.NoError(t, err)
	err = s.AppendOutput(context.Background(), runneragent.JobRef{JobID: "../../etc/passwd", Generation: 1}, runneragent.OutputFrame{Seq: 1})
	require.Error(t, err)
}

func TestOpenRefusesSpoolLargerThanCapacity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir, 8)
	require.NoError(t, err)
	ref := runneragent.JobRef{JobID: jobA, Generation: 1}
	for i := 1; i <= 5; i++ {
		require.NoError(t, s.AppendOutput(ctx, ref, runneragent.OutputFrame{Seq: uint64(i), Token: "t"}))
	}
	require.NoError(t, s.PutResult(ctx, ref, runneragent.Result{OK: false, Outcome: runner.SourceOutcome{Class: runner.OutcomeOther}}))
	_, err = Open(dir, 4)
	require.Error(t, err, "shrinking output_buffer_frames below the spooled frames must not silently drop them")
}
