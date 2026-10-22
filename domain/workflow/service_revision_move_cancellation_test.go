package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Cancel at the actual copy boundary; all stamping, tree writes and cleanup
// still run through the durable repository, journal and SQLite registry.
type cancellingMoveRepo struct {
	*filesystem.WorkflowRepo
	cancel   context.CancelFunc
	boundary string
	reached  bool
}

func (r *cancellingMoveRepo) CopyTreeStamped(ctx context.Context, from workflow.Scope, fn string, to workflow.Scope, tn string, overwrite bool, stamp func(*workflow.Workflow) error) error {
	err := r.WorkflowRepo.CopyTreeStamped(ctx, from, fn, to, tn, overwrite, func(wf *workflow.Workflow) error {
		if err := stamp(wf); err != nil {
			return err
		}
		if r.cancel != nil && r.boundary == "callback" {
			r.reached = true
			r.cancel()
		}
		return nil
	})
	if err == nil && r.cancel != nil && r.boundary == "visible-copy" {
		r.reached = true
		r.cancel()
	}
	return err
}

func (r *cancellingMoveRepo) CopyTree(ctx context.Context, from workflow.Scope, fn string, to workflow.Scope, tn string, overwrite bool) error {
	err := r.WorkflowRepo.CopyTree(ctx, from, fn, to, tn, overwrite)
	if err == nil && r.cancel != nil {
		r.reached = true
		r.cancel()
	}
	return err
}

func TestServiceRevisionMoveCancellationBeforeIdentityRollsBack(t *testing.T) {
	for _, operation := range []string{"private-rename", "shared-rename", "transfer"} {
		for _, boundary := range []string{"callback", "visible-copy"} {
			if operation == "transfer" && boundary == "callback" {
				continue // Ownership transfer copies without a stamp callback.
			}
			t.Run(operation+"/"+boundary, func(t *testing.T) {
				f := newCommitFixture(t, true)
				owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
				ctx := ctxWith(owner, true)
				repo := &cancellingMoveRepo{WorkflowRepo: f.repo, boundary: boundary}
				st, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: f.journal, Requester: f.requester, Settler: f.source})
				require.NoError(t, err)
				svc := workflow.NewWorkflowService(repo, f.pubs)
				svc.SetRegistry(f.registry)
				svc.SetRevisionStamper(st)
				svc.SetMutationCoordinator(f.repo)
				require.NoError(t, svc.Create(ctx, "source", stampWorkflow()))
				name, partition := "source", filepath.Join("users", owner.String())
				if operation == "shared-rename" {
					require.NoError(t, svc.Publish(ctx, name, "public", workflow.VisibilityPublic, ""))
					name, partition = "public", "shared"
				}
				sourceDir := filepath.Join(f.base, partition, name)
				support := filepath.Join(sourceDir, "support.txt")
				require.NoError(t, os.WriteFile(support, []byte("committed supporting bytes"), 0600))
				before, err := os.ReadFile(filepath.Join(sourceDir, "workflow.json"))
				require.NoError(t, err)
				entry, err := f.registry.Resolve(ctx, name)
				require.NoError(t, err)
				proof, published, err := f.pubs.PublicationOf(ctx, name)
				require.NoError(t, err)
				var rowCount int
				require.NoError(t, f.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&rowCount))
				moveCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				repo.cancel = cancel
				targetDir := filepath.Join(f.base, partition, "target")
				if operation == "transfer" {
					newOwner := uuid.New()
					targetDir = filepath.Join(f.base, "users", newOwner.String(), name)
					err = svc.TransferOwnership(moveCtx, name, newOwner)
				} else {
					err = svc.Rename(moveCtx, name, "target")
				}
				require.True(t, repo.reached, "cancellation must occur inside the move")
				require.ErrorIs(t, err, context.Canceled)
				require.NoDirExists(t, targetDir, "rollback must remove the copied destination")
				after, err := os.ReadFile(filepath.Join(sourceDir, "workflow.json"))
				require.NoError(t, err)
				require.Equal(t, before, after)
				data, err := os.ReadFile(support)
				require.NoError(t, err)
				require.Equal(t, "committed supporting bytes", string(data))
				afterEntry, err := f.registry.Resolve(ctx, name)
				require.NoError(t, err)
				require.Equal(t, entry, afterEntry)
				afterProof, afterPublished, err := f.pubs.PublicationOf(ctx, name)
				require.NoError(t, err)
				require.Equal(t, published, afterPublished)
				require.Equal(t, proof, afterProof)
				intents, err := f.journal.RelocationIntents(ctx)
				require.NoError(t, err)
				require.Empty(t, intents)
				entries, err := f.journal.Entries(ctx)
				require.NoError(t, err)
				require.Empty(t, entries, "cancelled input cannot become a recovery job")
				trees, err := f.repo.PendingTreeCommits(ctx)
				require.NoError(t, err)
				require.Empty(t, trees)
				var afterRows int
				require.NoError(t, f.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&afterRows))
				require.Equal(t, rowCount, afterRows)
				_, err = svc.Load(ctx, name)
				require.NoError(t, err, "a fresh read must not remain pending")
			})
		}
	}
}
