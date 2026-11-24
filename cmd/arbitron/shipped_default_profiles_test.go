package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const shippedDefaultsCatalog = `{"anthropic":{"id":"anthropic","models":{
 "claude-sonnet-5":{"id":"claude-sonnet-5","release_date":"2026-03-01"},
 "claude-sonnet-5-5":{"id":"claude-sonnet-5-5","release_date":"2026-08-01"},
 "claude-opus-5":{"id":"claude-opus-5","release_date":"2026-03-01"},
 "claude-opus-5-5":{"id":"claude-opus-5-5","release_date":"2026-08-01"},
 "claude-opus-5-5-fast":{"id":"claude-opus-5-5-fast","release_date":"2026-08-02"},
 "claude-haiku-4-5":{"id":"claude-haiku-4-5","release_date":"2025-10-01"}}}}`

// shippedDefaultsWant is the model each shipped default follows to on
// shippedDefaultsCatalog: the newest non-variant model of its tier.
var shippedDefaultsWant = map[string]string{
	"default-chat":  "claude-sonnet-5-5",
	"default-agent": "claude-sonnet-5-5",
	"assessor":      "claude-sonnet-5-5",
	"interviewer":   "claude-sonnet-5-5",
	"reviewer":      "claude-sonnet-5-5",
	"coder":         "claude-opus-5-5",
	"planner":       "claude-opus-5-5",
}

// shippedCatalog serves shippedDefaultsCatalog as the catalog source, or fails
// every fetch while down, and counts fetches.
type shippedCatalog struct {
	mu      sync.Mutex
	down    bool
	fetches int
}

func (c *shippedCatalog) deps() ModelDependencies {
	fetch := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.fetches++
		if c.down {
			return nil, errors.New("catalog unreachable")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(shippedDefaultsCatalog)), Request: r}, nil
	})
	offline := catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	return ModelDependencies{FetchTransport: fetch, ProviderTransport: offline}
}

func (c *shippedCatalog) fetched() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetches
}

// useFakeClaude points llm.binarypath at a claude stand-in that reports the
// --model it was given as the executed model, and returns its argv log.
func useFakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version) echo '2.1.330 (Claude Code)'; exit 0 ;; auth) echo '{\"loggedIn\":true}'; exit 0 ;; esac\n" +
		"echo \"$PWD $*\" >> '" + argvLog + "'\n" +
		"cat > /dev/null\n" +
		"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
		"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"s\",\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	t.Setenv("ARBITRON_LLM_BINARYPATH", bin)
	return argvLog
}

// bootShippedDefaults boots the real composition root on the config file a
// fresh install writes (adapterconfig.Load on a missing path), with only the
// install paths, the claude binary and the catalog transport replaced.
func bootShippedDefaults(t *testing.T, catalog *shippedCatalog) (*domainStack, string) {
	t.Helper()
	configPath := generatedConfigRoot(t)
	_, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.FileExists(t, configPath, "a fresh install writes the shipped default config")
	t.Setenv("ARBITRON_SAMPLES_SEED", "false")
	argvLog := useFakeClaude(t)

	ctx := context.Background()
	ds, err := initDomainWithModelDependencies(ctx, configPath, catalog.deps())
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(ctx) })
	require.NoError(t, ds.Start(ctx))
	return ds, argvLog
}

// The shipped defaults are what a fresh install runs the bundled samples on.
// Pinned profiles must name a fixed version, so every shipped default follows
// an Anthropic tier and the first boot selects the newest usable model.
func TestShippedDefaultProfilesSeedOnFirstBoot(t *testing.T) {
	catalog := &shippedCatalog{}
	ds, argvLog := bootShippedDefaults(t, catalog)
	ctx := context.Background()
	repo := adaptsqlite.NewProfileRepo(ds.db)

	shipped := map[string]bool{}
	for _, p := range ds.cfg.Profiles {
		shipped[p.Alias] = true
	}
	for alias := range shippedDefaultsWant {
		assert.True(t, shipped[alias], "the fresh-install config must ship %q", alias)
	}
	for alias := range shipped {
		model, ok := shippedDefaultsWant[alias]
		require.True(t, ok, "unexpected shipped default %q: add it to this test", alias)
		stored, err := repo.Get(ctx, alias)
		require.NoError(t, err, "shipped default %q must seed on the first boot", alias)
		assert.Equal(t, provider.SelectionFollowing, stored.SelectionMode, alias)
		assert.Equal(t, model, stored.Model, "%q follows the newest usable model of its tier", alias)
		binding, err := ds.profileResolver.Resolve(ctx, alias)
		require.NoError(t, err, "%q must be resolvable for runs", alias)
		assert.Equal(t, model, binding.Model)
	}
	pending, err := repo.PendingProfileSeed(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, pending, "no shipped default is left pending after a healthy first boot")
	assert.Empty(t, preflightNotifications(t, ds), "no shipped default refuses triggers")

	assert.Positive(t, catalog.fetched(), "selection must come from the catalog, not a hard-coded id")
	argv := strings.Join(readArgvLog(t, argvLog), "\n")
	assert.Contains(t, argv, "--model claude-sonnet-5-5", "the sonnet pick is proven usable through the CLI")
	assert.Contains(t, argv, "--model claude-opus-5-5", "the opus pick is proven usable through the CLI")
}

// With the catalog down on the first boot, the shipped defaults are deferred
// for retry, never refused: refusal would leave a fresh install with no
// profiles at all.
func TestShippedDefaultProfilesDeferWhenCatalogDown(t *testing.T) {
	catalog := &shippedCatalog{down: true}
	ds, _ := bootShippedDefaults(t, catalog)
	ctx := context.Background()
	repo := adaptsqlite.NewProfileRepo(ds.db)
	pending, err := repo.PendingProfileSeed(ctx, nil)
	require.NoError(t, err)
	var aliases []string
	for _, p := range pending {
		aliases = append(aliases, p.Alias)
		_, err := repo.Get(ctx, p.Alias)
		assert.ErrorIs(t, err, provider.ErrProfileNotFound)
	}
	assert.ElementsMatch(t, []string{"default-chat", "default-agent", "assessor", "interviewer", "reviewer", "coder", "planner"}, aliases,
		"every shipped default stays pending for the next refresh")
	assert.Positive(t, catalog.fetched(), "the deferral follows a real failed fetch")
}
