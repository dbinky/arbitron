package workflow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Faults only the named journal boundaries; every successful operation uses the
// real durable journal, including the receipt deliberately retained for replay.
type relocationFaultJournal struct {
	workflow.RevisionJournal
	relocateErr error
	removeErr   error
}

func (j *relocationFaultJournal) Remove(ctx context.Context, revision string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.removeErr != nil {
		return j.removeErr
	}
	return j.RevisionJournal.Remove(ctx, revision)
}

func (j *relocationFaultJournal) Relocate(ctx context.Context, from workflow.Scope, fromName string, to workflow.Scope, toName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.relocateErr != nil {
		return j.relocateErr
	}
	return j.RevisionJournal.Relocate(ctx, from, fromName, to, toName)
}

// Inserts a second real intent after rename's prechecks, while it owns both
// endpoint locks. Its private context must exempt only the first intent.
type overlappingRelocationJournal struct {
	workflow.RevisionJournal
	coverDestination bool
	extra            workflow.RelocationIntent
}

func (j *overlappingRelocationJournal) BeginRelocation(ctx context.Context, from workflow.Scope, fromName string, to workflow.Scope, toName string, id uuid.UUID) (workflow.RelocationIntent, error) {
	own, err := j.RevisionJournal.BeginRelocation(ctx, from, fromName, to, toName, id)
	if err != nil {
		return own, err
	}
	extraFrom := "unrelated"
	if j.coverDestination {
		extraFrom = toName
	}
	j.extra, err = j.RevisionJournal.BeginRelocation(ctx, to, extraFrom, to, "elsewhere", id)
	return own, err
}

func TestRevisionStamperActiveRenameContextIsNarrow(t *testing.T) {
	for _, covering := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrelated-intent", true: "foreign-covering-intent"}[covering], func(t *testing.T) {
			f := newCommitFixture(t, true)
			owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
			ctx := ctxWith(owner, true)
			scope := workflow.Private(owner)
			journal := &overlappingRelocationJournal{RevisionJournal: f.journal, coverDestination: covering}
			st, err := workflow.NewRevisionStamper(f.repo, workflow.StamperDeps{Journal: journal, Requester: f.requester, Settler: f.source})
			require.NoError(t, err)
			svc := workflow.NewWorkflowService(f.repo, f.pubs)
			svc.SetRegistry(f.registry)
			svc.SetRevisionStamper(st)
			svc.SetMutationCoordinator(f.repo)
			require.NoError(t, svc.Create(ctx, "original", stampWorkflow()))
			before, err := f.repo.Load(ctx, scope, "original")
			require.NoError(t, err)
			oldRow, err := f.store.Get(ctx, before.SavedRevision())
			require.NoError(t, err)
			err = svc.Rename(ctx, "original", "renamed")
			if covering {
				require.ErrorIs(t, err, workflow.ErrMutationPending)
				after, err := f.repo.Load(ctx, scope, "original")
				require.NoError(t, err)
				require.Equal(t, before, after)
				row, err := f.store.Get(ctx, before.SavedRevision())
				require.NoError(t, err)
				require.Equal(t, oldRow, row)
			} else {
				require.NoError(t, err)
				after, err := f.repo.Load(ctx, scope, "renamed")
				require.NoError(t, err)
				require.NotEqual(t, before.SavedRevision(), after.SavedRevision())
				row, err := f.store.Get(ctx, after.SavedRevision())
				require.NoError(t, err)
				require.Equal(t, "renamed", row.Destination.Name)
				require.Equal(t, workflow.SaveReasonRename, row.Reason)
				require.JSONEq(t, jsonValue(t, after), string(row.Manifest))
			}
			intents, err := f.journal.RelocationIntents(ctx)
			require.NoError(t, err)
			require.Equal(t, []workflow.RelocationIntent{journal.extra}, intents, "rename must not consume another operation's intent")
			var count int
			require.NoError(t, f.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&count))
			want := 2
			if covering {
				want = 1
			}
			require.Equal(t, want, count, "a foreign covering intent must prevent preparation and enqueue")
		})
	}
}

