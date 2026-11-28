package probe

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rcAlerts struct {
	mu                   sync.Mutex
	loggedOut, recovered int
}

func (a *rcAlerts) SourceLoggedOut(context.Context, runner.Source) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loggedOut++
	return nil
}
func (a *rcAlerts) SourceRecovered(context.Context, runner.Source) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recovered++
	return nil
}

type rcFixture struct {
	pool *hubtest.Pool
	mon  *runner.HealthMonitor
	d    *cfDispatcher
	src  runner.SourceID
}

// newRCFixture: a remote runner mac-a with one Claude subscription source in
// real SQLite, the real HealthMonitor, the real DispatchConfirmer and a
// recording dispatcher (the dispatcher/slot path is covered by Cycle 16 roots).
func newRCFixture(t *testing.T, profiles ProfileLister) *rcFixture {
	t.Helper()
	p := hubtest.NewPool(t)
	p.SeedRunner(t, "mac-a", 1)
	src := runner.Source{ID: runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, ""), RunnerID: "mac-a",
		Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}}
	require.NoError(t, p.Registry.ReplaceSources(context.Background(), "mac-a", []runner.Source{src}))
	d := &cfDispatcher{}
	c := NewDispatchConfirmer(profiles, t.TempDir(), time.Second)
	c.Attach(d)
	mon := runner.NewHealthMonitor(runner.MonitorDeps{Registry: p.Registry, Confirm: c, Alerts: &rcAlerts{},
		Events: p.Events, Notifier: p.Notifier, Clock: p.Clock.Now, Interval: time.Minute, Rand: func() float64 { return 0 }})
	return &rcFixture{pool: p, mon: mon, d: d, src: src.ID}
}

// exhaustUnknownResetThenConfirm drives the real episode: exhausted with
// unknown reset -> probation after one interval -> one bounded confirmation.
func (f *rcFixture) exhaustUnknownResetThenConfirm(t *testing.T, o runner.SourceOutcome) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, f.mon.ApplyOutcome(ctx, f.src, o))
	f.pool.Clock.Advance(2 * time.Minute)
	require.NoError(t, f.mon.Tick(ctx)) // exhausted -> probation
	require.NoError(t, f.mon.Tick(ctx)) // probation -> confirmation
	f.mon.Wait()
}

func (f *rcFixture) health(t *testing.T) runner.SourceHealth {
	t.Helper()
	s, err := f.pool.Registry.GetSource(context.Background(), f.src)
	require.NoError(t, err)
	return s.Health
}

var rcOpus = runner.SourceOutcome{Class: runner.OutcomeExhausted, Model: "claude-opus-4-1"}

