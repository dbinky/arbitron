// domain/runner/session_mover_test.go
package runner_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- fakes (prefix mv*: honor load-bearing arguments) ----

type mvLedger struct {
	runner.Ledger
	mu    sync.Mutex
	jobs  map[uuid.UUID]*runner.Job
	byKey map[runner.JobKey]uuid.UUID
	calls []string
	waits []*runner.WaitReason
}

func newMvLedger() *mvLedger {
	return &mvLedger{jobs: map[uuid.UUID]*runner.Job{}, byKey: map[runner.JobKey]uuid.UUID{}}
}

func (l *mvLedger) EnsureIntent(_ context.Context, key runner.JobKey, nodeID string, req runner.ExecRequirements) (runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if id, ok := l.byKey[key]; ok {
		return *l.jobs[id], nil
	}
	j := &runner.Job{ID: uuid.New(), Key: key, NodeID: nodeID, Op: req.Op, Group: req.Group, Model: req.Model, State: runner.JobState("queued")}
	l.jobs[j.ID], l.byKey[key] = j, j.ID
	return *j, nil
}

func (l *mvLedger) SetWait(_ context.Context, id uuid.UUID, w *runner.WaitReason) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.jobs[id].Wait = w
	l.waits = append(l.waits, w)
	return nil
}

func (l *mvLedger) Reserve(_ context.Context, id uuid.UUID, d runner.Decision, _ int, _ time.Time) (runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j := l.jobs[id]
	if j.State != runner.JobState("queued") {
		return runner.Job{}, fmt.Errorf("reserve %s in state %s", id, j.State)
	}
	j.RunnerID, j.SourceID, j.Generation, j.State, j.Wait = d.Runner, d.Source, j.Generation+1, runner.JobState("offered"), nil
	l.calls = append(l.calls, "reserve:"+string(d.Runner))
	return *j, nil
}

func (l *mvLedger) check(f runner.Fence) (*runner.Job, error) {
	j := l.jobs[f.JobID]
	if j == nil || j.Generation != f.Generation || j.RunnerID != f.RunnerID ||
		j.State == runner.JobState("completed") || j.State == runner.JobState("failed") || j.State == runner.JobState("fenced") {
		return nil, runner.ErrFenced
	}
	return j, nil
}

func (l *mvLedger) MarkRunning(_ context.Context, f runner.Fence) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, err := l.check(f)
	if err != nil {
		return err
	}
	j.State = runner.JobState("running")
	return nil
}

func (l *mvLedger) Complete(_ context.Context, f runner.Fence, _ string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, err := l.check(f)
	if err != nil {
		return err
	}
	j.State = runner.JobState("completed")
	l.calls = append(l.calls, "complete:"+string(f.RunnerID))
	return nil
}

func (l *mvLedger) Fail(_ context.Context, f runner.Fence, class runner.OutcomeClass) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, err := l.check(f)
	if err != nil {
		return err
	}
	j.State = runner.JobState("failed")
	l.calls = append(l.calls, "fail:"+string(f.RunnerID)+":"+string(class))
	return nil
}

func (l *mvLedger) FenceCheck(_ context.Context, f runner.Fence) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.check(f)
	return err
}

func (l *mvLedger) Get(_ context.Context, id uuid.UUID) (runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, ok := l.jobs[id]
	if !ok {
		return runner.Job{}, runner.ErrJobNotFound
	}
	return *j, nil
}

