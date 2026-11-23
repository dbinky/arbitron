package sources

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cmdAnswer struct {
	lines []string
	err   error
}

// fakeRunner answers by argv and records every RunSpec.
type fakeRunner struct {
	mu      sync.Mutex
	answers map[string]cmdAnswer
	specs   []claudecli.RunSpec
}

func (f *fakeRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	a, ok := f.answers[strings.Join(spec.Args, " ")]
	f.mu.Unlock()
	if !ok {
		return nil, nil, errors.New("unexpected command")
	}
	ch := make(chan string, len(a.lines))
	for _, l := range a.lines {
		ch <- l
	}
	close(ch)
	return ch, func() error { return a.err }, nil
}

func TestClaudeAuthReadsLoginStateWithoutAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stray")
	cases := map[string]struct {
		ans  cmdAnswer
		want runner.AuthState
	}{
		"logged in":   {cmdAnswer{lines: []string{`{"loggedIn":true,"authMethod":"claude.ai"}`}}, "ok"},
		"logged out":  {cmdAnswer{lines: []string{`{"loggedIn":false}`}, err: errors.New("exit status 1")}, "logged_out"},
		"unparseable": {cmdAnswer{lines: []string{"Logged in as op@example.com"}}, "unknown"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{answers: map[string]cmdAnswer{"auth status": tc.ans}}
			res, err := ClaudeAuth{Runner: r, Bin: "/opt/claude"}.Probe(context.Background(), runner.Source{Kind: runner.CredAnthropicSubscription})
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.Auth)
			assert.Nil(t, res.Capacity, "auth status never claims capacity")
			require.Len(t, r.specs, 1)
			assert.Equal(t, "/opt/claude", r.specs[0].Bin)
			for _, kv := range r.specs[0].Env {
				assert.False(t, strings.HasPrefix(kv, "ANTHROPIC_API_KEY="), "a stray API key must not mask the subscription login")
			}
		})
	}
}

func TestClaudeAuthRejectsOtherKinds(t *testing.T) {
	_, err := ClaudeAuth{Runner: &fakeRunner{}, Bin: "claude"}.Probe(context.Background(), runner.Source{Kind: runner.CredLunaRoute})
	assert.Error(t, err)
}

type fakeConnProbe struct {
	got []provider.ResolvedBinding
	err error
}

func (f *fakeConnProbe) Probe(ctx context.Context, b provider.ResolvedBinding) (provider.ProbeResult, error) {
	f.got = append(f.got, b)
	return provider.ProbeResult{Reachable: f.err == nil}, f.err
}

func TestLunaRouteAuthUsesRunnerKeyAndMapsOutcomes(t *testing.T) {
	b := provider.ResolvedBinding{Provider: provider.CanonicalLunaRoute, ConnectionRef: "lr-main", RevealKey: func(context.Context) (string, error) { return "lr_local", nil }}
	src := runner.Source{Kind: runner.CredLunaRoute, CredentialRef: "lr-main"}

	ok := &fakeConnProbe{}
	res, err := LunaRouteAuth{Conn: ok, Binding: b}.Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthState("ok"), res.Auth)
	require.Len(t, ok.got, 1)
	key, _ := ok.got[0].RevealKey(context.Background())
	assert.Equal(t, "lr_local", key)

	out := &fakeConnProbe{err: runner.WithOutcome(errors.New("401"), runner.SourceOutcome{Class: runner.OutcomeLoggedOut})}
	res, err = LunaRouteAuth{Conn: out, Binding: b}.Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthState("logged_out"), res.Auth)

	busy := &fakeConnProbe{err: runner.WithOutcome(errors.New("busy"), runner.SourceOutcome{Class: runner.OutcomeTransient})}
	_, err = LunaRouteAuth{Conn: busy, Binding: b}.Probe(context.Background(), src)
	assert.Error(t, err, "congestion is not an auth verdict")

	_, err = LunaRouteAuth{Conn: ok, Binding: b}.Probe(context.Background(), runner.Source{Kind: runner.CredLunaRoute, CredentialRef: "other"})
	assert.Error(t, err, "a prober answers only for its own connection")
}

// keyedProber answers per source kind so results cannot be mixed up.
type keyedProber struct {
	mu    sync.Mutex
	by    map[runner.CredentialKind]runner.ProbeResult
	errs  map[runner.CredentialKind]error
	calls []runner.Source
}

func (k *keyedProber) Probe(ctx context.Context, s runner.Source) (runner.ProbeResult, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls = append(k.calls, s)
	return k.by[s.Kind], k.errs[s.Kind]
}

