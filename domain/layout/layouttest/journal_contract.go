package layouttest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunJournalContract is shared, unchanged, by the durable RevisionJournal and
// FakeJournal. Captured input is immutable per revision: its identity is the
// whole request except Scope and Name, which relocation may change.
func RunJournalContract(t *testing.T, newJournal func(*testing.T) workflow.RevisionJournal) {
	ctx := context.Background()
	scope := workflow.Private(uuid.New())
	request := func(t *testing.T, name, label string) workflow.ArrangementRequest {
		t.Helper()
		rev := uuid.Must(uuid.NewV7()).String()
		now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
		retained := workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"a": {X: 5, Y: 9}}}
		wf := workflow.Workflow{Name: name, ID: uuid.New(), Nodes: []workflow.Node{{ID: "a", Label: label}}, Presentation: &workflow.Presentation{Revision: rev, SavedAt: now, Reason: workflow.SaveReasonUpdate, Scope: workflow.ScopeLabel(scope), OwnerID: scope.OwnerID, Name: name, Retained: retained}}
		data, err := json.Marshal(wf)
		require.NoError(t, err)
		return workflow.ArrangementRequest{Revision: rev, Scope: scope, Name: name, WorkflowID: wf.ID, Reason: workflow.SaveReasonUpdate, Manifest: data, Retained: retained, RequestedAt: now}
	}
	// conflicting keeps the revision but changes captured input consistently,
	// so only the immutability rule (not envelope validation) can reject it.
	conflicting := func(t *testing.T, r workflow.ArrangementRequest, label string) workflow.ArrangementRequest {
		t.Helper()
		var wf workflow.Workflow
		require.NoError(t, json.Unmarshal(r.Manifest, &wf))
		wf.Nodes[0].Label = label
		data, err := json.Marshal(wf)
		require.NoError(t, err)
		r.Manifest = data
		r.Retained = workflow.RetainedLayout{Source: r.Retained.Source, Positions: map[string]workflow.NodePosition{"a": {X: 5, Y: 9}}}
		return r
	}
	only := func(t *testing.T, j workflow.RevisionJournal) workflow.JournalEntry {
		t.Helper()
		entries, err := j.Entries(ctx)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		return entries[0]
	}

	t.Run("identical replay after commit stays committed", func(t *testing.T) {
		j := newJournal(t)
		req := request(t, "flow", "Saved")
		require.NoError(t, j.Record(ctx, req))
		assert.Equal(t, workflow.JournalPrepared, only(t, j).State)
		require.NoError(t, j.MarkCommitted(ctx, req.Revision))
		require.NoError(t, j.Record(ctx, req))
		got := only(t, j)
		assert.Equal(t, workflow.JournalCommitted, got.State)
		assert.Equal(t, req, got.Request)
		// Scope/Name are destination, not captured input: a replay addressed to
		// the relocated destination is still the same input and stays committed.
		moved := req
		moved.Scope, moved.Name = workflow.Shared(), "moved"
		require.NoError(t, j.Record(ctx, moved))
		got = only(t, j)
		assert.Equal(t, workflow.JournalCommitted, got.State)
		assert.Equal(t, req, got.Request, "replay never rewrites the recorded entry")
		require.NoError(t, j.Relocate(ctx, req.Scope, req.Name, workflow.Shared(), "moved"))
		require.NoError(t, j.Record(ctx, req))
		got = only(t, j)
		assert.Equal(t, workflow.JournalCommitted, got.State)
		assert.Equal(t, moved, got.Request)
	})

	t.Run("conflicting immutable input is rejected", func(t *testing.T) {
		j := newJournal(t)
		prepared := request(t, "prepared", "Original")
		require.NoError(t, j.Record(ctx, prepared))
		require.Error(t, j.Record(ctx, conflicting(t, prepared, "Rewritten")))
		got := only(t, j)
		assert.Equal(t, workflow.JournalPrepared, got.State)
		assert.Equal(t, prepared, got.Request)

		require.NoError(t, j.MarkCommitted(ctx, prepared.Revision))
		rewritten := conflicting(t, prepared, "Rewritten after commit")
		rewritten.Scope, rewritten.Name = workflow.Shared(), "elsewhere"
		require.Error(t, j.Record(ctx, rewritten), "a new destination never licenses new input")
		got = only(t, j)
		assert.Equal(t, workflow.JournalCommitted, got.State)
		assert.Equal(t, prepared, got.Request)
	})

	t.Run("supplied and returned requests are copies", func(t *testing.T) {
		j := newJournal(t)
		req := request(t, "owned", "Owned")
		want := req
		want.Manifest = append([]byte(nil), req.Manifest...)
		want.Retained.Positions = map[string]workflow.NodePosition{"a": req.Retained.Positions["a"]}
		require.NoError(t, j.Record(ctx, req))
		req.Manifest[0] = 'X'
		req.Retained.Positions["a"] = workflow.NodePosition{X: 99}
		got := only(t, j)
		assert.Equal(t, want, got.Request, "the journal must not alias the caller's buffers")
		got.Request.Manifest[0] = 'Y'
		got.Request.Retained.Positions["a"] = workflow.NodePosition{X: 77}
		assert.Equal(t, want, only(t, j).Request, "returned entries must not alias stored input")
		list, err := j.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		list[0].Manifest[0] = 'Z'
		list[0].Retained.Positions["a"] = workflow.NodePosition{X: 55}
		assert.Equal(t, want, only(t, j).Request, "listed requests must not alias stored input")
	})

	t.Run("receipts require recorded input", func(t *testing.T) {
		j := newJournal(t)
		require.Error(t, j.MarkCommitted(ctx, uuid.Must(uuid.NewV7()).String()))
		req := request(t, "removed", "Removed")
		require.NoError(t, j.Record(ctx, req))
		require.NoError(t, j.MarkCommitted(ctx, req.Revision))
		require.NoError(t, j.MarkCommitted(ctx, req.Revision))
		require.NoError(t, j.Remove(ctx, req.Revision))
		require.NoError(t, j.Remove(ctx, req.Revision))
		entries, err := j.Entries(ctx)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("relocation intents are scoped to their endpoints", func(t *testing.T) {
		j := newJournal(t)
		id := uuid.New()
		intent, err := j.BeginRelocation(ctx, scope, "from", workflow.Shared(), "to", id)
		require.NoError(t, err)
		for _, endpoint := range []workflow.MutationTarget{{Scope: scope, Name: "from"}, {Scope: workflow.Shared(), Name: "to"}} {
			got, err := j.RelocationIntentsAt(ctx, endpoint.Scope, endpoint.Name)
			require.NoError(t, err)
			assert.Equal(t, []workflow.RelocationIntent{intent}, got)
		}
		got, err := j.RelocationIntentsAt(ctx, scope, "to")
		require.NoError(t, err)
		assert.Empty(t, got)
		all, err := j.RelocationIntents(ctx)
		require.NoError(t, err)
		assert.Equal(t, []workflow.RelocationIntent{intent}, all)
		require.NoError(t, j.EndRelocation(ctx, intent.ID))
		all, err = j.RelocationIntents(ctx)
		require.NoError(t, err)
		assert.Empty(t, all)
	})
}