func (l *mvLedger) ListActive(_ context.Context) ([]runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []runner.Job
	for _, j := range l.jobs {
		if j.State == runner.JobState("queued") || j.State == runner.JobState("offered") || j.State == runner.JobState("running") {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (l *mvLedger) ListByRun(_ context.Context, runID uuid.UUID) ([]runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []runner.Job
	for _, j := range l.jobs {
		if j.Key.RunID == runID {
			out = append(out, *j)
		}
	}
	return out, nil
}

// seedTurn records an in-flight stream job (the interrupted turn) on runner.
func (l *mvLedger) seedTurn(runID uuid.UUID, nodeID string, on runner.RunnerID, invocationID uuid.UUID) runner.Fence {
	l.mu.Lock()
	defer l.mu.Unlock()
	j := &runner.Job{ID: uuid.New(), Key: runner.JobKey{RunID: runID, InvocationID: invocationID, OperationID: "turn/2", Attempt: 1},
		NodeID: nodeID, Op: runner.OpType("stream"), RunnerID: on, Generation: 1, State: runner.JobState("running")}
	l.jobs[j.ID] = j
	return runner.Fence{JobID: j.ID, RunnerID: on, Generation: 1}
}

func (l *mvLedger) callLog() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

type mvRegistry struct {
	runner.Registry
	snap runner.PoolSnapshot
}

func (r *mvRegistry) Snapshot(context.Context) (runner.PoolSnapshot, error) { return r.snap, nil }

type mvLeases struct {
	engine.TurnLeaseStore
	mu       sync.Mutex
	bumps    int
	acquires int
}

func (s *mvLeases) Acquire(context.Context, string, time.Duration) (engine.TurnLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquires++
	return engine.TurnLease{}, errors.New("mover must reuse the caller's lease")
}

func (s *mvLeases) BumpGeneration(_ context.Context, l engine.TurnLease) (engine.TurnLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumps++
	l.Generation++
	return l, nil
}

type mvCheckpoints struct {
	engine.SessionCheckpointStore
	bundles map[string]engine.SessionBundle
}

func (c *mvCheckpoints) Get(_ context.Context, ref string) (engine.SessionBundle, error) {
	b, ok := c.bundles[ref]
	if !ok {
		return engine.SessionBundle{}, fmt.Errorf("no bundle %q", ref)
	}
	return b, nil
}

type mvExecs struct {
	instance.NodeExecutionRepository
	execs []instance.NodeExecution
}

func (e *mvExecs) ListByInstance(_ context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	var out []instance.NodeExecution
	for _, x := range e.execs {
		if x.InstanceID == id {
			out = append(out, x)
		}
	}
	return out, nil
}

type mvMoves struct {
	mu        sync.Mutex
	recs      []runner.SessionMoveCommit
	err       error
	rows      map[uuid.UUID]runner.SessionMoveState
	intents   *mvIntents // set by the fixture in Cycle 3c
	ledger    *mvLedger
	resources *mvResources
}

// CommitMove models the SQLite transaction: the import job completes WITH the
// move, an owned intent closes, and a closed or foreign move is fenced.
func (m *mvMoves) CommitMove(ctx context.Context, rec runner.SessionMoveCommit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cur, ok := m.rows[rec.Move.ID]
	if !ok || !cur.State.Open() || cur.IntentID != rec.IntentID || rec.InvocationID != cur.Move.InvocationID ||
		rec.ExecutionID != cur.ExecutionID || rec.Lease.Key != heldLease.Key || rec.Lease.Token != heldLease.Token ||
		rec.Lease.Generation != cur.Move.Generation || rec.Move.Generation != cur.Move.Generation ||
		len(rec.Completions) != 1 || rec.Completions[0].JobFence.JobID != cur.JobID {
		return fmt.Errorf("%w: commit %s", engine.ErrTurnFenced, rec.Move.ID)
	}
	if cur.RestoreWorkspace {
		if cur.NewResource == nil || rec.Resource != *cur.NewResource || m.resources.Check(ctx, rec.Resource) != nil {
			return engine.ErrTurnFenced
		}
	}
	for _, c := range rec.Completions {
		f := runner.Fence{JobID: c.JobFence.JobID, RunnerID: runner.RunnerID(c.JobFence.RunnerID), Generation: c.JobFence.Generation}
		if err := m.ledger.Complete(ctx, f, ""); err != nil {
			return fmt.Errorf("%w: %v", engine.ErrTurnFenced, err)
		}
	}
	if cur.NewResource != nil {
		if err := m.resources.Release(ctx, rec.Resource); err != nil {
			return err
		}
	}
	cur.State, cur.Move = runner.MoveStateCommitted, rec.Move
	m.rows[rec.Move.ID] = cur
	if cur.OwnsIntent {
		m.intents.complete(cur.IntentID)
	}
	m.recs = append(m.recs, rec)
	return nil
}

func (l *mvLedger) Requeue(_ context.Context, f runner.Fence, class runner.OutcomeClass) (runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, err := l.check(f)
	if err != nil {
		return runner.Job{}, err
	}
	j.State = runner.JobState("queued")
	l.calls = append(l.calls, "requeue:"+string(f.RunnerID)+":"+string(class))
	return *j, nil
}

// mvResources models ResourceLeaseRepo: one active claim per key.
type mvResources struct {
	runner.ResourceLeaseStore
	mu         sync.Mutex
	active     map[string]engine.ResourceFenceRef
	holders    map[string]string
	claims     int
	releaseErr error
}

func (r *mvResources) Claim(_ context.Context, key, holder string, owner runner.RunnerID) (engine.ResourceFenceRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.active[key]; busy {
		return engine.ResourceFenceRef{}, fmt.Errorf("%w: %s is held by %q", runner.ErrResourceBusy, key, r.holders[key])
	}
	r.claims++
	ref := engine.ResourceFenceRef{Key: key, Owner: string(owner), Generation: int64(r.claims), Token: uuid.New()}
	r.active[key], r.holders[key] = ref, holder
	return ref, nil
}

func (r *mvResources) Check(_ context.Context, f engine.ResourceFenceRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[f.Key] != f {
		return runner.ErrResourceFenced
	}
	return nil
}

func (r *mvResources) Active(_ context.Context, key string) (runner.LeaseRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.active[key]
	if !ok {
		return runner.LeaseRecord{}, false, nil
	}
	return runner.LeaseRecord{Fence: ref, Holder: r.holders[key]}, true, nil
}

func (r *mvResources) Release(_ context.Context, f engine.ResourceFenceRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.releaseErr != nil {
		return r.releaseErr
	}
	if r.active[f.Key] != f {
		return runner.ErrResourceFenced
	}
	delete(r.active, f.Key)
	return nil
}

type mvHomes struct {
	runner.HomeStore
	mu      sync.Mutex
	homes   map[uuid.UUID]runner.Home
	handoff []string
}

func (h *mvHomes) GetHome(_ context.Context, id uuid.UUID) (runner.Home, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.homes[id], nil
}
func (h *mvHomes) HandOff(_ context.Context, id uuid.UUID, to runner.RunnerID, reason string, _ int64, expect int64, cause runner.HomeMoveCause) (runner.Home, error) {
	if cause.InvocationID == nil || *cause.InvocationID == uuid.Nil || cause.NodeID == "" || cause.FromGeneration != expect || cause.ToGeneration != expect+1 {
		return runner.Home{}, runner.ErrFenced
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.homes[id]
	if cur.Generation != expect {
		return runner.Home{}, runner.ErrFenced
	}
	cur.RunID, cur.RunnerID, cur.Generation = id, to, cur.Generation+1
	h.homes[id] = cur
	h.handoff = append(h.handoff, string(to)+":"+reason)
	return cur, nil
}
func (h *mvHomes) MarkDirty(_ context.Context, id uuid.UUID, dirty bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.homes[id]
	cur.Dirty = dirty
	h.homes[id] = cur
	return nil
}

// mvRestorer models WorkspaceSync.RestoreBuiltIn: it rewrites dir to the
// committed checkpoint content (one file) — discarding interrupted edits.
type mvRestorer struct {
	committed map[uuid.UUID]string
	restored  []uuid.UUID
}

func (r *mvRestorer) RestoreBuiltIn(_ context.Context, id uuid.UUID, dir string) error {
	content, ok := r.committed[id]
	if !ok {
		return fmt.Errorf("no committed checkpoint for %s", id)
	}
	r.restored = append(r.restored, id)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "work.txt"), []byte(content), 0o600)
}

// mvStepRunner records every Assignment/StepJob it receives and answers per op.
type mvStepRunner struct {
	id        runner.RunnerID
	mu        sync.Mutex
	got       []runner.StepJob
	assigns   []runner.Assignment
	importErr error
	importSID string
}

func (s *mvStepRunner) Run(_ context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, job)
	s.assigns = append(s.assigns, a)
	if a.Job.RunnerID != s.id {
		return runner.StepOutput{}, fmt.Errorf("runner %s got an assignment for %s", s.id, a.Job.RunnerID)
	}
	if job.Op != runner.OpSessionImport || job.Session == nil || job.Session.Bundle == nil {
		return runner.StepOutput{}, fmt.Errorf("unexpected op %s", job.Op)
	}
	if s.importErr != nil {
		return runner.StepOutput{}, s.importErr
	}
	return runner.StepOutput{Session: &runner.SessionOpResult{SessionID: s.importSID}}, nil
}

type mvRunnerSet map[runner.RunnerID]runner.StepRunner

func (s mvRunnerSet) For(id runner.RunnerID) (runner.StepRunner, bool) { r, ok := s[id]; return r, ok }

type mvEventLog struct {
	mu  sync.Mutex
	evs []runner.PoolEvent
}

func (l *mvEventLog) Append(_ context.Context, ev runner.PoolEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evs = append(l.evs, ev)
	return nil
}
func (l *mvEventLog) List(context.Context, int) ([]runner.PoolEvent, error) { return l.evs, nil }
func (l *mvEventLog) Prune(context.Context, int) error                      { return nil }

type mvBus struct {
	mu  sync.Mutex
	evs map[uuid.UUID][]engine.Event
}

func (b *mvBus) Publish(id uuid.UUID, e engine.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.evs == nil {
		b.evs = map[uuid.UUID][]engine.Event{}
	}
	b.evs[id] = append(b.evs[id], e)
}
func (b *mvBus) Subscribe(uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	close(ch)
	return ch, func() {}
}
func (b *mvBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) {
	ch := make(chan engine.ScopedEvent)
	close(ch)
	return ch, func() {}
}
func (b *mvBus) ofType(id uuid.UUID, typ string) []engine.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []engine.Event
	for _, e := range b.evs[id] {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

type mvLossy struct{ engine.SessionPortability }

func (mvLossy) AlwaysLossy() bool { return true }

// ---- fixture ----

var anthropic = modelidentity.Family("anthropic")

func mvRunner(id runner.RunnerID) runner.Runner {
	return runner.Runner{ID: id, Name: string(id), BuiltIn: id == runner.BuiltInRunnerID, StepLimit: 8,
		Compat: runner.Compatibility{State: runner.CompatState("compatible")}, Liveness: runner.Liveness("online")}
}

func mvSource(r runner.RunnerID, kind runner.CredentialKind, fam modelidentity.Family, backend string, capacity string) runner.Source {
	fingerprint, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: backend})
	if err != nil {
		panic(err)
	}
	return runner.Source{ID: runner.MakeSourceID(r, kind, ""), RunnerID: r, Kind: kind, Families: []modelidentity.Family{fam},
		Backends: []string{backend}, Health: runner.SourceHealth{Auth: runner.AuthState("ok"), Capacity: runner.CapacityState(capacity)},
		Capabilities: []runner.ExecutionCapability{{Op: runner.OpStream, Backend: backend, EndpointFingerprint: fingerprint,
			Families: []modelidentity.Family{fam}, Models: []string{"claude-opus-4-5"}, ToolPolicy: runner.ToolPolicyAllowlist,
			Tools: []string{"Read"}, ExecutedModel: true, Provider: "claude-cli"}}}
}

