package remote_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/adapter/runner/remote"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

const workDir = "/srv/arbitron/workspaces/run-1"

// builtinRunner stands in for the built-in StepRunner: it executes only
// built-in assignments, can hold work until released, and records each job.
type builtinRunner struct {
	mu       sync.Mutex
	hold     bool
	gate     chan struct{}
	jobs     []runner.StepJob
	inflight int
	max      int
}

func newBuiltinRunner() *builtinRunner { return &builtinRunner{gate: make(chan struct{})} }

func (b *builtinRunner) SetHold(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hold = v
}

// ReleaseAll stops holding and lets every held job finish.
func (b *builtinRunner) ReleaseAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hold = false
	close(b.gate)
	b.gate = make(chan struct{})
}

func (b *builtinRunner) Run(ctx context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	if a.Fence.RunnerID != runner.BuiltInRunnerID {
		return runner.StepOutput{}, fmt.Errorf("built-in runner handed an assignment for %q", a.Fence.RunnerID)
	}
	b.mu.Lock()
	rec := job
	if job.Chat != nil {
		c := *job.Chat
		rec.Chat = &c
	}
	b.jobs = append(b.jobs, rec)
	b.inflight++
	if b.inflight > b.max {
		b.max = b.inflight
	}
	hold, gate := b.hold, b.gate
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.inflight--
		b.mu.Unlock()
	}()
	if hold {
		select {
		case <-gate:
		case <-ctx.Done():
			return runner.StepOutput{}, ctx.Err()
		}
	}
	return runner.StepOutput{Chat: &engine.ChatResponse{Output: "builtin:" + job.Chat.Model, Model: job.Chat.Model}}, nil
}

func (b *builtinRunner) Jobs() []runner.StepJob {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]runner.StepJob(nil), b.jobs...)
}

func (b *builtinRunner) Inflight() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inflight
}

func (b *builtinRunner) Max() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.max
}

// innerSelector is the validating inner selector; routed providers must never
// execute through it (execution belongs to the placed runner).
type innerSelector struct{ t *testing.T }

func (s innerSelector) ChatProviderFor(provider.ResolvedBinding) (engine.ChatProvider, error) {
	return mustNotRun(s), nil
}

type mustNotRun struct{ t *testing.T }

func (m mustNotRun) Chat(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
	m.t.Error("the inner chat provider executed on the server")
	return nil, errors.New("inner provider executed")
}

func (m mustNotRun) StreamChat(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	m.t.Error("the inner chat provider streamed on the server")
	return nil, errors.New("inner provider executed")
}

type auditSpy struct {
	mu      sync.Mutex
	actions []string
}

func (a *auditSpy) Record(_ context.Context, action, _, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action)
}

func (a *auditSpy) Actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.actions...)
}

// pool is the real Phase 2 execution path: routing chat selector with the
// hermetic affinity → StepDispatcher → HubRunnerSet → built-in runner or
// remote.Runner → Hub → in-memory remote runners, over the SQLite pool.
type pool struct {
	*hubtest.Pool
	hub     *runner.Hub
	builtin *builtinRunner
	sel     engine.ChatProviderSelector
	runID   uuid.UUID
}

func newPool(t *testing.T, builtinLimit int) *pool {
	t.Helper()
	p := hubtest.NewPool(t)
	hub := p.NewHub(hubtest.HubConfig())
	p.SeedBuiltIn(t, builtinLimit, hubtest.BuiltInAnthropic())
	b := newBuiltinRunner()
	wake, unsubscribe := p.Notifier.Subscribe()
	t.Cleanup(unsubscribe)
	d := runner.NewStepDispatcher(runner.DispatcherDeps{
		Registry: p.Registry, Ledger: p.Ledger,
		Runners: runner.NewHubRunnerSet(b, remote.New(hub), p.Registry),
		Events:  p.Events, Status: runner.NewExecutionStatusProjector(p.Ledger), Health: p.Health, Telemetry: hub,
		Clock: p.Clock.Now, TransientMaxAttempts: 1, BackoffMin: time.Millisecond, BackoffMax: time.Millisecond, Wake: wake,
	})
	return &pool{Pool: p, hub: hub, builtin: b,
		sel: runner.NewRoutingChatSelector(innerSelector{t}, d, runner.HermeticChatAffinity), runID: p.SeedRun(t)}
}

func (p *pool) connect(t *testing.T, id runner.RunnerID, limit int, sources ...runner.SourceReport) *hubtest.InMemoryRunner {
	t.Helper()
	return hubtest.ConnectInMemoryRunner(t, p.hub, p.SeedRunner(t, id, limit), hubtest.DarwinHello(sources...))
}

