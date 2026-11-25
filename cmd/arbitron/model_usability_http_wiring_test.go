package main

import (
	"bytes"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real composition root consumes persisted connections/profiles/pins. Only
// the upstream HTTP service is substituted; it refuses missing request inputs.
func TestUsabilityHTTPExactModelRoot(t *testing.T) {
	pinSinkRootEnv(t)
	for _, protocol := range []struct {
		provider, model, nextModel string
	}{
		{"openrouter", "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"},
		{"anthropic", "claude-sonnet-4-6", "claude-sonnet-4-7"},
		{"anthropic-api", "claude-sonnet-4-6", "claude-sonnet-4-7"},
	} {
		model := protocol.model
		nativeAnthropic := protocol.provider == "anthropic" || protocol.provider == "anthropic-api"
		for _, interactive := range []bool{false, true} {
			for _, actual := range []string{model, "fallback", ""} {
				t.Run(fmt.Sprintf("provider=%s/interactive=%v/actual=%s", protocol.provider, interactive, actual), func(t *testing.T) {
					t.Parallel()
					var mu sync.Mutex
					var models []string
					actualModel := actual
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var body struct {
							Model    string `json:"model"`
							Stream   bool   `json:"stream"`
							Messages []struct {
								Content string `json:"content"`
							} `json:"messages"`
						}
						validRoute := r.Header.Get("Authorization") == "Bearer http-root-secret" && r.URL.Path == "/chat/completions"
						if nativeAnthropic {
							validRoute = r.Header.Get("x-api-key") == "http-root-secret" && r.Header.Get("anthropic-version") == "2023-06-01" && r.URL.Path == "/v1/messages"
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != model || body.Stream != interactive || len(body.Messages) == 0 || body.Messages[0].Content == "" || !validRoute {
							http.Error(w, "invalid execution inputs", http.StatusBadRequest)
							return
						}
						mu.Lock()
						models = append(models, body.Model)
						responseModel := actualModel
						mu.Unlock()
						if nativeAnthropic {
							if interactive {
								w.Header().Set("Content-Type", "text/event-stream")
								fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"model\":%q}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"verified output\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", responseModel)
							} else {
								w.Header().Set("Content-Type", "application/json")
								fmt.Fprintf(w, `{"type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"verified output"}]}`, responseModel)
							}
						} else if interactive {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprintf(w, "data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"verified output\"}}]}\n\ndata: [DONE]\n\n", responseModel)
						} else {
							w.Header().Set("Content-Type", "application/json")
							fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"verified output"}}]}`, responseModel)
						}
					}))
					t.Cleanup(upstream.Close)
					kind := "llm_call"
					if interactive {
						kind = "interactive_llm"
					}
					ds := bootSinkRoot(t, "", func(workspace string) {
						writeWorkflow(t, workspace, "usability-http", fmt.Sprintf(`{"name":"usability-http","nodes":[{"id":"work","type":%q,"entrypoint":true,"config":{"model_profile":"chat","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`, kind))
						writePinTemplate(t, workspace, "usability-http")
						require.NoError(t, os.WriteFile(filepath.Join(workspace, "users", localDefaultUser.String(), "usability-http", "templates", "o.md"), []byte("orchestrate"), 0o600))
					})
					ctx := adminCtxForLocalUser(t, ds)
					// Match the existing pin-sink tests: permit only the fixture's loopback URL
					// using a repo on the same migrated DB/key; runtime resolution is unchanged.
					crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
					require.NoError(t, err)
					conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
					require.NoError(t, conns.Create(ctx, provider.Connection{Name: "http-api", Provider: protocol.provider, BaseURL: upstream.URL}, "http-root-secret"))
					_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "chat", Kind: provider.ProfileKindChat, ConnectionRef: "http-api", Model: model})
					require.NoError(t, err)
					id, _, err := ds.eng.Trigger(ctx, "usability-http", nil, "normal")
					require.NoError(t, err)
					require.Eventually(t, func() bool {
						saved, e := ds.instanceRepo.Get(ctx, id)
						return e == nil && (saved.State.IsTerminal() || saved.State == instance.StateWaiting)
					}, 20*time.Second, 50*time.Millisecond)
					saved, err := ds.instanceRepo.Get(ctx, id)
					require.NoError(t, err)
					trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
					require.NoError(t, err)
					require.Len(t, trace, 1)
					assert.Equal(t, map[string]string{"chat": model}, saved.ModelPins)
					if actual == model {
						want := instance.StateCompleted
						if interactive {
							want = instance.StateWaiting
						}
						require.Equal(t, want, saved.State, "trace: %+v", trace)
						assert.Equal(t, instance.NodeStatusSucceeded, trace[0].Status)
						accepted := fmt.Sprint(trace[0].Output)
						if interactive {
							accepted = fmt.Sprint(trace[0].LLMConversation)
						}
						assert.Contains(t, accepted, "verified output")
						if interactive {
							// The continuation resolves the live profile again but must still send
							// the original pin. A wrong-model response must not append a transcript.
							_, _, err = ds.profileSvc.Update(ctx, provider.Profile{Alias: "chat", Kind: provider.ProfileKindChat, ConnectionRef: "http-api", Model: protocol.nextModel})
							require.NoError(t, err)
							before := fmt.Sprint(trace[0].LLMConversation)
							require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "follow-up"))
							after, err := ds.nodeExecRepo.ListByInstance(ctx, id)
							require.NoError(t, err)
							assert.NotEqual(t, before, fmt.Sprint(after[0].LLMConversation))
							assert.Contains(t, fmt.Sprint(after[0].LLMConversation), "follow-up")
							acceptedConversation := fmt.Sprint(after[0].LLMConversation)
							mu.Lock()
							actualModel = "fallback"
							mu.Unlock()
							require.ErrorContains(t, ds.conversationHandler.HandleMessage(ctx, id, "rejected turn"), "model execution evidence")
							rejected, err := ds.nodeExecRepo.ListByInstance(ctx, id)
							require.NoError(t, err)
							assert.Equal(t, acceptedConversation, fmt.Sprint(rejected[0].LLMConversation))
						}
					} else {
						require.Equal(t, instance.StateFailed, saved.State, "unverified output accepted: %+v", trace)
						assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
						assert.Contains(t, fmt.Sprint(trace[0].Error), "model execution evidence")
						assert.NotContains(t, fmt.Sprint(trace[0].Output, trace[0].LLMConversation), "verified output")
					}
					mu.Lock()
					gotModels := append([]string(nil), models...)
					mu.Unlock()
					wantModels := []string{model}
					if interactive && actual == model {
						wantModels = append(wantModels, model, model)
					}
					assert.Equal(t, wantModels, gotModels)
				})
			}
		}
	}
}