type mvFixture struct {
	intents   *mvIntents
	resources *mvResources
	ledger    *mvLedger
	reg       *mvRegistry
	leases    *mvLeases
	execs     *mvExecs
	moves     *mvMoves
	homes     *mvHomes
	restorer  *mvRestorer
	runners   mvRunnerSet
	macB      *mvStepRunner
	builtin   *mvStepRunner
	bus       *mvBus
	eventLog  *mvEventLog
	ckpts     *mvCheckpoints
	registry  *runner.SessionPortabilityRegistry
	mover     *runner.SessionMover
	runID     uuid.UUID
	exec      instance.NodeExecution
	workDir   string
}

func newMvFixture(t *testing.T, backend string) *mvFixture {
	t.Helper()
	f := &mvFixture{ledger: newMvLedger(), leases: &mvLeases{}, moves: &mvMoves{}, bus: &mvBus{}, eventLog: &mvEventLog{}}
	f.intents = &mvIntents{byID: map[uuid.UUID]engine.TurnIntent{}}
	f.moves = &mvMoves{rows: map[uuid.UUID]runner.SessionMoveState{}, intents: f.intents, ledger: f.ledger}
	f.resources = &mvResources{active: map[string]engine.ResourceFenceRef{}, holders: map[string]string{}}
	f.moves.resources = f.resources

	f.runID = uuid.New()
	f.workDir = filepath.Join(t.TempDir(), "ws")
	require.NoError(t, os.MkdirAll(f.workDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(f.workDir, "work.txt"), []byte("INTERRUPTED EDIT"), 0o600))
	// mac-a (the pin) is exhausted; mac-b serves the same group; mac-c serves only OpenAI.
	f.reg = &mvRegistry{snap: runner.PoolSnapshot{
		Runners: []runner.Runner{mvRunner("mac-a"), mvRunner("mac-b"), mvRunner("mac-c")},
		Sources: []runner.Source{
			mvSource("mac-a", runner.CredAnthropicSubscription, anthropic, backend, "exhausted"),
			mvSource("mac-b", runner.CredAnthropicSubscription, anthropic, backend, "available"),
			mvSource("mac-c", runner.CredOpenAISubscription, modelidentity.Family("openai"), "codex", "available"),
		},
		SlotsInUse: map[runner.RunnerID]int{}, SourceInFlight: map[runner.SourceID]int{},
		Homes: map[uuid.UUID]runner.Home{}, Now: time.Now(),
	}}
	ref := "ckpt-turn-2"
	gen := int64(2)
	pin := "mac-a"
	src := string(runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, ""))
	f.exec = instance.NodeExecution{ID: uuid.New(), InstanceID: f.runID, NodeID: "talk", Attempt: 1, SessionInvocationID: uuid.New(),
		LLMConversation: map[string]any{"sessionId": "sid-a", "nonce": "n-1", "messages": []any{
			map[string]any{"role": "user", "content": "FIRST PROMPT n-1"}, map[string]any{"role": "assistant", "content": "hi"}}},
		SessionCheckpointRef: &ref, SessionGeneration: gen, SessionPinRunner: &pin, SourceID: &src}
	f.execs = &mvExecs{execs: []instance.NodeExecution{f.exec}}
	f.ckpts = &mvCheckpoints{bundles: map[string]engine.SessionBundle{ref: {Backend: backend, SessionID: "sid-a", Checksum: "c", Data: []byte("bundle-bytes")}}}
	f.homes = &mvHomes{homes: map[uuid.UUID]runner.Home{f.runID: {RunID: f.runID, RunnerID: "mac-a", Generation: 4}}}
	f.restorer = &mvRestorer{committed: map[uuid.UUID]string{f.runID: "COMMITTED TURN 2"}}
	f.macB = &mvStepRunner{id: "mac-b", importSID: "sid-b"}
	f.builtin = &mvStepRunner{id: runner.BuiltInRunnerID, importSID: "sid-builtin"}
	f.runners = mvRunnerSet{"mac-b": f.macB, runner.BuiltInRunnerID: f.builtin}
	f.registry = runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{"codex": mvLossy{}})
	f.mover = runner.NewSessionMover(runner.MoverDeps{
		Intents: f.intents, Resources: f.resources,
		Registry: f.reg, Ledger: f.ledger, Runners: f.runners, Leases: f.leases, Checkpoints: f.ckpts,
		Executions: f.execs, Moves: f.moves, Homes: f.homes, Restorer: f.restorer, Portability: f.registry,
		Events: runner.NewEventRecorder(f.eventLog, f.bus, 1000, time.Now), Status: runner.NewExecutionStatusProjector(f.ledger),
		Bus: f.bus, Clock: time.Now, OpLease: time.Minute,
	})
	return f
}