func (p *pool) cordonBuiltIn(t *testing.T, cordoned bool) {
	t.Helper()
	require.NoError(t, p.Registry.SetCordoned(context.Background(), runner.BuiltInRunnerID, cordoned))
	p.Notifier.Notify()
}

func (p *pool) queued(t *testing.T) int {
	t.Helper()
	jobs, err := p.Ledger.ListByRun(context.Background(), p.runID)
	require.NoError(t, err)
	n := 0
	for _, j := range jobs {
		if j.State == runner.JobQueued {
			n++
		}
	}
	return n
}

// step runs one routed chat call under a fresh step identity of kind.
func (p *pool) step(ctx context.Context, kind engine.OperationKind, b provider.ResolvedBinding, req engine.ChatRequest) (*engine.ChatResponse, error) {
	ctx = engine.ContextWithStepIdentity(ctx, engine.StepIdentity{RunID: p.runID, NodeID: "llm-" + uuid.NewString()[:8],
		InvocationID: uuid.New(), OperationID: "node", Kind: kind, Attempt: 1})
	cp, err := p.sel.ChatProviderFor(b)
	if err != nil {
		return nil, err
	}
	return cp.Chat(ctx, req)
}

type stepResult struct {
	resp *engine.ChatResponse
	err  error
}

func (p *pool) goStep(ctx context.Context, kind engine.OperationKind, b provider.ResolvedBinding, req engine.ChatRequest) <-chan stepResult {
	ch := make(chan stepResult, 1)
	go func() {
		resp, err := p.step(ctx, kind, b, req)
		ch <- stepResult{resp, err}
	}()
	return ch
}

func await(t *testing.T, ch <-chan stepResult) stepResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("step did not finish")
		return stepResult{}
	}
}

func opus() provider.ResolvedBinding {
	return provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Model: "claude-opus-5-5"}
}

func opusReq() engine.ChatRequest {
	return engine.ChatRequest{Model: "claude-opus-5-5", Prompt: "summarise", WorkDir: workDir}
}

func TestRemote_FanOutRespectsEachStepLimit(t *testing.T) {
	p := newPool(t, 2)
	macA := p.connect(t, "mac-a", 2, hubtest.AnthropicSource())
	macB := p.connect(t, "mac-b", 2, hubtest.AnthropicSource())
	macA.SetHold(true)
	macB.SetHold(true)
	p.builtin.SetHold(true)
	ctx := context.Background()

	var first []<-chan stepResult
	for i := 0; i < 6; i++ {
		first = append(first, p.goStep(ctx, engine.OperationNode, opus(), opusReq()))
	}
	require.Eventually(t, func() bool { return p.builtin.Inflight() == 2 }, 3*time.Second, 2*time.Millisecond)
	macA.AwaitInflight(t, 2)
	macB.AwaitInflight(t, 2)

	seventh := p.goStep(ctx, engine.OperationNode, opus(), opusReq())
	require.Eventually(t, func() bool { return p.queued(t) == 1 }, 3*time.Second, 5*time.Millisecond, "the seventh step waits for a slot")
	assert.Never(t, func() bool { return macA.Inflight() > 2 || macB.Inflight() > 2 || p.builtin.Inflight() > 2 },
		150*time.Millisecond, 10*time.Millisecond)

	macA.SetHold(false)
	macA.ReleaseAll()
	r := await(t, seventh)
	require.NoError(t, r.err)
	assert.Equal(t, "remote:mac-a:claude-opus-5-5", r.resp.Output, "the freed mac-a slot takes the waiting step")

	p.builtin.ReleaseAll()
	macB.SetHold(false)
	macB.ReleaseAll()
	counts := map[string]int{}
	for _, ch := range first {
		res := await(t, ch)
		require.NoError(t, res.err)
		counts[res.resp.Output.(string)]++
	}
	assert.Equal(t, map[string]int{"builtin:claude-opus-5-5": 2, "remote:mac-a:claude-opus-5-5": 2, "remote:mac-b:claude-opus-5-5": 2}, counts)
	assert.Equal(t, 2, macA.MaxInflight())
	assert.Equal(t, 2, macB.MaxInflight())
	assert.Equal(t, 2, p.builtin.Max())
	for _, j := range p.builtin.Jobs() {
		assert.Equal(t, workDir, j.Chat.WorkDir, "built-in execution keeps the server workspace")
	}
}

