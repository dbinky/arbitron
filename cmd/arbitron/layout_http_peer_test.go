package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Each case boots the reader before the writer and stops recovery polling. The
// first surviving-peer read must itself fail closed, not benefit from a sweep.
func TestLayoutHTTPE2E_RenamePeerCrash(t *testing.T) {
	for _, kind := range []string{"private", "shared"} {
		for _, boundary := range []string{"before-identity", "after-identity"} {
			for _, first := range []string{"raw", "manifest", "layout"} {
				t.Run(kind+"/"+boundary+"/"+first, func(t *testing.T) {
					e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
					ds, _ := e.boot(t)
					ds.layout.worker.Stop()
					ctx := adminCtxForLocalUser(t, ds)
					router := newLayoutE2ERouter(t, ds)
					processCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					cmd := exec.CommandContext(processCtx, os.Args[0], "-test.run=^TestCommitProtocol_RenameProcess$")
					cmd.Env = append(os.Environ(), "ARBITRON_RENAME_CRASH_CONFIG="+e.configPath, "ARBITRON_RENAME_CRASH_STAGE="+kind+"/"+boundary)
					cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
					require.NoError(t, cmd.Start())
					t.Cleanup(func() {
						if cmd.ProcessState == nil {
							_ = cmd.Process.Kill()
							_ = cmd.Wait()
						}
					})
					require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "crash-ready")); return err == nil }, 20*time.Second, 10*time.Millisecond)
					require.NoError(t, cmd.Process.Kill())
					require.Error(t, cmd.Wait())
					captured, err := os.ReadFile(filepath.Join(e.root, "captured.json"))
					require.NoError(t, err)
					var req workflow.ArrangementRequest
					require.NoError(t, json.Unmarshal(captured, &req))
					prior, err := os.ReadFile(filepath.Join(e.root, "prior-manifest.json"))
					require.NoError(t, err)
					var old workflow.Workflow
					require.NoError(t, json.Unmarshal(prior, &old))
					require.NotEqual(t, old.SavedRevision(), req.Revision)
					source := "original"
					scope := workflow.Private(localDefaultUser)
					if kind == "shared" {
						source, scope = "published", workflow.Shared()
					}
					// Both complete trees are visible, so an unlocked read could leak either.
					targetPath, sourcePath := e.privatePath("renamed"), e.privatePath(source)
					if kind == "shared" {
						targetPath, sourcePath = e.sharedPath("renamed"), e.sharedPath(source)
					}
					require.Equal(t, req.Revision, layoutE2ESavedRevision(t, targetPath))
					require.Equal(t, old.SavedRevision(), layoutE2ESavedRevision(t, sourcePath))
					visible, err := os.ReadFile(targetPath)
					require.NoError(t, err)
					sourceVisible, err := os.ReadFile(sourcePath)
					require.NoError(t, err)
					paths := []string{"/api/v1/workflows/renamed/files/workflow.json", "/api/v1/workflows/renamed", "/api/v1/workflows/renamed/layout"}
					if first == "manifest" {
						paths[0], paths[1] = paths[1], paths[0]
					}
					if first == "layout" {
						paths[0], paths[2] = paths[2], paths[0]
					}
					// The intent covers the about-to-vanish source endpoint as well.
					for _, path := range paths[:3] {
						paths = append(paths, strings.Replace(path, "/renamed", "/"+source, 1))
					}
					for _, path := range paths {
						status, body := layoutE2EDo(t, router, http.MethodGet, path, "")
						require.Equal(t, http.StatusServiceUnavailable, status, "%s: %v", path, body)
						require.Equal(t, "UNAVAILABLE", body["code"])
						require.Equal(t, "workflow destination is awaiting durable mutation recovery; retry shortly", body["error"])
						require.NotContains(t, body, "geometry")
					}
					update, err := json.Marshal(queryE2EWorkflow("renamed", "Must not overwrite pending rename"))
					require.NoError(t, err)
					status, body := layoutE2EDo(t, router, http.MethodPut, "/api/v1/workflows/renamed", string(update))
					require.Equal(t, http.StatusServiceUnavailable, status, "%v", body)
					require.Equal(t, "UNAVAILABLE", body["code"])
					require.Equal(t, "workflow destination is awaiting durable mutation recovery; retry shortly", body["error"])
					afterSave, err := os.ReadFile(targetPath)
					require.NoError(t, err)
					require.Equal(t, visible, afterSave, "pending save must not alter any manifest bytes")
					afterSource, err := os.ReadFile(sourcePath)
					require.NoError(t, err)
					require.Equal(t, sourceVisible, afterSource, "pending reads must not alter the source endpoint")
					_, err = ds.layout.store.Get(ctx, req.Revision)
					require.ErrorIs(t, err, layout.ErrRecordNotFound, "reads/save cannot enqueue an undecided rename")

					requests, err := ds.layout.commits.CommittedRequests(ctx)
					require.NoError(t, err)
					for _, request := range requests {
						require.NoError(t, ds.layout.requester.RequestArrangement(ctx, request))
					}
					winner, loser, want := source, "renamed", prior
					if boundary == "after-identity" {
						winner, loser, want = "renamed", source, req.Manifest
					}
					for _, suffix := range []string{"/files/workflow.json", "", "/layout"} {
						status, body = layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/"+winner+suffix, "")
						require.Equal(t, http.StatusOK, status, "%v", body)
						if suffix == "/layout" {
							var wf workflow.Workflow
							require.NoError(t, json.Unmarshal(want, &wf))
							require.Equal(t, wf.SavedRevision(), body["savedRevision"])
						} else {
							got, err := json.Marshal(body)
							require.NoError(t, err)
							require.JSONEq(t, string(want), string(got))
						}
					}
					status, _ = layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/"+loser, "")
					require.Equal(t, http.StatusNotFound, status)
					if boundary == "after-identity" {
						row, err := ds.layout.store.Get(ctx, req.Revision)
						require.NoError(t, err)
						require.Equal(t, req.Manifest, row.Manifest)
						require.Equal(t, layout.Destination{Scope: scope, Name: winner, WorkflowID: old.ID}, row.Destination)
					} else {
						_, err := ds.layout.store.Get(ctx, req.Revision)
						require.ErrorIs(t, err, layout.ErrRecordNotFound)
					}
				})
			}
		}
	}
}

