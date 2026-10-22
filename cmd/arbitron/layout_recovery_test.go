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

type layoutCrashBarrier struct{ root, stage string }

func (b layoutCrashBarrier) pause() {
	if err := os.WriteFile(filepath.Join(b.root, "crash-ready"), []byte("ready"), 0600); err != nil {
		panic(err)
	}
	select {}
}

type layoutCrashJournal struct {
	workflow.RevisionJournal
	barrier layoutCrashBarrier
}

func (j layoutCrashJournal) Record(ctx context.Context, req workflow.ArrangementRequest) error {
	if err := j.RevisionJournal.Record(ctx, req); err != nil {
		return err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(j.barrier.root, "captured.json"), data, 0600); err != nil {
		return err
	}
	if j.barrier.stage == "prepare" {
		j.barrier.pause()
	}
	return nil
}
func (j layoutCrashJournal) MarkCommitted(ctx context.Context, rev string) error {
	if j.barrier.stage == "visible" {
		j.barrier.pause()
	}
	err := j.RevisionJournal.MarkCommitted(ctx, rev)
	if err == nil && j.barrier.stage == "receipt" {
		j.barrier.pause()
	}
	return err
}

type layoutCrashRequester struct {
	workflow.ArrangementRequester
	barrier layoutCrashBarrier
}

func (r layoutCrashRequester) RequestArrangement(ctx context.Context, req workflow.ArrangementRequest) error {
	err := r.ArrangementRequester.RequestArrangement(ctx, req)
	if err == nil && r.barrier.stage == "enqueue" {
		r.barrier.pause()
	}
	return err
}

type layoutCrashPublications struct {
	*adaptsqlite.PublishedWorkflowRepo
	barrier layoutCrashBarrier
}

func (p layoutCrashPublications) CommitPublication(ctx context.Context, name string, pub workflow.Publication) error {
	if p.barrier.stage == "before-proof" {
		p.barrier.pause()
	}
	err := p.PublishedWorkflowRepo.CommitPublication(ctx, name, pub)
	if err == nil && p.barrier.stage == "after-proof" {
		p.barrier.pause()
	}
	return err
}

func TestLayoutE2E_CrashProcess(t *testing.T) {
	configPath := os.Getenv("ARBITRON_LAYOUT_CRASH_CONFIG")
	if configPath == "" {
		return
	}
	kind, stage, _ := strings.Cut(os.Getenv("ARBITRON_LAYOUT_CRASH_STAGE"), "/")
	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	barrier := layoutCrashBarrier{root: filepath.Dir(configPath), stage: stage}
	ls := ds.layout
	stamper, err := workflow.NewRevisionStamper(ds.workflowRepo, workflow.StamperDeps{Journal: layoutCrashJournal{ls.journal, barrier}, Requester: layoutCrashRequester{ls.requester, barrier}, Displayed: ls.store, Settler: ls.commits})
	require.NoError(t, err)
	svc := ds.workflowSvc
	ctx := adminCtxForLocalUser(t, ds)
	if kind == "shared" || kind == "edit" {
		repo := ds.workflowRepo.(*filesystem.WorkflowRepo)
		if kind == "shared" {
			// Prepare a genuinely newer source save before instrumenting the target publication.
			require.NoError(t, svc.Update(ctx, "source", layoutE2EWorkflow("source", "New published bytes")))
			require.NoError(t, repo.WriteFile(ctx, workflow.Private(localDefaultUser), "source", "support.txt", []byte("new support")))
		}
		svc = workflow.NewWorkflowService(repo, layoutCrashPublications{ds.publishedRepo, barrier})
		svc.SetRegistry(adaptsqlite.NewWorkflowIdentityRepo(ds.db))
		svc.SetMutationCoordinator(ls.mutations)
	}
	svc.SetRevisionStamper(stamper)
	switch kind {
	case "shared":
		err = svc.Publish(ctx, "source", "target", workflow.VisibilityPublic, "")
	case "edit":
		// An admin edit of the shared tree: tree backup, rename, then exact proof.
		err = svc.Update(ctx, "target", layoutE2EWorkflow("target", "New shared edit"))
	case "create":
		err = svc.Create(ctx, "target", layoutE2EWorkflow("target", "New private create"))
	default:
		err = svc.Update(ctx, "target", layoutE2EWorkflow("target", "New private update"))
	}
	require.NoError(t, err)
	t.Fatal("protocol never reached kill barrier")
}

