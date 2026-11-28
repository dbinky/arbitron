// adapter/sqlite/session_mover_durable_test.go
package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- fakes at the non-persisted edges only (prefix dm*) ----

type dmRunner struct {
	mu      sync.Mutex
	imports int
	sid     string
}

func (s *dmRunner) Run(_ context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.Op != runner.OpSessionImport || job.Session == nil || job.Session.Bundle == nil || a.Job.RunnerID != runner.BuiltInRunnerID ||
		a.Fence != a.Job.Fence() || a.Job.Model != "claude-sonnet-4-5" || job.Binding.Model != "claude-sonnet-4-5" ||
		job.Binding.Provider != "claude-cli" || job.Binding.Backend != "claude-code" || job.Session.Backend != "claude-code" ||
		job.Session.WorkDir != "/ws" || job.Session.Bundle.SessionID != "sid-a" || string(job.Session.Bundle.Data) != "bundle" {
		return runner.StepOutput{}, fmt.Errorf("unexpected %s on %s", job.Op, a.Job.RunnerID)
	}
	s.imports++
	return runner.StepOutput{Session: &runner.SessionOpResult{SessionID: s.sid}}, nil
}

func (s *dmRunner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.imports
}

type dmRunners map[runner.RunnerID]runner.StepRunner

func (d dmRunners) For(id runner.RunnerID) (runner.StepRunner, bool) { r, ok := d[id]; return r, ok }

type dmCheckpoints struct {
	engine.SessionCheckpointStore
	bundles map[string]engine.SessionBundle
}

func (c dmCheckpoints) Get(_ context.Context, ref string) (engine.SessionBundle, error) {
	b, ok := c.bundles[ref]
	if !ok {
		return engine.SessionBundle{}, fmt.Errorf("no bundle %q", ref)
	}
	return b, nil
}

type dmNoRestore struct{}

func (dmNoRestore) RestoreBuiltIn(context.Context, uuid.UUID, string) error {
	return errors.New("durable move tests never restore a workspace")
}

type dmBus struct {
	mu sync.Mutex
	n  map[string]int
}

func (b *dmBus) Publish(_ uuid.UUID, e engine.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.n == nil {
		b.n = map[string]int{}
	}
	b.n[e.Type]++
}

func (b *dmBus) Subscribe(uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	close(ch)
	return ch, func() {}
}

func (b *dmBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) {
	ch := make(chan engine.ScopedEvent)
	close(ch)
	return ch, func() {}
}

func (b *dmBus) count(typ string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n[typ]
}

// ---- crash injection: the effect HAPPENS, then the process dies ----

var errCrash = errors.New("simulated process crash")

type crashLeases struct{ engine.TurnLeaseStore }

func (c crashLeases) BumpGeneration(ctx context.Context, l engine.TurnLease) (engine.TurnLease, error) {
	if _, err := c.TurnLeaseStore.BumpGeneration(ctx, l); err != nil {
		return engine.TurnLease{}, err
	}
	return engine.TurnLease{}, errCrash
}

type crashLedger struct{ runner.Ledger }

func (c crashLedger) Reserve(ctx context.Context, id uuid.UUID, d runner.Decision, limit int, until time.Time) (runner.Job, error) {
	if _, err := c.Ledger.Reserve(ctx, id, d, limit, until); err != nil {
		return runner.Job{}, err
	}
	return runner.Job{}, errCrash
}

type crashMoves struct {
	runner.SessionMoveStore
	afterCommit bool
}

func (c crashMoves) CommitMove(ctx context.Context, mc runner.SessionMoveCommit) error {
	if !c.afterCommit {
		return errCrash // the import ran; the commit never happened
	}
	if err := c.SessionMoveStore.CommitMove(ctx, mc); err != nil {
		return err
	}
	return errCrash // committed, then died before announcing/returning
}

// ---- fixture: session "talk" pinned to the lost runner mac-a (generation 1,
// checkpoint ckpt-1); the turn lease HELD at generation 1; the built-in runner
// serves the same anthropic group (newLedgerFixture). ----

const dmConversation = `{"sessionId":"sid-a","nonce":"n-1","acceptedTurnBinding":{"Model":"claude-sonnet-4-5"},` +
	`"messages":[{"role":"user","content":"FIRST PROMPT n-1"},{"role":"assistant","content":"hi"}]}`

type dmFixture struct {
	lf      ledgerFixture
	inst    *instance.Instance
	exec    *instance.NodeExecution
	inv     uuid.UUID
	lease   engine.TurnLease
	target  *dmRunner
	bus     *dmBus
	leases  *TurnLeaseRepo
	intents *TurnIntentRepo
	moves   *SessionMoveRepo
	execs   *NodeExecutionRepo
}

