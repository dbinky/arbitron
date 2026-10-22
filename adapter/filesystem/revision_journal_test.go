package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func journalRequest(t *testing.T, name string) workflow.ArrangementRequest {
	t.Helper()
	rev := uuid.Must(uuid.NewV7()).String()
	now := time.Now().UTC()
	retained := workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"a": {X: 5, Y: 9}}}
	wf := workflow.Workflow{Name: name, ID: uuid.New(), Nodes: []workflow.Node{{ID: "a"}}, Presentation: &workflow.Presentation{Revision: rev, SavedAt: now, Reason: workflow.SaveReasonCreate, Scope: "shared", Name: name, Retained: retained}}
	data, err := json.Marshal(wf)
	require.NoError(t, err)
	return workflow.ArrangementRequest{Revision: rev, Scope: workflow.Shared(), Name: name, WorkflowID: wf.ID, Reason: workflow.SaveReasonCreate, Manifest: data, Retained: retained, RequestedAt: now}
}

func TestRevisionJournal_Transitions(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	j := NewRevisionJournal(base)
	req := journalRequest(t, "demo")
	require.Error(t, j.MarkCommitted(ctx, req.Revision))
	require.NoError(t, j.Record(ctx, req))
	entries, err := j.Entries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, workflow.JournalPrepared, entries[0].State)
	require.Equal(t, req, entries[0].Request)
	require.NoError(t, j.MarkCommitted(ctx, req.Revision))
	require.NoError(t, j.MarkCommitted(ctx, req.Revision))
	require.NoError(t, j.Record(ctx, req))
	reopened := NewRevisionJournal(base)
	entries, err = reopened.Entries(ctx)
	require.NoError(t, err)
	require.Equal(t, workflow.JournalCommitted, entries[0].State)
	entries[0].Request.Manifest[0] = 'x'
	entries[0].Request.Retained.Positions["a"] = workflow.NodePosition{X: 99}
	list, err := j.List(ctx)
	require.NoError(t, err)
	require.Equal(t, req, list[0])
	conflict := req
	conflict.Manifest = []byte("{}")
	require.Error(t, j.Record(ctx, conflict))
	require.NoError(t, j.Relocate(ctx, req.Scope, req.Name, workflow.Private(uuid.New()), "moved"))
	require.NoError(t, j.Record(ctx, req))
	list, err = j.List(ctx)
	require.NoError(t, err)
	require.Equal(t, "moved", list[0].Name)
	require.Equal(t, req.Manifest, list[0].Manifest)
	require.NoError(t, j.Remove(ctx, req.Revision))
	require.NoError(t, j.Remove(ctx, req.Revision))
	entries, err = j.Entries(ctx)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestRevisionJournal_QuarantineAndCorruption(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	j := NewRevisionJournal(base)
	req := journalRequest(t, "good")
	bad := journalRequest(t, "bad")
	require.NoError(t, j.Record(ctx, req))
	require.NoError(t, j.Record(ctx, bad))
	require.NoError(t, j.MarkCommitted(ctx, bad.Revision))
	dir := filepath.Join(base, ".arbitron-layout-journal")
	original, err := os.ReadFile(filepath.Join(dir, bad.Revision+".json"))
	require.NoError(t, err)
	require.NoError(t, j.Quarantine(ctx, bad.Revision, "test-reason"))
	require.NoError(t, j.Quarantine(ctx, bad.Revision, "test-reason"))
	saved, err := os.ReadFile(filepath.Join(dir, "quarantine", bad.Revision, bad.Revision+".json"))
	require.NoError(t, err)
	require.Equal(t, original, saved)
	orphan := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, os.WriteFile(filepath.Join(dir, orphan+".committed"), []byte("{}"), 0600))
	corrupt := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, os.WriteFile(filepath.Join(dir, corrupt+".json"), []byte("broken"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "torn.tmp"), []byte("partial"), 0600))
	entries, err := j.Entries(ctx)
	require.Error(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, req.Revision, entries[0].Request.Revision)
	entries, err = j.Entries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestRevisionJournal_RelocationAndErrors(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	j := NewRevisionJournal(base)
	req := journalRequest(t, "demo")
	require.NoError(t, j.Record(ctx, req))
	require.NoError(t, j.MarkCommitted(ctx, req.Revision))
	to := workflow.Private(uuid.New())
	intent, err := j.BeginRelocation(ctx, req.Scope, req.Name, to, "moved", req.WorkflowID)
	require.NoError(t, err)
	require.NotEmpty(t, intent.ID)
	intents, err := NewRevisionJournal(base).RelocationIntents(ctx)
	require.NoError(t, err)
	require.Equal(t, []workflow.RelocationIntent{intent}, intents)
	require.NoError(t, j.Relocate(ctx, req.Scope, req.Name, to, "moved"))
	require.NoError(t, j.Relocate(ctx, req.Scope, req.Name, to, "moved"))
	entries, err := j.Entries(ctx)
	require.NoError(t, err)
	require.Equal(t, workflow.JournalCommitted, entries[0].State)
	expected := req
	expected.Scope = to
	expected.Name = "moved"
	require.Equal(t, expected, entries[0].Request)
	require.NoError(t, j.EndRelocation(ctx, intent.ID))
	require.NoError(t, j.EndRelocation(ctx, intent.ID))
	for _, rev := range []string{"../escape", "", uuid.NewString()} {
		require.Error(t, j.MarkCommitted(ctx, rev))
		require.Error(t, j.Remove(ctx, rev))
		require.Error(t, j.Quarantine(ctx, rev, "bad"))
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, j.Record(canceled, journalRequest(t, "cancel")), context.Canceled)
	blocked := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocked, []byte("file"), 0600))
	require.Error(t, NewRevisionJournal(blocked).Record(ctx, req))
}

func TestRevisionJournalContract(t *testing.T) {
	layouttest.RunJournalContract(t, func(t *testing.T) workflow.RevisionJournal { return NewRevisionJournal(t.TempDir()) })
}