func TestLayoutE2E_KillAndImmediatePeerRead(t *testing.T) {
	for _, stage := range []string{"private/prepare", "private/visible", "private/receipt", "private/enqueue", "create/prepare", "create/visible", "shared/before-proof", "shared/after-proof"} {
		t.Run(stage, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{})
			ds, _ := e.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			kind, boundary, _ := strings.Cut(stage, "/")
			scope := workflow.Private(localDefaultUser)
			path := e.privatePath("target")
			var oldManifest, oldCache []byte
			var oldRev string
			var oldProof workflow.Publication
			if kind == "shared" {
				scope = workflow.Shared()
				path = e.sharedPath("target")
				require.NoError(t, ds.workflowSvc.Create(ctx, "source", layoutE2EWorkflow("source", "Original publication")))
				require.NoError(t, ds.workflowRepo.WriteFile(ctx, workflow.Private(localDefaultUser), "source", "support.txt", []byte("old support")))
				require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "target", workflow.VisibilityPublic, ""))
				var ok bool
				var err error
				oldProof, ok, err = ds.publishedRepo.PublicationOf(ctx, "target")
				require.NoError(t, err)
				require.True(t, ok)
			} else if kind != "create" {
				require.NoError(t, ds.workflowSvc.Create(ctx, "target", layoutE2EWorkflow("target", "Old private bytes")))
			}
			if kind != "create" {
				oldRev = layoutE2ESavedRevision(t, path)
				layoutE2EWaitTerminal(t, ds, oldRev)
				var err error
				oldManifest, err = os.ReadFile(path)
				require.NoError(t, err)
				oldCache = []byte(`{"old-cache":"all bytes must survive rollback"}`)
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), filesystem.LayoutCacheFile), oldCache, 0600))
			}
			timeout, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			cmd := exec.CommandContext(timeout, os.Args[0], "-test.run=^TestLayoutE2E_CrashProcess$")
			cmd.Env = append(os.Environ(), "ARBITRON_LAYOUT_CRASH_CONFIG="+e.configPath, "ARBITRON_LAYOUT_CRASH_STAGE="+stage)
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
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
			if kind == "shared" {
				// Establish the actual crash window before any settling read can restore A.
				require.NotEqual(t, oldRev, req.Revision)
				require.Equal(t, req.Revision, layoutE2ESavedRevision(t, path))
				proof, ok, err := ds.publishedRepo.PublicationOf(ctx, "target")
				require.NoError(t, err)
				require.True(t, ok)
				if boundary == "before-proof" {
					require.Equal(t, oldProof, proof, "B's visible tree is not an exact publication commit")
				} else {
					require.Equal(t, req.Revision, proof.Revision)
					require.Equal(t, req.WorkflowID, proof.WorkflowID)
				}
			}
			// This composition root was already running before the crash. No poll/recovery
			// is invoked before its FIRST external read; SetReadSettler must decide visibility.
			loaded, readErr := ds.workflowRepo.Load(ctx, scope, "target")
			committed := boundary != "prepare" && boundary != "before-proof"
			if kind == "create" && !committed {
				require.ErrorIs(t, readErr, workflow.ErrNotFound)
			} else {
				require.NoError(t, readErr)
				want := oldRev
				if committed {
					want = req.Revision
				}
				require.Equal(t, want, loaded.SavedRevision())
			}
			if kind == "shared" && !committed {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, oldManifest, after)
				cache, err := os.ReadFile(filepath.Join(filepath.Dir(path), filesystem.LayoutCacheFile))
				require.NoError(t, err)
				require.Equal(t, oldCache, cache)
				support, err := os.ReadFile(filepath.Join(filepath.Dir(path), "support.txt"))
				require.NoError(t, err)
				require.Equal(t, "old support", string(support))
				proof, ok, err := ds.publishedRepo.PublicationOf(ctx, "target")
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, oldProof, proof, "rollback preserves every publication field as well as tree bytes")
			}
			if stage == "private/visible" {
				// The missed receipt must remain recoverable after a later authoring save
				// and deletion of the now-current tree, not just while its token is visible.
				require.NoError(t, ds.workflowSvc.Update(ctx, "target", layoutE2EWorkflow("target", "Overwritten after missed receipt")))
				latest := layoutE2ESavedRevision(t, path)
				layoutE2EWaitTerminal(t, ds, latest)
				require.NoError(t, ds.workflowSvc.Delete(ctx, "target"))
			}
			// A killed enqueueing process may already have claimed the row through its
			// real notifier. Expire that dead owner's lease without a five-minute sleep.
			if boundary == "enqueue" {
				row, err := ds.layout.store.Get(ctx, req.Revision)
				require.NoError(t, err)
				if row.State == layout.StateInProgress {
					_, err = ds.db.Exec("UPDATE layout_revisions SET lease_expires_at=? WHERE revision=?", time.Now().Add(-time.Minute).UnixNano(), req.Revision)
					require.NoError(t, err)
				}
				ds.layout.worker.Notify(req.Revision)
			}
			// Exercise the root's actual durable replay and worker after the immediate read.
			require.NoError(t, ds.layout.worker.Recover(ctx))
			if committed {
				rec := layoutE2EWaitTerminal(t, ds, req.Revision)
				layoutE2EAssertOutcome(t, rec)
				require.Equal(t, req.Manifest, rec.Manifest)
				require.NoError(t, ds.layout.worker.Recover(ctx))
				count := 1
				if oldRev != "" {
					count = 2
				}
				if stage == "private/visible" {
					count = 3
					require.NoFileExists(t, path)
				}
				require.Equal(t, count, layoutE2ECountRows(t, ds.db, "target"))
			} else {
				_, err := ds.layout.store.Get(ctx, req.Revision)
				require.ErrorIs(t, err, layout.ErrRecordNotFound)
			}
		})
	}
}
