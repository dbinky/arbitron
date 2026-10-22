package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Exercise the real caller, durable journal, tree rollback and publication proof.
// Only the filesystem syscall boundary is fault-injected, after visibility.
func TestRevisionStamper_RealRollbackPreservesRecoveryEvidence(t *testing.T) {
	for _, failRollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful rollback abandons", true: "failed rollback retains"}[failRollback], func(t *testing.T) {
			base := t.TempDir()
			db, err := adaptsqlite.Open(filepath.Join(base, "arbitron.db"))
			require.NoError(t, err)
			t.Cleanup(func() { db.Close() })
			require.NoError(t, adaptsqlite.RunMigrations(db))
			pubs := adaptsqlite.NewPublishedWorkflowRepo(db)
			registry := adaptsqlite.NewWorkflowIdentityRepo(db)
			store := adaptsqlite.NewLayoutRepo(db)
			requester := layout.NewRequester(store, 3, nil)
			workspace := filepath.Join(base, "workflows")
			repo := NewWorkflowRepo(workspace)
			journal := NewRevisionJournal(workspace)
			commits, err := workflow.NewCommitSource(workflow.CommitSourceDeps{
				Repo: repo, Journal: journal, Mutations: repo, Trees: repo,
				Publications: pubs, Registry: registry, Relocator: requester,
			})
			require.NoError(t, err)
			repo.SetReadSettler(commits)
			stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: requester, Settler: commits, Displayed: store})
			require.NoError(t, err)
			svc := workflow.NewWorkflowService(repo, pubs)
			svc.SetRegistry(registry)
			svc.SetRevisionStamper(stamper)
			svc.SetMutationCoordinator(repo)
			// Migrations create the canonical local user, satisfying the real
			// publication's publisher foreign key without disabling constraints.
			owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
			id := uuid.New()
			ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: owner, Role: identity.RoleAdmin})
			scope := workflow.Shared()
			priorRevision := uuid.Must(uuid.NewV7()).String()
			prior := &workflow.Workflow{ID: id, Name: "demo", Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeTransform, Label: "Committed old label"}}, Presentation: &workflow.Presentation{
				Revision: priorRevision, SavedAt: time.Now().UTC(), Reason: workflow.SaveReasonPublish, Scope: workflow.ScopeLabelShared, Name: "demo", Retained: workflow.RetainedLayout{Source: workflow.RetainedNone},
			}}
			require.NoError(t, registry.Register(ctx, workflow.RegistryEntry{ID: id, Name: "demo", Scope: workflow.ScopeShared}))
			require.NoError(t, repo.Create(ctx, scope, "demo", prior))
			proof := workflow.Publication{Revision: priorRevision, WorkflowID: id, Publisher: owner, Visibility: workflow.VisibilityPublic}
			require.NoError(t, pubs.CommitPublication(ctx, "demo", proof))
			require.NoError(t, repo.WriteFile(ctx, scope, "demo", "support.txt", []byte("old supporting bytes")))
			dir := filepath.Join(workspace, "shared", "demo")
			require.NoError(t, os.WriteFile(filepath.Join(dir, LayoutCacheFile), []byte("old cache bytes"), 0600))
			original := map[string][]byte{}
			for _, name := range []string{"workflow.json", "support.txt", LayoutCacheFile} {
				original[name], err = os.ReadFile(filepath.Join(dir, name))
				require.NoError(t, err)
			}
			writeErr := errors.New("manifest rename acknowledgement failed")
			var prepared workflow.TreeCommit
			var obstruction string
			repo.files.rename = func(from, to string) error {
				if err := os.Rename(from, to); err != nil {
					return err
				}
				if to != filepath.Join(dir, "workflow.json") {
					return nil
				}
				intents, err := repo.PendingTreeCommits(ctx)
				require.NoError(t, err)
				require.Len(t, intents, 1)
				prepared = intents[0]
				if failRollback {
					// A nonempty aside directory makes the real rollback rename
					// fail, preserving both backup and new manifest for recovery.
					obstruction = filepath.Join(filepath.Dir(dir), ".demo.old-"+prepared.ID)
					require.NoError(t, os.Mkdir(obstruction, 0700))
					require.NoError(t, os.WriteFile(filepath.Join(obstruction, "blocker"), []byte("blocked rollback"), 0600))
				}
				return writeErr
			}
			input := &workflow.Workflow{Name: "demo", Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeTransform, Label: "Uncommitted new label"}}}
			err = svc.Update(ctx, "demo", input)
			require.ErrorIs(t, err, writeErr)
			repo.files.rename = nil
			require.NotEmpty(t, prepared.ID)
			require.Equal(t, input.SavedRevision(), prepared.Revision)
			require.NotEqual(t, priorRevision, prepared.Revision)
			entries, err := journal.Entries(ctx)
			require.NoError(t, err)
			if failRollback {
				require.Len(t, entries, 1, "failed rollback must not abandon the only recovery input")
				require.Equal(t, workflow.JournalPrepared, entries[0].State)
				captured, err := json.Marshal(input)
				require.NoError(t, err)
				require.Equal(t, captured, entries[0].Request.Manifest)
				visible, err := os.ReadFile(filepath.Join(dir, "workflow.json"))
				require.NoError(t, err)
				require.JSONEq(t, string(captured), string(visible), "the failed acknowledgement followed a real manifest rename")
				backup, err := os.ReadFile(filepath.Join(repo.treeRoot(), prepared.ID, "backup", "workflow.json"))
				require.NoError(t, err)
				require.Equal(t, original["workflow.json"], backup)
				require.Equal(t, prepared.Revision, entries[0].Request.Revision)
				require.Error(t, svc.Update(ctx, "demo", &workflow.Workflow{Name: "demo", Nodes: input.Nodes}), "an undecided tree must block another save")
				after, err := journal.Entries(ctx)
				require.NoError(t, err)
				require.Equal(t, entries, after, "the blocked save must preserve exact prepared bytes")
				intents, err := repo.PendingTreeCommits(ctx)
				require.NoError(t, err)
				require.Equal(t, []workflow.TreeCommit{prepared}, intents)
				require.NoError(t, os.RemoveAll(obstruction))
			} else {
				require.Empty(t, entries, "a fully restored save can abandon its request")
			}

			// Reopen every filesystem collaborator to prove evidence is durable,
			// not a retained in-memory request or a simulated rollback result.
			reopened := NewWorkflowRepo(workspace)
			freshJournal := NewRevisionJournal(workspace)
			freshCommits, err := workflow.NewCommitSource(workflow.CommitSourceDeps{
				Repo: reopened, Journal: freshJournal, Mutations: reopened, Trees: reopened,
				Publications: adaptsqlite.NewPublishedWorkflowRepo(db), Registry: adaptsqlite.NewWorkflowIdentityRepo(db), Relocator: layout.NewRequester(adaptsqlite.NewLayoutRepo(db), 3, nil),
			})
			require.NoError(t, err)
			requests, err := freshCommits.CommittedRequests(ctx)
			require.NoError(t, err)
			require.Empty(t, requests, "a prepared, unrecorded revision cannot be fed to a worker")
			for name, want := range original {
				got, err := os.ReadFile(filepath.Join(dir, name))
				require.NoError(t, err)
				require.Equal(t, want, got, "rollback must restore %s byte-for-byte", name)
			}
			gotProof, ok, err := pubs.PublicationOf(ctx, "demo")
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, proof.Revision, gotProof.Revision)
			require.Equal(t, proof.WorkflowID, gotProof.WorkflowID)
			_, err = store.Get(ctx, prepared.Revision)
			require.ErrorIs(t, err, layout.ErrRecordNotFound)
			intents, err := reopened.PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Empty(t, intents)
			entries, err = freshJournal.Entries(ctx)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
