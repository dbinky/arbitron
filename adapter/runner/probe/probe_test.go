package probe

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
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// argRunner answers by the first argument (load-bearing) and records specs.
type argRunner struct {
	mu      sync.Mutex
	scripts map[string]argScript
	specs   []claudecli.RunSpec
}

type argScript struct {
	lines    []string
	waitErr  error
	startErr error
}

func (r *argRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	s := r.scripts[strings.Join(spec.Args, " ")]
	r.mu.Unlock()
	if s.startErr != nil {
		return nil, nil, s.startErr
	}
	ch := make(chan string, len(s.lines))
	for _, l := range s.lines {
		ch <- l
	}
	close(ch)
	return ch, func() error { return s.waitErr }, nil
}

func anthSource() runner.Source {
	return runner.Source{
		ID: runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, ""), RunnerID: runner.BuiltInRunnerID,
		Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}, Health: runner.SourceHealth{ConfirmationModel: "claude-sonnet-4-5"},
	}
}

// authFixture returns the stdout lines of a testdata/claude_auth_status fixture.
func authFixture(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude_auth_status", name))
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func TestClaudeAuthProber(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-leak")
	cases := []struct {
		name string
		s    argScript
		want runner.AuthState
	}{
		// Real shapes, copied from docs/evidence/remote-runners/claude-auth-status.md.
		{"observed logged in, exit 0", argScript{lines: authFixture(t, "logged_in.json")}, "ok"},
		{"observed logged out, exit 1", argScript{lines: authFixture(t, "logged_out.json"), waitErr: errors.New("exit status 1")}, "logged_out"},
		// Synthetic negatives: anything outside the observed contract is unknown.
		{"logged out but exit 0", argScript{lines: []string{`{"loggedIn":false}`}}, "unknown"},
		{"logged in but non-zero exit", argScript{lines: []string{`{"loggedIn":true}`}, waitErr: errors.New("exit status 1")}, "unknown"},
		{"null loggedIn", argScript{lines: []string{`{"loggedIn":null}`}, waitErr: errors.New("exit status 1")}, "unknown"},
		{"timeout with logged out JSON", argScript{lines: []string{`{"loggedIn":false}`}, waitErr: context.DeadlineExceeded}, "unknown"},
		{"non-boolean loggedIn", argScript{lines: []string{`{"loggedIn":"yes"}`}}, "unknown"},
		{"snake case alias is not read", argScript{lines: []string{`{"logged_in":false}`}, waitErr: errors.New("exit status 1")}, "unknown"},
		{"text exit 0 is not ok", argScript{lines: []string{"Logged in as someone"}}, "unknown"},
		{"bare non-zero exit", argScript{waitErr: errors.New("exit status 1")}, "unknown"},
		{"binary missing", argScript{startErr: errors.New("not found")}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &argRunner{scripts: map[string]argScript{"auth status": tc.s}}
			res, err := NewClaudeAuthProber("/opt/claude", r).Probe(context.Background(), anthSource())
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.Auth)
			assert.Nil(t, res.Capacity, "auth-only probe observes no capacity")
			require.Len(t, r.specs, 1)
			assert.Equal(t, "/opt/claude", r.specs[0].Bin)
			for _, kv := range r.specs[0].Env {
				assert.False(t, strings.HasPrefix(kv, "ANTHROPIC_API_KEY="), "stray API key masks the subscription login")
			}
		})
	}
	res, err := NewClaudeAuthProber("/opt/claude", &argRunner{}).Probe(context.Background(), runner.Source{Kind: runner.CredOpenAISubscription})
	require.NoError(t, err)
	assert.Equal(t, runner.AuthState("unknown"), res.Auth, "wrong kind is never judged")
}

func codexScripts(login argScript) map[string]argScript {
	return map[string]argScript{
		"--version":    {lines: []string{"codex-cli 0.171.0"}},
		"login status": login,
	}
}

// fakeLimits stands in for codex.RateLimitsRead (its own app-server stdio
// session, P1 T12); prober tests never script app-server through CommandRunner.
func fakeLimits(snap codex.RateLimitsSnapshot, err error) func(context.Context, codex.Config) (codex.RateLimitsSnapshot, error) {
	return func(context.Context, codex.Config) (codex.RateLimitsSnapshot, error) { return snap, err }
}

