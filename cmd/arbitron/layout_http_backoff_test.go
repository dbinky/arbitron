package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/dashboard"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

// Observe the actual router response, without supplying status/geometry or
// replacing authorization, settlement, SQLite, the client, or either route.
type layoutPollObservation struct {
	at   time.Time
	code int
	body []byte
}

func TestLayoutSurfaces_HTTPRecoveryRetainsGraphAndBacksOff(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := e.boot(t)
	h := adapthttp.NewRouterFromDeps(ds.routerDeps(nil, dashboard.Handler(), nil, nil))
	const name = "http-backoff-surface"
	surfaceAuthor(t, h, layoutE2EWorkflow(name, "Retained across real recovery"))
	saved := layoutE2EManifest(t, e.privatePath(name))
	rev := saved.SavedRevision()
	row := layoutRenderWaitTerminal(t, ds, rev, time.Minute)
	require.Equal(t, layout.StatePassed, row.State)
	id := surfaceSnapshot(t, ds, saved)
	cache := layoutCacheFile(t, filepath.Dir(e.privatePath(name)))
	ds.layout.worker.Stop() // only explicit settlement below may clear the intent
	// Exclude the disposable cache as a source of outage geometry: retention
	// must come from the previously fetched view in the mounted real hook.
	require.NoError(t, os.Remove(cache))

	var mu sync.Mutex
	observations := map[string][]layoutPollObservation{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/layout") && !strings.HasSuffix(r.URL.Path, "/layout/retry") {
			h.ServeHTTP(w, r)
			return
		}
		at := time.Now()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		mu.Lock()
		key := r.Method + " " + r.URL.Path
		observations[key] = append(observations[key], layoutPollObservation{at, rec.Code, append([]byte(nil), rec.Body.Bytes()...)})
		mu.Unlock()
		for key, values := range rec.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(s.Close)
	responses := func(method, path string) []layoutPollObservation {
		mu.Lock()
		defer mu.Unlock()
		return append([]layoutPollObservation(nil), observations[method+" "+path]...)
	}
	polls := func(path string) []layoutPollObservation { return responses(http.MethodGet, path) }

	for _, subject := range []string{"workflow", "instance"} {
		t.Run(subject, func(t *testing.T) {
			route := "/workflows/" + name
			if subject == "instance" {
				route = "/instances/" + id.String()
			}
			path := "/api/v1" + route + "/layout"
			view := surfaceView(t, h, path)
			require.Equal(t, rev, view.SavedRevision)
			require.NotNil(t, view.Geometry)
			page := surfaceBrowser(t)
			surfaceReady(t, page, s.URL+route, "Layout reviewed")
			surfaceAssertGeometry(t, page, view, "Layout reviewed")
			before := len(polls(path))

			var restore func()
			if subject == "workflow" {
				_, err := ds.layout.journal.BeginRelocation(context.Background(), workflow.Private(localDefaultUser), name, workflow.Private(localDefaultUser), "uncommitted-target", saved.ID)
				require.NoError(t, err)
				restore = func() {
					_, err := ds.layout.commits.CommittedRequests(context.Background())
					require.NoError(t, err)
				}
			} else {
				// A real recoverable SQL outage, not a fabricated LayoutView.
				_, err := ds.db.Exec("ALTER TABLE layout_revisions RENAME TO unavailable_layout_revisions")
				require.NoError(t, err)
				restore = func() {
					_, err := ds.db.Exec("ALTER TABLE unavailable_layout_revisions RENAME TO layout_revisions")
					require.NoError(t, err)
				}
			}
			defer func() {
				if restore != nil {
					restore()
				}
			}()
			// Terminal healthy rows do not poll; the real focus listener starts
			// the first refresh, then only the hook's own backoff timer runs.
			require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`window.dispatchEvent(new Event('focus'))`)))
			require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`[...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Could not refresh arrangement status'))`, chromedp.WithPollingTimeout(10*time.Second))))
			problem := "workflow destination is awaiting durable mutation recovery; retry shortly"
			if subject == "instance" {
				problem = "stored status unavailable"
			}
			require.Equal(t, "arranged", surfaceInspect(t, page).Mode, "outage must retain the previously drawn reviewed geometry")
			surfaceAssertGeometry(t, page, view, problem)
			first := polls(path)[before]
			if subject == "workflow" {
				require.Equal(t, http.StatusServiceUnavailable, first.code)
				var wire adapthttp.ErrorResponse
				require.NoError(t, json.Unmarshal(first.body, &wire))
				require.Equal(t, "UNAVAILABLE", wire.Code)
				require.Equal(t, problem, wire.Error)
			} else {
				require.Equal(t, http.StatusOK, first.code)
				var wire layout.View
				require.NoError(t, json.Unmarshal(first.body, &wire))
				require.False(t, wire.Available)
				require.Nil(t, wire.Geometry, "the server must not supply the retained DOM geometry")
				require.Equal(t, rev, wire.SavedRevision)
			}

			// Retry consumes the real non-2xx error/code body, never a mock
			// success, and cannot advance a generation during the outage.
			require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`[...document.querySelectorAll('button')].find(e=>e.textContent==='Retry arrangement').click()`)))
			require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`[...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Retry failed: API 503:'))`, chromedp.WithPollingTimeout(10*time.Second))))
			retries := responses(http.MethodPost, path+"/retry")
			require.Len(t, retries, 1)
			require.Equal(t, http.StatusServiceUnavailable, retries[0].code)
			var retryWire adapthttp.ErrorResponse
			require.NoError(t, json.Unmarshal(retries[0].body, &retryWire))
			require.Equal(t, "UNAVAILABLE", retryWire.Code)
			retryProblem := problem
			if subject == "instance" {
				retryProblem = "internal server error"
			}
			require.Equal(t, retryProblem, retryWire.Error)
			surfaceAssertGeometry(t, page, view, "Retry failed: API 503: "+retryProblem)

			require.Eventually(t, func() bool { return len(polls(path)) >= before+3 }, 12*time.Second, 20*time.Millisecond, "both outage forms must continue polling")
			during := polls(path)[before:]
			// Match useLayoutStatus's two-second base and exponential policy;
			// allow browser clock granularity, not a shorter polling policy.
			const pollInterval, tolerance = 2 * time.Second, 100 * time.Millisecond
			require.GreaterOrEqual(t, during[1].at.Sub(during[0].at), pollInterval-tolerance)
			require.GreaterOrEqual(t, during[2].at.Sub(during[1].at), 2*pollInterval-tolerance, "second failure must back off, not poll every two seconds")
			t.Logf("actual outage poll intervals: %s, %s", during[1].at.Sub(during[0].at), during[2].at.Sub(during[1].at))
			restore()
			restore = nil
			// No focus, navigation, manual refresh or worker replay after
			// recovery: the next scheduled GET must repair the actual DOM.
			require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="arranged"]') !== null && [...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Layout reviewed')&&!e.textContent.includes('Could not refresh arrangement status'))`, chromedp.WithPollingTimeout(12*time.Second))))
			surfaceAssertGeometry(t, page, view, "Layout reviewed")
			current, err := ds.layout.store.Get(context.Background(), rev)
			require.NoError(t, err)
			require.Equal(t, row.JobGeneration, current.JobGeneration)
			require.Equal(t, rev, layoutE2ESavedRevision(t, e.privatePath(name)))
		})
	}
}
