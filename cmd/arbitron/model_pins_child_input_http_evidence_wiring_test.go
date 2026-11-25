package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Paired row 5 (run pins ↔ HTTP evidence) on the child-input consumer: the
// spawn_workflow preparation sends a ChatRequest without RequireExecutedModel,
// so only the pinned binding from the root's PinningResolver makes the
// per-binding openrouter / anthropicapi adapter demand the response model. A
// fallback-served or model-less reply must fail the spawn before any child is
// created; dropping binding.Pinned turns those cases green.
func TestModelPinsChildInputHTTPEvidenceRoot(t *testing.T) {
	for _, protocol := range []struct{ provider, model string }{
		{"openrouter", "deepseek/deepseek-v4-flash"},
		{"anthropic-api", "claude-sonnet-4-6"},
	} {
		model := protocol.model
		native := protocol.provider == "anthropic-api"
		for _, actual := range []string{"exact", "fallback", "missing"} {
			t.Run(protocol.provider+"/"+actual, func(t *testing.T) {
				responseModel := map[string]string{"exact": model, "fallback": "fallback", "missing": ""}[actual]
				var mu sync.Mutex
				var sent []string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Model  string `json:"model"`
						Stream bool   `json:"stream"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Stream {
						http.Error(w, "invalid execution inputs", http.StatusBadRequest)
						return
					}
					mu.Lock()
					sent = append(sent, body.Model)
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					if native {
						fmt.Fprintf(w, `{"type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"{\"topic\":\"x\"}"}]}`, responseModel)
						return
					}
					fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"{\"topic\":\"x\"}"}}]}`, responseModel)
				}))
				t.Cleanup(upstream.Close)
				ds := bootSinkRoot(t, "", func(workspace string) {
					writeWorkflow(t, workspace, "evidence-child", `{"name":"evidence-child","description":"child","inputs":[{"name":"topic","type":"string","description":"topic","required":true}],"nodes":[{"id":"c1","type":"transform","entrypoint":true,"config":{"operations":[{"variable":"done","operation":"set","value":true}]}}],"edges":[]}`)
					writeWorkflow(t, workspace, "evidence-parent", `{"name":"evidence-parent","description":"spawn with prepared inputs","inputs":[],"nodes":[{"id":"spawn-1","type":"spawn_workflow","entrypoint":true,"config":{"childWorkflow":"evidence-child","spawnMode":"fire_and_forget","model_profile":"proof"}}],"edges":[]}`)
				})
				ctx := adminCtxForLocalUser(t, ds)
				// Same loopback allowance as TestUsabilityHTTPExactModelRoot; runtime
				// resolution is unchanged.
				crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
				require.NoError(t, err)
				conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
				require.NoError(t, conns.Create(ctx, provider.Connection{Name: "http-api", Provider: protocol.provider, BaseURL: upstream.URL}, "http-root-secret"))
				_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "proof", Kind: provider.ProfileKindChat, ConnectionRef: "http-api", Model: model})
				require.NoError(t, err)

				id, _, err := ds.eng.Trigger(ctx, "evidence-parent", map[string]any{}, "normal")
				require.NoError(t, err)
				saved := waitTerminal(t, ds, id)
				assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
				assert.Equal(t, map[string]string{"proof": model}, saved.ModelPins)
				mu.Lock()
				assert.Equal(t, []string{model}, sent, "child-input preparation must send the pinned model exactly once")
				mu.Unlock()

				if actual == "exact" {
					require.Equal(t, instance.StateCompleted, saved.State)
					require.Len(t, childrenOf(t, ds, id), 1, "verified preparation must spawn the child")
					return
				}
				require.Equal(t, instance.StateFailed, saved.State, "unverified child inputs were accepted")
				assert.Empty(t, childrenOf(t, ds, id), "an unverified preparation must not create a child")
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
				assert.Contains(t, fmt.Sprint(trace[0].Error), "model execution evidence")
				assert.False(t, strings.Contains(fmt.Sprint(trace[0].Output), "topic"), "unverified output reached the node row")
			})
		}
	}
}