func TestCodexProber(t *testing.T) {
	src := runner.Source{ID: runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredOpenAISubscription, ""), Kind: runner.CredOpenAISubscription}
	f, tr := false, true
	used100, used30 := 100.0, 30.0
	reset := time.Unix(1795262400, 0).UTC()
	later := time.Unix(1795565327, 0).UTC()
	snapFalse := codex.RateLimitsSnapshot{OrdinaryUsageAllowed: &f, Windows: []runner.LimitWindow{{LimitID: "codex", DurationMins: 10080, UsedPercent: &used100, ResetAt: &reset}}}
	// Shape of the observed logged-in answer (docs/evidence/remote-runners/codex-app-server.md).
	snapTrue := codex.RateLimitsSnapshot{OrdinaryUsageAllowed: &tr, Windows: []runner.LimitWindow{{LimitID: "codex", DurationMins: 10080, UsedPercent: &used30, ResetAt: &later}}}
	prober := func(login argScript, limits func(context.Context, codex.Config) (codex.RateLimitsSnapshot, error)) *CodexProber {
		p := NewCodexProber(codex.Config{BinaryPath: "codex", Runner: &argRunner{scripts: codexScripts(login)}})
		p.readLimits = limits
		return p
	}

	res, err := prober(argScript{}, fakeLimits(snapFalse, nil)).Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthState("ok"), res.Auth)
	require.NotNil(t, res.Capacity)
	require.NotNil(t, res.Capacity.OrdinaryUsageAllowed)
	assert.False(t, *res.Capacity.OrdinaryUsageAllowed)
	assert.Equal(t, runner.CapacityState("exhausted"), res.Capacity.State)
	require.NotNil(t, res.Capacity.ResetAt)
	assert.Equal(t, int64(1795262400), res.Capacity.ResetAt.Unix())

	res, err = prober(argScript{}, fakeLimits(snapTrue, nil)).Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, runner.CapacityState("available"), res.Capacity.State)
	assert.InDelta(t, 0.70, *res.Capacity.Headroom, 1e-9)

	called := false
	res, err = prober(argScript{waitErr: errors.New("exit status 1")}, func(context.Context, codex.Config) (codex.RateLimitsSnapshot, error) {
		called = true
		return codex.RateLimitsSnapshot{}, nil
	}).Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthState("logged_out"), res.Auth)
	assert.Nil(t, res.Capacity)
	assert.False(t, called, "no rate-limit read for a logged-out login")

	// Any RateLimitsRead failure (id-1 error such as the observed -32600
	// "authentication required", EOF before id 1, timeout, garbage) is
	// capacity unknown and never changes auth.
	res, err = prober(argScript{}, fakeLimits(codex.RateLimitsSnapshot{}, codex.ErrRateLimitsUnavailable)).Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthState("ok"), res.Auth)
	assert.Nil(t, res.Capacity, "unreadable rate limits = capacity unknown, never available")

	res, err = NewCodexProber(codex.Config{BinaryPath: "codex", Runner: &argRunner{scripts: map[string]argScript{"--version": {startErr: errors.New("not found")}}}}).Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthState("unknown"), res.Auth)
}

// ---- LunaRoute ----

type lrStore struct {
	conns map[string]provider.Connection
}

func (s *lrStore) Create(context.Context, provider.Connection, string) error {
	return errors.New("unused")
}
func (s *lrStore) Update(context.Context, provider.Connection, *string) error {
	return errors.New("unused")
}
func (s *lrStore) Delete(context.Context, string) error { return errors.New("unused") }
func (s *lrStore) Get(_ context.Context, name string) (provider.Connection, error) {
	c, ok := s.conns[name]
	if !ok {
		return provider.Connection{}, provider.ErrConnectionNotFound
	}
	return c, nil
}
func (s *lrStore) List(context.Context) ([]provider.Connection, error) { return nil, nil }
func (s *lrStore) Reveal(context.Context, string) (string, error)      { return "k", nil }

