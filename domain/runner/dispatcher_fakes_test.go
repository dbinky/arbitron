package runner_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// memPool is an in-memory Registry + Ledger + IntentAbandoner with the same
// slot, generation and fence rules as the SQLite repos (P1 T5/T6). Registry and
// Ledger methods the dispatcher never calls fall through to the nil embedded
// interfaces and panic, proving they are unused.
type memPool struct {
	runner.Registry
	runner.Ledger

	mu        sync.Mutex
	runners   []runner.Runner
	sources   []runner.Source
	jobs      map[uuid.UUID]*runner.Job
	byKey     map[runner.JobKey]uuid.UUID
	attempts  map[uuid.UUID][]runner.Attempt
	abandoned []uuid.UUID
	ensureErr error
	now       func() time.Time
}

func newMemPool(now func() time.Time, runners []runner.Runner, sources []runner.Source) *memPool {
	return &memPool{runners: runners, sources: sources, jobs: map[uuid.UUID]*runner.Job{},
		byKey: map[runner.JobKey]uuid.UUID{}, attempts: map[uuid.UUID][]runner.Attempt{}, now: now}
}

func (p *memPool) Snapshot(context.Context) (runner.PoolSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	snap := runner.PoolSnapshot{Runners: append([]runner.Runner(nil), p.runners...), Sources: append([]runner.Source(nil), p.sources...),
		SlotsInUse: map[runner.RunnerID]int{}, SourceInFlight: map[runner.SourceID]int{},
		Telemetry: map[runner.RunnerID]runner.Telemetry{}, Homes: map[uuid.UUID]runner.Home{}}
	for _, j := range p.jobs {
		if j.State.HoldsSlot() {
			snap.SlotsInUse[j.RunnerID]++
			if j.SourceID != "" {
				snap.SourceInFlight[j.SourceID]++
			}
		}
	}
	return snap, nil
}

func (p *memPool) sourceLocked(id runner.SourceID) *runner.Source {
	for i := range p.sources {
		if p.sources[i].ID == id {
			return &p.sources[i]
		}
	}
	return nil
}

func (p *memPool) RecordHealth(_ context.Context, id runner.SourceID, h runner.SourceHealth) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sourceLocked(id)
	if s == nil {
		return runner.ErrSourceNotFound
	}
	s.Health = h
	return nil
}

func (p *memPool) health(id runner.SourceID) runner.SourceHealth {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sourceLocked(id).Health
}

func (p *memPool) EnsureIntent(_ context.Context, key runner.JobKey, nodeID string, req runner.ExecRequirements) (runner.Job, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ensureErr != nil {
		return runner.Job{}, p.ensureErr
	}
	if id, ok := p.byKey[key]; ok {
		j := p.jobs[id]
		if !j.State.Active() {
			j.State, j.Generation, j.RunnerID, j.SourceID, j.Reason, j.Wait = runner.JobQueued, j.Generation+1, "", "", "", nil
		}
		return *j, nil
	}
	j := &runner.Job{ID: uuid.New(), Key: key, NodeID: nodeID, Op: req.Op, Group: req.Group, Model: req.Model, State: runner.JobQueued, CreatedAt: p.now()}
	p.jobs[j.ID], p.byKey[key] = j, j.ID
	return *j, nil
}

func (p *memPool) SetWait(_ context.Context, id uuid.UUID, w *runner.WaitReason) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	j, ok := p.jobs[id]
	if !ok {
		return runner.ErrJobNotFound
	}
	if j.State != runner.JobQueued {
		return runner.ErrFenced
	}
	j.Wait = w
	return nil
}

func (p *memPool) Reserve(_ context.Context, id uuid.UUID, d runner.Decision, limit int, lease time.Time) (runner.Job, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	j, ok := p.jobs[id]
	if !ok {
		return runner.Job{}, runner.ErrJobNotFound
	}
	if j.State != runner.JobQueued {
		return runner.Job{}, runner.ErrFenced
	}
	used := 0
	for _, o := range p.jobs {
		if o.State.HoldsSlot() && o.RunnerID == d.Runner {
			used++
		}
	}
	if used >= limit {
		return runner.Job{}, runner.ErrSlotTaken
	}
	l := lease
	j.State, j.RunnerID, j.SourceID, j.Reason, j.Wait, j.LeaseUntil = runner.JobOffered, d.Runner, d.Source, d.Reason, nil, &l
	j.Generation++
	p.attempts[id] = append(p.attempts[id], runner.Attempt{JobID: id, Generation: j.Generation, RunnerID: d.Runner, SourceID: d.Source, Reason: d.Reason, StartedAt: p.now()})
	return *j, nil
}

func (p *memPool) fenceLocked(f runner.Fence) (*runner.Job, error) {
	j, ok := p.jobs[f.JobID]
	if !ok {
		return nil, runner.ErrJobNotFound
	}
	if !j.State.HoldsSlot() || j.Generation != f.Generation || j.RunnerID != f.RunnerID {
		return j, runner.ErrFenced
	}
	return j, nil
}

