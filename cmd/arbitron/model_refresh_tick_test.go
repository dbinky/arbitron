package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshAdvancesBySuccessfulFourHourTick(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	arrived, release := make(chan struct{}), make(chan struct{})
	var first, unblock sync.Once
	requests := make(chan string, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if r.Header.Get("Authorization") != "Bearer tick-key" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model == "" || len(body.Messages) == 0 {
			http.Error(w, "bad input", 400)
			return
		}
		if body.Messages[len(body.Messages)-1].Content == "hello" {
			requests <- body.Model
			first.Do(func() {
				close(arrived)
				select {
				case <-release:
				case <-r.Context().Done():
				}
			})
		}
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"tick-ok"},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer upstream.Close()
	defer unblock.Do(func() { close(release) })
	ds := bootSinkRoot(t, "profiles: []\n", func(workspace string) {
		writeWorkflow(t, workspace, "tick-exec", `{"name":"tick-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}},{"id":"after","type":"llm_call","config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[{"source":"ask","target":"after"}]}`)
		writePinTemplate(t, workspace, "tick-exec")
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "tick", Provider: "openrouter", BaseURL: upstream.URL}, "tick-key"))
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC).Unix())
	deps := ds.refreshDeps
	deps.Clock = func() time.Time { return time.Unix(now.Load(), 0) }
	fetch := &fixtureFetcher{body: fmt.Sprintf(`{"openrouter":{"id":"openrouter","models":{%q:{"id":%q}}}}`, oldModel, oldModel)}
	deps.Fetcher = fetch
	ticks := make(chan time.Time)
	started := make(chan time.Duration, 1)
	deps.NewTicker = func(d time.Duration) (<-chan time.Time, func()) { started <- d; return ticks, func() {} }
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	ds.profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: provider.ProfileKindChat, ConnectionRef: "tick", Model: oldModel, SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}})
	require.NoError(t, err)
	require.NoError(t, refresh.startupPass(context.Background()))
	refresh.startScheduled(context.Background())
	select {
	case d := <-started:
		assert.Equal(t, 4*time.Hour, d)
	case <-time.After(time.Second):
		t.Fatal("root did not construct the injected ticker")
	}
	old, _, err := ds.eng.Trigger(ctx, "tick-exec", nil, "normal")
	require.NoError(t, err)
	select {
	case <-arrived:
	case <-time.After(time.Second):
		t.Fatal("old invocation did not start")
	}
	fetch.mu.Lock()
	fetch.body = refreshCatalogFixture
	fetch.mu.Unlock()
	now.Add(int64((4 * time.Hour) / time.Second))
	select {
	case ticks <- deps.Clock():
	case <-time.After(time.Second):
		t.Fatal("loop did not consume tick")
	}
	require.Eventually(t, func() bool { p, e := ds.profileSvc.Get(ctx, "worker"); return e == nil && p.Model == newModel }, time.Second, time.Millisecond)
	unblock.Do(func() { close(release) })
	assert.Equal(t, instance.StateCompleted, waitTerminal(t, ds, old).State)
	fresh, _, err := ds.eng.Trigger(ctx, "tick-exec", nil, "normal")
	require.NoError(t, err)
	assert.Equal(t, instance.StateCompleted, waitTerminal(t, ds, fresh).State)
	assert.Equal(t, []string{oldModel, oldModel, newModel, newModel}, []string{<-requests, <-requests, <-requests, <-requests})
	view, err := ds.catalogStore.Snapshot(context.Background())
	require.NoError(t, err)
	// The stored report time round-trips as UTC; compare instants, not zones.
	assert.True(t, deps.Clock().Equal(view.Reports[len(view.Reports)-1].At), "report at %v, tick at %v", view.Reports[len(view.Reports)-1].At, deps.Clock())
}