func (f *mvFixture) request(reason string, backend string) runner.MoveRequest {
	fingerprint, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: backend})
	if err != nil {
		panic(err)
	}
	return runner.MoveRequest{
		RunID: f.runID, InvocationID: f.exec.SessionInvocationID, RequestID: uuid.New(), NodeID: "talk", Reason: reason, From: "mac-a",
		Requirements: runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: anthropic},
			Model: "claude-opus-4-5", Backend: backend, Provider: "claude-cli", EndpointFingerprint: fingerprint, Tools: []string{"Read"}},
		ExecutionID: f.exec.ID, TurnIndex: 2, Binding: provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Model: "claude-opus-4-5", Backend: backend, Provider: "claude-cli"},
		WorkDir: f.workDir, RestoreWorkspace: true,
	}
}

var heldLease = engine.TurnLease{Key: "run:x", Token: uuid.New(), Generation: 2, ExpiresAt: time.Now().Add(time.Minute)}

// ---- tests ----

func TestSessionMover_ExhaustedMovesToSameGroupTargetAndImports(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	old := f.ledger.seedTurn(f.runID, "talk", "mac-a", f.exec.SessionInvocationID)

	res, err := f.mover.Move(context.Background(), heldLease, f.request("exhausted", "claude-code"))
	require.NoError(t, err)

	assert.Equal(t, runner.RunnerID("mac-b"), res.To, "same anthropic group, never the OpenAI runner")
	assert.Equal(t, "sid-b", res.SessionID)
	assert.Equal(t, int64(3), res.Generation, "generation reserved under the held lease")
	assert.False(t, res.PendingRebuild)
	assert.True(t, res.Lossy, "cross-source Claude move: retention unverified (RR-14)")
	assert.Equal(t, engine.SessionLossNotice, res.Notice)
	assert.Equal(t, 0, f.leases.acquires, "the mover never re-acquires the caller's lease")
	assert.Equal(t, 1, f.leases.bumps)

	// The import reached mac-b with the latest committed bundle bytes and the stable workdir.
	require.Len(t, f.macB.got, 1)
	assert.Equal(t, []byte("bundle-bytes"), f.macB.got[0].Session.Bundle.Data)
	assert.Equal(t, f.workDir, f.macB.got[0].Session.WorkDir)
	assert.Empty(t, f.builtin.got)

	// Old owner fenced BEFORE the target reservation.
	log := f.ledger.callLog()
	require.GreaterOrEqual(t, len(log), 2)
	assert.Equal(t, "fail:mac-a:exhausted", log[0])
	assert.Equal(t, "reserve:mac-b", log[1])
	assert.ErrorIs(t, f.ledger.FenceCheck(context.Background(), old), runner.ErrFenced, "late output from mac-a is fenced")

	// Sink: the committed move record.
	require.Len(t, f.moves.recs, 1)
	rec := f.moves.recs[0]
	assert.Equal(t, "mac-a", rec.Move.FromRunner)
	assert.Equal(t, "mac-b", rec.Move.ToRunner)
	assert.Equal(t, int64(3), rec.Move.Generation, "strictly newer than the committed generation 2")
	assert.Equal(t, "sid-b", rec.SessionID)
	assert.Equal(t, f.exec.ID, rec.ExecutionID)

	// Sink: run-scoped events.
	moved := f.bus.ofType(f.runID, engine.EventTypeSessionMoved)
	require.Len(t, moved, 1)
	notices := f.bus.ofType(f.runID, engine.EventTypeInteractiveContextNotice)
	require.Len(t, notices, 1)
	assert.Equal(t, "tool details and reasoning may be lost", notices[0].Data.(map[string]any)["notice"])
	require.Len(t, f.eventLog.evs, 1)
	assert.Equal(t, map[string]any{"from": "mac-a", "to": "mac-b", "reason": "exhausted", "lossy": true, "nodeId": "talk"}, f.eventLog.evs[0].Detail)
}