func newDmFixture(t *testing.T) *dmFixture {
	t.Helper()
	ctx := context.Background()
	lf := newLedgerFixture(t)
	inst, exec := seedSessionExec(t, lf.db, "talk", 1, time.Now().UTC().Add(-time.Minute))
	_, err := lf.db.DB.Exec(`UPDATE node_executions SET llm_conversation = ? WHERE id = ?`, dmConversation, uuidToString(exec.ID))
	require.NoError(t, err)
	inv, ref, pin := uuid.New(), "ckpt-1", "mac-a"
	execs := NewNodeExecutionRepo(lf.db.DB)
	require.NoError(t, execs.SetSessionState(ctx, exec.ID, instance.SessionState{InvocationID: inv, CheckpointRef: &ref, PinRunner: &pin, Generation: 1}))
	leases := NewTurnLeaseRepo(lf.db.DB, "proc-a")
	lease, err := leases.Acquire(ctx, engine.RunLeaseKey(inst.ID), time.Minute)
	require.NoError(t, err)
	require.EqualValues(t, 1, lease.Generation)
	return &dmFixture{lf: lf, inst: inst, exec: exec, inv: inv, lease: lease, target: &dmRunner{sid: "sid-builtin"}, bus: &dmBus{},
		leases: leases, intents: NewTurnIntentRepo(lf.db.DB), moves: NewSessionMoveRepo(lf.db.DB), execs: execs}
}

// mover builds a SessionMover over the fixture's REAL SQLite stores; wrap
// installs one crash boundary.
func (f *dmFixture) mover(wrap func(*runner.MoverDeps)) *runner.SessionMover {
	db := f.lf.db.DB
	now := func() time.Time { return ledgerNow }
	d := runner.MoverDeps{
		Registry: f.lf.reg, Ledger: f.lf.ledger, Runners: dmRunners{runner.BuiltInRunnerID: f.target}, Leases: f.leases,
		Intents: f.intents,
		Checkpoints: dmCheckpoints{bundles: map[string]engine.SessionBundle{
			"ckpt-1": {Backend: "claude-code", SessionID: "sid-a", Checksum: "c", Data: []byte("bundle")}}},
		Executions: f.execs, Moves: f.moves, Homes: NewWorkspaceHomeRepo(db), Restorer: dmNoRestore{},
		Resources:   NewResourceLeaseRepo(db, "boot-1"),
		Portability: runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{}),
		Events:      runner.NewEventRecorder(NewPoolEventRepo(db), f.bus, 1000, now),
		Status:      runner.NewExecutionStatusProjector(f.lf.ledger), Bus: f.bus, Clock: now, OpLease: time.Minute,
	}
	if wrap != nil {
		wrap(&d)
	}
	return runner.NewSessionMover(d)
}

func (f *dmFixture) request() runner.MoveRequest {
	return runner.MoveRequest{RunID: f.inst.ID, InvocationID: f.inv, NodeID: "talk", Reason: engine.MoveReasonRunnerLost, From: "mac-a",
		Requirements: runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"},
			Model: "claude-sonnet-4-5", Backend: "claude-code"},
		ExecutionID: f.exec.ID, TurnIndex: 2, WorkDir: "/ws",
		Binding: provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-sonnet-4-5", Backend: "claude-code"}}
}

// held is the lease as a restarted process recovers it (same token, current generation).
func (f *dmFixture) held(t *testing.T) engine.TurnLease {
	t.Helper()
	l, ok, err := f.leases.Current(context.Background(), f.lease.Key)
	require.NoError(t, err)
	require.True(t, ok)
	return l
}

// moveIntent reads the move's own kind=move intent (pending or completed).
func (f *dmFixture) moveIntent(t *testing.T) engine.TurnIntent {
	t.Helper()
	in, err := scanTurnIntent(f.lf.db.DB.QueryRow(`SELECT ` + turnIntentColumns + ` FROM turn_intents WHERE kind = 'move'`))
	require.NoError(t, err)
	return in
}