type lrProbes struct {
	err  error
	seen []provider.ResolvedBinding
}

func (p *lrProbes) ProbeFor(b provider.ResolvedBinding) (provider.ConnectionProbe, error) {
	p.seen = append(p.seen, b)
	return lrProbe{err: p.err}, nil
}

type lrProbe struct{ err error }

func (p lrProbe) Probe(context.Context, provider.ResolvedBinding) (provider.ProbeResult, error) {
	if p.err != nil {
		return provider.ProbeResult{}, p.err
	}
	return provider.ProbeResult{Reachable: true}, nil
}

func TestLunaRouteProber(t *testing.T) {
	store := &lrStore{conns: map[string]provider.Connection{"lr": {Name: "lr", Provider: provider.CanonicalLunaRoute, BaseURL: "https://lr"}}}
	src := runner.Source{ID: runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredLunaRoute, "lr"), Kind: runner.CredLunaRoute, CredentialRef: "lr"}

	probes := &lrProbes{}
	res, err := NewLunaRouteProber(store, probes).Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthState("ok"), res.Auth)
	require.Len(t, probes.seen, 1)
	assert.Equal(t, "lr", probes.seen[0].ConnectionRef, "probes the source's own connection")

	busy := &lrProbes{err: provider.ErrLunaRouteBusy}
	res, err = NewLunaRouteProber(store, busy).Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthState("unknown"), res.Auth, "busy is never logged-out nor exhausted")
	assert.Nil(t, res.Capacity)

	missing := src
	missing.CredentialRef = "gone"
	res, err = NewLunaRouteProber(store, &lrProbes{}).Probe(context.Background(), missing)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthState("unknown"), res.Auth)
}

// ---- DispatchConfirmer ----

type cfDispatcher struct {
	mu   sync.Mutex
	reqs []runner.ExecRequirements
	jobs []runner.StepJob
	ids  []engine.StepIdentity
	err  error
	wait time.Duration
	out  *runner.ConfirmationResult
}

func (d *cfDispatcher) Dispatch(ctx context.Context, req runner.ExecRequirements, job runner.StepJob) (runner.StepOutput, error) {
	id, _ := engine.StepIdentityFromContext(ctx)
	d.mu.Lock()
	d.reqs, d.jobs, d.ids = append(d.reqs, req), append(d.jobs, job), append(d.ids, id)
	d.mu.Unlock()
	if d.wait > 0 {
		select {
		case <-ctx.Done():
			return runner.StepOutput{}, ctx.Err()
		case <-time.After(d.wait):
		}
	}
	if d.err != nil {
		return runner.StepOutput{}, d.err
	}
	if job.Op == runner.OpConfirm {
		res := d.out
		if res == nil {
			res = &runner.ConfirmationResult{RequestedModel: job.Binding.Model, ExecutedModel: job.Binding.Model, SessionID: "s"}
		}
		return runner.StepOutput{Confirmation: res}, nil
	}
	return runner.StepOutput{Chat: &engine.ChatResponse{Output: "OK"}}, nil
}

func (c *DispatchConfirmer) withDispatcher(d Dispatcher) *DispatchConfirmer { c.Attach(d); return c }

type cfProfiles struct{ ps []provider.Profile }

func (p cfProfiles) List(context.Context) ([]provider.Profile, error) { return p.ps, nil }