func TestSessionMover_ImportFailureRebuildsWithLossyNotice(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	f.macB.importErr = fmt.Errorf("import: %w", engine.ErrBundleRequiresRebuild)

	res, err := f.mover.Move(context.Background(), heldLease, f.request("logged_out", "claude-code"))
	require.NoError(t, err)
	assert.Equal(t, runner.RunnerID("mac-b"), res.To)
	assert.True(t, res.PendingRebuild)
	assert.Equal(t, "", res.SessionID, "a rebuild starts a fresh provider session")
	assert.True(t, res.Lossy)
	require.Len(t, f.moves.recs, 1)
	assert.True(t, f.moves.recs[0].PendingRebuild)
	assert.Equal(t, engine.SessionLossNotice, f.moves.recs[0].Move.Notice)
	assert.Len(t, f.bus.ofType(f.runID, engine.EventTypeInteractiveContextNotice), 1)
}

func TestSessionMover_CodexImportAlwaysLossy(t *testing.T) {
	f := newMvFixture(t, "codex")
	// Same source for the target would still be lossy for codex: put mac-b's
	// source ID equal to the previous source to isolate the AlwaysLossy rule.
	prev := string(runner.MakeSourceID("mac-b", runner.CredAnthropicSubscription, ""))
	f.execs.execs[0].SourceID = &prev
	res, err := f.mover.Move(context.Background(), heldLease, f.request("drain", "codex"))
	require.NoError(t, err)
	assert.Equal(t, "sid-b", res.SessionID, "native import succeeded")
	assert.True(t, res.Lossy, "codex strips reasoning: every move is lossy")
	assert.Equal(t, engine.SessionLossNotice, res.Notice)
}

func TestSessionMover_NoSameGroupTargetWaitsCapacity(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	// mac-b's anthropic source is exhausted too; only the OpenAI runner is free.
	f.reg.snap.Sources[1].Health = runner.SourceHealth{Auth: runner.AuthState("ok"), Capacity: runner.CapacityState("exhausted"), ResetAt: &reset}

	_, err := f.mover.Move(context.Background(), heldLease, f.request("exhausted", "claude-code"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrNoSessionTarget), "a dry same-group pool is a wait, not an error")
	var wait *engine.SessionWaitError
	require.True(t, errors.As(err, &wait))
	assert.Equal(t, "capacity", wait.Kind)
	assert.Equal(t, runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: anthropic}.String(), wait.Group)
	require.NotNil(t, wait.NextReset)
	assert.True(t, reset.Equal(*wait.NextReset))

	// Sink: the move job carries the visible wait (projected as waiting_capacity).
	require.Len(t, f.ledger.waits, 1)
	assert.Equal(t, runner.WaitKind("capacity"), f.ledger.waits[0].Kind)
	assert.Empty(t, f.moves.recs)
	assert.Equal(t, 0, f.leases.bumps, "no generation is reserved without a target")
	assert.Empty(t, f.macB.got)
	assert.Empty(t, f.bus.ofType(f.runID, engine.EventTypeSessionMoved))
}

func TestSessionMover_RefusesLoadMove(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	for _, reason := range []string{"load", "cordon", "suspect", "slot"} {
		_, err := f.mover.Move(context.Background(), heldLease, f.request(reason, "claude-code"))
		assert.True(t, errors.Is(err, engine.ErrSessionMoveNotAllowed), reason)
	}
	assert.Empty(t, f.ledger.callLog())
	assert.Empty(t, f.moves.recs)
}

func TestSessionMover_StaleGenerationCommitPublishesNothing(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	f.moves.err = fmt.Errorf("commit: %w", instance.ErrStaleSessionGeneration)
	_, err := f.mover.Move(context.Background(), heldLease, f.request("exhausted", "claude-code"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, instance.ErrStaleSessionGeneration))
	assert.Empty(t, f.bus.ofType(f.runID, engine.EventTypeSessionMoved))
	assert.Empty(t, f.bus.ofType(f.runID, engine.EventTypeInteractiveContextNotice))
}

func TestSessionMover_OversizedRebuildIsRestorationError(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	f.macB.importErr = engine.ErrBundleRequiresRebuild
	big := make([]byte, 600_000)
	for i := range big {
		big[i] = 'x'
	}
	f.execs.execs[0].LLMConversation["messages"] = []any{
		map[string]any{"role": "user", "content": string(big)}, map[string]any{"role": "assistant", "content": "a"}}
	_, err := f.mover.Move(context.Background(), heldLease, f.request("exhausted", "claude-code"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrSessionRestoreFailed))
	assert.False(t, errors.Is(err, engine.ErrNoSessionTarget), "deterministic restore failure never masquerades as a capacity wait")
	assert.Empty(t, f.moves.recs)
}

func TestSessionMover_RestoresCommittedWorkspaceBuiltInAndRemote(t *testing.T) {
	// Remote target: server copy restored, home handed to mac-b and marked dirty
	// so mac-b pulls the committed snapshot (never the interrupted edit).
	f := newMvFixture(t, "claude-code")
	_, err := f.mover.Move(context.Background(), heldLease, f.request("runner_lost", "claude-code"))
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(f.workDir, "work.txt"))
	require.NoError(t, err)
	assert.Equal(t, "COMMITTED TURN 2", string(b))
	assert.Equal(t, []string{"mac-b:session_runner_lost"}, f.homes.handoff)
	assert.Equal(t, runner.RunnerID("mac-b"), f.homes.homes[f.runID].RunnerID)
	assert.True(t, f.homes.homes[f.runID].Dirty)

	// Built-in target: only the built-in can serve; the built-in dir is restored.
	g := newMvFixture(t, "claude-code")
	g.reg.snap.Runners = append(g.reg.snap.Runners, mvRunner(runner.BuiltInRunnerID))
	g.reg.snap.Sources[1] = mvSource(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, anthropic, "claude-code", "available")
	res, err := g.mover.Move(context.Background(), heldLease, g.request("exhausted", "claude-code"))
	require.NoError(t, err)
	assert.Equal(t, runner.BuiltInRunnerID, res.To)
	b, err = os.ReadFile(filepath.Join(g.workDir, "work.txt"))
	require.NoError(t, err)
	assert.Equal(t, "COMMITTED TURN 2", string(b))
	assert.False(t, g.homes.homes[g.runID].Dirty, "the built-in copy IS the restored checkpoint")
	require.Len(t, g.builtin.got, 1)
}