func TestInventoryProbeKeysResultsBySource(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	p := &keyedProber{
		by: map[runner.CredentialKind]runner.ProbeResult{
			runner.CredAnthropicSubscription: {Auth: "ok"},
			runner.CredLunaRoute:             {Auth: "ok", Capacity: &runner.CapacityObservation{State: "exhausted", ResetAt: &reset}},
		},
		errs: map[runner.CredentialKind]error{},
	}
	inv := New([]Spec{
		{Source: runneragent.Source{Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}}, Prober: p},
		{Source: runneragent.Source{Kind: runner.CredLunaRoute, CredentialRef: "lr-main", Families: []modelidentity.Family{"openai"}}, Prober: p},
	}, map[string]string{"claude": "2.1.400"}, func() time.Time { return now })

	before := inv.Sources(context.Background())
	require.Len(t, before, 2)
	for _, s := range before {
		assert.Equal(t, runner.AuthState("unknown"), s.Health.Auth, "never ok before a probe")
	}

	after := inv.Probe(context.Background())
	require.Len(t, after, 2)
	assert.Equal(t, runner.AuthState("ok"), after[0].Health.Auth)
	assert.Equal(t, runner.CapacityState(""), after[0].Health.Capacity)
	assert.Equal(t, runner.CapacityState("exhausted"), after[1].Health.Capacity)
	assert.Equal(t, &reset, after[1].Health.ResetAt)
	assert.Equal(t, &now, after[1].Health.LastProbeAt)
	assert.Equal(t, after, inv.Sources(context.Background()))
	require.Len(t, p.calls, 2)
	assert.Equal(t, "lr-main", p.calls[1].CredentialRef)
	assert.Equal(t, map[string]string{"claude": "2.1.400"}, inv.HarnessVersions())

	p.errs[runner.CredAnthropicSubscription] = errors.New("timeout")
	again := inv.Probe(context.Background())
	assert.Equal(t, runner.AuthState("ok"), again[0].Health.Auth, "a failed probe keeps the last known auth")
	assert.Equal(t, "probe failed", again[0].Health.Detail)
}

func TestDiscoverAdvertisesClaudeWhenVersionProbeSucceeds(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700))
	r := &fakeRunner{answers: map[string]cmdAnswer{"--version": {lines: []string{"2.1.400 (Claude Code)"}}}}
	specs, harness := Discover(context.Background(), DiscoverInput{ClaudeBinary: bin, Runner: r})
	require.Len(t, specs, 1)
	s := specs[0].Source
	assert.Equal(t, runner.CredAnthropicSubscription, s.Kind)
	assert.Equal(t, []modelidentity.Family{"anthropic"}, s.Families)
	assert.Equal(t, []string{""}, s.Backends)
	assert.Equal(t, "2.1.400", harness["claude"])
	ca, ok := specs[0].Prober.(ClaudeAuth)
	require.True(t, ok)
	assert.Equal(t, bin, ca.Bin)
	for _, sp := range specs {
		assert.NotEqual(t, runner.CredOpenAISubscription, sp.Source.Kind, "openai_subscription is never advertised in Phase 2")
	}
}

func TestDiscoverSkipsClaudeWhenVersionProbeFails(t *testing.T) {
	r := &fakeRunner{answers: map[string]cmdAnswer{"--version": {err: errors.New("exit status 127")}}}
	specs, harness := Discover(context.Background(), DiscoverInput{ClaudeBinary: "/nonexistent/claude", Runner: r})
	assert.Empty(t, specs)
	assert.NotContains(t, harness, "claude")
}

func TestDiscoverAdvertisesConfiguredLunaRouteSource(t *testing.T) {
	r := &fakeRunner{answers: map[string]cmdAnswer{"--version": {err: errors.New("no claude")}}}
	cp := &fakeConnProbe{}
	lr := config.LunaRoute{ConnectionRef: "lr-main", Families: []modelidentity.Family{"openai", "deepseek"}, Models: []string{"gpt-6.1"}, APIKey: "lr_local"}
	specs, _ := Discover(context.Background(), DiscoverInput{ClaudeBinary: "/x/claude", Runner: r, LunaRoute: lr, LunaRouteProbe: cp})
	require.Len(t, specs, 1)
	s := specs[0].Source
	assert.Equal(t, runner.CredLunaRoute, s.Kind)
	assert.Equal(t, "lr-main", s.CredentialRef)
	assert.Equal(t, []modelidentity.Family{"openai", "deepseek"}, s.Families)
	assert.Equal(t, []string{"gpt-6.1"}, s.Models)
	la, ok := specs[0].Prober.(LunaRouteAuth)
	require.True(t, ok)
	key, _ := la.Binding.RevealKey(context.Background())
	assert.Equal(t, "lr_local", key)
	assert.Equal(t, "", la.Binding.BaseURL)

	none, _ := Discover(context.Background(), DiscoverInput{ClaudeBinary: "/x/claude", Runner: r, LunaRoute: config.LunaRoute{}, LunaRouteProbe: cp})
	assert.Empty(t, none, "no LunaRoute key: no LunaRoute source")
}