func TestRemote_LunaRouteStepsRunOnlyWhereTheSourceIsAdvertised(t *testing.T) {
	p := newPool(t, 2)
	macA := p.connect(t, "mac-a", 2, hubtest.AnthropicSource(), hubtest.LunaRouteSource("lunaroute", "zhipuai"))
	macB := p.connect(t, "mac-b", 2, hubtest.AnthropicSource())
	var revealed atomic.Bool
	b := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: provider.CanonicalLunaRoute, Model: "glm-5.3",
		ConnectionRef: "lunaroute", Pinned: true, BaseURL: "https://lunaroute.internal.example/v1",
		RevealKey: func(context.Context) (string, error) { revealed.Store(true); return "lr_live_secret", nil }}
	ctx := context.Background()

	var chans []<-chan stepResult
	for i := 0; i < 3; i++ {
		chans = append(chans, p.goStep(ctx, engine.OperationNode, b, engine.ChatRequest{Model: "glm-5.3", Prompt: "classify", WorkDir: workDir}))
	}
	for _, ch := range chans {
		r := await(t, ch)
		require.NoError(t, r.err)
		assert.Equal(t, "remote:mac-a:glm-5.3", r.resp.Output)
	}
	assert.False(t, revealed.Load(), "the server never reveals the LunaRoute key for remote work")
	assert.Empty(t, macB.Dispatches(), "mac-b does not advertise LunaRoute")
	assert.Empty(t, p.builtin.Jobs(), "the built-in runner has no LunaRoute source in this pool")
	require.Len(t, macA.Dispatches(), 3)
	for _, d := range macA.Dispatches() {
		require.NotNil(t, d.Dispatch.Chat)
		assert.Equal(t, runner.CredLunaRoute, d.Dispatch.Chat.CredentialKind, "the runner uses its own LunaRoute key")
		assert.Equal(t, "glm-5.3", d.Dispatch.Chat.Model)
		assert.True(t, d.Dispatch.Chat.RequireExecutedModel, "a pinned model must be proven by the runner")
		dump := fmt.Sprintf("%+v", *d.Dispatch.Chat)
		for _, secret := range []string{"lunaroute.internal", "lr_live_secret", workDir} {
			assert.NotContains(t, dump, secret)
		}
	}
}

func TestRemote_OnlyHermeticChatLeavesTheServer(t *testing.T) {
	p := newPool(t, 2)
	macA := p.connect(t, "mac-a", 2, hubtest.AnthropicSource())
	ctx := context.Background()
	withTools := opus()
	withTools.AllowedTools = []string{"Read"}
	resume := opusReq()
	resume.SessionID = "sess-1"

	for _, tc := range []struct {
		name string
		kind engine.OperationKind
		b    provider.ResolvedBinding
		req  engine.ChatRequest
	}{
		{"interactive turn", engine.OperationTurn, opus(), opusReq()},
		{"conflict resolution", engine.OperationResolution, opus(), opusReq()},
		{"tool-bearing chat", engine.OperationNode, withTools, opusReq()},
		{"session resume", engine.OperationNode, opus(), resume},
	} {
		r, err := p.step(ctx, tc.kind, tc.b, tc.req)
		require.NoError(t, err, tc.name)
		assert.Equal(t, "builtin:claude-opus-5-5", r.Output, tc.name)
	}
	assert.Empty(t, macA.Dispatches(), "only hermetic chat is ever offered to a remote runner")

	p.cordonBuiltIn(t, true)
	r, err := p.step(ctx, engine.OperationNode, opus(), opusReq())
	require.NoError(t, err)
	assert.Equal(t, "remote:mac-a:claude-opus-5-5", r.Output, "llm_call chat runs remotely when the built-in runner is unavailable")
	r, err = p.step(ctx, engine.OperationChildInput, opus(), opusReq())
	require.NoError(t, err)
	assert.Equal(t, "remote:mac-a:claude-opus-5-5", r.Output, "child-input preparation runs remotely too")

	tctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_, err = p.step(tctx, engine.OperationTurn, opus(), opusReq())
	assert.ErrorIs(t, err, context.DeadlineExceeded, "a turn waits for the built-in runner rather than leaving the server")
	assert.Len(t, macA.Dispatches(), 2)
	for _, j := range p.builtin.Jobs() {
		assert.Equal(t, workDir, j.Chat.WorkDir)
	}
}