func TestSessionMover_FirstTurnFencesAndRestoresWithoutRecordOrImport(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	old := f.ledger.seedTurn(f.runID, "talk", "mac-a", f.exec.SessionInvocationID)
	req := f.request("exhausted", "claude-code")
	req.FirstTurn, req.ExecutionID = true, uuid.Nil
	res, err := f.mover.Move(context.Background(), heldLease, req)
	require.NoError(t, err)
	assert.Equal(t, int64(3), res.Generation)
	assert.ErrorIs(t, f.ledger.FenceCheck(context.Background(), old), runner.ErrFenced)
	assert.Empty(t, f.macB.got, "no bundle exists before the first success: nothing to import")
	b, err := os.ReadFile(filepath.Join(f.workDir, "work.txt"))
	require.NoError(t, err)
	assert.Equal(t, "COMMITTED TURN 2", string(b))
	assert.Empty(t, f.moves.recs, "a first-turn move has no session record or session_moves row")
}

func TestSessionMover_MoveSessionAdaptsEngineRequest(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	var port engine.SessionMover = f.mover
	res, err := port.MoveSession(context.Background(), heldLease, engine.SessionMoveRequest{
		RunID: f.runID, InvocationID: uuid.New(), RequestID: uuid.New(), ExecutionID: f.exec.ID, NodeID: "talk", TurnIndex: 2,
		Reason: "exhausted", From: "mac-a", WorkDir: f.workDir, RestoreWorkspace: true,
		Binding: provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Model: "claude-opus-4-5", Backend: "claude-code", Provider: "claude-cli", AllowedTools: []string{"Read"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "mac-b", res.To)
	assert.Equal(t, "sid-b", res.SessionID)
}

func (m *mvMoves) BeginMove(_ context.Context, b runner.SessionMoveBegin) (runner.SessionMoveState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[uuid.UUID]runner.SessionMoveState{}
	}
	if cur, ok := m.rows[b.State.Move.ID]; ok {
		return cur, nil
	}
	for _, r := range m.rows {
		if r.IntentID == b.State.IntentID && r.State.Open() {
			return runner.SessionMoveState{}, fmt.Errorf("%w: intent already has an open move", engine.ErrTurnFenced)
		}
	}
	if b.Intent != nil && m.intents != nil {
		m.intents.put(*b.Intent)
	}
	m.rows[b.State.Move.ID] = b.State
	return b.State, nil
}

func (m *mvMoves) MoveForIntent(_ context.Context, id uuid.UUID) (runner.SessionMoveState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var last runner.SessionMoveState
	found := false
	for _, r := range m.rows {
		if r.IntentID != id {
			continue
		}
		if r.State.Open() {
			return r, true, nil
		}
		last, found = r, true
	}
	return last, found, nil
}

func (m *mvMoves) AdvanceMove(_ context.Context, st runner.SessionMoveState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.rows[st.Move.ID]
	if !ok || !cur.State.Open() || !st.State.Open() || st.Move.Generation < cur.Move.Generation {
		return fmt.Errorf("%w: advance %s", engine.ErrTurnFenced, st.Move.ID)
	}
	m.rows[st.Move.ID] = st
	return nil
}

func (m *mvMoves) FailMove(_ context.Context, id uuid.UUID, cause string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.rows[id]
	switch {
	case !ok:
		return instance.ErrNotFound
	case cur.State == runner.MoveStateFailed:
		return nil
	case !cur.State.Open():
		return engine.ErrTurnFenced
	}
	cur.State, cur.Failure = runner.MoveStateFailed, cause
	m.rows[id] = cur
	if cur.OwnsIntent && m.intents != nil {
		m.intents.complete(cur.IntentID)
	}
	return nil
}

// only returns the single persisted move.
func (m *mvMoves) only(t *testing.T) runner.SessionMoveState {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.Len(t, m.rows, 1)
	for _, r := range m.rows {
		return r
	}
	return runner.SessionMoveState{}
}

// mvIntents is an in-memory TurnIntentStore (one pending intent per lease key).
type mvIntents struct {
	engine.TurnIntentStore
	mu   sync.Mutex
	byID map[uuid.UUID]engine.TurnIntent
}

func (s *mvIntents) put(in engine.TurnIntent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[in.ID] = in
}

func (s *mvIntents) complete(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in, ok := s.byID[id]; ok && in.CompletedAt == nil {
		now := time.Now()
		in.CompletedAt = &now
		s.byID[id] = in
	}
}

func (s *mvIntents) PendingFor(_ context.Context, key string) (*engine.TurnIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, in := range s.byID {
		if in.LeaseKey == key && in.CompletedAt == nil {
			cp := in
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *mvIntents) Complete(_ context.Context, id uuid.UUID) error {
	s.complete(id)
	return nil
}

// Error: ResumeMove finishes only kind=move intents, only under the recovered
// lease of that session, never acquires one itself, and reports a missing
// move as obsolete (the controller then closes the intent).
func TestSessionMover_ResumeMoveNeedsMoveIntentAndItsRecoveredLease(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	in := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), LeaseKey: heldLease.Key, RunID: f.runID, NodeID: "talk",
		Kind: engine.TurnIntentMove}
	ctx := engine.ContextWithTurnLease(context.Background(), heldLease)

	msg := in
	msg.Kind = engine.TurnIntentMessage
	assert.ErrorIs(t, f.mover.ResumeMove(ctx, msg), runner.ErrNotMoveIntent)
	assert.ErrorIs(t, f.mover.ResumeMove(context.Background(), in), engine.ErrTurnLeaseLost, "no recovered lease: never acquire")
	other := heldLease
	other.Key = "run:other"
	assert.ErrorIs(t, f.mover.ResumeMove(engine.ContextWithTurnLease(context.Background(), other), in), engine.ErrTurnLeaseLost)
	assert.ErrorIs(t, f.mover.ResumeMove(ctx, in), instance.ErrNotFound)
	assert.Equal(t, 0, f.leases.acquires)
	assert.Equal(t, 0, f.leases.bumps)
	assert.Empty(t, f.ledger.callLog())
}

// Edge: the restore runs only under the move's OWN resource claim. While the
// old owner still holds the resource, the move stays visible and retryable,
// its target slot is released, and the next attempt finishes the SAME move
// without a second generation bump.
func TestSessionMover_RestoreWaitsForResourceOwnershipAndReleasesSlot(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	busy := engine.ResourceFenceRef{Key: "run:" + f.runID.String(), Owner: "mac-a", Generation: 4, Token: uuid.New()}
	f.resources.active[busy.Key], f.resources.holders[busy.Key] = busy, "job:old-turn"

	_, err := f.mover.Move(context.Background(), heldLease, f.request("runner_lost", "claude-code"))
	require.ErrorIs(t, err, engine.ErrSessionRestoreFailed)
	require.ErrorIs(t, err, runner.ErrResourceBusy)
	assert.Empty(t, f.restorer.restored, "no write while the old owner holds the resource")
	assert.Empty(t, f.moves.recs)
	st := f.moves.only(t)
	assert.Equal(t, runner.MoveStateRestoring, st.State, "visible and retryable")
	assert.Equal(t, int64(3), st.Move.Generation)
	assert.Nil(t, st.NewResource)
	assert.Equal(t, runner.JobState("queued"), f.ledger.jobs[st.JobID].State, "the target slot is released, never held indefinitely")
	pending, err := f.intents.PendingFor(context.Background(), heldLease.Key)
	require.NoError(t, err)
	require.NotNil(t, pending)
	assert.Equal(t, engine.TurnIntentMove, pending.Kind)

	delete(f.resources.active, busy.Key) // the old owner stopped (P3 stop evidence released its claim)
	reserved := heldLease
	reserved.Generation = 3
	res, err := f.mover.Move(context.Background(), reserved, f.request("runner_lost", "claude-code"))
	require.NoError(t, err)
	assert.Equal(t, int64(3), res.Generation)
	assert.Equal(t, 1, f.leases.bumps, "the generation was reserved exactly once")
	require.Len(t, f.moves.recs, 1)
	assert.Equal(t, st.Move.ID, f.moves.recs[0].Move.ID, "the same move ID finished")
	assert.Equal(t, busy.Key, f.moves.recs[0].Resource.Key, "the commit carries the move's resource")
	assert.Equal(t, string(runner.BuiltInRunnerID), f.moves.recs[0].Resource.Owner)
	assert.NotEqual(t, uuid.Nil, f.moves.recs[0].Resource.Token)
	assert.Empty(t, f.resources.active, "the atomic commit releases the move's own claim")
	assert.Equal(t, []uuid.UUID{f.runID}, f.restorer.restored)
	pending, err = f.intents.PendingFor(context.Background(), heldLease.Key)
	require.NoError(t, err)
	assert.Nil(t, pending, "the move intent closed with the commit")
}

// ---- Cycle 3d: a gone target fails the move (decision unstick 2026-11-24) ----

// Abandon lets mvLedger satisfy runner.IntentAbandoner: a queued intent that
// will never be placed is closed.
func (l *mvLedger) Abandon(_ context.Context, id uuid.UUID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if j := l.jobs[id]; j != nil && j.State == runner.JobState("queued") {
		j.State = runner.JobState("failed")
		l.calls = append(l.calls, "abandon")
	}
	return nil
}

func (m *mvMoves) row(id uuid.UUID) runner.SessionMoveState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[id]
}