// assertCommittedOnce: one committed move, one generation reservation, one
// import job reserved once and completed, the intent closed, no slot held.
func (f *dmFixture) assertCommittedOnce(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	got, err := f.execs.GetByInstanceAndNode(ctx, f.inst.ID, "talk")
	require.NoError(t, err)
	require.NotNil(t, got.SessionPinRunner)
	assert.Equal(t, string(runner.BuiltInRunnerID), *got.SessionPinRunner, "the pin follows the committed move")
	assert.EqualValues(t, 2, got.SessionGeneration, "generation reserved exactly once")
	require.Len(t, got.SessionMoves, 1, "exactly one committed move row")
	assert.EqualValues(t, 2, got.SessionMoves[0].Generation)
	assert.Equal(t, "mac-a", got.SessionMoves[0].FromRunner)
	assert.EqualValues(t, 2, f.held(t).Generation, "no second BumpGeneration")
	pending, err := f.intents.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "the move intent closed with the commit")
	assert.Zero(t, f.lf.slots(t), "no import slot left reserved")
	jobs, err := f.lf.ledger.ListByRun(ctx, f.inst.ID)
	require.NoError(t, err)
	require.Len(t, jobs, 1, "one import job for the one move")
	assert.Equal(t, runner.JobCompleted, jobs[0].State)
	attempts, err := f.lf.ledger.Attempts(ctx, jobs[0].ID)
	require.NoError(t, err)
	assert.Len(t, attempts, 1, "the reserved target slot was reused, never re-reserved")
}

// Edge (alignment): crash at every effect boundary of a reserved move. Before
// the restart the old pin stays, tracked by the open persisted move; the
// restart finishes the SAME move once, without a second bump or a leaked slot.
func TestSessionMover_CrashAfterGenerationReserveDoesNotLosePinOrLeakSlot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wrap      func(*runner.MoverDeps)
		openState runner.MoveState // "" = the crash came after the commit
		imports   int              // total imports once resumed
	}{
		{"after BumpGeneration", func(d *runner.MoverDeps) { d.Leases = crashLeases{d.Leases} }, runner.MoveStatePreparing, 1},
		{"after Reserve", func(d *runner.MoverDeps) { d.Ledger = crashLedger{d.Ledger} }, runner.MoveStatePreparing, 1},
		{"after import, before commit", func(d *runner.MoverDeps) { d.Moves = crashMoves{SessionMoveStore: d.Moves} }, runner.MoveStateImporting, 2},
		{"after commit", func(d *runner.MoverDeps) { d.Moves = crashMoves{SessionMoveStore: d.Moves, afterCommit: true} }, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newDmFixture(t)
			_, err := f.mover(tc.wrap).Move(ctx, f.lease, f.request())
			require.ErrorIs(t, err, errCrash)

			in := f.moveIntent(t)
			st, found, err := f.moves.MoveForIntent(ctx, in.ID)
			require.NoError(t, err)
			require.True(t, found, "the move row was persisted before the first effect")
			if tc.openState != "" {
				assert.Equal(t, tc.openState, st.State)
				assert.EqualValues(t, 2, st.Move.Generation, "the planned generation is durable")
				got, err := f.execs.GetByInstanceAndNode(ctx, f.inst.ID, "talk")
				require.NoError(t, err)
				assert.Equal(t, "mac-a", *got.SessionPinRunner, "the old pin stays until the commit, tracked by the open move")
				assert.EqualValues(t, 1, got.SessionGeneration)
				assert.Empty(t, got.SessionMoves, "an open move is not committed history")
				pending, err := f.intents.PendingFor(ctx, f.lease.Key)
				require.NoError(t, err)
				require.NotNil(t, pending)
				assert.Equal(t, in.ID, pending.ID, "the move intent stays pending for the startup scan")
			}

			restarted := f.mover(nil)
			require.NoError(t, restarted.ResumeMove(engine.ContextWithTurnLease(ctx, f.held(t)), in))

			f.assertCommittedOnce(t)
			after, _, err := f.moves.MoveForIntent(ctx, in.ID)
			require.NoError(t, err)
			assert.Equal(t, st.Move.ID, after.Move.ID, "the same move ID finished")
			assert.Equal(t, runner.MoveStateCommitted, after.State)
			assert.Equal(t, tc.imports, f.target.count())
			assert.LessOrEqual(t, f.bus.count(engine.EventTypeSessionMoved), 1, "session.moved is never announced twice")
		})
	}
}