func TestRemoteConfirmation_WrongModelCannotRecover(t *testing.T) {
	f := newRCFixture(t, cfProfiles{ps: []provider.Profile{{Alias: "opus", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"}}})
	f.d.out = &runner.ConfirmationResult{RequestedModel: "claude-opus-4-1", ExecutedModel: "claude-sonnet-4-5", SessionID: "s"}
	f.exhaustUnknownResetThenConfirm(t, rcOpus)
	require.Len(t, f.d.reqs, 1, "exactly one bounded confirmation ran")
	assert.Equal(t, f.src, f.d.reqs[0].RequiredSource)
	assert.Equal(t, runner.RunnerID("mac-a"), f.d.reqs[0].PinnedRunner)
	h := f.health(t)
	assert.Equal(t, runner.CapacityProbation, h.Capacity)
	assert.Equal(t, "claude-opus-4-1", h.ConfirmationModel)
	assert.Equal(t, runner.DetailConfirmMismatch, h.Detail)
	assert.Empty(t, f.pool.EventsOf(t, "source.recovered"))
}

func TestRemoteConfirmation_NoMatchingProfileRetainsProbation(t *testing.T) {
	f := newRCFixture(t, cfProfiles{ps: []provider.Profile{
		{Alias: "sonnet", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-5"},
		{Alias: "opus-azure", Kind: provider.ProfileKindChat, ConnectionRef: "azure", Model: "claude-opus-4-1"},
		{Alias: "opus-codex", Kind: provider.ProfileKindAgent, Backend: "codex", Model: "claude-opus-4-1"},
	}})
	f.exhaustUnknownResetThenConfirm(t, rcOpus)
	assert.Empty(t, f.d.reqs, "no same-family, connection or other-backend substitute is dispatched")
	h := f.health(t)
	assert.Equal(t, runner.CapacityProbation, h.Capacity)
	assert.Nil(t, h.ResetAt)
	assert.Equal(t, runner.DetailConfirmNoProfile, h.Detail)
	assert.Empty(t, f.pool.EventsOf(t, "source.recovered"))
	require.NoError(t, f.mon.ApplyOutcome(context.Background(), f.src, runner.SourceOutcome{Class: runner.OutcomeNone, Model: "claude-opus-4-1"}))
	assert.Equal(t, runner.CapacityAvailable, f.health(t).Capacity, "a later real exact-model success may still confirm recovery")
}

func TestRemoteConfirmation_QuotaOrExportFailureCannotMarkAvailable(t *testing.T) {
	profiles := cfProfiles{ps: []provider.Profile{{Alias: "opus", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"}}}

	quota := newRCFixture(t, profiles)
	reset := quota.pool.Clock.Now().Add(5 * time.Hour)
	quota.d.err = runner.WithOutcome(assert.AnError, runner.SourceOutcome{Class: runner.OutcomeExhausted, Model: "claude-opus-4-1", ResetAt: &reset})
	quota.exhaustUnknownResetThenConfirm(t, rcOpus)
	h := quota.health(t)
	assert.Equal(t, runner.CapacityExhausted, h.Capacity, "the confirmation's own quota failure starts a new episode")
	require.NotNil(t, h.ResetAt)
	assert.True(t, h.ResetAt.Equal(reset))

	lost := newRCFixture(t, profiles)
	lost.d.err = runner.WithOutcome(assert.AnError, runner.SourceOutcome{Class: runner.OutcomeRunnerLost})
	lost.exhaustUnknownResetThenConfirm(t, rcOpus)
	h = lost.health(t)
	assert.Equal(t, runner.CapacityProbation, h.Capacity, "transport loss cannot recover the source")
	assert.Equal(t, runner.DetailConfirmTransport, h.Detail)
	for _, f := range []*rcFixture{quota, lost} {
		assert.Empty(t, f.pool.EventsOf(t, "source.recovered"))
	}
	t.Run("export/commit failure", func(t *testing.T) {
		testDeferredConfirmation(t, assert.AnError, false)
	})
}

// rcNativeResult checks the assignment that actually won placement, rather than
// returning evidence for an unrelated source, model, backend or operation.
type rcNativeResult struct {
	source  runner.SourceID
	workDir string
	result  runner.ConfirmationResult
}

func (r rcNativeResult) Run(ctx context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	id, ok := engine.StepIdentityFromContext(ctx)
	if !ok || !id.Valid() || id.Kind != engine.OperationProbe || id.NodeID != "probe:"+string(r.source) ||
		a.Job.SourceID != r.source || a.Fence.RunnerID != "mac-a" || job.Op != runner.OpConfirm ||
		job.Binding.Kind != provider.ProfileKindAgent || job.Binding.Provider != "claude-cli" ||
		job.Binding.Model != r.result.RequestedModel || job.Binding.Backend != "claude-code" ||
		job.Binding.ContextWindow != provider.Context1M || job.Binding.Effort != provider.EffortHigh ||
		job.Confirmation == nil || job.Confirmation.WorkDir != r.workDir {
		return runner.StepOutput{}, fmt.Errorf("unexpected confirmation assignment: %+v / %+v", a, job)
	}
	return runner.StepOutput{Confirmation: &r.result}, nil
}

// rcCommit checks that the dispatcher builds a validated receipt from native
// evidence while the exact fenced job is still running. Success delegates to
// the production SQLite committer; failure models a rejected durable commit.
type rcCommit struct {
	t      *testing.T
	pool   *hubtest.Pool
	source runner.SourceID
	fail   error
	calls  int
}

func (c *rcCommit) CommitProbe(ctx context.Context, f engine.JobFenceRef, result runner.ProbeCompletion) error {
	c.calls++
	assert.Equal(c.t, runner.ProbeCompletion{RequestedModel: rcOpus.Model, ExecutedModel: rcOpus.Model, Validated: true}, result)
	var source, state, assigned string
	var generation int64
	err := c.pool.DB.QueryRowContext(ctx, `SELECT source_id, state, runner_id, generation FROM runner_jobs WHERE id=?`, f.JobID.String()).Scan(&source, &state, &assigned, &generation)
	if err != nil {
		return err
	}
	assert.Equal(c.t, string(c.source), source)
	assert.Equal(c.t, "running", state)
	assert.Equal(c.t, assigned, f.RunnerID)
	assert.EqualValues(c.t, generation, f.Generation)
	sourceHealth, err := c.pool.Registry.GetSource(ctx, c.source)
	if err != nil {
		return err
	}
	assert.Equal(c.t, runner.CapacityProbation, sourceHealth.Health.Capacity, "health cannot recover before durable completion")
	if c.fail != nil {
		return c.fail
	}
	return adaptsqlite.NewProbeCommitRepo(c.pool.DB).CommitProbe(ctx, f, result)
}

func testDeferredConfirmation(t *testing.T, commitErr error, invalidEvidence bool) {
	t.Helper()
	ctx := context.Background()
	p := hubtest.NewPool(t)
	p.SeedRunner(t, "mac-a", 1)
	require.NoError(t, p.Registry.SetLiveness(ctx, "mac-a", runner.LivenessOnline, p.Clock.Now()))
	binding := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Backend: "claude-code", Model: rcOpus.Model,
		ContextWindow: provider.Context1M, Effort: provider.EffortHigh}
	capability := hubtest.Capability(runner.OpConfirm, binding, "anthropic")
	capability.ContextWindows = []provider.ContextWindow{binding.ContextWindow}
	capability.Efforts = []provider.Effort{binding.Effort}
	src := runner.Source{ID: runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, ""), RunnerID: "mac-a",
		Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"},
		Capabilities: []runner.ExecutionCapability{capability},
		Health:       runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}}
	require.NoError(t, p.Registry.ReplaceSources(ctx, "mac-a", []runner.Source{src}))
	workDir := t.TempDir()
	confirmer := NewDispatchConfirmer(cfProfiles{ps: []provider.Profile{{Alias: "opus", Model: rcOpus.Model,
		ContextWindow: provider.Context1M, Effort: provider.EffortHigh}}}, workDir, time.Second)
	mon := runner.NewHealthMonitor(runner.MonitorDeps{Registry: p.Registry, Confirm: confirmer, Alerts: &rcAlerts{}, Events: p.Events,
		Notifier: p.Notifier, Clock: p.Clock.Now, Interval: time.Minute, Rand: func() float64 { return 0 }})
	native := rcNativeResult{source: src.ID, workDir: workDir, result: runner.ConfirmationResult{RequestedModel: rcOpus.Model, ExecutedModel: rcOpus.Model, SessionID: "native-session"}}
	if invalidEvidence {
		native.result.SessionID = ""
	}
	commit := &rcCommit{t: t, pool: p, source: src.ID, fail: commitErr}
	d := runner.NewStepDispatcher(runner.DispatcherDeps{Registry: p.Registry, Ledger: p.Ledger,
		Runners: runner.NewHealthReportingRunnerSet(runner.StaticRunnerSet{"mac-a": native}, mon),
		Health:  mon, Events: p.Events, Status: runner.NewExecutionStatusProjector(p.Ledger), ProbeCommits: commit,
		Clock: p.Clock.Now, TransientMaxAttempts: 1, BackoffMin: time.Millisecond, BackoffMax: time.Second})
	t.Cleanup(d.Close)
	confirmer.Attach(d)
	require.NoError(t, mon.ApplyOutcome(ctx, src.ID, rcOpus))
	p.Clock.Advance(2 * time.Minute)
	require.NoError(t, mon.Tick(ctx))
	require.NoError(t, mon.Tick(ctx))
	mon.Wait()
	stored, err := p.Registry.GetSource(ctx, src.ID)
	require.NoError(t, err)
	if commitErr != nil || invalidEvidence {
		assert.Equal(t, runner.CapacityProbation, stored.Health.Capacity)
		wantDetail := runner.DetailConfirmFailed
		if invalidEvidence {
			wantDetail = runner.DetailConfirmMismatch
		}
		assert.Equal(t, wantDetail, stored.Health.Detail)
		assert.Empty(t, p.EventsOf(t, "source.recovered"))
	} else {
		assert.Equal(t, runner.CapacityAvailable, stored.Health.Capacity)
		assert.Len(t, p.EventsOf(t, "source.recovered"), 1)
	}
	if invalidEvidence {
		assert.Zero(t, commit.calls, "unverified native evidence cannot reach durable completion")
	} else {
		assert.Equal(t, 1, commit.calls, "recovery must cross the durable completion boundary")
	}
	var state, resultRef string
	require.NoError(t, p.DB.QueryRow(`SELECT state, result_ref FROM runner_jobs WHERE operation_id='probe/confirm'`).Scan(&state, &resultRef))
	if commitErr != nil || invalidEvidence {
		assert.Equal(t, "failed", state)
		assert.Empty(t, resultRef)
	} else {
		assert.Equal(t, "completed", state)
		assert.Equal(t, "probe:"+rcOpus.Model, resultRef)
	}
	active, err := p.Ledger.ListActive(ctx)
	require.NoError(t, err)
	assert.Empty(t, active, "both durable completion and commit failure release the slot")
}

func TestRemoteConfirmation_DeferredCommitUsesVerifiedEvidence(t *testing.T) {
	t.Run("exact native evidence", func(t *testing.T) { testDeferredConfirmation(t, nil, false) })
	t.Run("missing native session", func(t *testing.T) { testDeferredConfirmation(t, nil, true) })
}
