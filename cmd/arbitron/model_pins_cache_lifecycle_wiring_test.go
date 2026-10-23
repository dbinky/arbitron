package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Drive the real root's release callbacks, not a test-installed callback. The
// HTTP gate proves a non-empty persisted pin was cached before each transition.
func TestModelPinsWiringTerminalCacheLifecycle(t *testing.T) {
	for _, outcome := range []string{"completed", "failed", "cancelled", "paused"} {
		t.Run(outcome, func(t *testing.T) {
			arrived := make(chan struct{}, 2)
			proceed := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(proceed) }) }
			defer release()
			var mu sync.Mutex
			var models []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string                     `json:"model"`
					Stream   bool                       `json:"stream"`
					Messages []struct{ Content string } `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != lifecycleModelX || body.Stream || r.Method != http.MethodPost || len(body.Messages) == 0 || body.Messages[0].Content == "" || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer cache-root-secret" {
					http.Error(w, "missing execution inputs", http.StatusBadRequest)
					return
				}
				mu.Lock()
				models = append(models, body.Model)
				mu.Unlock()
				arrived <- struct{}{}
				select {
				case <-proceed:
				case <-r.Context().Done():
					return
				}
				actual := body.Model
				if outcome == "failed" {
					actual = "fallback"
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"cache lifecycle output"}}]}`, actual)
			}))
			t.Cleanup(upstream.Close)
			ds := bootSinkStack(t)
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			conns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
			require.NoError(t, conns.Create(ctx, provider.Connection{Name: "cache-sink", Provider: "openrouter", BaseURL: upstream.URL}, "cache-root-secret"))
			_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "cache-sink", Model: lifecycleModelX})
			require.NoError(t, err)
			id, _, err := ds.eng.Trigger(ctx, "pin-llm", nil, "normal")
			require.NoError(t, err)
			select {
			case <-arrived:
			case <-time.After(20 * time.Second):
				t.Fatal("the pinned invocation never reached the HTTP sink")
			}
			require.True(t, ds.pinLookup.Cached(id))
			assertLifecyclePins(t, ds, id, lifecycleModelX)
			// Cache cleanup must never change either the live model or the run's
			// original immutable map. A retry/reload still obtains X, not Y.
			_, _, err = ds.profileSvc.Update(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "cache-sink", Model: lifecycleModelY})
			require.NoError(t, err)
			switch outcome {
			case "cancelled":
				require.NoError(t, ds.eng.Cancel(ctx, id))
			case "paused":
				require.NoError(t, ds.eng.Pause(ctx, id))
			}
			release()
			ds.eng.Shutdown() // include terminal callback completion, not just its SQL write
			saved, err := ds.instanceRepo.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, instance.State(outcome), saved.State)
			assert.Equal(t, outcome == "paused", ds.pinLookup.Cached(id), "terminal runs release pins; paused runs retain them")
			assertLifecyclePins(t, ds, id, lifecycleModelX)
			trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
			require.NoError(t, err)
			wantNodes := 1
			if outcome == "completed" {
				wantNodes = 2
			} else if outcome == "cancelled" {
				// Cancellation interrupts the in-flight call before its node record is persisted.
				wantNodes = 0
			}
			require.Len(t, trace, wantNodes)
			for _, node := range trace {
				if outcome == "completed" || outcome == "paused" {
					assert.Equal(t, instance.NodeStatusSucceeded, node.Status)
					assert.Contains(t, fmt.Sprint(node.Output), "cache lifecycle output")
				} else {
					assert.Equal(t, instance.NodeStatusFailed, node.Status)
					assert.NotContains(t, fmt.Sprint(node.Output, node.LLMConversation), "cache lifecycle output")
				}
			}
			mu.Lock()
			seen := append([]string(nil), models...)
			mu.Unlock()
			wantModels := []string{lifecycleModelX}
			if outcome == "completed" {
				wantModels = append(wantModels, lifecycleModelX)
			}
			assert.Equal(t, wantModels, seen)
			pins, err := ds.pinLookup.Lookup(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"pin-alias": lifecycleModelX}, pins.Models)
		})
	}
}