func (m *mvMoves) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func (f *mvFixture) setLiveness(id runner.RunnerID, l runner.Liveness) {
	for i := range f.reg.snap.Runners {
		if f.reg.snap.Runners[i].ID == id {
			f.reg.snap.Runners[i].Liveness = l
		}
	}
}

func (f *mvFixture) unregister(id runner.RunnerID) {
	var rs []runner.Runner
	for _, r := range f.reg.snap.Runners {
		if r.ID != id {
			rs = append(rs, r)
		}
	}
	var ss []runner.Source
	for _, s := range f.reg.snap.Sources {
		if s.RunnerID != id {
			ss = append(ss, s)
		}
	}
	f.reg.snap.Runners, f.reg.snap.Sources = rs, ss
}

// addTarget registers another same-group runner that can import the session.
func (f *mvFixture) addTarget(id runner.RunnerID, backend string) *mvStepRunner {
	f.reg.snap.Runners = append(f.reg.snap.Runners, mvRunner(id))
	f.reg.snap.Sources = append(f.reg.snap.Sources, mvSource(id, runner.CredAnthropicSubscription, anthropic, backend, "available"))
	sr := &mvStepRunner{id: id, importSID: "sid-" + string(id)}
	f.runners[id] = sr
	return sr
}

// openRestoringMove leaves ONE open move to mac-b in state restoring: the old
// owner still holds the workspace, generation 3 is reserved, the slot requeued.
func (f *mvFixture) openRestoringMove(t *testing.T) (runner.SessionMoveState, engine.ResourceFenceRef) {
	t.Helper()
	busy := engine.ResourceFenceRef{Key: "run:" + f.runID.String(), Owner: "mac-a", Generation: 4, Token: uuid.New()}
	f.resources.active[busy.Key], f.resources.holders[busy.Key] = busy, "job:old-turn"
	_, err := f.mover.Move(context.Background(), heldLease, f.request("runner_lost", "claude-code"))
	require.ErrorIs(t, err, runner.ErrResourceBusy)
	st := f.moves.only(t)
	require.Equal(t, "mac-b", st.Move.ToRunner)
	require.Equal(t, runner.MoveStateRestoring, st.State)
	require.Equal(t, int64(3), st.Move.Generation)
	return st, busy
}

