package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D12 at the root (decided 2026-10-21): an opencode route's discovery probe
// disables every MCP server OpenCode resolves across its layers, proves the
// effective config has none enabled, and runs under --pure. The real refresh
// reaches the real opencode adapter through the production factory, persisted
// connection and four-hour ticker. The process leaf plays OpenCode: it resolves
// the hostile inherited global and $HOME/.opencode servers from the real files,
// deep-merges the launch's inline overlay, and refuses any probe run that lacks
// the isolation. An exact export delivers the Teams card and durable proof; a
// fallback export or an ignored overlay delivers nothing until a later tick
// confirms. An ordinary pinned run keeps its own unrestricted posture.
func TestUsabilityOpenCodeD12IsolationRoot(t *testing.T) {
	const model, secret = "gpt-5-sol", "opencode-isolation-secret"
	for _, initial := range []string{"exact", "wrong model", "overlay ignored"} {
		t.Run(initial, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "opencode")
			require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 99\n"), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			var mu sync.Mutex
			outcome, probeRoot, home := initial, "", ""
			validatedProbes := 0
			output := func(lines ...string) (<-chan string, func() error, error) {
				ch := make(chan string, len(lines))
				for _, line := range lines {
					ch <- line
				}
				close(ch)
				return ch, func() error { return nil }, nil
			}
			overlay := func(env []string) map[string]map[string]any {
				for _, kv := range env {
					if raw, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_CONTENT="); ok {
						var doc struct {
							MCP map[string]map[string]any `json:"mcp"`
						}
						_ = json.Unmarshal([]byte(raw), &doc)
						return doc.MCP
					}
				}
				return nil
			}
			// resolved merges the real inherited layers with the launch overlay.
			resolved := func(env []string, honor bool) string {
				mcp := map[string]map[string]any{}
				for _, layer := range []string{filepath.Join(home, ".config", "opencode", "opencode.json"), filepath.Join(home, ".opencode", "opencode.json")} {
					raw, err := os.ReadFile(layer)
					if err != nil {
						continue
					}
					var doc struct {
						MCP map[string]map[string]any `json:"mcp"`
					}
					_ = json.Unmarshal(raw, &doc)
					for name, entry := range doc.MCP {
						mcp[name] = entry
					}
				}
				if honor {
					for name, fields := range overlay(env) {
						if entry, ok := mcp[name]; ok {
							for k, v := range fields {
								entry[k] = v
							}
						}
					}
				}
				b, _ := json.Marshal(map[string]any{"mcp": mcp})
				return string(b)
			}
			runner := lifecycleRunner(func(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
				mu.Lock()
				defer mu.Unlock()
				if slices.Equal(spec.Args, []string{"--version"}) {
					return output("opencode 1.19.3")
				}
				if ctx.Err() != nil || spec.Bin != bin || !slices.Contains(spec.Env, "OPENAI_API_KEY="+secret) || !slices.Contains(spec.Env, "HOME="+home) {
					return nil, nil, fmt.Errorf("missing real binary/credential/account inputs")
				}
				probe := filepath.Dir(spec.Dir) == probeRoot
				switch spec.Args[0] {
				case "debug":
					if !probe || !slices.Equal(spec.Args, []string{"debug", "config", "--pure"}) {
						return nil, nil, fmt.Errorf("config resolution outside a probe")
					}
					return output(resolved(spec.Env, outcome != "overlay ignored"))
				case "run":
					flag := func(name, value string) bool {
						i := slices.Index(spec.Args, name)
						return i >= 0 && i+1 < len(spec.Args) && spec.Args[i+1] == value
					}
					if !flag("-m", "openai/"+model) || !flag("--format", "json") {
						return nil, nil, fmt.Errorf("missing real model/format inputs")
					}
					if slices.Contains(spec.Args, "--pure") != probe {
						return nil, nil, fmt.Errorf("--pure must be probe-only")
					}
					session := "ses_mission"
					if probe {
						disabled := overlay(spec.Env)
						for _, name := range []string{"hostile-global", "hostile-dotdir"} {
							if fmt.Sprint(disabled[name]) != "map[enabled:false]" {
								return nil, nil, fmt.Errorf("probe run with inherited MCP server %s enabled", name)
							}
						}
						if !strings.Contains(fmt.Sprint(spec.Env), `"permission":{"*":"deny"}`) || !slices.Contains(spec.Args, "Reply with the single word: ok") {
							return nil, nil, fmt.Errorf("missing deny-all posture or identity prompt")
						}
						validatedProbes++
						session = "ses_probe"
					} else if len(overlay(spec.Env)) != 0 || !strings.Contains(spec.Dir, string(filepath.Separator)+"instances"+string(filepath.Separator)) {
						return nil, nil, fmt.Errorf("ordinary mission must keep its own posture and workdir")
					}
					return output(fmt.Sprintf(`{"type":"text","sessionID":%q,"part":{"id":"prt_1","sessionID":%q,"messageID":"msg_1","type":"text","text":"executed %s"}}`, session, session, model))
				case "export":
					if slices.Contains(spec.Args, "--pure") != probe {
						return nil, nil, fmt.Errorf("export posture must match its run")
					}
					actual := model
					if probe && outcome == "wrong model" {
						actual = "fallback"
					}
					return output(fmt.Sprintf(`{"info":{"id":%q},"messages":[{"info":{"id":"msg_1","sessionID":%q,"role":"assistant","providerID":"openai","modelID":%q,"time":{"created":1,"completed":2}},"parts":[]}]}`, spec.Args[1], spec.Args[1], actual))
				}
				return nil, nil, fmt.Errorf("unexpected opencode invocation %v", spec.Args)
			})
			receiver := newAnnouncementReceiver(t)
			ds, ticks, intervals := buildLifecycleRootWithDependencies(t, "profiles: []\nllm:\n  defaultagentbackend: opencode\n  agentallowalltools: true\n", &lifecycleHTTP{}, ModelDependencies{Runner: runner, AnnouncementTransport: receiver.transport, FetchTransport: catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.String() != "https://models.dev/api.json" {
					return nil, fmt.Errorf("wrong catalog source")
				}
				return lifecycleResponse(r, `{"openai":{"id":"openai","models":{"gpt-5-sol":{"id":"gpt-5-sol"}}}}`), nil
			})})
			probeRoot, home = ds.refreshDeps.ProbeDir, os.Getenv("HOME")
			for layer, server := range map[string]string{filepath.Join(home, ".config", "opencode"): "hostile-global", filepath.Join(home, ".opencode"): "hostile-dotdir"} {
				require.NoError(t, os.MkdirAll(layer, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(layer, "opencode.json"), []byte(`{"mcp":{"`+server+`":{"type":"local","command":["never-run"],"enabled":true}}}`), 0o600))
			}
			require.NoError(t, os.MkdirAll(filepath.Join(home, ".config", "opencode", "plugin"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(home, ".config", "opencode", "plugin", "hostile.js"), []byte(`throw new Error("hostile plugin loaded")`), 0o600))
			ctx := adminCtxForLocalUser(t, ds)
			writeWorkflow(t, ds.cfg.Workspace.Path, "lifecycle", `{"name":"lifecycle","nodes":[{"id":"ask","type":"agent_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "oc-openai", Provider: "openai", BaseURL: "https://api.openai.com"}, secret))
			_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: provider.ProfileKindAgent, Backend: "opencode", ConnectionRef: "oc-openai", Model: "openai/" + model})
			require.NoError(t, err)
			setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/opencode?token=private-setting")
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			mu.Lock()
			checked := validatedProbes
			mu.Unlock()
			if initial == "overlay ignored" {
				assert.Zero(t, checked, "an unverified overlay must stop the probe before any run")
			} else {
				assert.Positive(t, checked, "each outcome must reach the fully validated isolated launch")
			}
			if initial != "exact" {
				assert.Empty(t, receiver.cards(model))
				var events int
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
				require.Zero(t, events)
				mu.Lock()
				outcome = "exact"
				mu.Unlock()
				view, err := ds.catalogStore.Snapshot(ctx)
				require.NoError(t, err)
				expireProbeBackoff(t, ds.db)
				rootTick(t, ticks)
				rootReports(t, ds, len(view.Reports)+1)
			}
			cards := receiver.cards(model)
			require.Len(t, cards, 1, "isolated proof must reach the receiver")
			assertAnnouncementEvent(t, ds, "openai/"+model, "announced", 0)
			assert.NotContains(t, cards[0], secret)
			paths, err := ds.pathInventory.Snapshot(ctx)
			require.NoError(t, err)
			view, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			var target string
			for _, p := range paths.Paths {
				if p.Backend == "opencode" && p.Mode == modelcatalog.ExecutionModeBatch && slices.Contains(p.Aliases, "worker") {
					target = p.Key
					require.True(t, slices.ContainsFunc(view.Evidence, func(ev modelcatalog.UsabilityEvidence) bool {
						return ev.PathKey == p.Key && ev.ConfigRevision == p.ConfigRevision && ev.NativeModel == model && ev.Confirmed() && strings.Contains(ev.ProofRef, "ses_probe")
					}), "workflow's own route needs durable proof naming the probe session")
				}
			}
			require.NotEmpty(t, target)
			id, _, err := ds.eng.Trigger(ctx, "lifecycle", nil, "normal")
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				run, err := ds.instanceRepo.Get(ctx, id)
				return err == nil && run.State.IsTerminal()
			}, 5*time.Second, time.Millisecond)
			run, err := ds.instanceRepo.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, instance.StateCompleted, run.State)
			assert.Equal(t, "openai/"+model, run.ModelPins["worker"])
			ne, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "ask")
			require.NoError(t, err)
			assert.Contains(t, fmt.Sprint(ne.Output), "executed "+model)
		})
	}
}
