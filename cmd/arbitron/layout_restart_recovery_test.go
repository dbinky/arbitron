package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Paired row 2: no root survives the crash. Only a fresh initDomain's boot
// settlement and its worker's first sweeps may decide and replay the input;
// nothing here calls Recover, Notify or Drain. Paired row 3: shared/after-proof
// and shared/before-proof share one prior publication row, so only the exact
// proof revision and WorkflowID choose roll-forward over restore. Paired row 4:
// a private save killed before its rename is quarantined, one killed after it
// rolls forward; an admin shared Update ("edit") is decided by its tree backup
// and exact proof exactly like a re-publish.
func TestLayoutE2E_RestartSettlesThenWorkerRecovers(t *testing.T) {
	for _, stage := range []string{"private/prepare", "private/receipt", "private/visible", "shared/before-proof", "shared/after-proof", "edit/before-proof", "edit/after-proof"} {
		t.Run(stage, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{})
			ds, closeFirst := e.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			kind, boundary, _ := strings.Cut(stage, "/")
			shared := kind != "private"
			scope, path := workflow.Private(localDefaultUser), e.privatePath("target")
			if shared {
				scope, path = workflow.Shared(), e.sharedPath("target")
				require.NoError(t, ds.workflowSvc.Create(ctx, "source", layoutE2EWorkflow("source", "Original publication")))
				require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "target", workflow.VisibilityPublic, ""))
			} else {
				require.NoError(t, ds.workflowSvc.Create(ctx, "target", layoutE2EWorkflow("target", "Old private bytes")))
			}
			oldRev := layoutE2ESavedRevision(t, path)
			layoutE2EWaitTerminal(t, ds, oldRev)
			var oldProof workflow.Publication
			if shared {
				var ok bool
				var err error
				oldProof, ok, err = ds.publishedRepo.PublicationOf(ctx, "target")
				require.NoError(t, err)
				require.True(t, ok)
			}
			cachePath := filepath.Join(filepath.Dir(path), filesystem.LayoutCacheFile)
			oldCache := []byte(`{"old-cache":"restored revision bytes"}`)
			require.NoError(t, os.WriteFile(cachePath, oldCache, 0600))
			closeFirst()

			timeout, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			cmd := exec.CommandContext(timeout, os.Args[0], "-test.run=^TestLayoutE2E_CrashProcess$")
			cmd.Env = append(os.Environ(), "ARBITRON_LAYOUT_CRASH_CONFIG="+e.configPath, "ARBITRON_LAYOUT_CRASH_STAGE="+stage)
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			require.NoError(t, cmd.Start())
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					cmd.Process.Kill()
					cmd.Wait()
				}
			})
			require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "crash-ready")); return err == nil }, 8*time.Second, 10*time.Millisecond)
			var req workflow.ArrangementRequest
			b, err := os.ReadFile(filepath.Join(e.root, "captured.json"))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(b, &req))
			require.NoError(t, cmd.Process.Kill())
			require.Error(t, cmd.Wait())
			if boundary == "prepare" {
				require.Equal(t, oldRev, layoutE2ESavedRevision(t, path), "the kill precedes the rename")
			} else {
				require.Equal(t, req.Revision, layoutE2ESavedRevision(t, path), "the killed candidate is visible on disk")
			}

			// With no process alive, the durable evidence is the journal alone.
			journal := filesystem.NewRevisionJournal(e.workspaceDir)
			entries, err := journal.Entries(context.Background())
			require.NoError(t, err)
			require.Len(t, entries, 1)
			wantState := workflow.JournalPrepared
			if boundary == "receipt" {
				wantState = workflow.JournalCommitted
			}
			require.Equal(t, wantState, entries[0].State)
			db, err := adaptsqlite.Open(e.dbPath)
			require.NoError(t, err)
			_, err = adaptsqlite.NewLayoutRepo(db).Get(context.Background(), req.Revision)
			require.ErrorIs(t, err, layout.ErrRecordNotFound, "the kill precedes enqueue")
			if shared {
				// Both boundaries leave a same-name proof row; only its revision differs.
				proof, ok, err := adaptsqlite.NewPublishedWorkflowRepo(db).PublicationOf(context.Background(), "target")
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, oldProof.WorkflowID, req.WorkflowID, "re-publish keeps the shared identity")
				if boundary == "before-proof" {
					require.Equal(t, oldProof, proof)
				} else {
					require.Equal(t, req.Revision, proof.Revision)
					require.Equal(t, req.WorkflowID, proof.WorkflowID)
				}
			}
			require.NoError(t, db.Close())

			ds, _ = e.boot(t)
			committed := boundary != "before-proof" && boundary != "prepare"
			if committed {
				rec := layoutE2EWaitTerminal(t, ds, req.Revision)
				layoutE2EAssertOutcome(t, rec)
				require.Equal(t, req.Manifest, rec.Manifest)
				require.Equal(t, layout.Destination{Scope: scope, Name: "target", WorkflowID: req.WorkflowID}, rec.Destination)
				require.Equal(t, 2, layoutE2ECountRows(t, ds.db, "target"))
				require.Equal(t, req.Revision, layoutE2ESavedRevision(t, path), "boot settlement kept the committed tree")
				if kind == "shared" {
					support, err := os.ReadFile(filepath.Join(filepath.Dir(path), "support.txt"))
					require.NoError(t, err)
					require.Equal(t, "new support", string(support), "roll-forward keeps the whole committed tree")
				}
			} else {
				require.Equal(t, oldRev, layoutE2ESavedRevision(t, path), "boot settlement restored the prior tree")
				_, err := ds.layout.store.Get(context.Background(), req.Revision)
				require.ErrorIs(t, err, layout.ErrRecordNotFound, "a prepared-only candidate is never arranged")
				require.Equal(t, 1, layoutE2ECountRows(t, ds.db, "target"))
				reason, err := os.ReadFile(filepath.Join(e.workspaceDir, ".arbitron-layout-journal", "quarantine", req.Revision, "reason.txt"))
				require.NoError(t, err, "the uncommitted input is quarantined, not silently dropped")
				want := "not-committed"
				if boundary == "before-proof" {
					want = "publish-not-recorded"
				}
				require.Equal(t, want, string(reason))
				// The worker may republish the restored revision's cache, never the candidate's.
				cache, err := os.ReadFile(cachePath)
				require.NoError(t, err)
				if string(cache) != string(oldCache) {
					var entry layout.CacheEntry
					require.NoError(t, json.Unmarshal(cache, &entry))
					require.Equal(t, oldRev, entry.Revision)
				}
			}
			// The journal is removed only after the row is acknowledged (or quarantined).
			require.Eventually(t, func() bool {
				entries, err := ds.layout.journal.Entries(context.Background())
				return err == nil && len(entries) == 0
			}, 5*time.Second, 10*time.Millisecond)
			// Settlement finished or restored the backup; none is left to decide later.
			pending, err := ds.workflowRepo.(*filesystem.WorkflowRepo).PendingTreeCommits(context.Background())
			require.NoError(t, err)
			require.Empty(t, pending)
			if shared {
				proof, ok, err := ds.publishedRepo.PublicationOf(context.Background(), "target")
				require.NoError(t, err)
				require.True(t, ok)
				want := oldProof
				if committed {
					want.Revision = req.Revision
				}
				require.Equal(t, want, proof, "settlement never rewrites the publication row")
			}
		})
	}
}