func TestRemote_LostRunnerReplacesStepWithNextGeneration(t *testing.T) {
	p := newPool(t, 2)
	ctx := context.Background()
	key := p.SeedRunner(t, "mac-a", 2)
	macA := hubtest.ConnectInMemoryRunner(t, p.hub, key, hubtest.DarwinHello(hubtest.AnthropicSource()))
	macA.SetHold(true)
	p.cordonBuiltIn(t, true)
	sink := engine.NewPlacementSink()

	ch := p.goStep(engine.ContextWithPlacementSink(ctx, sink), engine.OperationNode, opus(), opusReq())
	macA.AwaitInflight(t, 1)
	jobID := macA.Dispatches()[0].JobID

	p.cordonBuiltIn(t, false)
	p.Clock.Advance(121 * time.Second)
	require.NoError(t, p.hub.Tick(ctx))

	r := await(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, "builtin:claude-opus-5-5", r.resp.Output)
	jobs := p.builtin.Jobs()
	require.Len(t, jobs, 1)
	assert.Equal(t, workDir, jobs[0].Chat.WorkDir, "the re-placed built-in attempt still has the server workspace")

	job, err := p.Ledger.Get(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), job.Generation)
	assert.Equal(t, runner.JobCompleted, job.State)
	assert.Equal(t, runner.BuiltInRunnerID, job.RunnerID)
	assert.Equal(t, 1, job.Key.Attempt, "no workflow retry is spent")
	attempts, err := p.Ledger.Attempts(ctx, jobID)
	require.NoError(t, err)
	require.Len(t, attempts, 2)
	assert.Equal(t, runner.RunnerID("mac-a"), attempts[0].RunnerID)
	assert.Equal(t, "runner_lost", attempts[0].Outcome)
	assert.Equal(t, runner.BuiltInRunnerID, attempts[1].RunnerID)
	assert.Equal(t, "completed", attempts[1].Outcome)

	facts := sink.Facts()
	require.Len(t, facts, 2)
	assert.Equal(t, "mac-a", facts[0].RunnerID)
	assert.Equal(t, int64(1), facts[0].Generation)
	assert.Equal(t, "runner_lost", facts[0].Outcome)
	assert.Equal(t, string(runner.BuiltInRunnerID), facts[1].RunnerID)
	assert.Equal(t, int64(2), facts[1].Generation)
	assert.Equal(t, "completed", facts[1].Outcome)

	rerouted := p.EventsOf(t, runner.EventJobRerouted)
	require.Len(t, rerouted, 1)
	assert.Equal(t, runner.OutcomeRunnerLost, rerouted[0].Cause.Outcome)
	assert.Equal(t, runner.RunnerID("mac-a"), rerouted[0].RunnerID)
	assert.Contains(t, p.EventKinds(t), "runner.lost")

	// mac-a comes back still holding generation 1 and replays its late result.
	hello := hubtest.DarwinHello(hubtest.AnthropicSource())
	hello.Held = []runner.HeldJobInfo{{JobID: jobID, Generation: 1, State: runner.HeldTerminal}}
	back := hubtest.ConnectInMemoryRunner(t, p.hub, key, hello)
	back.Conn().Await(t, runner.MsgFenced, jobID)
	require.NoError(t, p.hub.Handle(ctx, back.Session(), runner.Inbound{Kind: runner.MsgResult, JobID: jobID, Generation: 1,
		Result: &runner.RemoteResult{OK: true, Chat: &runner.RemoteChatResult{Output: "stale from mac-a"}}}))
	assert.Equal(t, 2, back.Conn().Count(runner.MsgFenced, jobID), "the late generation-1 result is fenced")
	assert.Zero(t, back.Conn().Count(runner.MsgAck, jobID), "a stale result is never acknowledged as consumed")
	after, err := p.Ledger.Get(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, job, after, "the step keeps the newer outcome")
	assert.Equal(t, "builtin:claude-opus-5-5", r.resp.Output)
}

