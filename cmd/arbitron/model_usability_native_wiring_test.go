package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This task's sink is the root's exact-model evidence. Full-source scheduling,
// durable observation/restart and advancement consume it in P3T6/P3T7.
func TestUsabilityNativeUnknownTierRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const model = "vendor/native-preview-20261018"
	for _, mode := range []string{modelcatalog.ExecutionModeBatch, modelcatalog.ExecutionModeInteractive} {
		for _, outcome := range []string{"exact", "wrong", "missing", "request echo", "malformed", "failure", "stale config", "changed during call", "wrong mode", "wrong revision"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				var calls atomic.Int32
				var rotate func() error
				rotationResult := make(chan error, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var body struct {
						Model    string                           `json:"model"`
						Stream   bool                             `json:"stream"`
						Messages []struct{ Role, Content string } `json:"messages"`
						Tools    []any                            `json:"tools"`
					}
					if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != model || body.Stream != (mode == modelcatalog.ExecutionModeInteractive) || len(body.Messages) != 1 || body.Messages[0].Content == "" || len(body.Tools) != 0 || r.Header.Get("Authorization") != "Bearer native-proof-secret" {
						http.Error(w, "missing probe input", 400)
						return
					}
					if rotate != nil {
						err := rotate()
						select {
						case rotationResult <- err:
						default:
						}
						if err != nil {
							http.Error(w, "credential rotation failed", 500)
							return
						}
					}
					if outcome == "failure" {
						http.Error(w, "native-proof-secret", 401)
						return
					}
					if outcome == "malformed" {
						fmt.Fprint(w, `{"model":`)
						return
					}
					if outcome == "request echo" {
						_ = json.NewEncoder(w).Encode(body)
						return
					}
					actual := model
					if outcome == "wrong" {
						actual = "fallback"
					}
					if outcome == "missing" {
						actual = ""
					}
					if body.Stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", actual)
					} else {
						fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, actual)
					}
				}))
				defer upstream.Close()
				ds := bootSinkRoot(t, "profiles: []\n", func(ws string) {
					writeWorkflow(t, ws, "native-probe", `{"name":"native-probe","nodes":[{"id":"ask","type":"interactive_llm","entrypoint":true,"config":{"model_profile":"native-chat","templateFile":"templates/p.md"}}],"edges":[]}`)
					writePinTemplate(t, ws, "native-probe")
				})
				ctx := adminCtxForLocalUser(t, ds)
				crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
				require.NoError(t, err)
				conns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
				conn := provider.Connection{Name: "native-proof", Provider: "openrouter", BaseURL: upstream.URL}
				require.NoError(t, conns.Create(ctx, conn, "native-proof-secret"))
				_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "native-chat", Kind: provider.ProfileKindChat, ConnectionRef: conn.Name, Model: "deepseek/deepseek-v4-flash"})
				require.NoError(t, err)
				snap, err := ds.pathInventory.Snapshot(ctx)
				require.NoError(t, err)
				var path modelcatalog.ExecutionPath
				for _, p := range snap.Paths {
					if p.ConnectionRef == conn.Name && p.Mode == mode && p.Backend == "" {
						path = p
						break
					}
				}
				require.NotEmpty(t, path.Key, "real inventory must enumerate an unfollowed connection in this mode")
				rotate = func() error {
					key := "rotated-secret"
					return conns.Update(ctx, conn, &key)
				}
				switch outcome {
				case "stale config":
					require.NoError(t, rotate())
					rotate = nil
				case "wrong mode":
					path.Mode = modelcatalog.ExecutionModeInteractive
					if mode == modelcatalog.ExecutionModeInteractive {
						path.Mode = modelcatalog.ExecutionModeBatch
					}
					rotate = nil
				case "wrong revision":
					path.ConfigRevision = "not-the-inventory-revision"
					rotate = nil
				case "changed during call":
				default:
					rotate = nil
				}
				refresh, err := newModelRefresh(ds.refreshDeps)
				require.NoError(t, err)
				defer refresh.stop()
				entry := modelidentity.Entry{CanonicalID: "openrouter/" + model, Provider: "openrouter", NativeID: model}
				ev, err := refresh.probe.Check(context.Background(), path, entry)
				require.NoError(t, err)
				assert.Equal(t, outcome == "exact", ev.Confirmed(), "only returned execution identity on the unchanged real path can confirm: %+v", ev)
				assert.Equal(t, model, ev.NativeModel)
				assert.Equal(t, path.Key, ev.PathKey)
				assert.Equal(t, path.ConfigRevision, ev.ConfigRevision)
				assert.NotContains(t, ev.ProofRef, "secret")
				if outcome == "changed during call" {
					select {
					case err := <-rotationResult:
						require.NoError(t, err)
					default:
						t.Fatal("probe never reached the credential rotation")
					}
					stored, err := conns.Get(ctx, conn.Name)
					require.NoError(t, err)
					assert.Greater(t, stored.ConfigRevision, int64(1))
				}
				if outcome == "exact" {
					require.NotEmpty(t, ev.ProofRef)
				} else {
					assert.Empty(t, ev.ProofRef)
				}
				if outcome == "stale config" || outcome == "wrong revision" || outcome == "wrong mode" {
					assert.Zero(t, calls.Load(), "mismatched paths must be refused before credential/transport use")
				} else {
					assert.EqualValues(t, 1, calls.Load(), "every non-stale check must exercise the real HTTP leaf exactly once")
				}
			})
		}
	}
}
