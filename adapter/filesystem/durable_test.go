package filesystem

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func injectedDurabilityFailure(stage string, boom error) durableFiles {
	return durableFiles{
		sync: func(f *os.File) error {
			info, err := f.Stat()
			if err != nil {
				return err
			}
			if stage == "file sync" && !info.IsDir() || stage == "directory sync" && info.IsDir() {
				return boom
			}
			return f.Sync()
		},
		rename: func(from, to string) error {
			if stage == "rename" {
				return boom
			}
			return os.Rename(from, to)
		},
	}
}
func TestRevisionJournal_DurabilityErrors(t *testing.T) {
	for _, stage := range []string{"file sync", "rename", "directory sync"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			j := NewRevisionJournal(t.TempDir())
			req := journalRequest(t, "demo")
			boom := errors.New(stage)
			j.files = injectedDurabilityFailure(stage, boom)
			require.ErrorIs(t, j.Record(ctx, req), boom)
			entries, err := j.Entries(ctx)
			require.NoError(t, err)
			if stage == "directory sync" {
				require.Len(t, entries, 1)
				require.Equal(t, workflow.JournalPrepared, entries[0].State)
			} else {
				require.Empty(t, entries)
			}
			j.files = durableFiles{}
			require.NoError(t, j.Record(ctx, req))
			j.files = injectedDurabilityFailure(stage, boom)
			require.ErrorIs(t, j.MarkCommitted(ctx, req.Revision), boom)
			j.files = durableFiles{}
			entries, err = j.Entries(ctx)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			if stage == "directory sync" {
				require.Equal(t, workflow.JournalCommitted, entries[0].State)
			} else {
				require.Equal(t, workflow.JournalPrepared, entries[0].State)
			}
			files, err := os.ReadDir(j.dir)
			require.NoError(t, err)
			for _, file := range files {
				require.False(t, strings.HasSuffix(file.Name(), ".tmp"), "incomplete atomic write leaked")
			}
		})
	}
}
func TestTreeCommit_PrepareDurabilityFailurePreservesPrior(t *testing.T) {
	ctx := context.Background()
	r := NewWorkflowRepo(t.TempDir())
	s := workflow.Shared()
	require.NoError(t, r.Create(ctx, s, "demo", &workflow.Workflow{Name: "old"}))
	require.NoError(t, r.WriteFile(ctx, s, "demo", "file", []byte("original")))
	boom := errors.New("intent fsync")
	r.files = injectedDurabilityFailure("file sync", boom)
	_, err := r.PrepareTreeCommit(ctx, workflow.MutationTarget{Scope: s, Name: "demo"}, uuid.Must(uuid.NewV7()).String())
	require.ErrorIs(t, err, boom)
	got, err := r.Load(ctx, s, "demo")
	require.NoError(t, err)
	require.Equal(t, "old", got.Name)
	pending, err := r.PendingTreeCommits(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
}
func TestLayoutCache_WriteFailureRetainsPublishedBytes(t *testing.T) {
	ctx := context.Background()
	r, c, d, e := cacheFixture(t)
	require.NoError(t, c.PublishCache(ctx, d, e, func(context.Context) error { return nil }))
	boom := errors.New("cache rename")
	r.files = injectedDurabilityFailure("rename", boom)
	next := e
	next.JobGeneration++
	require.ErrorIs(t, c.PublishCache(ctx, d, next, func(context.Context) error { return nil }), boom)
	got, ok, err := c.ReadCache(ctx, d.Scope, d.Name)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, e, got)
	wf, err := r.Load(ctx, d.Scope, d.Name)
	require.NoError(t, err)
	require.Equal(t, e.Revision, wf.SavedRevision())
	require.Equal(t, layout.VerdictNeedsAttention, got.Verdict)
}