func reservedLease() engine.TurnLease {
	l := heldLease
	l.Generation = 3
	return l
}

// Happy + edge (decision unstick 2026-11-24): an open move whose target is
// lost, offline (also the revoked case: revocation marks the runner lost) or
// unregistered is FAILED, never retried there; Move starts ONE replacement move
// past the failed move's reserved generation (the extra bump is accepted), and
// only the committed replacement announces session.moved.
func TestSessionMover_GoneTargetFailsTheMoveAndANewMoveReplacesIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		gone func(*mvFixture)
		why  string
	}{
		{"lost", func(f *mvFixture) { f.setLiveness("mac-b", runner.LivenessLost) }, "lost"},
		{"offline", func(f *mvFixture) { f.setLiveness("mac-b", runner.LivenessOffline) }, "offline"},
		{"unregistered", func(f *mvFixture) { f.unregister("mac-b") }, "unregistered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMvFixture(t, "claude-code")
			old, busy := f.openRestoringMove(t)
			delete(f.resources.active, busy.Key) // the old owner stopped
			tc.gone(f)
			macD := f.addTarget("mac-d", "claude-code")

			res, err := f.mover.Move(context.Background(), reservedLease(), f.request("runner_lost", "claude-code"))
			require.NoError(t, err)
			assert.Equal(t, runner.RunnerID("mac-d"), res.To, "a new move to a live target")
			assert.Equal(t, int64(4), res.Generation, "the replacement plans past the failed move's reserved generation")
			assert.Equal(t, 2, f.leases.bumps, "one bump per move: the extra bump is accepted")

			failed := f.moves.row(old.Move.ID)
			assert.Equal(t, runner.MoveStateFailed, failed.State, "the old move is failed, not retried on its target")
			assert.Contains(t, failed.Failure, tc.why)
			assert.Contains(t, failed.Failure, runner.ErrMoveTargetGone.Error())
			assert.False(t, f.ledger.jobs[old.JobID].State.Active(), "the failed move's import job never stays queued or holds a slot")
			assert.Empty(t, f.macB.got, "nothing is ever sent to the gone target")

			assert.Equal(t, 2, f.moves.count())
			require.Len(t, f.moves.recs, 1)
			assert.NotEqual(t, old.Move.ID, f.moves.recs[0].Move.ID, "a NEW move, not the same one")
			assert.Equal(t, "mac-d", f.moves.recs[0].Move.ToRunner)
			assert.Len(t, macD.got, 1, "one import on the replacement target")

			moved := f.bus.ofType(f.runID, engine.EventTypeSessionMoved)
			require.Len(t, moved, 1, "only the committed replacement is announced")
			var ids []uuid.UUID
			for _, ev := range f.eventLog.evs {
				if ev.Kind == engine.EventTypeSessionMoved {
					ids = append(ids, ev.ID)
				}
			}
			assert.Equal(t, []uuid.UUID{f.moves.recs[0].Move.ID}, ids, "PoolEvent.ID is the replacement's move ID")
			pending, err := f.intents.PendingFor(context.Background(), heldLease.Key)
			require.NoError(t, err)
			assert.Nil(t, pending, "both move intents are closed: the failed one by FailMove, the replacement by its commit")
		})
	}
}

// Edge: a suspect target is inside the liveness grace. It is NOT gone; the
// SAME move keeps waiting on it, with no new generation and no new row.
func TestSessionMover_SuspectTargetKeepsTheSameMove(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	old, _ := f.openRestoringMove(t) // the old owner still holds the workspace
	f.setLiveness("mac-b", runner.LivenessSuspect)

	_, err := f.mover.Move(context.Background(), reservedLease(), f.request("runner_lost", "claude-code"))
	require.ErrorIs(t, err, runner.ErrResourceBusy)
	assert.NotErrorIs(t, err, runner.ErrMoveTargetGone)
	st := f.moves.only(t)
	assert.Equal(t, old.Move.ID, st.Move.ID)
	assert.Equal(t, runner.MoveStateRestoring, st.State)
	assert.Empty(t, st.Failure)
	assert.Equal(t, 1, f.leases.bumps)
}

// Failure: ResumeMove (P4 T10's recovery path) fails a move whose target is
// gone and returns ErrMoveTargetGone. It closes the intent, releases the slot
// and does NOT plan the replacement itself: P4 T10's controller starts the new
// move through RecoverSession -> MoveSession.
func TestSessionMover_ResumeMoveFailsAGoneTargetAndClosesTheIntent(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	old, busy := f.openRestoringMove(t)
	delete(f.resources.active, busy.Key)
	f.setLiveness("mac-b", runner.LivenessLost)
	in, err := f.intents.PendingFor(context.Background(), heldLease.Key)
	require.NoError(t, err)
	require.NotNil(t, in)

	err = f.mover.ResumeMove(engine.ContextWithTurnLease(context.Background(), reservedLease()), *in)
	require.ErrorIs(t, err, runner.ErrMoveTargetGone)
	st := f.moves.only(t)
	assert.Equal(t, old.Move.ID, st.Move.ID)
	assert.Equal(t, runner.MoveStateFailed, st.State)
	assert.Contains(t, st.Failure, "lost")
	assert.False(t, f.ledger.jobs[old.JobID].State.Active())
	assert.Empty(t, f.moves.recs)
	assert.Empty(t, f.bus.ofType(f.runID, engine.EventTypeSessionMoved))
	assert.Equal(t, 1, f.leases.bumps, "no replacement generation from the resume path")
	pending, err := f.intents.PendingFor(context.Background(), heldLease.Key)
	require.NoError(t, err)
	assert.Nil(t, pending, "the failed move's owned intent is closed")
	assert.Zero(t, f.resources.claims, "no restore claim is taken for a gone target")
}