func TestRemote_UnacceptedOfferIsReofferedWithNextGeneration(t *testing.T) {
	p := newPool(t, 2)
	ctx := context.Background()
	macA := p.connect(t, "mac-a", 2, hubtest.AnthropicSource())
	macA.SetAcceptIf(func(gen int64) bool { return gen >= 2 })
	p.cordonBuiltIn(t, true)

	ch := p.goStep(ctx, engine.OperationNode, opus(), opusReq())
	require.Eventually(t, func() bool { return len(macA.Dispatches()) == 1 }, 3*time.Second, 2*time.Millisecond)
	jobID := macA.Dispatches()[0].JobID

	p.Clock.Advance(31 * time.Second) // past OfferTTL (30 s); a heartbeat keeps mac-a online
	require.NoError(t, p.hub.Handle(ctx, macA.Session(), runner.Inbound{Kind: runner.MsgHeartbeat,
		Heartbeat: &runner.HeartbeatInfo{Telemetry: runner.Telemetry{SampledAt: p.Clock.Now()}}}))
	require.NoError(t, p.hub.Tick(ctx))

	r := await(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, "remote:mac-a:claude-opus-5-5", r.resp.Output)
	var gens []int64
	for _, d := range macA.Dispatches() {
		require.Equal(t, jobID, d.JobID)
		gens = append(gens, d.Generation)
	}
	assert.Equal(t, []int64{1, 2}, gens, "the expired offer is re-offered at the next generation")
	assert.Equal(t, 1, macA.Conn().Count(runner.MsgCancel, jobID), "the expired offer is withdrawn")
	job, err := p.Ledger.Get(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), job.Generation)
	assert.Equal(t, runner.JobCompleted, job.State)
	assert.Equal(t, 1, job.Key.Attempt, "no workflow retry is spent")
	attempts, err := p.Ledger.Attempts(ctx, jobID)
	require.NoError(t, err)
	require.Len(t, attempts, 2)
	assert.Equal(t, "runner_lost", attempts[0].Outcome)
	assert.Equal(t, "completed", attempts[1].Outcome)
}

func TestRemote_RevokedKeyReplacesInFlightChatSteps(t *testing.T) {
	p := newPool(t, 2)
	ctx := context.Background()
	audit := &auditSpy{}
	keys := runner.NewKeyService(runner.KeyServiceDeps{Store: adaptsqlite.NewRunnerKeyRepo(p.DB), Registry: p.Registry,
		Audit: audit, Revoker: p.hub, Clock: p.Clock.Now, DefaultStepLimit: 2})
	admin := identity.WithIdentity(ctx, identity.Identity{UserID: uuid.New(), Username: "root", Role: identity.RoleAdmin})
	plaintext, k, err := keys.Create(admin, "mac-a")
	require.NoError(t, err)
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	nonce, err := runner.NewChallengeNonce()
	require.NoError(t, err)
	k, err = keys.ProveMachine(ctx, k, nonce, pub, ed25519.Sign(priv, runner.ChallengeMessage(k.ID, nonce)))
	require.NoError(t, err)

	macA := hubtest.ConnectInMemoryRunner(t, p.hub, k, hubtest.DarwinHello(hubtest.AnthropicSource()))
	macA.SetHold(true)
	p.cordonBuiltIn(t, true)
	steps := []<-chan stepResult{
		p.goStep(ctx, engine.OperationNode, opus(), opusReq()),
		p.goStep(ctx, engine.OperationNode, opus(), opusReq()),
	}
	macA.AwaitInflight(t, 2)
	p.cordonBuiltIn(t, false)

	require.NoError(t, keys.Revoke(admin, k.ID, runner.RevokeAck{Acknowledged: true}))
	for _, ch := range steps {
		r := await(t, ch)
		require.NoError(t, r.err)
		assert.Equal(t, "builtin:claude-opus-5-5", r.resp.Output, "in-flight chat steps are re-placed")
	}
	code, _, closed := macA.Conn().Closed()
	assert.True(t, closed)
	assert.Equal(t, runner.CloseRevoked, code)
	assert.Len(t, p.EventsOf(t, runner.EventJobRerouted), 2)
	assert.Contains(t, p.EventKinds(t), "runner.key_revoked")
	assert.Contains(t, audit.Actions(), "runner_key.revoke")
	got, _, err := p.Registry.GetRunner(ctx, "mac-a")
	require.NoError(t, err)
	assert.Equal(t, runner.LivenessLost, got.Liveness)

	_, err = keys.Authenticate(ctx, plaintext)
	assert.ErrorIs(t, err, runner.ErrKeyRevoked)
	_, err = p.hub.Admit(ctx, k, hubtest.NewFakeConn(), hubtest.DarwinHello(hubtest.AnthropicSource()))
	assert.ErrorIs(t, err, runner.ErrRunnerRevoked)
	r, err := p.step(ctx, engine.OperationNode, opus(), opusReq())
	require.NoError(t, err)
	assert.Equal(t, "builtin:claude-opus-5-5", r.Output)
	assert.Len(t, macA.Dispatches(), 2, "a revoked runner receives no new work")
}
