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
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A pinned chat-kind continuation whose HTTP stream first names X and streams
// tokens to the user, then names Y (or ends without its terminal frame), must
// fail the turn without recording it: identity is read per frame, before Done,
// and HandleMessage persists only after Done. Reopened sessions and an otherwise
// valid completion sentinel must retain the old conversation/output/cache too.
// The next verified X turn must still succeed.
func TestModelPinsWiringInteractiveHTTPContinuationMidStreamEvidence(t *testing.T) {
	for _, protocol := range []struct {
		provider, model, nextModel string
	}{
		{"openrouter", "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"},
		{"anthropic-api", "claude-sonnet-4-6", "claude-sonnet-4-7"},
	} {
		native := protocol.provider == "anthropic-api"
		model := protocol.model
		stream := func(mode, partial string) string {
			if native {
				start := func(m string) string {
					return fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"model\":%q}}\n\n", m)
				}
				token := func(s string) string {
					return fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", s)
				}
				const stop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				switch mode {
				case "switch":
					return start(model) + token(partial) + start(protocol.nextModel) + token("served-by-y") + stop
				case "truncated":
					return start(model) + token(partial)
				case "complete":
					return start(model) + token(partial) + stop
				}
				return start(model) + token("verified output") + stop
			}
			frame := func(m, s string) string {
				return fmt.Sprintf("data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", m, s)
			}
			const done = "data: [DONE]\n\n"
			switch mode {
			case "switch":
				return frame(model, partial) + frame(protocol.nextModel, "served-by-y") + done
			case "truncated":
				return frame(model, partial)
			case "complete":
				return frame(model, partial) + done
			}
			return frame(model, "verified output") + done
		}

		for _, tc := range []struct {
			mode       string
			restart    bool
			completion bool
		}{
			{mode: "switch"},
			{mode: "truncated"},
			{mode: "switch", restart: true},
			{mode: "truncated", restart: true},
			{mode: "switch", restart: true, completion: true},
		} {
			t.Run(fmt.Sprintf("provider=%s/%s/restart=%t/completion=%t", protocol.provider, tc.mode, tc.restart, tc.completion), func(t *testing.T) {
				var mu sync.Mutex
				var models []string
				serve, partial := "exact", "partial-x "
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Model  string `json:"model"`
						Stream bool   `json:"stream"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Stream {
						http.Error(w, "invalid execution inputs", http.StatusBadRequest)
						return
					}
					mu.Lock()
					models = append(models, body.Model)
					current, text := serve, partial
					mu.Unlock()
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, stream(current, text))
				}))
				t.Cleanup(upstream.Close)

				ds, closeStack := bootSinkRootWithClose(t, "", func(workspace string) {
					writeWorkflow(t, workspace, "pin-http-midstream", `{"name":"pin-http-midstream","nodes":[{"id":"work","type":"interactive_llm","entrypoint":true,"config":{"model_profile":"chat","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`)
					writePinTemplate(t, workspace, "pin-http-midstream")
					require.NoError(t, os.WriteFile(filepath.Join(workspace, "users", localDefaultUser.String(), "pin-http-midstream", "templates", "o.md"), []byte("orchestrate"), 0o600))
				})
				ctx := adminCtxForLocalUser(t, ds)
				crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
				require.NoError(t, err)
				conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
				require.NoError(t, conns.Create(ctx, provider.Connection{Name: "http-api", Provider: protocol.provider, BaseURL: upstream.URL}, "http-root-secret"))
				_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "chat", Kind: provider.ProfileKindChat, ConnectionRef: "http-api", Model: model})
				require.NoError(t, err)

				id, _, err := ds.eng.Trigger(ctx, "pin-http-midstream", nil, "normal")
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					saved, e := ds.instanceRepo.Get(ctx, id)
					return e == nil && (saved.State.IsTerminal() || saved.State == instance.StateWaiting)
				}, 20*time.Second, 50*time.Millisecond)
				saved, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				require.Equal(t, instance.StateWaiting, saved.State)

				// Advance the live profile so a re-resolving continuation would send Y.
				_, _, err = ds.profileSvc.Update(ctx, provider.Profile{Alias: "chat", Kind: provider.ProfileKindChat, ConnectionRef: "http-api", Model: protocol.nextModel})
				require.NoError(t, err)
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				accepted := fmt.Sprint(trace[0].LLMConversation)
				if tc.restart {
					configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
					closeStack()
					reopened, err := initDomain(context.Background(), configPath)
					require.NoError(t, err)
					t.Cleanup(func() { reopened.Close(context.Background()) })
					ds = reopened
					require.False(t, ds.pinLookup.Cached(id), "reopen must start with a cold pin cache")
				}

				text := "partial-x "
				if tc.completion {
					nonce, ok := trace[0].LLMConversation[engine.ConversationKeyNonce].(string)
					require.True(t, ok)
					require.NotEmpty(t, nonce)
					text = fmt.Sprintf(`<arbitron:result:%s>{"summary":"premature"}</arbitron:result:%s>`, nonce, nonce)
				}
				mu.Lock()
				serve, partial = tc.mode, text
				mu.Unlock()
				events, unsubscribe := ds.bus.Subscribe(id)
				err = ds.conversationHandler.HandleMessage(ctx, id, "rejected turn")
				unsubscribe()
				require.ErrorContains(t, err, "model execution evidence")
				assert.NotContains(t, err.Error(), protocol.nextModel, "evidence errors stay free of upstream identity")
				var streamed string
				for ev := range events {
					assert.NotEqual(t, engine.EventTypeInteractiveEnded, ev.Type, "a rejected completion must not end the session")
					if ev.Type == engine.EventTypeInteractiveMessage {
						assert.Equal(t, "user", ev.Data.(map[string]any)["role"], "a rejected turn must not publish assistant success")
					}
					if ev.Type == engine.EventTypeInteractiveToken {
						data, ok := ev.Data.(map[string]any)
						require.True(t, ok)
						streamed += fmt.Sprint(data["token"])
					}
				}
				assert.Equal(t, partial, streamed, "X-verified tokens reached the user; nothing after the identity break did")

				after, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, after, 1)
				assert.Equal(t, accepted, fmt.Sprint(after[0].LLMConversation), "a rejected turn must not be recorded")
				assert.NotContains(t, fmt.Sprint(after[0].LLMConversation), "rejected turn")
				assert.Equal(t, instance.NodeStatusSucceeded, after[0].Status)
				assert.Nil(t, after[0].Output, "a rejected sentinel must not persist completion output")
				assert.True(t, ds.pinLookup.Cached(id), "a failed turn must keep the reloaded pin cache")
				saved, err = ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, instance.StateWaiting, saved.State)
				assert.NotContains(t, saved.Variables, "work", "a rejected sentinel must not publish completion variables")
				assert.Equal(t, map[string]string{"chat": model}, saved.ModelPins)

				// The session is not poisoned: the next clean X turn is recorded.
				mu.Lock()
				serve = "exact"
				if tc.completion {
					serve = "complete" // prove the same nonce/sentinel completes when X is verified
				}
				mu.Unlock()
				require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "accepted follow-up"))
				if tc.completion {
					require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State, "verified completion must advance the real run")
				}
				after, err = ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				assert.Contains(t, fmt.Sprint(after[0].LLMConversation), "accepted follow-up")
				assert.NotContains(t, fmt.Sprint(after[0].LLMConversation), "served-by-y")
				assert.NotContains(t, fmt.Sprint(after[0].LLMConversation), "partial-x")
				if tc.completion {
					assert.Equal(t, map[string]any{"summary": "premature"}, after[0].Output)
					saved, err = ds.instanceRepo.Get(ctx, id)
					require.NoError(t, err)
					assert.Equal(t, instance.StateCompleted, saved.State)
					assert.Equal(t, map[string]any{"summary": "premature"}, saved.Variables["work"])
					assert.False(t, ds.pinLookup.Cached(id), "verified completion releases pins")
				}

				mu.Lock()
				defer mu.Unlock()
				assert.Equal(t, []string{model, model, model}, models, "every turn sends the pin, never the advanced model")
			})
		}
	}
}
