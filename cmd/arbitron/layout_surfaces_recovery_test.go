package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/dashboard"
	"github.com/arbitron/arbitron/adapter/filesystem"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

// Pause the actual rename of an ALREADY mounted, reviewed workflow. Only the
// durable identity boundary is decorated; no read, layout or review is replaced.
func TestLayoutSurfaces_RenameWriterProcess(t *testing.T) {
	configPath := os.Getenv("ARBITRON_SURFACE_RENAME_CONFIG")
	if configPath == "" {
		return
	}
	ds, err := initDomainWithModelDependencies(context.Background(), configPath, offlineModelDependencies())
	require.NoError(t, err)
	require.NoError(t, ds.Start(context.Background()))
	ds.layout.worker.Stop()
	probe := &renameCrashProbe{layoutCrashBarrier: layoutCrashBarrier{root: filepath.Dir(configPath), stage: os.Getenv("ARBITRON_SURFACE_RENAME_STAGE")}, armed: true}
	repo := ds.workflowRepo.(*filesystem.WorkflowRepo)
	stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: renameCrashJournal{ds.layout.journal, probe}, Requester: ds.layout.requester, Displayed: ds.layout.store, Settler: ds.layout.commits})
	require.NoError(t, err)
	svc := workflow.NewWorkflowService(repo, renameCrashPublications{ds.publishedRepo, probe})
	svc.SetRegistry(renameCrashRegistry{adaptsqlite.NewWorkflowIdentityRepo(ds.db), probe})
	svc.SetRevisionStamper(stamper)
	svc.SetMutationCoordinator(ds.layout.mutations)
	require.NoError(t, svc.Rename(adminCtxForLocalUser(t, ds), "surface-source", "surface-renamed"))
	t.Fatal("rename did not reach the requested kill boundary")
}

