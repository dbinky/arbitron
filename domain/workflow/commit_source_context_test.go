package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type contextRelocationJournal struct {
	workflow.RevisionJournal
	mode string
}

func (j contextRelocationJournal) BeginRelocation(ctx context.Context, from workflow.Scope, fromName string, to workflow.Scope, toName string, id uuid.UUID) (workflow.RelocationIntent, error) {
	intent, err := j.RevisionJournal.BeginRelocation(ctx, from, fromName, to, toName, id)
	if err != nil {
		return intent, err
	}
	switch j.mode {
	case "wrong-intent":
		// Simulate a mismatched private operation token without exposing a bypass.
		intent.ID = uuid.NewString()
	case "foreign-intent", "unrelated-intent":
		name := toName
		if j.mode == "unrelated-intent" {
			name = "unrelated"
		}
		_, err = j.RevisionJournal.BeginRelocation(ctx, to, name, to, "elsewhere", id)
	}
	return intent, err
}

func TestCommitSource_ActiveRenameContextMatchesOnlyItsIntent(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, mode := range []string{"same-intent", "wrong-intent", "foreign-intent", "unrelated-intent"} {
			t.Run(map[bool]string{false: "private/", true: "shared/"}[shared]+mode, func(t *testing.T) {
				f := newCommitFixture(t, true)
				owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
				ctx := ctxWith(owner, true)
				st, err := workflow.NewRevisionStamper(f.repo, workflow.StamperDeps{Journal: contextRelocationJournal{f.journal, mode}, Requester: f.requester, Settler: f.source})
				require.NoError(t, err)
				svc := workflow.NewWorkflowService(f.repo, f.pubs)
				svc.SetRegistry(f.registry)
				svc.SetRevisionStamper(st)
				svc.SetMutationCoordinator(f.repo)
				require.NoError(t, svc.Create(ctx, "original", stampWorkflow()))
				scope, partition, name := workflow.Private(owner), filepath.Join("users", owner.String()), "original"
				baselineRows := 1
				if shared {
					require.NoError(t, svc.Publish(ctx, name, "published", workflow.VisibilityPublic, ""))
					scope, partition, name, baselineRows = workflow.Shared(), "shared", "published", 2
				}
				path := filepath.Join(f.base, partition, name, "workflow.json")
				before, err := os.ReadFile(path)
				require.NoError(t, err)
				prior, err := f.repo.Load(ctx, scope, name)
				require.NoError(t, err)
				err = svc.Rename(ctx, name, "renamed")
				allowed := mode == "same-intent" || mode == "unrelated-intent"
				if allowed {
					require.NoError(t, err)
					after, err := f.repo.Load(ctx, scope, "renamed")
					require.NoError(t, err)
					require.NotEqual(t, prior.SavedRevision(), after.SavedRevision())
					row, err := f.store.Get(ctx, after.SavedRevision())
					require.NoError(t, err)
					require.JSONEq(t, jsonValue(t, after), string(row.Manifest))
					require.Equal(t, "renamed", row.Destination.Name)
					require.Equal(t, prior.ID, row.Destination.WorkflowID)
					baselineRows++
				} else {
					renameErr := err
					after, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, before, after)
					require.NoDirExists(t, filepath.Join(f.base, partition, "renamed"))
					resolved, err := f.registry.ResolveName(ctx, prior.ID)
					require.NoError(t, err)
					require.Equal(t, name, resolved)
					require.ErrorIs(t, renameErr, workflow.ErrMutationPending)
				}
				var count int
				require.NoError(t, f.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&count))
				require.Equal(t, baselineRows, count, "wrong or foreign context must not create a saved revision")
			})
		}
	}
}