// A subprocess leaves the same durable PrepareTreeCommit evidence as a crash
// between the two overwrite renames. There is deliberately a same-name shared
// tree: checking existence before settlement would serve the wrong scope.
func TestLayoutHTTPPrivateSwapProcess(t *testing.T) {
	configPath := os.Getenv("ARBITRON_HTTP_SWAP_CONFIG")
	if configPath == "" {
		return
	}
	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	ds.layout.worker.Stop()
	ctx := adminCtxForLocalUser(t, ds)
	scope := workflow.Private(localDefaultUser)
	repo := ds.workflowRepo.(*filesystem.WorkflowRepo)
	require.NoError(t, repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: "swap"}}, func(owned context.Context) error {
		prior, err := repo.Load(owned, scope, "swap")
		if err != nil {
			return err
		}
		next := *prior
		next.Description = "uncommitted replacement"
		stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: ds.layout.journal, Requester: ds.layout.requester, Settler: ds.layout.commits})
		if err != nil {
			return err
		}
		req, err := stamper.Stamp(owned, scope, "swap", &next, prior, workflow.SaveReasonUpdate)
		if err != nil {
			return err
		}
		captured, err := json.Marshal(req)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(configPath), "captured.json"), captured, 0600); err != nil {
			return err
		}
		tree, err := repo.PrepareTreeCommit(owned, workflow.MutationTarget{Scope: scope, Name: "swap"}, req.Revision)
		if err != nil {
			return err
		}
		dir := filepath.Join(ds.cfg.Workspace.Path, "users", localDefaultUser.String(), "swap")
		if err := os.Rename(dir, filepath.Join(filepath.Dir(dir), ".swap.old-"+tree.ID)); err != nil {
			return err
		}
		parent, err := os.Open(filepath.Dir(dir))
		if err != nil {
			return err
		}
		err = parent.Sync()
		closeErr := parent.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		layoutCrashBarrier{root: filepath.Dir(configPath)}.pause()
		return nil
	}))
	t.Fatal("swap never reached kill barrier")
}

func TestLayoutHTTPE2E_PrivateSwapGapRawFirst(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "swap", layoutE2EWorkflow("swap", "Committed private graph")))
	// Legacy on-disk scopes may contain the same name even though current
	// registry-backed authoring prohibits creating that collision.
	require.NoError(t, ds.workflowRepo.Create(ctx, workflow.Shared(), "swap", layoutE2EWorkflow("swap", "Wrong shared fallback")))
	rev := layoutE2ESavedRevision(t, e.privatePath("swap"))
	layoutE2EWaitTerminal(t, ds, rev)
	ds.layout.worker.Stop()
	prior, err := os.ReadFile(e.privatePath("swap"))
	require.NoError(t, err)
	router := newLayoutE2ERouter(t, ds)
	processCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(processCtx, os.Args[0], "-test.run=^TestLayoutHTTPPrivateSwapProcess$")
	cmd.Env = append(os.Environ(), "ARBITRON_HTTP_SWAP_CONFIG="+e.configPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "crash-ready")); return err == nil }, 20*time.Second, 10*time.Millisecond)
	require.NoFileExists(t, e.privatePath("swap"))
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	captured, err := os.ReadFile(filepath.Join(e.root, "captured.json"))
	require.NoError(t, err)
	var prepared workflow.ArrangementRequest
	require.NoError(t, json.Unmarshal(captured, &prepared))
	require.NotEqual(t, rev, prepared.Revision)
	status, body := layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/swap/files/workflow.json", "")
	require.Equal(t, http.StatusOK, status, "%v", body)
	got, err := json.Marshal(body)
	require.NoError(t, err)
	require.JSONEq(t, string(prior), string(got), "first raw read must restore private bytes, not fall through to shared")
	restored, err := os.ReadFile(e.privatePath("swap"))
	require.NoError(t, err)
	require.Equal(t, prior, restored)
	status, body = layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/swap/layout", "")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, rev, body["savedRevision"])
	entries, err := ds.layout.journal.Entries(ctx)
	require.NoError(t, err)
	require.Empty(t, entries, "uncommitted replacement is quarantined, never arranged")
	_, err = ds.layout.store.Get(ctx, prepared.Revision)
	require.ErrorIs(t, err, layout.ErrRecordNotFound)
}
