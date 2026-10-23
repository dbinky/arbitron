package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const probeOutputLimit = 1 << 20

// Each body measures bytes at the actual HTTP reader, not a post-read slice.
type probeCountingBody struct {
	io.Reader
	read *atomic.Int64
}

func (b probeCountingBody) Read(p []byte) (int, error) {
	n, e := b.Reader.Read(p)
	b.read.Add(int64(n))
	return n, e
}
func (b probeCountingBody) Close() error { return nil }

func TestUsabilityProbeIndependentBoundsRoot(t *testing.T) {
	for _, family := range []string{"openrouter", "anthropic"} {
		for _, configured := range []struct {
			max          int64
			timeout      int
			wantDeadline time.Duration
		}{{4 * probeOutputLimit, 120, 30 * time.Second}, {0, 0, 30 * time.Second}, {4 * probeOutputLimit, 1, time.Second}} {
			for _, mode := range []string{modelcatalog.ExecutionModeBatch, modelcatalog.ExecutionModeInteractive} {
				t.Run(fmt.Sprintf("%s/%d/%ds/%s", family, configured.max, configured.timeout, mode), func(t *testing.T) {
					const model = "claude-native-bounds"
					var size atomic.Int64
					var reads atomic.Int64
					var calls atomic.Int32
					var ordinary atomic.Bool
					size.Store(probeOutputLimit)
					leaf := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
						var body struct {
							Model    string
							Stream   bool
							Messages []struct{ Role, Content string }
							Tools    []any
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							return nil, err
						}
						expectedAuth := r.Header.Get("Authorization") == "Bearer bounds-secret"
						if family == "anthropic" {
							expectedAuth = r.Header.Get("x-api-key") == "bounds-secret" && r.Header.Get("anthropic-version") == "2023-06-01"
						}
						if !expectedAuth || body.Model != model || len(body.Messages) != 1 || body.Messages[0].Content != "Reply with the single word: ok" || len(body.Tools) != 0 || body.Stream != (mode == modelcatalog.ExecutionModeInteractive) {
							return nil, fmt.Errorf("missing execution inputs")
						}
						deadline, ok := r.Context().Deadline()
						if !ordinary.Load() {
							assert.True(t, ok, "probe needs a deadline")
							assert.LessOrEqual(t, time.Until(deadline), configured.wantDeadline, "probe must clamp or preserve the configured shorter deadline")
						} else if configured.timeout > 30 {
							assert.Greater(t, time.Until(deadline), time.Minute, "ordinary timeout must not be clamped")
						}
						calls.Add(1)
						data := fmt.Sprintf(`{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"}}]}`, model)
						if family == "anthropic" {
							data = fmt.Sprintf(`{"type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"ok"}]}`, model)
						}
						if body.Stream {
							data = fmt.Sprintf("data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", model)
							if family == "anthropic" {
								data = fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"model\":%q}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", model)
							}
							// Trailing SSE comments must not bypass the cap after a valid terminal event.
							data += strings.Repeat(":\n", (int(size.Load())-len(data))/2)
						}
						data += strings.Repeat(" ", int(size.Load())-len(data))
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: probeCountingBody{strings.NewReader(data), &reads}, Request: r}, nil
					})
					ds, _, _ := buildLifecycleRootWithDependencies(t, fmt.Sprintf("profiles: []\nllm:\n  timeoutseconds: %d\n  maxresponsebytes: %d\n", configured.timeout, configured.max), &lifecycleHTTP{}, ModelDependencies{ProviderTransport: leaf})
					ctx := adminCtxForLocalUser(t, ds)
					require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "bounds", Provider: family, BaseURL: "https://bounds.example.test"}, "bounds-secret"))
					writeWorkflow(t, ds.cfg.Workspace.Path, "bounds", `{"name":"bounds","nodes":[{"id":"ask","type":"interactive_llm","entrypoint":true,"config":{"model_profile":"bounds","templateFile":"templates/p.md"}}],"edges":[]}`)
					initial := "claude-sonnet-4-6"
					if family == "openrouter" {
						initial = "deepseek/deepseek-v4-flash"
					}
					_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "bounds", Kind: provider.ProfileKindChat, ConnectionRef: "bounds", Model: initial})
					require.NoError(t, err)
					snap, err := ds.pathInventory.Snapshot(ctx)
					require.NoError(t, err)
					var path modelcatalog.ExecutionPath
					for _, p := range snap.Paths {
						if p.ConnectionRef == "bounds" && p.Mode == mode && p.Backend == "" {
							path = p
							break
						}
					}
					require.NotEmpty(t, path.Key)
					entry := modelidentity.Entry{CanonicalID: family + "/" + model, Provider: family, NativeID: model}
					for _, n := range []int64{probeOutputLimit, probeOutputLimit + 1, 2 * probeOutputLimit} {
						size.Store(n)
						reads.Store(0)
						before := calls.Load()
						ev, err := ds.modelRefresh.probe.Check(ctx, path, entry)
						require.NoError(t, err)
						assert.Equal(t, n == probeOutputLimit, ev.Confirmed(), "size=%d evidence=%+v", n, ev)
						assert.Equal(t, before+1, calls.Load(), "must reach checked HTTP leaf")
						assert.LessOrEqual(t, reads.Load(), int64(probeOutputLimit+1), "bound the actual read")
					}
					// The same factory, binding and parser still accept larger ordinary runs.
					ordinary.Store(true)
					reads.Store(0)
					size.Store(2 * probeOutputLimit)
					b, err := ds.pathInventory.WithBinding(ctx, path, model)
					require.NoError(t, err)
					req := engine.ChatRequest{Model: model, Prompt: "Reply with the single word: ok", RequireExecutedModel: true}
					if mode == modelcatalog.ExecutionModeBatch {
						chat, err := ds.refreshDeps.Chats.ChatProviderFor(b)
						require.NoError(t, err)
						result, err := chat.Chat(ctx, req)
						require.NoError(t, err)
						assert.Equal(t, model, result.Model)
					} else {
						chat, err := ds.refreshDeps.Streams.StreamProviderFor(b)
						require.NoError(t, err)
						chunks, err := chat.StreamChat(ctx, req)
						require.NoError(t, err)
						done := false
						for c := range chunks {
							require.NoError(t, c.Error)
							done = done || c.Done
						}
						require.True(t, done)
					}
				})
			}
		}
	}
}
