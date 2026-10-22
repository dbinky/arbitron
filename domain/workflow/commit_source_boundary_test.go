package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Fault only the selected I/O boundary. All successful calls use the actual
// journal/tree adapter, so retry has to consume the evidence left on disk.
type settlementJournal struct {
	workflow.RevisionJournal
	boundary string
	err      error
}

func (j settlementJournal) Entries(ctx context.Context) ([]workflow.JournalEntry, error) {
	entries, err := j.RevisionJournal.Entries(ctx)
	if j.boundary == "entries" {
		err = errors.Join(err, j.err)
	}
	return entries, err
}
func (j settlementJournal) MarkCommitted(ctx context.Context, revision string) error {
	if j.boundary == "receipt" {
		return j.err
	}
	return j.RevisionJournal.MarkCommitted(ctx, revision)
}
func (j settlementJournal) Quarantine(ctx context.Context, revision, reason string) error {
	if j.boundary == "quarantine" {
		return j.err
	}
	return j.RevisionJournal.Quarantine(ctx, revision, reason)
}

type settlementTrees struct {
	workflow.TreeCommitter
	boundary string
	err      error
}

func (t settlementTrees) PendingTreeCommits(ctx context.Context) ([]workflow.TreeCommit, error) {
	if t.boundary == "trees" {
		return nil, t.err
	}
	return t.TreeCommitter.PendingTreeCommits(ctx)
}
func (t settlementTrees) FinishTreeCommit(ctx context.Context, id string) error {
	if t.boundary == "finish" {
		return t.err
	}
	return t.TreeCommitter.FinishTreeCommit(ctx, id)
}
func (t settlementTrees) RollbackTreeCommit(ctx context.Context, id string) error {
	if t.boundary == "rollback" {
		return t.err
	}
	return t.TreeCommitter.RollbackTreeCommit(ctx, id)
}

func TestCommitSource_VisibleUnprovenWithoutBackupStaysPending(t *testing.T) {
	ctx := context.Background()
	f := newCommitFixture(t, true)
	req := committedInput(t, "target", workflow.Shared())
	require.NoError(t, f.journal.Record(ctx, req))
	writeCommittedInput(t, f, req)
	path := filepath.Join(f.base, "shared", req.Name, "workflow.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	got, err := f.source.CommittedRequests(ctx)
	require.Empty(t, got)
	require.ErrorIs(t, err, workflow.ErrMutationPending)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	entries, err := f.journal.Entries(ctx)
	require.NoError(t, err)
	require.Equal(t, []workflow.JournalEntry{{Request: req, State: workflow.JournalPrepared}}, entries)
	_, err = f.store.Get(ctx, req.Revision)
	require.ErrorIs(t, err, layout.ErrRecordNotFound)
}

func TestCommitSource_UnlinkedTreeUsesExactProof(t *testing.T) {
	for _, proof := range []string{"exact", "old", "absent"} {
		t.Run(proof, func(t *testing.T) {
			ctx := context.Background()
			f := newCommitFixture(t, true)
			old := committedInput(t, "target", workflow.Shared())
			writeCommittedInput(t, f, old)
			path := filepath.Join(f.base, "shared", old.Name, "workflow.json")
			prior, err := os.ReadFile(path)
			require.NoError(t, err)
			next := committedInput(t, old.Name, old.Scope)
			_, err = f.repo.PrepareTreeCommit(ctx, workflow.MutationTarget{Scope: next.Scope, Name: next.Name}, next.Revision)
			require.NoError(t, err)
			writeCommittedInput(t, f, next)
			if proof != "absent" {
				revision := next.Revision
				if proof == "old" {
					revision = old.Revision
				}
				require.NoError(t, f.pubs.CommitPublication(ctx, next.Name, workflow.Publication{Revision: revision, WorkflowID: next.WorkflowID, Publisher: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Visibility: workflow.VisibilityPublic}))
			}
			got, err := f.source.CommittedRequests(ctx)
			require.NoError(t, err)
			require.Empty(t, got, "cleanup must not fabricate acknowledged input")
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			if proof == "exact" {
				var visible workflow.Workflow
				require.NoError(t, json.Unmarshal(data, &visible))
				require.Equal(t, next.Revision, visible.SavedRevision())
			} else {
				require.Equal(t, prior, data)
			}
			pending, err := f.repo.PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Empty(t, pending)
		})
	}
}

func TestCommitSource_SettlementIOKeepsEvidenceAndPartialSuccess(t *testing.T) {
	for _, boundary := range []string{"receipt", "finish", "rollback", "quarantine", "entries", "trees"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			f := newCommitFixture(t, true)
			history := committedInput(t, "deleted-history", workflow.Private(uuid.New()))
			require.NoError(t, f.journal.Record(ctx, history))
			require.NoError(t, f.journal.MarkCommitted(ctx, history.Revision))
			old := committedInput(t, "target", workflow.Shared())
			writeCommittedInput(t, f, old)
			require.NoError(t, f.repo.WriteFile(ctx, old.Scope, old.Name, "support", []byte("prior support")))
			path := filepath.Join(f.base, "shared", old.Name, "workflow.json")
			prior, err := os.ReadFile(path)
			require.NoError(t, err)
			next := committedInput(t, old.Name, old.Scope)
			require.NoError(t, f.journal.Record(ctx, next))
			_, err = f.repo.PrepareTreeCommit(ctx, workflow.MutationTarget{Scope: next.Scope, Name: next.Name}, next.Revision)
			require.NoError(t, err)
			writeCommittedInput(t, f, next)
			proven := boundary != "rollback" && boundary != "quarantine"
			if proven {
				require.NoError(t, f.pubs.CommitPublication(ctx, next.Name, workflow.Publication{Revision: next.Revision, WorkflowID: next.WorkflowID, Publisher: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Visibility: workflow.VisibilityPublic}))
			}
			boom := errors.New("injected " + boundary)
			d := f.deps
			d.Repo = noCatalogRepo{f.repo}
			d.Journal = settlementJournal{f.journal, boundary, boom}
			d.Trees = settlementTrees{f.repo, boundary, boom}
			source, err := workflow.NewCommitSource(d)
			require.NoError(t, err)
			got, err := source.CommittedRequests(ctx)
			require.ErrorIs(t, err, boom)
			require.Contains(t, got, history, "an unrelated I/O failure must not hide committed history")
			entries, err := f.journal.Entries(ctx)
			require.NoError(t, err)
			require.Len(t, entries, 2, "undecided input must remain retryable")
			_, err = f.store.Get(ctx, next.Revision)
			require.ErrorIs(t, err, layout.ErrRecordNotFound)
			if boundary == "rollback" || boundary == "trees" || boundary == "receipt" {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				var visible workflow.Workflow
				require.NoError(t, json.Unmarshal(data, &visible))
				require.Equal(t, next.Revision, visible.SavedRevision(), "failed decision must not silently discard visible evidence")
			}
			got, err = f.source.CommittedRequests(ctx)
			require.NoError(t, err)
			want := []workflow.ArrangementRequest{history}
			if proven {
				want = append(want, next)
			} else {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, prior, after, "retry restores every old manifest byte")
			}
			require.Equal(t, want, got)
			for _, req := range got {
				require.NoError(t, f.requester.RequestArrangement(ctx, req))
				row, err := f.store.Get(ctx, req.Revision)
				require.NoError(t, err)
				require.Equal(t, req.Manifest, row.Manifest)
			}
			pending, err := f.repo.PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Empty(t, pending)
		})
	}
}
