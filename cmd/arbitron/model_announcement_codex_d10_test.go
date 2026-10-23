package main

import (
	"context"
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
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each mode must independently supply the first proof and receiver card. The
// other mode always returns a fallback, so batch cannot mask a broken interactive
// launcher (or vice versa). Only process/HTTP I/O is substituted.
func TestModelAnnouncementCodexD10DiscoveryRoot(t *testing.T) {
	const model = "gpt-5-sol"
	const prompt = "Reply with only your model identity on one line. Do not use tools or take any other action."
	for _, mode := range []string{modelcatalog.ExecutionModeBatch, modelcatalog.ExecutionModeInteractive} {
		for _, initial := range []string{"exact", "wrong model", "missing turn", "missing cache", "rejected restrictions"} {
			t.Run(mode+"/"+initial, func(t *testing.T) {
				dir := t.TempDir()
				home := filepath.Join(dir, "account")
				writeCodexAccountModels(t, home, model)
				t.Setenv("CODEX_HOME", home)
				t.Setenv("OPENAI_API_KEY", "unrelated-api-secret")
				t.Setenv(codexInteractiveVerifiedEnv, "true")
				const auth = `{"account":"subscription-secret"}`
				const config = "[mcp_servers.hostile]\ncommand = 'never-run'\n[plugins.\"hostile@local\"]\nenabled = true\n"
				require.NoError(t, os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600))
				bin := filepath.Join(dir, "codex")
				require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 99\n"), 0700))
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				if initial == "missing cache" {
					require.NoError(t, os.Remove(filepath.Join(home, "models_cache.json")))
				}

				var mu sync.Mutex
				outcome := initial
				var probeRoot string
				serial, probes := 0, 0
				listed := map[string]bool{}
				runner := lifecycleRunner(func(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
					mu.Lock()
					defer mu.Unlock()
					output := func(lines ...string) (<-chan string, func() error, error) {
						ch := make(chan string, len(lines))
						for _, line := range lines {
							ch <- line
						}
						close(ch)
						return ch, func() error { return nil }, nil
					}
					if spec.Bin != bin || ctx.Err() != nil || !slices.Contains(spec.Env, "CODEX_HOME="+home) || slices.ContainsFunc(spec.Env, func(s string) bool { return strings.HasPrefix(s, "OPENAI_API_KEY=") }) {
						return nil, nil, fmt.Errorf("missing binary/account/context isolation")
					}
					if slices.Equal(spec.Args, []string{"--version"}) {
						return output("codex-cli 0.163.0")
					}
					authBytes, err := os.ReadFile(filepath.Join(home, "auth.json"))
					if err != nil || string(authBytes) != auth {
						return nil, nil, fmt.Errorf("wrong subscription auth")
					}
					flag := func(name string) string {
						i := slices.Index(spec.Args, name)
						if i < 0 || i+1 >= len(spec.Args) {
							return ""
						}
						return spec.Args[i+1]
					}
					probe := filepath.Dir(spec.Dir) == probeRoot
					actual := flag("-m")
					if probe {
						deadline, ok := ctx.Deadline()
						limit := procenv.ResponseLimit(ctx, spec.MaxResponseBytes)
						if !ok || time.Until(deadline) > procenv.ProbeTimeout || limit <= 0 || limit > procenv.ProbeResponseBytes || slices.Contains(spec.Args, "mcp") == slices.Contains(spec.Args, "--strict-config") {
							return nil, nil, fmt.Errorf("missing probe bounds/strict config")
						}
						overrides := map[string]string{}
						for i, a := range spec.Args {
							if a == "-c" && i+1 < len(spec.Args) {
								k, v, ok := strings.Cut(spec.Args[i+1], "=")
								if !ok {
									return nil, nil, fmt.Errorf("malformed restriction")
								}
								overrides[k] = v
							}
						}
						for k, v := range map[string]string{"features.plugins": "false", "features.apps": "false", "web_search": `"disabled"`, "sandbox_workspace_write.network_access": "false"} {
							if overrides[k] != v {
								return nil, nil, fmt.Errorf("missing restriction %s", k)
							}
						}
						if slices.Contains(spec.Args, "mcp") {
							configBytes, err := os.ReadFile(filepath.Join(home, "config.toml"))
							if err != nil || string(configBytes) != config || spec.Stdin != "" || len(spec.Args) < 3 || !slices.Equal(spec.Args[len(spec.Args)-3:], []string{"mcp", "list", "--json"}) {
								return nil, nil, fmt.Errorf("missing inherited config enumeration")
							}
							listed[spec.Dir] = true
							return output(`[{"name":"hostile","enabled":true}]`)
						}
						if !listed[spec.Dir] || overrides["mcp_servers"] != `{"hostile"={enabled=false}}` || spec.Stdin != prompt || slices.Contains(spec.Args, "--dangerously-bypass-approvals-and-sandbox") {
							return nil, nil, fmt.Errorf("missing enumerated MCP override/identity-only posture")
						}
						launchMode := modelcatalog.ExecutionModeBatch
						sandbox := flag("--sandbox")
						if slices.Contains(spec.Args, "-s") {
							launchMode, sandbox = modelcatalog.ExecutionModeInteractive, flag("-s")
						}
						if sandbox != "read-only" {
							return nil, nil, fmt.Errorf("missing read-only sandbox")
						}
						if launchMode == mode {
							probes++
						}
						if outcome == "rejected restrictions" {
							return nil, nil, fmt.Errorf("unsupported inherited restriction config")
						}
						if launchMode != mode || outcome == "wrong model" {
							actual = "fallback"
						}
					} else {
						validPrompt := spec.Stdin == "hello"
						if mode == modelcatalog.ExecutionModeInteractive {
							validPrompt = strings.HasPrefix(spec.Stdin, "orchestrate\n\nhello")
						}
						if !validPrompt || !strings.Contains(spec.Dir, string(filepath.Separator)+"instances"+string(filepath.Separator)) {
							return nil, nil, fmt.Errorf("missing real mission/workspace")
						}
					}
					if len(spec.Args) == 0 || spec.Args[0] != "exec" || flag("-m") != model || flag("--cd") != spec.Dir || !slices.Contains(spec.Args, "--json") || spec.Dir == "" {
						return nil, nil, fmt.Errorf("missing exact model/cwd/output inputs")
					}
					serial++
					sid := fmt.Sprintf("receiver-thread-%d", serial)
					path := filepath.Join(home, "sessions", "2026", "10", "20", "rollout-t-"+sid+".jsonl")
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						return nil, nil, err
					}
					raw := fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n", sid)
					if !probe || outcome != "missing turn" {
						raw += fmt.Sprintf("{\"type\":\"turn_context\",\"payload\":{\"model\":%q}}\n", actual)
					}
					if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
						return nil, nil, err
					}
					return output(fmt.Sprintf(`{"type":"thread.started","thread_id":%q}`, sid), fmt.Sprintf(`{"type":"item.completed","item":{"type":"agent_message","text":"executed %s"}}`, actual), `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`)
				})

				receiver := newAnnouncementReceiver(t)
				deps := ModelDependencies{Runner: runner, AnnouncementTransport: receiver.transport, FetchTransport: catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodGet || r.URL.String() != "https://models.dev/api.json" {
						return nil, fmt.Errorf("wrong source")
					}
					return lifecycleResponse(r, `{"openai":{"id":"openai","models":{"gpt-5-sol":{"id":"gpt-5-sol"}}}}`), nil
				})}
				ds, ticks, intervals := buildLifecycleRootWithDependencies(t, "profiles: []\nllm:\n  defaultagentbackend: codex\n", &lifecycleHTTP{}, deps)
				probeRoot = ds.refreshDeps.ProbeDir
				ctx := adminCtxForLocalUser(t, ds)
				kind := "agent_call"
				if mode == modelcatalog.ExecutionModeInteractive {
					kind = "interactive_agent"
				}
				writeWorkflow(t, ds.cfg.Workspace.Path, "lifecycle", fmt.Sprintf(`{"name":"lifecycle","nodes":[{"id":"ask","type":%q,"entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`, kind))
				require.NoError(t, os.WriteFile(filepath.Join(ds.cfg.Workspace.Path, "users", localDefaultUser.String(), "lifecycle", "templates", "o.md"), []byte("orchestrate"), 0600))
				_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: provider.ProfileKindAgent, Backend: "codex", Model: model})
				require.NoError(t, err)
				setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/d10?token=private-setting")
				require.NoError(t, ds.Start(ctx))
				rootTicker(t, intervals)
				if initial != "exact" {
					assert.Empty(t, receiver.cards(model), "unproved/rejected probes cannot send a card")
					var events int
					require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
					require.Zero(t, events)
					mu.Lock()
					outcome = "exact"
					mu.Unlock()
					if initial == "missing cache" {
						writeCodexAccountModels(t, home, model)
					}
					expireProbeBackoff(t, ds.db)
					if mode == modelcatalog.ExecutionModeBatch {
						view, err := ds.catalogStore.Snapshot(ctx)
						require.NoError(t, err)
						rootTick(t, ticks)
						rootReports(t, ds, len(view.Reports)+1)
					} else {
						result, _ := mcpToolCaller(t, ctx, ds)("model_catalog_refresh", nil)
						require.False(t, result.IsError, "%+v", result.Content)
					}
				}
				cards := receiver.cards(model)
				require.Len(t, cards, 1, "the restricted target mode must independently reach the receiver")
				assert.Contains(t, cards[0], "No profiles changed")
				for _, secret := range []string{"subscription-secret", "unrelated-api-secret", "private-setting", home, "receiver-thread-"} {
					assert.NotContains(t, cards[0], secret)
				}
				payload := assertAnnouncementEvent(t, ds, "openai/"+model, "announced", 0)
				facts := announcementCardFacts(t, cards[0])
				assert.Equal(t, []string{payload.EventID}, facts["Event"])
				assert.Equal(t, []string{"openai/" + model}, facts["Model"])
				assert.Equal(t, []string{"openai"}, facts["Provider"])
				var pathFacts []string
				paths, err := ds.pathInventory.Snapshot(ctx)
				require.NoError(t, err)
				for _, ev := range payload.Paths {
					i := slices.IndexFunc(paths.Paths, func(p modelcatalog.ExecutionPath) bool { return p.Key == ev.PathKey })
					require.NotEqual(t, -1, i)
					assert.Equal(t, mode, paths.Paths[i].Mode)
					assert.Equal(t, "openai", ev.Provider)
					assert.Equal(t, model, ev.NativeModel)
					assert.NotEmpty(t, ev.ConfigRevision)
					assert.NotEmpty(t, ev.ProofRef)
					assert.NotContains(t, cards[0], ev.ProofRef)
					pathFacts = append(pathFacts, ev.PathKey+" ("+ev.ConfigRevision+")")
				}
				assert.Equal(t, pathFacts, facts["Path"])

				configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
				ds.Close(context.Background())
				mu.Lock()
				before := probes
				mu.Unlock()
				reopened, err := initDomainWithModelDependencies(ctx, configPath, deps)
				require.NoError(t, err)
				t.Cleanup(func() { reopened.Close(context.Background()) })
				require.NoError(t, reopened.Start(ctx))
				assert.Equal(t, payload, assertAnnouncementEvent(t, reopened, "openai/"+model, "announced", 0))
				assert.Len(t, receiver.cards(model), 1, "restart cannot repeat a lifetime discovery")
				view, err := reopened.catalogStore.Snapshot(ctx)
				require.NoError(t, err)
				for _, ev := range payload.Paths {
					assert.Contains(t, view.Evidence, ev, "exact proof and offering provenance must survive reopen")
				}
				mu.Lock()
				assert.Positive(t, probes)
				assert.Equal(t, before, probes, "unchanged confirmed target-mode evidence must be reused")
				mu.Unlock()
				ds = reopened

				// Reopened proof/profile state must feed a real new run, not just a DTO/card.
				id, _, err := ds.eng.Trigger(ctx, "lifecycle", nil, "normal")
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					run, err := ds.instanceRepo.Get(ctx, id)
					return err == nil && (run.State.IsTerminal() || run.State == instance.StateWaiting)
				}, 5*time.Second, time.Millisecond)
				run, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				wantState := instance.StateCompleted
				if mode == modelcatalog.ExecutionModeInteractive {
					wantState = instance.StateWaiting
				}
				require.Equal(t, wantState, run.State)
				assert.Equal(t, model, run.ModelPins["worker"])
				ne, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "ask")
				require.NoError(t, err)
				if mode == modelcatalog.ExecutionModeInteractive {
					assert.Contains(t, fmt.Sprint(ne.LLMConversation), "executed "+model)
				} else {
					assert.Contains(t, fmt.Sprint(ne.Output), "executed "+model)
				}

			})
		}
	}
}
