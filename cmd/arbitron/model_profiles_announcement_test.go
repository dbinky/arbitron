package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelProfilesAnnouncementInstalledBeforeStartupRoot(t *testing.T) {
	var received atomic.Int32
	const rawURL = "https://teams.example.test/incoming?token=private-setting"
	deps := ModelDependencies{AnnouncementTransport: catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || r.URL.String() != rawURL || !strings.Contains(string(raw), rootOld) || strings.Contains(string(raw), "lifecycle-key") || strings.Contains(string(raw), "private-setting") {
			return nil, fmt.Errorf("invalid announcement inputs")
		}
		received.Add(1)
		return lifecycleResponse(r, ""), nil
	})}
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	ds, _, intervals := buildLifecycleRootWithDependencies(t, "profiles: []\n", leaf, deps)
	ctx := lifecycleConnection(t, ds)
	url := rawURL
	_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{TeamsWebhookURL: &url})
	require.NoError(t, err)
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	assert.EqualValues(t, 1, received.Load(), "first no-follower discovery reaches the final sender before serving")
	var state string
	require.NoError(t, ds.db.QueryRow(`SELECT state FROM model_announcement_events`).Scan(&state))
	assert.Equal(t, "announced", state)
}

func TestModelProfilesCloseJoinsManualAndTickerRoot(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			leaf := &lifecycleHTTP{source: []string{rootOld}}
			ds, ticks, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
			ctx := lifecycleConnection(t, ds)
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			entered := make(chan struct{})
			leaf.mu.Lock()
			leaf.entered = entered
			leaf.mu.Unlock()
			done := make(chan struct{})
			if manual {
				call := mcpToolCaller(t, ctx, ds)
				go func() { defer close(done); res, _ := call("model_catalog_refresh", nil); assert.True(t, res.IsError) }()
			} else {
				rootTick(t, ticks)
				close(done)
			}
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("refresh did not reach source")
			}
			closed := make(chan struct{})
			go func() { ds.Close(context.Background()); close(closed) }()
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown did not cancel and join work")
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("manual request outlived Close")
			}
			select {
			case <-leaf.tickerStopped:
			default:
				t.Fatal("owned ticker was not stopped before database close")
			}
			assert.Error(t, ds.db.Ping())
		})
	}
}

// Cancellation alone does not prove a join: hold the actual sender open after
// cancellation, and require storage to stay available until it returns.
func TestModelProfilesCloseJoinsAnnouncementSenderRoot(t *testing.T) {
	for _, entry := range []string{"startup", "manual", "ticker"} {
		t.Run(entry, func(t *testing.T) {
			const destination = "https://teams.example.test/incoming?token=private-setting"
			entered := make(chan error, 1)
			cancelled, release := make(chan struct{}), make(chan struct{})
			exited := make(chan error, 1)
			var releaseOnce sync.Once
			allowExit := func() { releaseOnce.Do(func() { close(release) }) }
			var ds *domainStack
			wantModel := rootOld
			if entry != "startup" {
				wantModel = rootNew
			}
			transport := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				raw, err := io.ReadAll(r.Body)
				if err != nil || r.Method != http.MethodPost || r.URL.String() != destination || !strings.Contains(string(raw), wantModel) || strings.Contains(string(raw), "private-setting") || strings.Contains(string(raw), "lifecycle-key") {
					entered <- fmt.Errorf("announcement lost its production inputs")
					return nil, fmt.Errorf("invalid announcement inputs")
				}
				entered <- nil
				<-r.Context().Done()
				close(cancelled)
				<-release
				// This executes on the real sender stack before it returns. An
				// early database close cannot be hidden by a later assertion.
				exited <- ds.db.Ping()
				return nil, r.Context().Err()
			})
			leaf := &lifecycleHTTP{source: []string{rootOld}}
			built, ticks, intervals := buildLifecycleRootWithDependencies(t, "profiles: []\n", leaf, ModelDependencies{AnnouncementTransport: transport})
			ds = built
			t.Cleanup(allowExit) // release before the root fixture's Close cleanup
			ctx := lifecycleConnection(t, ds)
			if entry != "startup" {
				require.NoError(t, ds.Start(ctx))
				rootTicker(t, intervals)
				leaf.mu.Lock()
				leaf.source = append(leaf.source, rootNew)
				leaf.mu.Unlock()
			}
			url := destination
			_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{TeamsWebhookURL: &url})
			require.NoError(t, err)
			finished := make(chan struct{})
			switch entry {
			case "startup":
				go func() {
					defer close(finished)
					assert.ErrorIs(t, ds.Start(ctx), context.Canceled)
				}()
			case "manual":
				call := mcpToolCaller(t, ctx, ds)
				go func() {
					defer close(finished)
					res, raw := call("model_catalog_refresh", nil)
					assert.False(t, res.IsError, "committed refresh still reports its result: %s", raw)
				}()
			case "ticker":
				rootTick(t, ticks)
				close(finished)
			}
			select {
			case err := <-entered:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("refresh never delivered its durable event to Teams")
			}
			var inFlight int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events WHERE state='in_flight'`).Scan(&inFlight))
			require.Equal(t, 1, inFlight)
			closed := make(chan struct{})
			go func() { ds.Close(context.Background()); close(closed) }()
			select {
			case <-cancelled:
			case <-time.After(5 * time.Second):
				t.Fatal("Close never cancelled the announcement request")
			}
			select {
			case <-closed:
				t.Fatal("Close returned before the sender exited")
			case <-time.After(50 * time.Millisecond):
			}
			require.NoError(t, ds.db.Ping(), "database must remain open while the cancelled sender unwinds")
			allowExit()
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("Close never joined the released announcement sender")
			}
			select {
			case err := <-exited:
				require.NoError(t, err, "sender exited before database closure")
			default:
				t.Fatal("sender outlived Close")
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("startup or manual caller outlived Close")
			}
			if entry != "startup" {
				select {
				case <-leaf.tickerStopped:
				default:
					t.Fatal("ticker outlived Close")
				}
			}
			require.Error(t, ds.db.Ping(), "Close must finally close storage")
		})
	}
}

// The root's test seam replaces only HTTP I/O: the URL guard still wraps the
// announcement client, so a destination that reached storage unvalidated (a
// loopback literal written past the settings service) is never dialled.
func TestModelProfilesAnnouncementClientKeepsURLGuardRoot(t *testing.T) {
	var received atomic.Int32
	deps := ModelDependencies{AnnouncementTransport: catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		received.Add(1)
		return lifecycleResponse(r, ""), nil
	})}
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	ds, _, intervals := buildLifecycleRootWithDependencies(t, "profiles: []\n", leaf, deps)
	ctx := lifecycleConnection(t, ds)
	_, err := ds.db.Exec(`UPDATE system_settings SET value=? WHERE key='model_announcements.teams_webhook_url'`, "https://127.0.0.1/incoming")
	require.NoError(t, err)
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	assert.Zero(t, received.Load(), "the guard refuses a loopback destination before the transport runs")
	var state string
	require.NoError(t, ds.db.QueryRow(`SELECT state FROM model_announcement_events`).Scan(&state))
	assert.NotEqual(t, "announced", state)
}
