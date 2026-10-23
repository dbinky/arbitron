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

func TestUsabilityPiRestrictionsRoot(t *testing.T) {
	const model, secret = "gpt-5-sol", "pi-isolation-secret"
	for _, initial := range []string{"exact", "wrong model", "rejected restrictions"} {
		t.Run(initial, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "pi")
			require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 99\n"), 0700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			var mu sync.Mutex
			outcome, probeRoot, home := initial, "", ""
			validatedProbes := 0
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
					return output("1.3.0")
				}
				flag := func(name, value string) bool {
					i := slices.Index(spec.Args, name)
					return i >= 0 && i+1 < len(spec.Args) && spec.Args[i+1] == value
				}
				probe := filepath.Dir(spec.Dir) == probeRoot
				requested := "openai/" + model
				if probe {
					requested = model
				}
				if ctx.Err() != nil || spec.Bin != bin || !flag("--model", requested) || !flag("--mode", "json") || !slices.Contains(spec.Args, "--print") || !slices.Contains(spec.Args, "--no-session") || !slices.Contains(spec.Env, "OPENAI_API_KEY="+secret) || !slices.Contains(spec.Env, "HOME="+home) {
					return nil, nil, fmt.Errorf("missing real model/credential/binary/account inputs")
				}
				actual := model
				for _, f := range []string{"--no-tools", "--no-mcp", "--no-extensions", "--offline"} {
					if slices.Contains(spec.Args, f) != probe {
						return nil, nil, fmt.Errorf("incorrect probe-only restriction %s", f)
					}
				}
				if probe {
					settings, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "settings.json"))
					extension, extErr := os.ReadFile(filepath.Join(home, ".pi", "agent", "extensions", "hostile.js"))
					mcp, mcpErr := os.ReadFile(filepath.Join(home, ".pi", "agent", "mcp.json"))
					if err != nil || !strings.Contains(string(settings), "hostile") || extErr != nil || len(extension) == 0 || mcpErr != nil || !strings.Contains(string(mcp), "hostile") || spec.Stdin != "Reply with the single word: ok" {
						return nil, nil, fmt.Errorf("missing hostile inheritance or identity prompt")
					}
					validatedProbes++
					if outcome == "wrong model" {
						actual = "fallback"
					}
					if outcome == "rejected restrictions" {
						return nil, nil, fmt.Errorf("unsupported isolation configuration")
					}
				} else if !strings.Contains(spec.Dir, string(filepath.Separator)+"instances"+string(filepath.Separator)) || !strings.Contains(spec.Stdin, "hello") {
					return nil, nil, fmt.Errorf("missing ordinary mission/workdir")
				}
				return output(`{"type":"session","version":3,"id":"pi-restricted-session"}`, fmt.Sprintf(`{"type":"message_end","message":{"role":"assistant","provider":"openai","model":%q,"content":[{"type":"text","text":"executed %s"}],"stopReason":"stop"}}`, actual, actual))
			})
			receiver := newAnnouncementReceiver(t)
			ds, ticks, intervals := buildLifecycleRootWithDependencies(t, "profiles: []\nllm:\n  defaultagentbackend: pi\n", &lifecycleHTTP{}, ModelDependencies{Runner: runner, AnnouncementTransport: receiver.transport, FetchTransport: catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.String() != "https://models.dev/api.json" {
					return nil, fmt.Errorf("wrong catalog source")
				}
				return lifecycleResponse(r, `{"openai":{"id":"openai","models":{"gpt-5-sol":{"id":"gpt-5-sol"}}}}`), nil
			})})
			probeRoot, home = ds.refreshDeps.ProbeDir, os.Getenv("HOME")
			agentDir := filepath.Join(home, ".pi", "agent")
			require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "extensions"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"extensions":["./extensions/hostile.js"],"packages":["npm:hostile-not-installed"]}`), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(agentDir, "extensions", "hostile.js"), []byte(`export default function() { throw new Error("hostile extension loaded"); }`), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(agentDir, "mcp.json"), []byte(`{"mcpServers":{"hostile":{"command":"never-run"}}}`), 0600))
			ctx := adminCtxForLocalUser(t, ds)
			writeWorkflow(t, ds.cfg.Workspace.Path, "lifecycle", `{"name":"lifecycle","nodes":[{"id":"ask","type":"agent_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "pi-api", Provider: "openai", BaseURL: "https://api.openai.com"}, secret))
			_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: provider.ProfileKindAgent, Backend: "pi", ConnectionRef: "pi-api", Model: "openai/" + model})
			require.NoError(t, err)
			setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/pi?token=private-setting")
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			mu.Lock()
			checked := validatedProbes
			mu.Unlock()
			assert.Positive(t, checked, "each outcome must reach the fully validated restricted launch")
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
			require.Len(t, cards, 1, "restricted proof must reach the receiver")
			assertAnnouncementEvent(t, ds, "openai/"+model, "announced", 0)
			paths, err := ds.pathInventory.Snapshot(ctx)
			require.NoError(t, err)
			view, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			var target string
			for _, p := range paths.Paths {
				if p.Backend == "pi" && p.Mode == modelcatalog.ExecutionModeBatch && slices.Contains(p.Aliases, "worker") {
					target = p.Key
					require.True(t, slices.ContainsFunc(view.Evidence, func(ev modelcatalog.UsabilityEvidence) bool {
						return ev.PathKey == p.Key && ev.ConfigRevision == p.ConfigRevision && ev.NativeModel == model && ev.Confirmed()
					}), "workflow's own route needs durable proof")
				}
			}
			require.NotEmpty(t, target)
			assert.NotContains(t, cards[0], secret)
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
