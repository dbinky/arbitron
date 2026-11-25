package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A live deployment edit must not turn an immutable model pin into a request to
// an opaque deployment. Only HTTP is substituted; profiles, pins and selection
// are produced by initDomain over migrated SQLite.
func TestModelPinsWiringRejectsIncompatibleLiveRoute(t *testing.T) {
	for _, tc := range []struct {
		route       string
		interactive bool
	}{
		{"azure-openai", false}, {"azure-openai", true},
		{"bedrock", false}, {"bedrock", true},
	} {
		interactive := tc.interactive
		t.Run(fmt.Sprintf("%s/interactive=%v", tc.route, interactive), func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string                     `json:"model"`
					Stream   bool                       `json:"stream"`
					Messages []struct{ Content string } `json:"messages"`
				}
				decodeErr := json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				seen = append(seen, r.URL.Path+":"+body.Model)
				mu.Unlock()
				validAuth := r.Header.Get("Authorization") == "Bearer route-secret" || r.Header.Get("api-key") == "route-secret"
				if decodeErr != nil || body.Model == "" || body.Stream != interactive || !validAuth || len(body.Messages) == 0 || body.Messages[0].Content == "" {
					http.Error(w, "missing execution inputs", http.StatusBadRequest)
					return
				}
				if interactive {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"route output\"}}]}\n\ndata: [DONE]\n\n", body.Model)
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"route output"}}]}`, body.Model)
				}
			}))
			t.Cleanup(upstream.Close)
			requests := func() []string {
				mu.Lock()
				defer mu.Unlock()
				return append([]string(nil), seen...)
			}
			kind := "llm_call"
			if interactive {
				kind = "interactive_llm"
			}
			ds := bootSinkRoot(t, "", func(dir string) {
				writeWorkflow(t, dir, "pin-route", fmt.Sprintf(`{"name":"pin-route","nodes":[{"id":"ask","type":%q,"entrypoint":true,"config":{"model_profile":"pin-alias","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`, kind))
				writePinTemplate(t, dir, "pin-route")
				require.NoError(t, os.WriteFile(filepath.Join(dir, "users", localDefaultUser.String(), "pin-route", "templates", "o.md"), []byte("orchestrate"), 0o600))
			})
			waitWaiting := func(id uuid.UUID) {
				require.Eventually(t, func() bool {
					saved, err := ds.instanceRepo.Get(context.Background(), id)
					return err == nil && saved.State == instance.StateWaiting
				}, 20*time.Second, 50*time.Millisecond)
			}
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
			for name, route := range map[string]string{"native": "openrouter", "deployment": tc.route} {
				require.NoError(t, conns.Create(ctx, provider.Connection{Name: name, Provider: route, BaseURL: upstream.URL, Extras: map[string]string{"api-version": "2024-10-21", "region": "us-east-1", "access-key-id": "route-access"}}, "route-secret"))
			}
			profile := provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "native", Model: lifecycleModelX}
			_, _, err = ds.profileSvc.Create(ctx, profile)
			require.NoError(t, err)
			if !interactive {
				// Hold queue capacity so the live edit happens after capture but
				// before the first actual resolution of this run.
				ds.queueMgr.UpdateConfig(1, 1)
				require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
					ID: uuid.New(), OwnerID: localDefaultUser, WorkflowName: "holder",
					State: instance.StateRunning, Priority: instance.PriorityNormal,
					WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
					ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
				}))
			}
			id, started, err := ds.eng.Trigger(ctx, "pin-route", nil, "normal")
			require.NoError(t, err)
			if interactive {
				waitWaiting(id)
			} else {
				require.False(t, started)
			}
			assertLifecyclePins(t, ds, id, lifecycleModelX)
			before := requests()
			var transcript string
			if interactive {
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				transcript = fmt.Sprint(trace[0].LLMConversation)
				require.Contains(t, transcript, "route output")
			}
			profile.ConnectionRef, profile.Model = "deployment", "mutable-deployment"
			storeProfileUnchecked(t, ctx, ds, profile)
			if interactive {
				assert.ErrorContains(t, ds.conversationHandler.HandleMessage(ctx, id, "must not reach deployment"), "specific model version")
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				assert.Equal(t, transcript, fmt.Sprint(trace[0].LLMConversation))
			} else {
				require.NoError(t, ds.eng.StartInstance(context.Background(), id))
				assert.Equal(t, instance.StateFailed, waitTerminal(t, ds, id).State)
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
				assert.NotContains(t, fmt.Sprint(trace[0].Output), "route output")
			}
			assert.Equal(t, before, requests(), "reject incompatible route before network I/O")

			// Restoring a compatible live route permits the OLD pin X again;
			// only a new run may execute the new profile model Y.
			profile.ConnectionRef, profile.Model = "native", lifecycleModelY
			_, _, err = ds.profileSvc.Update(ctx, profile)
			require.NoError(t, err)
			if interactive {
				require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "compatible again"))
			} else {
				ds.eng.Shutdown()
				require.NoError(t, ds.eng.Retry(ctx, id, nil))
				require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
			}
			assert.Equal(t, append(before, "/chat/completions:"+lifecycleModelX), requests())
			fresh, started, err := ds.eng.Trigger(ctx, "pin-route", nil, "normal")
			require.NoError(t, err)
			if interactive {
				waitWaiting(fresh)
			} else {
				require.False(t, started)
				require.NoError(t, ds.eng.StartInstance(context.Background(), fresh))
				require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, fresh).State)
			}
			assertLifecyclePins(t, ds, fresh, lifecycleModelY)
			assert.Equal(t, append(before, "/chat/completions:"+lifecycleModelX, "/chat/completions:"+lifecycleModelY), requests())
		})
	}
}