func (p *memPool) closeAttemptLocked(f runner.Fence, outcome string) {
	as := p.attempts[f.JobID]
	for i := range as {
		if as[i].Generation == f.Generation && as[i].EndedAt == nil {
			now := p.now()
			as[i].Outcome, as[i].EndedAt = outcome, &now
		}
	}
}

func (p *memPool) MarkRunning(_ context.Context, f runner.Fence) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	j, err := p.fenceLocked(f)
	if err != nil {
		return err
	}
	j.State = runner.JobRunning
	return nil
}

func (p *memPool) finish(f runner.Fence, state runner.JobState, outcome, ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	j, err := p.fenceLocked(f)
	if errors.Is(err, runner.ErrFenced) && j.State == state && j.Generation == f.Generation && j.RunnerID == f.RunnerID {
		return nil
	}
	if err != nil {
		return err
	}
	j.State, j.ResultRef = state, ref
	p.closeAttemptLocked(f, outcome)
	return nil
}

func outcomeString(c runner.OutcomeClass) string {
	if c == runner.OutcomeNone {
		return string(runner.OutcomeOther)
	}
	return string(c)
}

func (p *memPool) Complete(_ context.Context, f runner.Fence, ref string) error {
	return p.finish(f, runner.JobCompleted, "completed", ref)
}

func (p *memPool) Fail(_ context.Context, f runner.Fence, c runner.OutcomeClass) error {
	return p.finish(f, runner.JobFailed, outcomeString(c), "")
}

func (p *memPool) Requeue(_ context.Context, f runner.Fence, c runner.OutcomeClass) (runner.Job, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	j, err := p.fenceLocked(f)
	if err != nil {
		return runner.Job{}, err
	}
	p.closeAttemptLocked(f, outcomeString(c))
	j.State, j.RunnerID, j.SourceID, j.Reason, j.LeaseUntil = runner.JobQueued, "", "", "", nil
	j.Generation++
	return *j, nil
}

func (p *memPool) FenceCheck(_ context.Context, f runner.Fence) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.fenceLocked(f)
	return err
}

func (p *memPool) Get(_ context.Context, id uuid.UUID) (runner.Job, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	j, ok := p.jobs[id]
	if !ok {
		return runner.Job{}, runner.ErrJobNotFound
	}
	return *j, nil
}

func (p *memPool) ListActive(context.Context) ([]runner.Job, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []runner.Job
	for _, j := range p.jobs {
		if j.State.Active() {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (p *memPool) Abandon(_ context.Context, id uuid.UUID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if j, ok := p.jobs[id]; ok && j.State == runner.JobQueued {
		j.State, j.Wait = runner.JobFailed, nil
		p.abandoned = append(p.abandoned, id)
	}
	return nil
}

func (p *memPool) jobFor(run uuid.UUID) runner.Job {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, j := range p.jobs {
		if j.Key.RunID == run {
			return *j
		}
	}
	return runner.Job{}
}

func (p *memPool) attemptsOf(id uuid.UUID) []runner.Attempt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]runner.Attempt(nil), p.attempts[id]...)
}

func (p *memPool) jobCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.jobs)
}

// fakeHealth transitions the pool's source health the way HealthMonitor does
// for the classes the dispatcher reports, and records every call.
type appliedOutcome struct {
	src runner.SourceID
	o   runner.SourceOutcome
}

type fakeHealth struct {
	pool    *memPool
	mu      sync.Mutex
	applied []appliedOutcome
	err     error
}

func (h *fakeHealth) ApplyOutcome(_ context.Context, src runner.SourceID, o runner.SourceOutcome) error {
	h.mu.Lock()
	h.applied = append(h.applied, appliedOutcome{src, o})
	err := h.err
	h.mu.Unlock()
	if err != nil {
		return err
	}
	h.pool.mu.Lock()
	defer h.pool.mu.Unlock()
	s := h.pool.sourceLocked(src)
	if s == nil {
		return runner.ErrSourceNotFound
	}
	switch o.Class {
	case runner.OutcomeExhausted:
		s.Health.Capacity, s.Health.ResetAt = runner.CapacityExhausted, o.ResetAt
	case runner.OutcomeLoggedOut:
		s.Health.Auth = runner.AuthLoggedOut
	}
	return nil
}

func (h *fakeHealth) outcomes() []appliedOutcome {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]appliedOutcome(nil), h.applied...)
}

// fakeClock gates time: Sleep advances Now by exactly the requested duration.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func (c *fakeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// scriptedRunner is a StepRunner for ONE runner ID. It records every call and
// plays a per-source script; an assignment for another runner is an error.
type runCall struct {
	a   runner.Assignment
	job runner.StepJob
	id  engine.StepIdentity
}

type runStep func(ctx context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error)

