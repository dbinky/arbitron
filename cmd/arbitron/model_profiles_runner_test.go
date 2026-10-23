package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lifecycleRunner func(context.Context, claudecli.RunSpec) (<-chan string, func() error, error)

func (f lifecycleRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	return f(ctx, spec)
}

// The root runner must reach both Codex modes, including version probes. The
// process requires a leaf-only marker; absent injection cannot produce durable
// confirmation. Model-list and rollout proof still come from actual files.
func TestModelProfilesRunnerCodexModesRoot(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "account")
	require.NoError(t, os.MkdirAll(home, 0700))
	t.Setenv("CODEX_HOME", home)
	t.Setenv(codexInteractiveVerifiedEnv, "true")
	writeCodexAccountModels(t, home, "gpt-5-sol")
	script := strings.NewReplacer("@ARGV@", filepath.Join(dir, "argv"), "@FALLBACK@", filepath.Join(dir, "fallback")).Replace(codexProbeScript)
	script = strings.Replace(script, "#!/bin/sh", "#!/bin/sh\n[ \"$MODEL_IO_LEAF\" = present ] || exit 51", 1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := lifecycleRunner(func(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
		if !slices.Contains(spec.Env, "CODEX_HOME="+home) || ctx.Err() != nil {
			return nil, nil, fmt.Errorf("missing account/context")
		}
		if !slices.Contains(spec.Args, "--version") {
			listing := slices.Contains(spec.Args, "mcp")
			validPrompt := listing && spec.Stdin == "" || !listing && spec.Stdin == "Reply with only your model identity on one line. Do not use tools or take any other action."
			if !strings.HasSuffix(filepath.Dir(spec.Dir), ".model-probe") || (!listing && !slices.Contains(spec.Args, "gpt-5-sol")) || !slices.Contains(spec.Args, "--json") || !validPrompt || spec.MaxResponseBytes <= 0 {
				return nil, nil, fmt.Errorf("missing model/mode/probe inputs")
			}
		}
		spec.Env = append(spec.Env, "MODEL_IO_LEAF=present")
		return claudecli.NewExecRunner().Run(ctx, spec)
	})
	receiver := newAnnouncementReceiver(t)
	deps := ModelDependencies{AnnouncementTransport: receiver.transport, Runner: runner, FetchTransport: catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://models.dev/api.json" {
			return nil, fmt.Errorf("wrong source")
		}
		return lifecycleResponse(r, `{"openai":{"id":"openai","models":{"gpt-5-sol":{"id":"gpt-5-sol"}}}}`), nil
	})}
	ds, _, intervals := buildLifecycleRootWithDependencies(t, "profiles: []\nllm:\n  defaultagentbackend: codex\n", &lifecycleHTTP{}, deps)
	ctx := adminCtxForLocalUser(t, ds)
	setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/default-harness?token=private-setting")
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	modes := map[string]bool{}
	paths, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	for _, ev := range view.Evidence {
		if ev.Confirmed() {
			for _, p := range paths.Paths {
				if p.Key == ev.PathKey && p.Backend == "codex" {
					modes[p.Mode] = true
				}
			}
		}
	}
	assert.Equal(t, map[string]bool{modelcatalog.ExecutionModeBatch: true}, modes)
	var count int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&count))
	assert.Equal(t, 1, count, "no-profile deployment harness reaches durable discovery")
	cards := receiver.cards("gpt-5-sol")
	require.Len(t, cards, 1, "no-profile default harness reaches the receiver")
	assert.Contains(t, cards[0], "No profiles changed")
	assertAnnouncementEvent(t, ds, "openai/gpt-5-sol", "announced", 0)
	writeWorkflow(t, ds.cfg.Workspace.Path, "lifecycle", `{"name":"lifecycle","nodes":[{"id":"ask","type":"interactive_agent","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: provider.ProfileKindAgent, Backend: "codex", Model: "gpt-5-sol"})
	require.NoError(t, err)
	call := mcpToolCaller(t, ctx, ds)
	res, _ := call("model_catalog_refresh", nil)
	require.False(t, res.IsError, "%+v", res.Content)
	view, err = ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	paths, err = ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	interactive := false
	for _, ev := range view.Evidence {
		for _, p := range paths.Paths {
			if p.Key == ev.PathKey && p.Mode == modelcatalog.ExecutionModeInteractive && ev.Confirmed() {
				interactive = true
			}
		}
	}
	assert.True(t, interactive, "real interactive runner must feed durable exact-model proof")
	assert.Len(t, receiver.cards("gpt-5-sol"), 1, "a later path does not announce the same canonical model twice")
}
