package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayoutHTTPE2E_ImmediatePeerCrashRead(t *testing.T) {
	for _, boundary := range []string{"before-proof", "after-proof"} {
		for _, first := range []string{"manifest", "layout", "raw"} {
			t.Run(boundary+"/"+first, func(t *testing.T) {
				e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
				ds, _ := e.boot(t)
				ctx := adminCtxForLocalUser(t, ds)
				require.NoError(t, ds.workflowSvc.Create(ctx, "source", layoutE2EWorkflow("source", "Committed old graph")))
				require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "target", workflow.VisibilityPublic, ""))
				oldRev := layoutE2ESavedRevision(t, e.sharedPath("target"))
				layoutE2EWaitTerminal(t, ds, oldRev)
				ds.layout.worker.Stop() // first HTTP read, not a recovery sweep, must settle the peer's bytes
				router := newLayoutE2ERouter(t, ds)
				timeout, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(timeout, os.Args[0], "-test.run=^TestLayoutE2E_CrashProcess$")
				cmd.Env = append(os.Environ(), "ARBITRON_LAYOUT_CRASH_CONFIG="+e.configPath, "ARBITRON_LAYOUT_CRASH_STAGE=shared/"+boundary)
				cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
				require.NoError(t, cmd.Start())
				t.Cleanup(func() {
					if cmd.ProcessState == nil {
						cmd.Process.Kill()
						cmd.Wait()
					}
				})
				require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "crash-ready")); return err == nil }, 10*time.Second, 10*time.Millisecond)
				data, err := os.ReadFile(filepath.Join(e.root, "captured.json"))
				require.NoError(t, err)
				var captured workflow.ArrangementRequest
				require.NoError(t, json.Unmarshal(data, &captured))
				require.NotEqual(t, oldRev, captured.Revision)
				require.NoError(t, cmd.Process.Kill())
				require.Error(t, cmd.Wait())
				want := oldRev
				if boundary == "after-proof" {
					want = captured.Revision
				}
				paths := []string{"/api/v1/workflows/target", "/api/v1/workflows/target/layout"}
				switch first {
				case "layout":
					paths[0], paths[1] = paths[1], paths[0]
				case "raw": // the raw file route must settle too, never serve the staged candidate
					paths[0] = "/api/v1/workflows/target/files/workflow.json"
				}
				for _, path := range paths {
					status, body := layoutE2EDo(t, router, http.MethodGet, path, "")
					require.Equal(t, http.StatusOK, status, "%v", body)
					if path != "/api/v1/workflows/target/layout" {
						assert.Equal(t, want, body["presentation"].(map[string]any)["revision"])
					} else {
						assert.Equal(t, want, body["savedRevision"])
					}
				}
				entries, err := ds.layout.commits.CommittedRequests(ctx)
				require.NoError(t, err)
				found := false
				for _, entry := range entries {
					if entry.Revision == captured.Revision {
						found = true
						assert.Equal(t, captured.Manifest, entry.Manifest)
					}
				}
				assert.Equal(t, boundary == "after-proof", found)
			})
		}
	}
}

// Exercise authoring while a durable relocation is pending. Actual Service.Rename
// peer-crash visibility is covered by TestLayoutHTTPE2E_RenamePeerCrash.
func TestLayoutHTTPE2E_PendingRelocationReadAndSave(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "http-pending", queryE2EWorkflow("http-pending", "Committed graph")))
	rev := manifestRevisionOnDisk(t, ds, "http-pending")
	layoutE2EWaitTerminal(t, ds, rev)
	ds.layout.worker.Stop()
	saved, err := ds.workflowSvc.Load(ctx, "http-pending")
	require.NoError(t, err)
	intent, err := ds.layout.journal.BeginRelocation(ctx, workflow.Private(localDefaultUser), "http-pending", workflow.Private(localDefaultUser), "http-destination", saved.ID)
	require.NoError(t, err)
	router := newLayoutE2ERouter(t, ds)
	body, err := json.Marshal(queryE2EWorkflow("http-pending", "Must not commit"))
	require.NoError(t, err)
	base := "/api/v1/workflows/http-pending"
	// Manual layout writes are absent even while a durable mutation is pending.
	removed := httptest.NewRecorder()
	router.ServeHTTP(removed, httptest.NewRequest(http.MethodPut, base+"/layout", nil))
	require.Equal(t, http.StatusMethodNotAllowed, removed.Code)
	require.Contains(t, removed.Header().Get("Allow"), http.MethodGet)
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, base, ""},
		{http.MethodGet, base + "/layout", ""},
		{http.MethodPost, base + "/layout/retry", `{"expectedRevision":"` + rev + `"}`},
		{http.MethodPut, base, string(body)},
		{http.MethodPost, "/api/v1/workflows/", string(body)},
		{http.MethodDelete, base, ""},
		{http.MethodPost, base + "/validate", ""},
		{http.MethodPost, base + "/trigger", `{}`},
		{http.MethodPost, base + "/publish", `{"publishedName":"http-published"}`},
		{http.MethodPut, base + "/files/support.txt", "new support"},
		{http.MethodDelete, base + "/files/support.txt", ""},
	} {
		status, got := layoutE2EDo(t, router, req.method, req.path, req.body)
		assert.Equal(t, http.StatusServiceUnavailable, status, "%s %s: %v", req.method, req.path, got)
		assert.Equal(t, "UNAVAILABLE", got["code"])
		assert.Equal(t, "workflow destination is awaiting durable mutation recovery; retry shortly", got["error"])
	}
	assert.Equal(t, rev, manifestRevisionOnDisk(t, ds, "http-pending"))
	// Registry authority still points to the source: actual settlement clears the intent.
	_, err = ds.layout.commits.CommittedRequests(ctx)
	require.NoError(t, err)
	intents, err := ds.layout.journal.RelocationIntents(ctx)
	require.NoError(t, err)
	assert.NotContains(t, intents, intent)
	status, got := layoutE2EDo(t, router, http.MethodGet, base, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, rev, got["presentation"].(map[string]any)["revision"])
}

func TestLayoutHTTPE2E_PendingSharedAuthoring(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "source", queryE2EWorkflow("source", "Committed")))
	require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "http-shared", workflow.VisibilityPublic, ""))
	wf := layoutE2EManifest(t, e.sharedPath("http-shared"))
	layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
	ds.layout.worker.Stop()
	_, err := ds.layout.journal.BeginRelocation(ctx, workflow.Shared(), "http-shared", workflow.Shared(), "http-moved", wf.ID)
	require.NoError(t, err)
	router := newLayoutE2ERouter(t, ds)
	for _, req := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/workflows/http-shared/copy", `{"newName":"http-copy"}`},
		{http.MethodPut, "/api/v1/workflows/http-shared/visibility", `{"visibility":"public"}`},
	} {
		status, got := layoutE2EDo(t, router, req.method, req.path, req.body)
		assert.Equal(t, http.StatusServiceUnavailable, status, "%v", got)
		assert.Equal(t, "UNAVAILABLE", got["code"])
		assert.Equal(t, "workflow destination is awaiting durable mutation recovery; retry shortly", got["error"])
	}
	require.NoFileExists(t, e.privatePath("http-copy"))
}
