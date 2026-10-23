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
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsabilityClaudeRestrictionsRoot(t *testing.T) {
	const model = "claude-sonnet-4-6"
	const secret = "claude-isolation-secret"
	const hostile = `{"enabledPlugins":{"hostile@local":true},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"never-run"}]}]}}`
	for _, route := range []struct {
		kind                    provider.ProfileKind
		backend, mode, nodeKind string
	}{
		{provider.ProfileKindAgent, "claude-code", modelcatalog.ExecutionModeBatch, "agent_call"},
		{provider.ProfileKindAgent, "claude-code", modelcatalog.ExecutionModeInteractive, "interactive_agent"},
		{provider.ProfileKindChat, "", modelcatalog.ExecutionModeBatch, "llm_call"},
		{provider.ProfileKindChat, "", modelcatalog.ExecutionModeInteractive, "interactive_llm"},
	} {
		mode := route.mode
		for _, initial := range []string{"exact", "wrong model", "rejected restrictions"} {
			t.Run(route.nodeKind+"/"+initial, func(t *testing.T) {
				dir := t.TempDir()
				home := filepath.Join(dir, "claude-account")
				require.NoError(t, os.MkdirAll(home, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(home, "settings.json"), []byte(hostile), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(home, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"`+secret+`"}}`), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"hostile":{"command":"never-run"}}}`), 0600))
				t.Setenv("CLAUDE_CONFIG_DIR", home)
				bin := filepath.Join(dir, "claude")
				require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 99\n"), 0700))
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				var mu sync.Mutex
				outcome, probeRoot := initial, ""
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
					if slices.Equal(spec.Args, []string{"--version"}) {
						return output("2.1.304 (Claude Code)")
					}
					flag := func(name, want string) bool {
						i := slices.Index(spec.Args, name)
						return i >= 0 && i+1 < len(spec.Args) && spec.Args[i+1] == want
					}
					if ctx.Err() != nil || spec.Bin != bin || !flag("--model", model) || !flag("--output-format", "stream-json") || !slices.Contains(spec.Env, "CLAUDE_CONFIG_DIR="+home) || slices.ContainsFunc(spec.Env, func(e string) bool { return strings.HasPrefix(e, "ANTHROPIC_API_KEY=") }) || slices.Contains(spec.Args, "--fallback-model") {
						return nil, nil, fmt.Errorf("missing real model/auth/binary inputs")
					}
					auth, err := os.ReadFile(filepath.Join(home, ".credentials.json"))
					if err != nil || !strings.Contains(string(auth), secret) {
						return nil, nil, fmt.Errorf("missing subscription account")
					}
					actual := model
					if filepath.Dir(spec.Dir) == probeRoot {
						settings, err := os.ReadFile(filepath.Join(home, "settings.json"))
						mcp, mcpErr := os.ReadFile(filepath.Join(home, ".claude.json"))
						if err != nil || string(settings) != hostile || mcpErr != nil || !strings.Contains(string(mcp), "hostile") {
							return nil, nil, fmt.Errorf("missing hostile inherited configuration")
						}
						for _, name := range []string{"--safe-mode", "--strict-mcp-config", "--no-chrome"} {
							if !slices.Contains(spec.Args, name) {
								return nil, nil, fmt.Errorf("missing restriction %s", name)
							}
						}
						if !flag("--tools", "") || !flag("--setting-sources", "") || !flag("--mcp-config", `{"mcpServers":{}}`) || !flag("--allowedTools", "") || slices.Contains(spec.Args, "--dangerously-skip-permissions") || slices.Contains(spec.Args, "--bare") || spec.Stdin != "Reply with the single word: ok" {
							return nil, nil, fmt.Errorf("missing probe isolation/prompt")
						}
						for _, env := range []string{"DISABLE_UPDATES=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1"} {
							if !slices.Contains(spec.Env, env) {
								return nil, nil, fmt.Errorf("missing update isolation")
							}
						}
						if outcome == "wrong model" {
							actual = "fallback"
						}
						if outcome == "rejected restrictions" {
							return nil, nil, fmt.Errorf("unsupported restriction configuration")
						}
					} else if !strings.Contains(spec.Dir, string(filepath.Separator)+"instances"+string(filepath.Separator)) || !strings.Contains(spec.Stdin, "hello") || slices.Contains(spec.Args, "--dangerously-skip-permissions") != (route.kind == provider.ProfileKindAgent) || slices.Contains(spec.Args, "--safe-mode") {
						return nil, nil, fmt.Errorf("ordinary execution changed or missing mission")
					}
					return output(fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"executed %s"}]}}`, actual), fmt.Sprintf(`{"type":"result","result":"executed %s","session_id":"claude-isolation-session","modelUsage":{%q:{}}}`, actual, actual))
				})
				receiver := newAnnouncementReceiver(t)
				ds, ticks, intervals := buildLifecycleRootWithDependencies(t, "profiles: []\nllm:\n  defaultagentbackend: claude-code\n  binarypath: "+bin+"\n", &lifecycleHTTP{}, ModelDependencies{Runner: runner, AnnouncementTransport: receiver.transport, FetchTransport: catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodGet || r.URL.String() != "https://models.dev/api.json" {
						return nil, fmt.Errorf("wrong catalog source")
					}
					return lifecycleResponse(r, `{"anthropic":{"id":"anthropic","models":{"claude-sonnet-4-6":{"id":"claude-sonnet-4-6"}}}}`), nil
				})})
				probeRoot = ds.refreshDeps.ProbeDir
				ctx := adminCtxForLocalUser(t, ds)
				writeWorkflow(t, ds.cfg.Workspace.Path, "lifecycle", fmt.Sprintf(`{"name":"lifecycle","nodes":[{"id":"ask","type":%q,"entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`, route.nodeKind))
				require.NoError(t, os.WriteFile(filepath.Join(ds.cfg.Workspace.Path, "users", localDefaultUser.String(), "lifecycle", "templates", "o.md"), []byte("orchestrate"), 0600))
				_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: route.kind, Backend: route.backend, Model: model, AllowedTools: []string{"Read"}})
				require.NoError(t, err)
				setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/claude?token=private-setting")
				require.NoError(t, ds.Start(ctx))
				rootTicker(t, intervals)
				if initial != "exact" {
					assert.Empty(t, receiver.cards(model))
					var events int
					require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
					require.Zero(t, events, "no exact restricted proof means no durable discovery")
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
				require.Len(t, cards, 1, "restricted root probe must reach the real sender/receiver")
				payload := assertAnnouncementEvent(t, ds, "anthropic/"+model, "announced", 0)
				require.NotEmpty(t, payload.Paths)
				for _, ev := range payload.Paths {
					assert.Equal(t, model, ev.NativeModel)
					assert.NotEmpty(t, ev.ProofRef)
				}
				// The event can originate on another path; require this workflow's own
				// mode/configuration proof at the durable sink as well.
				paths, err := ds.pathInventory.Snapshot(ctx)
				require.NoError(t, err)
				view, err := ds.catalogStore.Snapshot(ctx)
				require.NoError(t, err)
				var target string
				for _, p := range paths.Paths {
					if p.Kind == string(route.kind) && p.Backend == route.backend && p.Mode == mode && slices.Contains(p.Aliases, "worker") {
						target = p.Key
						require.True(t, slices.ContainsFunc(view.Evidence, func(ev modelcatalog.UsabilityEvidence) bool {
							return ev.PathKey == p.Key && ev.ConfigRevision == p.ConfigRevision && ev.NativeModel == model && ev.Confirmed()
						}), "this mode must persist its own exact restricted proof")
					}
				}
				require.NotEmpty(t, target)
				assert.Contains(t, cards[0], "No profiles changed")
				assert.NotContains(t, cards[0], secret)
				id, _, err := ds.eng.Trigger(ctx, "lifecycle", nil, "normal")
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					run, err := ds.instanceRepo.Get(ctx, id)
					return err == nil && (run.State.IsTerminal() || run.State == instance.StateWaiting)
				}, 5*time.Second, time.Millisecond)
				run, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, model, run.ModelPins["worker"])
				ne, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "ask")
				require.NoError(t, err)
				if mode == modelcatalog.ExecutionModeBatch {
					assert.Equal(t, instance.StateCompleted, run.State)
					assert.Contains(t, fmt.Sprint(ne.Output), "executed "+model)
				} else {
					assert.Equal(t, instance.StateWaiting, run.State)
					assert.Contains(t, fmt.Sprint(ne.LLMConversation), "executed "+model)
				}
			})
		}
	}
}