type scriptedRunner struct {
	id       runner.RunnerID
	mu       sync.Mutex
	calls    []runCall
	script   map[runner.SourceID][]runStep
	fallback runStep
}

func (r *scriptedRunner) Run(ctx context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	sid, _ := engine.StepIdentityFromContext(ctx)
	r.mu.Lock()
	r.calls = append(r.calls, runCall{a: a, job: job, id: sid})
	step := r.fallback
	if q := r.script[a.Job.SourceID]; len(q) > 0 {
		step, r.script[a.Job.SourceID] = q[0], q[1:]
	}
	r.mu.Unlock()
	if a.Job.RunnerID != r.id {
		return runner.StepOutput{}, fmt.Errorf("assignment for %s delivered to %s", a.Job.RunnerID, r.id)
	}
	if step == nil {
		return succeed("ok")(ctx, a, job)
	}
	return step(ctx, a, job)
}

func (r *scriptedRunner) callList() []runCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runCall(nil), r.calls...)
}

func succeed(text string) runStep {
	return func(_ context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
		return runner.StepOutput{Chat: &engine.ChatResponse{Output: text + "@" + string(a.Job.RunnerID), Model: job.Chat.Model}}, nil
	}
}

func failWith(err error) runStep {
	return func(context.Context, runner.Assignment, runner.StepJob) (runner.StepOutput, error) {
		return runner.StepOutput{}, err
	}
}

func streamOf(chunks ...engine.ChatChunk) runStep {
	return func(context.Context, runner.Assignment, runner.StepJob) (runner.StepOutput, error) {
		ch := make(chan engine.ChatChunk)
		go func() {
			defer close(ch)
			for _, c := range chunks {
				ch <- c
			}
		}()
		return runner.StepOutput{Stream: ch}, nil
	}
}

type dispatchFixture struct {
	pool     *memPool
	health   *fakeHealth
	bus      *recordingBus
	log      *memoryEventLog
	clock    *fakeClock
	notifier *runner.PoolNotifier
	d        *runner.StepDispatcher
}

func newDispatchFixture(t *testing.T, runners []runner.Runner, sources []runner.Source, set runner.StaticRunnerSet) *dispatchFixture {
	t.Helper()
	clock := &fakeClock{now: placeNow}
	pool := newMemPool(clock.Now, runners, sources)
	bus, log := &recordingBus{}, &memoryEventLog{}
	notifier := runner.NewPoolNotifier()
	wake, unsub := notifier.Subscribe()
	t.Cleanup(unsub)
	health := &fakeHealth{pool: pool}
	d := runner.NewStepDispatcher(runner.DispatcherDeps{
		Registry: pool, Ledger: pool, Runners: set,
		Events: runner.NewEventRecorder(log, bus, 1000, clock.Now), Status: runner.NewExecutionStatusProjector(pool),
		Health: health, Telemetry: runner.EmptyTelemetry{}, Clock: clock.Now,
		TransientMaxAttempts: 3, BackoffMin: time.Second, BackoffMax: 30 * time.Second,
		Wake: wake, Sleep: clock.Sleep,
	})
	t.Cleanup(d.Close)
	return &dispatchFixture{pool: pool, health: health, bus: bus, log: log, clock: clock, notifier: notifier, d: d}
}

func stamped(run uuid.UUID, attempt int) (context.Context, engine.StepIdentity, *engine.PlacementSink) {
	id := engine.StepIdentity{RunID: run, NodeID: "draft", InvocationID: uuid.New(), OperationID: "node", Kind: engine.OperationNode, Attempt: attempt}
	sink := engine.NewPlacementSink()
	return engine.ContextWithPlacementSink(engine.ContextWithStepIdentity(context.Background(), id), sink), id, sink
}

func chatJob(op runner.OpType) runner.StepJob {
	return runner.StepJob{Op: op,
		Binding: provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Model: "claude-sonnet-4-5", AllowedTools: []string{"Read", "Grep"}, Pinned: true},
		Chat:    &engine.ChatRequest{Model: "claude-sonnet-4-5", Prompt: "summarise", RequireExecutedModel: true}}
}

func chatReq(run uuid.UUID, op runner.OpType, aff runner.Affinity) runner.ExecRequirements {
	return runner.ExecRequirements{Group: anthropicGroup, Model: "claude-sonnet-4-5", Backend: "", Op: op, Affinity: aff, RunID: run,
		RequireExecutedModel: true, Tools: []string{"Read", "Grep"}}
}

func statuses(bus *recordingBus, run uuid.UUID) []instance.ExecutionStatus {
	var out []instance.ExecutionStatus
	for _, e := range bus.ofType("execution.status") {
		if e.InstanceID == run {
			out = append(out, e.Event.Data.(instance.ExecutionStatus))
		}
	}
	return out
}

var errBusy = &provider.LunaRouteFailure{Kind: provider.ErrLunaRouteBusy, Operation: provider.LunaRouteChat, Status: 429, RetryAfter: "7"}
