package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTreeCommit_RestoreAllBytes(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "overwrite"}[prior], func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			r := NewWorkflowRepo(base)
			s := workflow.Shared()
			dest := workflow.MutationTarget{Scope: s, Name: "demo"}
			rev := uuid.Must(uuid.NewV7()).String()
			original := []byte("{\"name\":\"old\",\"unknown\":{\"keep\":true}}\n")
			dir := filepath.Join(base, "shared", "demo")
			if prior {
				require.NoError(t, r.Create(ctx, s, "demo", &workflow.Workflow{Name: "old"}))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"), original, 0600))
				require.NoError(t, r.WriteFile(ctx, s, "demo", "nested/file", []byte("old bytes")))
				require.NoError(t, os.WriteFile(filepath.Join(dir, LayoutCacheFile), []byte("old cache"), 0600))
			}
			intent, err := r.PrepareTreeCommit(ctx, dest, rev)
			require.NoError(t, err)
			require.Equal(t, prior, intent.HadPrior)
			if prior {
				require.NoError(t, r.Delete(ctx, s, "demo"))
			}
			require.NoError(t, r.Create(ctx, s, "demo", &workflow.Workflow{Name: "new", Presentation: &workflow.Presentation{Revision: rev}}))
			require.NoError(t, r.WriteFile(ctx, s, "demo", "new", []byte("new")))
			reopened := NewWorkflowRepo(base)
			pending, err := reopened.PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Equal(t, []workflow.TreeCommit{intent}, pending)
			require.NoError(t, reopened.RollbackTreeCommit(ctx, intent.ID))
			require.NoError(t, reopened.RollbackTreeCommit(ctx, intent.ID))
			if prior {
				data, err := os.ReadFile(filepath.Join(dir, "workflow.json"))
				require.NoError(t, err)
				require.Equal(t, original, data)
				data, err = os.ReadFile(filepath.Join(dir, "nested/file"))
				require.NoError(t, err)
				require.Equal(t, "old bytes", string(data))
				data, err = os.ReadFile(filepath.Join(dir, LayoutCacheFile))
				require.NoError(t, err)
				require.Equal(t, "old cache", string(data))
				_, err = os.Stat(filepath.Join(dir, "new"))
				require.True(t, os.IsNotExist(err))
			} else {
				_, err := os.Stat(dir)
				require.True(t, os.IsNotExist(err))
			}
			pending, err = reopened.PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Empty(t, pending)
		})
	}
}
func TestTreeCommit_FinishRetriesAfterCleanupError(t *testing.T) {
	ctx := context.Background()
	r := NewWorkflowRepo(t.TempDir())
	rev := uuid.Must(uuid.NewV7()).String()
	intent, err := r.PrepareTreeCommit(ctx, workflow.MutationTarget{Scope: workflow.Shared(), Name: "demo"}, rev)
	require.NoError(t, err)
	blocked := filepath.Join(r.treeRoot(), ".finished-"+intent.ID)
	require.NoError(t, os.WriteFile(blocked, []byte("blocked cleanup"), 0600))
	require.Error(t, r.FinishTreeCommit(ctx, intent.ID))
	pending, err := r.PendingTreeCommits(ctx)
	require.NoError(t, err)
	require.Equal(t, []workflow.TreeCommit{intent}, pending)
	require.NoError(t, os.Remove(blocked))
	require.NoError(t, r.FinishTreeCommit(ctx, intent.ID))
	pending, err = r.PendingTreeCommits(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestTreeCommit_FinishedCleanupIsNotAnIncompleteBackup(t *testing.T) {
	ctx := context.Background()
	r := NewWorkflowRepo(t.TempDir())
	s := workflow.Shared()
	rev := uuid.Must(uuid.NewV7()).String()
	intent, err := r.PrepareTreeCommit(ctx, workflow.MutationTarget{Scope: s, Name: "demo"}, rev)
	require.NoError(t, err)
	tombstone := filepath.Join(r.treeRoot(), ".finished-"+intent.ID)
	require.NoError(t, os.Rename(filepath.Join(r.treeRoot(), intent.ID), tombstone))
	pending, err := r.PendingTreeCommits(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoError(t, r.FinishTreeCommit(ctx, intent.ID))
}

func TestTreeCommit_GuardsAndIncomplete(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	r := NewWorkflowRepo(base)
	s := workflow.Shared()
	dest := workflow.MutationTarget{Scope: s, Name: "demo"}
	rev := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, r.Create(ctx, s, "demo", &workflow.Workflow{Name: "old"}))
	intent, err := r.PrepareTreeCommit(ctx, dest, rev)
	require.NoError(t, err)
	require.NoError(t, r.Update(ctx, s, "demo", &workflow.Workflow{Name: "newer", Presentation: &workflow.Presentation{Revision: uuid.Must(uuid.NewV7()).String()}}))
	require.Error(t, r.RollbackTreeCommit(ctx, intent.ID))
	pending, err := r.PendingTreeCommits(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, r.FinishTreeCommit(ctx, intent.ID))
	require.NoError(t, r.FinishTreeCommit(ctx, intent.ID))
	incomplete := filepath.Join(base, ".arbitron-tree-commits", uuid.NewString())
	require.NoError(t, os.MkdirAll(filepath.Join(incomplete, "backup"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(incomplete, "backup", "workflow.json"), []byte("partial"), 0600))
	_, err = r.PendingTreeCommits(ctx)
	require.Error(t, err)
	require.Error(t, r.RollbackTreeCommit(ctx, filepath.Base(incomplete)))
	got, err := r.Load(ctx, s, "demo")
	require.NoError(t, err)
	require.Equal(t, "newer", got.Name)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = r.PrepareTreeCommit(canceled, dest, rev)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.PrepareTreeCommit(ctx, dest, "../escape")
	require.Error(t, err)
}

func TestTreeCommit_StagingRenameFailureRestoresAside(t *testing.T) {
	for _, restoreFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("restoreFails=%t", restoreFails), func(t *testing.T) {
			ctx := context.Background()
			r := NewWorkflowRepo(t.TempDir())
			scope := workflow.Shared()
			require.NoError(t, r.Create(ctx, scope, "demo", &workflow.Workflow{Name: "prior"}))
			dir, err := r.resolveWorkflowDir(scope, "demo")
			require.NoError(t, err)
			prior, err := os.ReadFile(filepath.Join(dir, "workflow.json"))
			require.NoError(t, err)
			rev := uuid.Must(uuid.NewV7()).String()
			intent, err := r.PrepareTreeCommit(ctx, workflow.MutationTarget{Scope: scope, Name: "demo"}, rev)
			require.NoError(t, err)
			require.NoError(t, r.Update(ctx, scope, "demo", &workflow.Workflow{Name: "candidate", Presentation: &workflow.Presentation{Revision: rev}}))
			candidate, err := os.ReadFile(filepath.Join(dir, "workflow.json"))
			require.NoError(t, err)
			staging := filepath.Join(filepath.Dir(dir), ".demo.tmp-"+intent.ID)
			aside := filepath.Join(filepath.Dir(dir), ".demo.old-"+intent.ID)
			stageErr, restoreErr := errors.New("staging rename failed"), errors.New("aside restore failed")
			r.files.rename = func(from, to string) error {
				if from == staging {
					return stageErr
				}
				if from == aside && restoreFails {
					return restoreErr
				}
				return os.Rename(from, to)
			}
			err = r.RollbackTreeCommit(ctx, intent.ID)
			require.ErrorIs(t, err, stageErr)
			candidateDir := dir
			if restoreFails {
				require.ErrorIs(t, err, restoreErr, "errors.Join must retain both failed renames")
				require.NoDirExists(t, dir)
				candidateDir = aside
			} else {
				require.NoDirExists(t, aside)
			}
			got, err := os.ReadFile(filepath.Join(candidateDir, "workflow.json"))
			require.NoError(t, err)
			require.Equal(t, candidate, got, "failed rollback must preserve the candidate for the next decision")
			pending, err := r.PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Equal(t, []workflow.TreeCommit{intent}, pending)
			r.files.rename = nil
			require.NoError(t, r.RollbackTreeCommit(ctx, intent.ID))
			got, err = os.ReadFile(filepath.Join(dir, "workflow.json"))
			require.NoError(t, err)
			require.Equal(t, prior, got)
			pending, err = r.PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Empty(t, pending)
		})
	}
}
