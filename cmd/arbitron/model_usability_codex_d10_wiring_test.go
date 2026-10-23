package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsabilityCodexD10RestrictionsRoot(t *testing.T) {
	const prompt = "Reply with only your model identity on one line. Do not use tools or take any other action."
	const model = "gpt-native-d10"
	const sid = "d10-correlated-thread"
	for _, mode := range []string{modelcatalog.ExecutionModeBatch, modelcatalog.ExecutionModeInteractive} {
		for _, outcome := range []string{"exact", "missing cache", "wrong model", "unsupported restrictions", "unreadable config"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				dir := t.TempDir()
				home := filepath.Join(dir, "account")
				writeCodexAccountModels(t, home, model)
				t.Setenv("CODEX_HOME", home)
				t.Setenv("OPENAI_API_KEY", "unrelated-key")
				t.Setenv(codexInteractiveVerifiedEnv, "true")
				require.NoError(t, os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"account":"subscription-account"}`), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.hostile]\ncommand = 'never-run'\n[plugins.\"hostile@local\"]\nenabled = true\n"), 0600))
				bin := filepath.Join(dir, "codex")
				require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 99\n"), 0700))
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				var root string
				listed, executions := false, 0
				runner := lifecycleRunner(func(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
					output := func(lines ...string) (<-chan string, func() error, error) {
						ch := make(chan string, len(lines))
						for _, line := range lines {
							ch <- line
						}
						close(ch)
						return ch, func() error { return nil }, nil
					}
					if slices.Equal(spec.Args, []string{"--version"}) {
						return output("codex-cli 0.163.0")
					}
					if slices.Contains(spec.Args, "mcp") == slices.Contains(spec.Args, "--strict-config") || strings.Contains(strings.Join(spec.Env, "\n"), "unrelated-connection-secret") || spec.Bin != bin || filepath.Dir(spec.Dir) != root || !slices.Contains(spec.Env, "CODEX_HOME="+home) || slices.ContainsFunc(spec.Env, func(s string) bool { return strings.HasPrefix(s, "OPENAI_API_KEY=") }) {
						return nil, nil, fmt.Errorf("missing resolved account/binary/isolated cwd")
					}
					auth, err := os.ReadFile(filepath.Join(home, "auth.json"))
					if err != nil || !strings.Contains(string(auth), "subscription-account") {
						return nil, nil, fmt.Errorf("wrong auth home")
					}
					overrides := map[string]string{}
					for i, a := range spec.Args {
						if a == "-c" && i+1 < len(spec.Args) {
							k, v, ok := strings.Cut(spec.Args[i+1], "=")
							if !ok {
								return nil, nil, fmt.Errorf("invalid override")
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
						if outcome == "unreadable config" {
							return nil, nil, fmt.Errorf("config unavailable")
						}
						config, err := os.ReadFile(filepath.Join(home, "config.toml"))
						if err != nil || !strings.Contains(string(config), "[mcp_servers.hostile]") || !strings.Contains(string(config), "enabled = true") {
							return nil, nil, fmt.Errorf("hostile fixture absent")
						}
						listed = true
						return output(`[{"name":"hostile","enabled":true}]`)
					}
					if !listed || spec.Stdin != prompt || overrides["mcp_servers"] != `{"hostile"={enabled=false}}` || !slices.Contains(spec.Args, model) || !slices.Contains(spec.Args, "--json") || slices.Contains(spec.Args, "--dangerously-bypass-approvals-and-sandbox") {
						return nil, nil, fmt.Errorf("missing exact probe recipe")
					}
					modelIndex := slices.Index(spec.Args, "-m")
					if modelIndex < 0 || modelIndex+1 >= len(spec.Args) || spec.Args[modelIndex+1] != model {
						return nil, nil, fmt.Errorf("missing exact model flag")
					}
					resumed := slices.Contains(spec.Args, "resume")
					if resumed {
						if overrides["sandbox_mode"] != `"read-only"` || slices.Contains(spec.Args, "-s") || slices.Contains(spec.Args, "--cd") || !slices.Contains(spec.Args, sid) || spec.Args[len(spec.Args)-1] != "-" {
							return nil, nil, fmt.Errorf("wrong resume recipe")
						}
					} else {
						flag := "--sandbox"
						if mode == modelcatalog.ExecutionModeInteractive {
							flag = "-s"
						}
						i := slices.Index(spec.Args, flag)
						cwd := slices.Index(spec.Args, "--cd")
						if i < 0 || i+1 >= len(spec.Args) || spec.Args[i+1] != "read-only" || cwd < 0 || cwd+1 >= len(spec.Args) || spec.Args[cwd+1] != spec.Dir {
							return nil, nil, fmt.Errorf("wrong initial recipe")
						}
					}
					if outcome == "unsupported restrictions" {
						return nil, nil, fmt.Errorf("unsupported config")
					}
					executions++
					actual := model
					if outcome == "wrong model" {
						actual = "fallback"
					}
					path := filepath.Join(home, "sessions", "2026", "10", "19", "rollout-t-"+sid+".jsonl")
					require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
					raw := fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n", sid)
					if resumed {
						prior, err := os.ReadFile(path)
						require.NoError(t, err)
						raw = string(prior)
					}
					raw += fmt.Sprintf("{\"type\":\"turn_context\",\"payload\":{\"model\":%q}}\n", actual)
					require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
					return output(fmt.Sprintf(`{"type":"thread.started","thread_id":%q}`, sid), `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`)
				})
				ds, _, _ := buildLifecycleRootWithDependencies(t, "profiles: []\nllm:\n  defaultagentbackend: codex\n", &lifecycleHTTP{}, ModelDependencies{Runner: runner})
				root = ds.refreshDeps.ProbeDir
				ctx := adminCtxForLocalUser(t, ds)
				writeWorkflow(t, ds.cfg.Workspace.Path, "d10", `{"name":"d10","nodes":[{"id":"ask","type":"interactive_agent","entrypoint":true,"config":{"model_profile":"d10","templateFile":"templates/p.md"}}],"edges":[]}`)
				require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "unrelated", Provider: "openai", BaseURL: "https://unrelated.example.test"}, "unrelated-connection-secret"))
				_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "d10", Kind: provider.ProfileKindAgent, Backend: "codex", ConnectionRef: "unrelated", Model: "gpt-5-sol"})
				require.NoError(t, err)
				snap, err := ds.pathInventory.Snapshot(ctx)
				require.NoError(t, err)
				var path modelcatalog.ExecutionPath
				for _, p := range snap.Paths {
					if p.Backend == "codex" && p.Mode == mode && slices.Contains(p.Aliases, "d10") {
						path = p
						break
					}
				}
				require.NotEmpty(t, path.Key)
				if outcome == "missing cache" {
					require.NoError(t, os.Remove(filepath.Join(home, "models_cache.json")))
				}
				e := modelidentity.Entry{Provider: "openai", CanonicalID: "openai/" + model, NativeID: model}
				ev, err := ds.modelRefresh.probe.Check(ctx, path, e)
				require.NoError(t, err)
				assert.Equal(t, outcome == "exact", ev.Confirmed(), "%+v", ev)
				if outcome == "exact" {
					require.Positive(t, executions)
					require.NotEmpty(t, ev.ProofRef)
				} else {
					assert.Empty(t, ev.ProofRef)
				}
				if outcome != "exact" || mode != modelcatalog.ExecutionModeInteractive {
					return
				}
				// The real selector's continuation must repeat the restrictions and prove a
				// new turn in this exact account/session, not reuse the first turn's proof.
				b, err := ds.pathInventory.WithBinding(ctx, path, model)
				require.NoError(t, err)
				stream, err := ds.refreshDeps.Streams.StreamProviderFor(b)
				require.NoError(t, err)
				wd, err := os.MkdirTemp(root, "check-")
				require.NoError(t, err)
				defer os.RemoveAll(wd)
				listed = false
				resp, err := stream.Chat(ctx, engine.ChatRequest{Model: model, Prompt: "ignored caller prompt", SessionID: sid, WorkDir: wd, SuppressTools: true, RequireExecutedModel: true})
				require.NoError(t, err)
				assert.Equal(t, model, resp.Model)
				assert.Equal(t, sid, resp.SessionID)
				assert.Equal(t, 2, executions)
			})
		}
	}
}