// Success (alignment): after a crash with the target slot reserved, a
// duplicate trigger and the startup scan both reach the persisted move; the
// move finishes once (same ID/target/generation, one import, one event).
func TestSessionMover_RestartFinishesReservedMoveOnce(t *testing.T) {
	ctx := context.Background()
	f := newDmFixture(t)
	_, err := f.mover(func(d *runner.MoverDeps) { d.Ledger = crashLedger{d.Ledger} }).Move(ctx, f.lease, f.request())
	require.ErrorIs(t, err, errCrash)
	in := f.moveIntent(t)
	before, _, err := f.moves.MoveForIntent(ctx, in.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, f.lf.slots(t), "the target slot is reserved across the crash")

	restarted := f.mover(nil)
	res, err := restarted.Move(ctx, f.held(t), f.request()) // a pool event re-triggers before the scan
	require.NoError(t, err)
	assert.Equal(t, runner.BuiltInRunnerID, res.To)
	assert.Equal(t, "sid-builtin", res.SessionID)
	assert.EqualValues(t, 2, res.Generation)
	require.NoError(t, restarted.ResumeMove(engine.ContextWithTurnLease(ctx, f.held(t)), in))
	require.NoError(t, restarted.ResumeMove(engine.ContextWithTurnLease(ctx, f.held(t)), in), "a duplicate scan is a no-op")

	f.assertCommittedOnce(t)
	after, _, err := f.moves.MoveForIntent(ctx, in.ID)
	require.NoError(t, err)
	assert.Equal(t, before.Move.ID, after.Move.ID)
	assert.Equal(t, 1, f.target.count(), "the bundle imported exactly once")
	assert.Equal(t, 1, f.bus.count(engine.EventTypeSessionMoved), "session.moved exactly once, for the committed move")
	events, err := NewPoolEventRepo(f.lf.db.DB).List(ctx, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, before.Move.ID, events[0].ID, "the durable pool event is keyed by the move ID")
}

// Edge (alignment): a mid-turn native import commits a PROVISIONAL resume
// while the turn's exact message stays the pending resend source; a refusal
// of that resume (P4 T9 handles it once) rebuilds from exactly this committed
// state and sends the SAME message once.
func TestSessionMover_NativeResumeRefusalRebuildsSameMessage(t *testing.T) {
	ctx := context.Background()
	f := newDmFixture(t)
	turn := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(),
		Binding:  engine.AcceptedTurnBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-sonnet-4-5", Backend: "claude-code"},
		LeaseKey: f.lease.Key, RunID: f.inst.ID, InvocationID: f.inv, NodeID: "talk", TurnIndex: 2, Kind: engine.TurnIntentMessage,
		Message: "SAME MESSAGE", PriorCheckpointRef: "ckpt-1", SessionGeneration: 1, OperationID: "turn/2"}
	require.NoError(t, f.intents.Persist(ctx, turn))

	req := f.request()
	req.Reason, req.RequestID = engine.MoveReasonExhausted, turn.RequestID
	res, err := f.mover(nil).Move(ctx, f.lease, req)
	require.NoError(t, err)
	assert.Equal(t, "sid-builtin", res.SessionID, "native import succeeded")
	assert.False(t, res.PendingRebuild)

	pending, err := f.intents.Pending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "no kind=move intent beside the in-flight turn")
	assert.Equal(t, turn.ID, pending[0].ID)
	assert.Equal(t, engine.TurnIntentMessage, pending[0].Kind)
	assert.Equal(t, "SAME MESSAGE", pending[0].Message, "the exact accepted message is retained for the resend")
	st, found, err := f.moves.MoveForIntent(ctx, turn.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, runner.MoveStateCommitted, st.State)
	assert.False(t, st.OwnsIntent, "the turn commit, not the move, completes the turn intent")

	got, err := f.execs.GetByInstanceAndNode(ctx, f.inst.ID, "talk")
	require.NoError(t, err)
	conv := got.LLMConversation
	assert.Equal(t, "sid-builtin", conv[engine.ConversationKeySessionID])
	assert.Equal(t, true, conv[engine.ConversationKeyProvisionalResume], "the first actual resume of an imported session is provisional")
	assert.Equal(t, false, conv[engine.ConversationKeyPendingRebuild])
	assert.Equal(t, "n-1", conv[engine.ConversationKeyNonce], "original nonce kept")
	assert.Len(t, conv[engine.ConversationKeyMessages], 2, "transcript kept")
	assert.Equal(t, map[string]any{"Model": "claude-sonnet-4-5"}, conv[engine.ConversationKeyAcceptedTurnBinding], "accepted binding kept")

	prompt, err := engine.BuildRebuildPrompt(conv, pending[0].Message, engine.RebuildLimitBytes(provider.ContextDefault))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(prompt, "FIRST PROMPT n-1"), "the original first prompt verbatim")
	assert.Equal(t, 1, strings.Count(prompt, "SAME MESSAGE"))
	assert.True(t, strings.HasSuffix(prompt, "SAME MESSAGE"))
}
