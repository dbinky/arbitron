package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generatedConfigRoot points the deployment's paths at a temp dir via env, so
// the config file itself is exactly what Load generates when none exists.
func generatedConfigRoot(t *testing.T) (configPath string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ARBITRON_MASTER_KEY", "XV1dXV1dXV1dXV1dXV1dXV1dXV1dXV1dXV1dXV1dXV0=")
	t.Setenv("ANTHROPIC_API_KEY", "")
	root := t.TempDir()
	ws := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(ws, 0700))
	t.Setenv("ARBITRON_DATABASE_PATH", filepath.Join(root, "arbitron.db"))
	t.Setenv("ARBITRON_WORKSPACE_PATH", ws)
	t.Setenv("ARBITRON_LOGGING_FILE", filepath.Join(root, "arbitron.log"))
	return filepath.Join(root, "config.yaml")
}

func offlineModelDependencies() ModelDependencies {
	offline := catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	return ModelDependencies{FetchTransport: offline, ProviderTransport: offline}
}

func bootRoot(t *testing.T, configPath string) *domainStack {
	t.Helper()
	ds, err := initDomainWithModelDependencies(context.Background(), configPath, offlineModelDependencies())
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	require.NoError(t, ds.Start(context.Background()))
	return ds
}

func preflightNotifications(t *testing.T, ds *domainStack) []string {
	t.Helper()
	rows, err := ds.db.Query(`SELECT workflow_name FROM notifications WHERE type = ? ORDER BY workflow_name`, string(notification.TypeProfileRefusesTriggers))
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		out = append(out, name)
	}
	require.NoError(t, rows.Err())
	return out
}

// previousGeneratedProfiles is the profile block the generated config shipped
// before fixed versions were required.
const previousGeneratedProfiles = `samples:
  seed: false
profiles:
  - {alias: "default-chat", kind: "chat", model: ""}
  - {alias: "default-agent", kind: "agent", model: ""}
  - {alias: "assessor", kind: "chat", model: ""}
  - {alias: "interviewer", kind: "agent", model: "", allowedtools: ["Read", "Write", "Grep", "Glob", "Bash"]}
  - {alias: "reviewer", kind: "agent", model: "", allowedtools: ["Read", "Grep", "Glob", "Bash(gh:*)", "Bash(git:*)"]}
  - {alias: "coder", kind: "agent", backend: "claude-code", model: "opus-4.8", allowedtools: ["Read", "Write", "Edit", "Grep", "Glob", "Bash"]}
  - {alias: "planner", kind: "agent", backend: "claude-code", model: "opus-4.8", allowedtools: ["Read", "Write", "Grep", "Glob", "Bash"]}
`

// An upgraded install whose seed journal froze an empty default-chat boots
// from the generated default config: the corrected loader repairs the pending
// original with the following default, every shipped default alias persists
// on a concrete model with its kind visible to workflow validation, and the
// preflight reports nothing. Aliases the old boot completed (coder, planner)
// stay as the database has them.
func TestGeneratedDefaultConfigBootsAndRepairsPendingSeedRoot(t *testing.T) {
	configPath := generatedConfigRoot(t)

	// Old boot on the previous generated defaults: the journal freezes the
	// empty-model aliases, each refused.
	require.NoError(t, os.WriteFile(configPath, []byte(previousGeneratedProfiles), 0600))
	old := bootRoot(t, configPath)
	ctx := adminCtxForLocalUser(t, old)
	_, err := old.profileSvc.Get(ctx, "default-chat")
	require.ErrorIs(t, err, provider.ErrProfileNotFound)
	var frozen int
	require.NoError(t, old.db.QueryRow(`SELECT COUNT(*) FROM profile_seed_pending`).Scan(&frozen))
	require.Equal(t, 5, frozen, "the five empty-model originals stay pending")
	old.Close(context.Background())

	// Upgrade: the config file is the generated default, and the catalog is
	// reachable so the repaired following defaults can select a model.
	require.NoError(t, os.Remove(configPath))
	useFakeClaude(t)
	ds, err := initDomainWithModelDependencies(context.Background(), configPath, (&shippedCatalog{}).deps())
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	require.NoError(t, ds.Start(context.Background()))
	generated, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(generated), `alias: "default-chat"`, "the boot used the generated config")
	ctx = adminCtxForLocalUser(t, ds)

	want := map[string]provider.ProfileKind{
		"default-chat": provider.ProfileKindChat, "assessor": provider.ProfileKindChat,
		"default-agent": provider.ProfileKindAgent, "interviewer": provider.ProfileKindAgent,
		"reviewer": provider.ProfileKindAgent, "coder": provider.ProfileKindAgent, "planner": provider.ProfileKindAgent,
	}
	for alias, kind := range want {
		p, err := ds.profileSvc.Get(ctx, alias)
		require.NoError(t, err, "%s persists from the generated config", alias)
		assert.NotEmpty(t, p.Model, "%s names a fixed model", alias)
		got, ok := ds.profileChecker.ProfileKind(alias)
		require.True(t, ok, "%s is known to workflow validation", alias)
		assert.Equal(t, string(kind), got)
	}
	repaired, err := ds.profileSvc.Get(ctx, "default-chat")
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5", repaired.Model, "the pending empty original was repaired")
	assert.Equal(t, provider.SelectionFollowing, repaired.SelectionMode, "the repair adopts the following default")

	// A workflow on the shipped defaults validates against the live checker.
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(`{"name":"defaults","nodes":[
		{"id":"chat","type":"llm_call","entrypoint":true,"config":{"model_profile":"default-chat","templateFile":"t.md"}},
		{"id":"agent","type":"agent_call","config":{"model_profile":"default-agent","templateFile":"t.md"}}],
		"edges":[{"source":"chat","target":"agent"}]}`), &wf))
	assert.Empty(t, workflow.Validate(&wf, everyFileExists{}, ds.profileChecker, nil), "default-profile workflow validates")

	var left int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_seed_pending`).Scan(&left))
	assert.Zero(t, left, "no seed stays pending")
	assert.Empty(t, preflightNotifications(t, ds), "every shipped default pins")
}

// A persisted fixed-mode profile that cannot pin is named at boot with a
// notification, once per unchanged condition; a following profile is never
// flagged.
func TestPinPreflightNotifiesRefusingProfilesRoot(t *testing.T) {
	configPath := generatedConfigRoot(t)
	require.NoError(t, os.WriteFile(configPath, []byte("samples:\n  seed: false\nprofiles: []\n"), 0600))
	ds := bootRoot(t, configPath)
	ctx := adminCtxForLocalUser(t, ds)
	storeProfileUnchecked(t, ctx, ds, provider.Profile{Alias: "legacy", Kind: provider.ProfileKindChat, Model: "sonnet"})
	ds.Close(context.Background())

	again := bootRoot(t, configPath)
	assert.Equal(t, []string{"profile legacy"}, preflightNotifications(t, again))
	again.Close(context.Background())

	third := bootRoot(t, configPath)
	assert.Equal(t, []string{"profile legacy"}, preflightNotifications(t, third), "an unchanged refusal is not re-notified")
}

type everyFileExists struct{}

func (everyFileExists) FileExists(string, string) bool { return true }