func TestDispatchConfirmer(t *testing.T) {
	profiles := cfProfiles{ps: []provider.Profile{
		{Alias: "zz-opus", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"},
		{Alias: "gpt", Kind: provider.ProfileKindAgent, Backend: "codex", Model: "gpt-5.2-codex", ContextWindow: provider.Context1M, Effort: provider.EffortHigh},
		{Alias: "opencode-sonnet", Kind: provider.ProfileKindAgent, Backend: "opencode", Model: "claude-sonnet-4-5"},
		{Alias: "azure", Kind: provider.ProfileKindChat, ConnectionRef: "azure", Model: "claude-sonnet-4-5"},
		{Alias: "aa-sonnet", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-5"},
	}}
	dir := t.TempDir()
	c := NewDispatchConfirmer(profiles, dir, time.Second)

	assert.ErrorIs(t, c.Confirm(context.Background(), anthSource()), ErrConfirmerNotAttached, "fails closed until attached")

	d := &cfDispatcher{}
	c.Attach(d)
	require.NoError(t, c.Confirm(context.Background(), anthSource()))
	require.Len(t, d.reqs, 1)
	req, job, id := d.reqs[0], d.jobs[0], d.ids[0]
	assert.Equal(t, "claude-sonnet-4-5", req.Model, "exact persisted exhausted model, with deterministic profile selection")
	assert.Equal(t, runner.CredAnthropicSubscription, req.Group.Kind)
	assert.Equal(t, runner.BuiltInRunnerID, req.PinnedRunner)
	assert.Equal(t, runner.OpConfirm, req.Op)
	assert.Equal(t, runner.OpConfirm, job.Op)
	assert.Equal(t, runner.AffinityNone, req.Affinity)
	assert.Equal(t, anthSource().ID, req.RequiredSource)
	assert.True(t, req.RequireExecutedModel)
	assert.Equal(t, "claude-code", req.Backend)
	assert.Equal(t, provider.ProfileKindAgent, job.Binding.Kind)
	assert.Equal(t, "claude-code", job.Binding.Backend)
	require.NotNil(t, job.Confirmation)
	assert.Equal(t, dir, job.Confirmation.WorkDir)
	assert.Nil(t, job.Chat)
	assert.Equal(t, engine.OperationProbe, id.Kind)
	assert.True(t, id.Valid())
	assert.Equal(t, "probe:"+string(anthSource().ID), id.NodeID)

	oa := runner.Source{ID: runner.MakeSourceID("mac-a", runner.CredOpenAISubscription, ""), RunnerID: "mac-a", Kind: runner.CredOpenAISubscription,
		Families: []modelidentity.Family{"openai"}, Health: runner.SourceHealth{ConfirmationModel: "gpt-5.2-codex"}}
	require.NoError(t, c.Confirm(context.Background(), oa))
	req, job = d.reqs[1], d.jobs[1]
	assert.Equal(t, runner.RunnerID("mac-a"), req.PinnedRunner)
	assert.Equal(t, oa.ID, req.RequiredSource)
	assert.Equal(t, "codex", job.Binding.Backend)
	assert.Equal(t, runner.CredOpenAISubscription, req.Group.Kind)
	assert.Equal(t, provider.Context1M, req.ContextWindow)
	assert.Equal(t, provider.EffortHigh, job.Binding.Effort)

	d.out = &runner.ConfirmationResult{RequestedModel: "claude-sonnet-4-5", ExecutedModel: "claude-opus-4-1", SessionID: "s"}
	assert.ErrorIs(t, c.Confirm(context.Background(), anthSource()), runner.ErrConfirmationModelMismatch)
	d.out = nil
	d.err = runner.WithOutcome(errors.New("quota"), runner.SourceOutcome{Class: runner.OutcomeExhausted})
	err := c.Confirm(context.Background(), anthSource())
	assert.Equal(t, runner.OutcomeExhausted, runner.ClassifyError(err).Class)

	d.err = nil
	assert.ErrorIs(t, c.Confirm(context.Background(), runner.Source{Kind: runner.CredLunaRoute}), runner.ErrConfirmationUnsupported)
	assert.ErrorIs(t, ErrNoConfirmationModel, runner.ErrConfirmationNoProfile)
	swapped := anthSource()
	swapped.Health.ConfirmationModel = "gpt-5.2-codex"
	assert.ErrorIs(t, c.Confirm(context.Background(), swapped), ErrNoConfirmationModel)
	assert.ErrorIs(t, NewDispatchConfirmer(cfProfiles{}, dir, time.Second).withDispatcher(d).Confirm(context.Background(), anthSource()), ErrNoConfirmationModel)

	slow := &cfDispatcher{wait: time.Minute}
	c2 := NewDispatchConfirmer(profiles, dir, 50*time.Millisecond)
	c2.Attach(slow)
	assert.ErrorIs(t, c2.Confirm(context.Background(), anthSource()), context.DeadlineExceeded, "a capacity wait is bounded")
}