func TestRevisionStamperRelocationFailurePreservesIntent(t *testing.T) {
	for _, boundary := range []string{"journal", "store"} {
		t.Run(boundary, func(t *testing.T) {
			f := newCommitFixture(t, true)
			owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
			ctx := ctxWith(owner, true)
			scope := workflow.Private(owner)
			journal := &relocationFaultJournal{RevisionJournal: f.journal, removeErr: errors.New("receipt cleanup unavailable")}
			st, err := workflow.NewRevisionStamper(f.repo, workflow.StamperDeps{Journal: journal, Requester: f.requester, Settler: f.source})
			require.NoError(t, err)
			svc := workflow.NewWorkflowService(f.repo, f.pubs)
			svc.SetRegistry(f.registry)
			svc.SetRevisionStamper(st)
			svc.SetMutationCoordinator(f.repo)
			require.NoError(t, svc.Create(ctx, "original", stampWorkflow()))
			before, err := f.repo.Load(ctx, scope, "original")
			require.NoError(t, err)
			original, err := f.store.Get(ctx, before.SavedRevision())
			require.NoError(t, err)
			if boundary == "journal" {
				journal.relocateErr = errors.New("journal relocation unavailable")
			} else {
				_, err := f.db.Exec(`CREATE TRIGGER relocation_outage BEFORE UPDATE OF workflow_name ON layout_revisions
 WHEN NEW.workflow_name = 'renamed' BEGIN SELECT RAISE(ABORT, 'relocation unavailable'); END`)
				require.NoError(t, err)
			}
			require.NoError(t, svc.Rename(ctx, "original", "renamed"), "postcommit relocation outage must not undo the saved revision")
			intents, err := f.journal.RelocationIntents(ctx)
			require.NoError(t, err)
			require.Len(t, intents, 1)
			paths := []string{
				filepath.Join(f.base, "users", owner.String(), "original", "workflow.json"),
				filepath.Join(f.base, "users", owner.String(), "renamed", "workflow.json"),
			}
			var saved [][]byte
			for _, path := range paths {
				data, err := os.ReadFile(path)
				require.NoError(t, err, "both endpoints must survive incomplete relocation")
				saved = append(saved, data)
			}
			entries, err := f.journal.Entries(ctx)
			require.NoError(t, err)
			require.Len(t, entries, 2, "both immutable inputs must remain discoverable")
			for _, entry := range entries {
				require.Equal(t, workflow.JournalCommitted, entry.State)
			}
			// A fresh recovery collaborator sees the same fault and must keep the
			// intent, bytes and SQLite history rather than acknowledging a half-move.
			deps := f.deps
			deps.Journal = journal
			recovery, err := workflow.NewCommitSource(deps)
			require.NoError(t, err)
			_, err = recovery.CommittedRequests(ctx)
			require.Error(t, err)
			afterIntents, err := f.journal.RelocationIntents(ctx)
			require.NoError(t, err)
			require.Equal(t, intents, afterIntents)
			afterEntries, err := f.journal.Entries(ctx)
			require.NoError(t, err)
			require.Equal(t, entries, afterEntries)
			for i, name := range []string{"original", "renamed"} {
				data, err := os.ReadFile(paths[i])
				require.NoError(t, err)
				require.Equal(t, saved[i], data)
				_, err = f.repo.Load(ctx, scope, name)
				require.ErrorIs(t, err, workflow.ErrMutationPending, "external reads cannot inherit the active rename context")
			}
			row, err := f.store.Get(ctx, original.Revision)
			require.NoError(t, err)
			require.Equal(t, original, row)
			journal.relocateErr = nil
			if boundary == "store" {
				_, err := f.db.Exec("DROP TRIGGER relocation_outage")
				require.NoError(t, err)
			}
			recovered, err := recovery.CommittedRequests(ctx)
			require.NoError(t, err)
			require.Len(t, recovered, 2)
			for _, req := range recovered {
				require.Equal(t, scope, req.Scope)
				require.Equal(t, "renamed", req.Name)
				if req.Revision == original.Revision {
					require.Equal(t, original.Manifest, req.Manifest, "relocation must not restamp immutable input")
				}
				require.NoError(t, f.requester.RequestArrangement(ctx, req))
			}
			row, err = f.store.Get(ctx, original.Revision)
			require.NoError(t, err)
			expected := original
			expected.Destination.Name = "renamed"
			// Relocation may update bookkeeping time, never immutable input.
			expected.UpdatedAt = row.UpdatedAt
			require.Equal(t, expected, row)
			require.NoFileExists(t, paths[0])
			data, err := os.ReadFile(paths[1])
			require.NoError(t, err)
			require.Equal(t, saved[1], data)
			afterIntents, err = f.journal.RelocationIntents(ctx)
			require.NoError(t, err)
			require.Empty(t, afterIntents)
			var count int
			require.NoError(t, f.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&count))
			require.Equal(t, 2, count, "recovery reuses the two original tokens")
		})
	}
}