func TestLayoutSurfaces_KilledRenameRetainsReviewedGraphUntilSettlement(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, scope := range []string{"private", "shared"} {
		for _, boundary := range []string{"before-identity", "after-identity"} {
			t.Run(scope+"/"+boundary, func(t *testing.T) {
				e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
				useLayoutE2EBrowser(t, browser)
				ds, closeRoot := e.boot(t)
				ctx := adminCtxForLocalUser(t, ds)
				h := adapthttp.NewRouterFromDeps(ds.routerDeps(nil, dashboard.Handler(), nil, nil))
				sourcePath := e.privatePath("surface-source")
				if scope == "private" {
					surfaceAuthor(t, h, layoutE2EWorkflow("surface-source", "Committed original 原始 🚀"))
				} else {
					surfaceAuthor(t, h, layoutE2EWorkflow("author-source", "Committed shared 原始 🚀"))
					require.NoError(t, ds.workflowSvc.Publish(ctx, "author-source", "surface-source", workflow.VisibilityPublic, ""))
					sourcePath = e.sharedPath("surface-source")
				}
				saved := layoutE2EManifest(t, sourcePath)
				row := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), time.Minute)
				require.Equal(t, layout.StatePassed, row.State, "%+v", row.Findings)
				id := surfaceSnapshot(t, ds, saved)
				view := surfaceView(t, h, "/api/v1/workflows/surface-source/layout")
				historical := surfaceView(t, h, "/api/v1/instances/"+id.String()+"/layout")
				require.NotNil(t, view.Geometry)
				require.NotNil(t, historical.Geometry)
				require.Equal(t, layout.GeometryDigest(*view.Geometry), layout.GeometryDigest(*historical.Geometry))
				ds.layout.worker.Stop() // no recovery sweep may win the first browser read
				require.NoError(t, os.Remove(filepath.Join(filepath.Dir(sourcePath), "layout.json")))

				var mu sync.Mutex
				var polls []layoutPollObservation
				currentHandler := h
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					handler := currentHandler
					mu.Unlock()
					rec := httptest.NewRecorder()
					at := time.Now()
					handler.ServeHTTP(rec, r)
					if r.Method == http.MethodGet && r.URL.Path == "/api/v1/workflows/surface-source/layout" {
						mu.Lock()
						polls = append(polls, layoutPollObservation{at, rec.Code, append([]byte(nil), rec.Body.Bytes()...)})
						mu.Unlock()
					}
					for key, values := range rec.Header() {
						w.Header()[key] = values
					}
					w.WriteHeader(rec.Code)
					_, _ = w.Write(rec.Body.Bytes())
				}))
				t.Cleanup(server.Close)
				observed := func() []layoutPollObservation {
					mu.Lock()
					defer mu.Unlock()
					return append([]layoutPollObservation(nil), polls...)
				}
				page, instancePage := surfaceBrowser(t), surfaceBrowser(t)
				surfaceReady(t, page, server.URL+"/workflows/surface-source", "Layout reviewed")
				surfaceAssertGeometry(t, page, view, "Layout reviewed")
				surfaceReady(t, instancePage, server.URL+"/instances/"+id.String(), "Layout reviewed")
				before := len(observed())

				candidate := killLayoutPeerWriter(t, e, "TestLayoutSurfaces_RenameWriterProcess", "ARBITRON_SURFACE_RENAME", boundary)
				require.NotEqual(t, saved.SavedRevision(), candidate.Revision)
				targetPath := e.privatePath("surface-renamed")
				if scope == "shared" {
					targetPath = e.sharedPath("surface-renamed")
				}
				require.Equal(t, candidate.Revision, layoutE2ESavedRevision(t, targetPath), "unsettled bytes are genuinely present on disk")
				require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`window.dispatchEvent(new Event('focus'))`)))
				const unavailable = "workflow destination is awaiting durable mutation recovery; retry shortly"
				require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`[...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Could not refresh arrangement status')&&e.textContent.includes('awaiting durable mutation recovery'))`, chromedp.WithPollingTimeout(10*time.Second))))
				surfaceAssertGeometry(t, page, view, unavailable)
				require.Eventually(t, func() bool { return len(observed()) >= before+3 }, 12*time.Second, 20*time.Millisecond)
				during := observed()[before:]
				for _, response := range during {
					require.Equal(t, http.StatusServiceUnavailable, response.code)
					var wire adapthttp.ErrorResponse
					require.NoError(t, json.Unmarshal(response.body, &wire))
					require.Equal(t, "UNAVAILABLE", wire.Code)
					require.Equal(t, unavailable, wire.Error)
				}
				const pollInterval, tolerance = 2 * time.Second, 100 * time.Millisecond
				require.GreaterOrEqual(t, during[1].at.Sub(during[0].at), pollInterval-tolerance)
				require.GreaterOrEqual(t, during[2].at.Sub(during[1].at), 2*pollInterval-tolerance)
				surfaceAssertGeometry(t, instancePage, historical, "Layout reviewed")
				_, err := ds.layout.store.Get(ctx, candidate.Revision)
				require.ErrorIs(t, err, layout.ErrRecordNotFound, "unsettled input cannot be queued by reads")

				// The real commit source decides from registry authority. Restart
				// the real root/worker (same SQLite and files), not a passing fake.
				_, err = ds.layout.commits.CommittedRequests(ctx)
				require.NoError(t, err)
				closeRoot()
				recovered, _ := e.boot(t)
				h = adapthttp.NewRouterFromDeps(recovered.routerDeps(nil, dashboard.Handler(), nil, nil))
				mu.Lock()
				currentHandler = h
				mu.Unlock()
				if boundary == "before-identity" {
					// The mounted page repairs itself on its scheduled backoff GET;
					// no navigation/focus/WebSocket is allowed to rescue polling.
					require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`[...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Layout reviewed')&&!e.textContent.includes('Could not refresh'))`, chromedp.WithPollingTimeout(15*time.Second))))
					surfaceAssertGeometry(t, page, view, "Layout reviewed")
					require.Equal(t, saved.SavedRevision(), layoutE2ESavedRevision(t, sourcePath))
					require.NoFileExists(t, targetPath)
					_, err := recovered.layout.store.Get(ctx, candidate.Revision)
					require.ErrorIs(t, err, layout.ErrRecordNotFound)
				} else {
					current := layoutRenderWaitTerminal(t, recovered, candidate.Revision, time.Minute)
					require.Equal(t, layout.StatePassed, current.State, "%+v", current.Findings)
					next := surfaceView(t, h, "/api/v1/workflows/surface-renamed/layout")
					require.Equal(t, candidate.Revision, next.SavedRevision)
					require.Equal(t, current.ResultGeneration, next.ResultGeneration)
					result, err := recovered.layout.store.Result(ctx, candidate.Revision, current.ResultGeneration)
					require.NoError(t, err)
					require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
					assertEvidenceOnDisk(t, e, result.Review.Evidence)
					require.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*next.Geometry))
					require.NoFileExists(t, sourcePath)
					surfaceReady(t, page, server.URL+"/workflows/surface-renamed", "Layout reviewed")
					surfaceAssertGeometry(t, page, next, "Layout reviewed")
				}
				// An authorized historical snapshot never follows the new name,
				// revision, generation or geometry, even after root replacement.
				after := surfaceView(t, h, "/api/v1/instances/"+id.String()+"/layout")
				require.Equal(t, historical.SavedRevision, after.SavedRevision)
				require.Equal(t, historical.ResultGeneration, after.ResultGeneration)
				require.Equal(t, layout.GeometryDigest(*historical.Geometry), layout.GeometryDigest(*after.Geometry))
				require.NoError(t, chromedp.Do(instancePage, chromedp.Evaluate[chromedp.Void](`window.dispatchEvent(new Event('focus'))`)))
				surfaceAssertGeometry(t, instancePage, after, "Layout reviewed")
			})
		}
	}
}
