// domain/runner/hub_adopt_test.go
package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedAdmitted plays the server before the restart: it registers the runner
// and admits it once through a throwaway hub, so its sources are recorded and
// the ledger can reserve jobs on it.
func seedAdmitted(t *testing.T, p *hubtest.Pool, id runner.RunnerID) runner.RunnerKey {
	t.Helper()
	key := p.SeedRunner(t, id, 2)
	_, err := p.NewHub(hubtest.HubConfig()).Admit(context.Background(), key, hubtest.NewFakeConn(), hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.NoError(t, err)
	return key
}

// reserveProcess reserves a slot-only process job on id: remote process work
// needs no source capability (GroupFor(process) = CredNone).
func reserveProcess(t *testing.T, p *hubtest.Pool, run uuid.UUID, id runner.RunnerID) runner.Assignment {
	t.Helper()
	return p.ReserveJobFor(t, run, id, "", runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, RunID: run,
		Op: runner.OpProcess, Affinity: runner.AffinityNone})
}

func TestAdoptionBook_ResultBeforeOrAfterWait(t *testing.T) {
	var b runner.AdoptionBook
	f := runner.Fence{JobID: uuid.New(), RunnerID: "mac-1", Generation: 2}

	b.Deliver(f, runner.RemoteResult{OK: true, Process: &engine.ProcessResult{Stdout: "early"}})
	r, err := b.Wait(context.Background(), f)
	require.NoError(t, err, "a result replayed before the engine re-entered is retained")
	assert.Equal(t, "early", r.Process.Stdout)

	done := make(chan runner.RemoteResult, 1)
	go func() {
		r, err := b.Wait(context.Background(), f)
		if err == nil {
			done <- r
		}
	}()
	require.Eventually(t, func() bool { return b.Waiting(f) }, time.Second, time.Millisecond)
	b.Deliver(f, runner.RemoteResult{OK: true, Process: &engine.ProcessResult{Stdout: "late"}})
	select {
	case r := <-done:
		assert.Equal(t, "late", r.Process.Stdout)
	case <-time.After(time.Second):
		t.Fatal("waiter never received the result")
	}

	stale := runner.Fence{JobID: f.JobID, RunnerID: "mac-1", Generation: 1}
	b.Deliver(stale, runner.RemoteResult{OK: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = b.Wait(ctx, f)
	require.ErrorIs(t, err, context.DeadlineExceeded, "results are matched by the full fence (generation included)")
}

func TestAdoptionBook_RunnerLossReleasesWaiters(t *testing.T) {
	var b runner.AdoptionBook
	f1 := runner.Fence{JobID: uuid.New(), RunnerID: "mac-1", Generation: 1}
	f2 := runner.Fence{JobID: uuid.New(), RunnerID: "mac-2", Generation: 1}
	errs := make(chan error, 2)
	for _, f := range []runner.Fence{f1, f2} {
		f := f
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_, err := b.Wait(ctx, f)
			errs <- err
		}()
	}
	require.Eventually(t, func() bool { return b.Waiting(f1) && b.Waiting(f2) }, time.Second, time.Millisecond)
	b.FailRunner("mac-1", runner.ErrRunnerLost)
	require.ErrorIs(t, <-errs, runner.ErrRunnerLost, "only mac-1's waiter is released")
	require.ErrorIs(t, <-errs, context.DeadlineExceeded)

	f3 := runner.Fence{JobID: uuid.New(), RunnerID: "mac-3", Generation: 1}
	b.FailJob(f3, runner.ErrRunnerLost)
	_, err := b.Wait(context.Background(), f3)
	require.ErrorIs(t, err, runner.ErrRunnerLost, "a job the reconnecting runner no longer holds fails even if Wait comes later")
}

func TestAdoptedOutput(t *testing.T) {
	out, err := runner.AdoptedOutput(runner.OpAgent, runner.RemoteResult{OK: true, Agent: &engine.AgentResult{Summary: "s"}})
	require.NoError(t, err)
	assert.Equal(t, "s", out.Agent.Summary)

	reset := time.Now().Add(time.Hour)
	_, err = runner.AdoptedOutput(runner.OpAgent, runner.RemoteResult{OK: false, ErrorText: "usage limit",
		Outcome: runner.SourceOutcome{Class: runner.OutcomeExhausted, ResetAt: &reset}})
	require.Error(t, err)
	assert.Equal(t, runner.OutcomeExhausted, runner.ClassifyError(err).Class, "the remote outcome is preserved")

	chat, err := runner.AdoptedOutput(runner.OpChat, runner.RemoteResult{OK: true, Chat: &runner.RemoteChatResult{Output: "kept"}})
	require.NoError(t, err, "alignment: a durable chat result is consumed, not requeued")
	assert.Equal(t, "kept", chat.Chat.Output)

	_, err = runner.AdoptedOutput(runner.OpProcess, runner.RemoteResult{OK: true, Chat: &runner.RemoteChatResult{Output: "x"}})
	require.Error(t, err, "a payload that does not match the operation is refused")

	_, err = runner.AdoptedOutput(runner.OpStream, runner.RemoteResult{OK: true})
	require.ErrorIs(t, err, runner.ErrAdoptionUnsupported, "a stream is adopted by Hub.Adopt, never from one result")
	assert.False(t, errors.Is(err, runner.ErrRunnerLost))
}

// A fresh hub plays the restarted server: it never dispatched the job, but the
// ledger still holds it live on mac-1, so reconnecting must adopt, not fence.
func TestHubAdopt_RestartedServerAdoptsInsteadOfFencing(t *testing.T) {
	ctx := context.Background()
	p := hubtest.NewPool(t)
	key := seedAdmitted(t, p, "mac-1")
	run := p.SeedRun(t)
	a := reserveProcess(t, p, run, "mac-1")
	gone := reserveProcess(t, p, run, "mac-1")
	h := p.NewHub(hubtest.HubConfig())

	goneErr := make(chan error, 1)
	go func() {
		_, err := h.Adopt(ctx, gone, runner.StepJob{Op: runner.OpProcess})
		goneErr <- err
	}()
	conn := hubtest.NewFakeConn()
	hello := hubtest.DarwinHello(hubtest.AnthropicSource())
	hello.Held = []runner.HeldJobInfo{{JobID: a.Fence.JobID, Generation: a.Fence.Generation, State: runner.HeldTerminal}}
	s, err := h.Admit(ctx, key, conn, hello)
	require.NoError(t, err)
	assert.Zero(t, conn.Count(runner.MsgFenced, a.Fence.JobID), "a ledger-live job is adopted, never fenced")
	select {
	case err := <-goneErr:
		require.ErrorIs(t, err, runner.ErrRunnerLost, "a job the runner no longer holds is released for recovery")
	case <-time.After(2 * time.Second):
		t.Fatal("adoption of the job the runner dropped never failed")
	}

	got := make(chan runner.StepOutput, 1)
	go func() {
		out, err := h.Adopt(ctx, a, runner.StepJob{Op: runner.OpProcess})
		if err == nil {
			got <- out
		}
	}()
	require.NoError(t, h.Handle(ctx, s, runner.Inbound{Kind: runner.MsgResult, JobID: a.Fence.JobID, Generation: a.Fence.Generation,
		Result: &runner.RemoteResult{OK: true, Process: &engine.ProcessResult{Stdout: "done before restart"}}}))
	select {
	case out := <-got:
		assert.Equal(t, "done before restart", out.Process.Stdout)
	case <-time.After(2 * time.Second):
		t.Fatal("the replayed result never reached the adopter")
	}
	assert.Equal(t, 2, conn.Count(runner.MsgAck, a.Fence.JobID), "the held job is acknowledged on hello and its result on delivery")
	assert.Zero(t, conn.Count(runner.MsgFenced, a.Fence.JobID))
}

func TestHubAdopt_GraceDefersLossThenLossReleasesAdopters(t *testing.T) {
	ctx := context.Background()
	p := hubtest.NewPool(t)
	seedAdmitted(t, p, "mac-1")
	run := p.SeedRun(t)
	a := p.ReserveJob(t, run, "mac-1", runner.MakeSourceID("mac-1", runner.CredAnthropicSubscription, ""), runner.OpChat)
	require.NoError(t, p.Registry.SetLiveness(ctx, "mac-1", runner.LivenessOnline, p.Clock.Now()))
	cfg := hubtest.HubConfig()
	h := p.NewHub(cfg)
	h.BeginRestartGrace(p.Clock.Now().Add(3 * cfg.LostAfter))

	errs := make(chan error, 1)
	go func() {
		_, err := h.Adopt(ctx, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
		errs <- err
	}()
	p.Clock.Advance(2 * cfg.LostAfter)
	require.NoError(t, h.Tick(ctx))
	r, _, err := p.Registry.GetRunner(ctx, "mac-1")
	require.NoError(t, err)
	assert.NotEqual(t, runner.LivenessLost, r.Liveness, "no runner is lost inside the restart grace")

	p.Clock.Advance(2 * cfg.LostAfter)
	require.NoError(t, h.Tick(ctx))
	select {
	case err := <-errs:
		require.ErrorIs(t, err, runner.ErrRunnerLost)
	case <-time.After(2 * time.Second):
		t.Fatal("loss after the grace never released the adopter")
	}
	j, err := p.Ledger.Get(ctx, a.Fence.JobID)
	require.NoError(t, err)
	assert.NotEqual(t, runner.JobFailed, j.State, "an awaited job is left to its adopter to requeue, not failed by the hub")
}

// Alignment (Success): a chat result journaled and acknowledged before the
// restart is consumed by the restarted hub with no dispatch (no provider repeat).
func TestHubAdopt_DurableChatResultAvoidsProviderRepeat(t *testing.T) {
	ctx := context.Background()
	p := hubtest.NewPool(t)
	key := seedAdmitted(t, p, "mac-1")
	run := p.SeedRun(t)
	a := p.ReserveJob(t, run, "mac-1", runner.MakeSourceID("mac-1", runner.CredAnthropicSubscription, ""), runner.OpChat)

	before := p.NewHub(hubtest.HubConfig())
	conn := hubtest.NewFakeConn()
	hello := hubtest.DarwinHello(hubtest.AnthropicSource())
	hello.Held = []runner.HeldJobInfo{{JobID: a.Fence.JobID, Generation: a.Fence.Generation, State: runner.HeldRunning}}
	s, err := before.Admit(ctx, key, conn, hello)
	require.NoError(t, err)
	require.NoError(t, before.Handle(ctx, s, runner.Inbound{Kind: runner.MsgResult, JobID: a.Fence.JobID, Generation: a.Fence.Generation,
		Result: &runner.RemoteResult{OK: true, Chat: &runner.RemoteChatResult{Output: "answered once"}}}))

	after := p.NewHub(hubtest.HubConfig()) // the restarted server: no session, no waiter
	out, err := after.Adopt(ctx, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	require.NoError(t, err)
	require.NotNil(t, out.Chat)
	assert.Equal(t, "answered once", out.Chat.Output)
	assert.Zero(t, conn.Count(runner.MsgDispatch, a.Fence.JobID), "the provider request is never repeated")
}

// Alignment: an in-flight stream resumes from the persisted output cursor:
// journaled frames replay, the runner resends only after the cursor, and the
// live tail plus the terminal chunk follow. No dispatch is sent.
func TestHubAdopt_RestartLoadsPersistedOutputCursor(t *testing.T) {
	ctx := context.Background()
	p := hubtest.NewPool(t)
	key := seedAdmitted(t, p, "mac-1")
	run := p.SeedRun(t)
	a := p.ReserveJob(t, run, "mac-1", runner.MakeSourceID("mac-1", runner.CredAnthropicSubscription, ""), runner.OpStream)
	held := func(seq uint64) runner.HelloInfo {
		h := hubtest.DarwinHello(hubtest.AnthropicSource())
		h.Held = []runner.HeldJobInfo{{JobID: a.Fence.JobID, Generation: a.Fence.Generation, State: runner.HeldRunning, LastOutputSeq: seq}}
		return h
	}
	frame := func(seq uint64, tok string) runner.Inbound {
		return runner.Inbound{Kind: runner.MsgOutput, JobID: a.Fence.JobID, Generation: a.Fence.Generation, Output: &runner.OutputFrame{Seq: seq, Token: tok}}
	}

	before := p.NewHub(hubtest.HubConfig())
	s1, err := before.Admit(ctx, key, hubtest.NewFakeConn(), held(0))
	require.NoError(t, err)
	require.NoError(t, before.Handle(ctx, s1, frame(1, "Hel")))
	require.NoError(t, before.Handle(ctx, s1, frame(2, "lo")))

	after := p.NewHub(hubtest.HubConfig())
	conn := hubtest.NewFakeConn()
	s2, err := after.Admit(ctx, key, conn, held(2))
	require.NoError(t, err)
	ack := conn.Await(t, runner.MsgAck, a.Fence.JobID)
	require.NotNil(t, ack.Ack)
	assert.Equal(t, uint64(2), ack.Ack.Seq, "the persisted cursor tells the runner what to resend")

	out, err := after.Adopt(ctx, a, hubtest.ChatJob("claude-opus-5-5", runner.OpStream))
	require.NoError(t, err)
	require.NotNil(t, out.Stream)
	require.NoError(t, after.Handle(ctx, s2, frame(3, " world")))
	require.NoError(t, after.Handle(ctx, s2, runner.Inbound{Kind: runner.MsgResult, JobID: a.Fence.JobID, Generation: a.Fence.Generation,
		Result: &runner.RemoteResult{OK: true, Chat: &runner.RemoteChatResult{Output: "Hello world"}}}))
	text, terminal := "", false
	for c := range out.Stream {
		text += c.Token
		if c.Done {
			terminal = true
			require.NoError(t, c.Error)
		}
	}
	assert.Equal(t, "Hello world", text)
	assert.True(t, terminal)
	assert.Zero(t, conn.Count(runner.MsgDispatch, a.Fence.JobID))
}

// Decision D6: a ledger-live job missing from a reconnect hello is reported
// durably (runner_job_absences + job.absent_on_reconnect) and its adoption fails
// with ErrJobAbsentOnReconnect, which still wraps ErrRunnerLost.
func TestHubAdopt_AbsentOnReconnectIsDurableStopEvidence(t *testing.T) {
	ctx := context.Background()
	p := hubtest.NewPool(t)
	key := seedAdmitted(t, p, "mac-1")
	run := p.SeedRun(t)
	gone := reserveProcess(t, p, run, "mac-1")
	h := p.NewHub(hubtest.HubConfig())

	errs := make(chan error, 1)
	go func() {
		_, err := h.Adopt(ctx, gone, runner.StepJob{Op: runner.OpProcess})
		errs <- err
	}()
	_, err := h.Admit(ctx, key, hubtest.NewFakeConn(), hubtest.DarwinHello(hubtest.AnthropicSource())) // Held is empty
	require.NoError(t, err)
	select {
	case err := <-errs:
		require.ErrorIs(t, err, runner.ErrJobAbsentOnReconnect)
		require.ErrorIs(t, err, runner.ErrRunnerLost, "callers that only know runner loss still see it")
		assert.Equal(t, runner.OutcomeRunnerLost, runner.ClassifyError(err).Class)
	case <-time.After(2 * time.Second):
		t.Fatal("adoption of the absent job never failed")
	}
	var absences, events int
	require.NoError(t, p.DB.QueryRow(`SELECT COUNT(*) FROM runner_job_absences WHERE job_id = ? AND generation = ? AND runner_id = 'mac-1'`,
		gone.Fence.JobID.String(), gone.Fence.Generation).Scan(&absences))
	assert.Equal(t, 1, absences, "the reconnect report is durable stop evidence")
	require.NoError(t, p.DB.QueryRow(`SELECT COUNT(*) FROM pool_events WHERE kind = ?`, runner.EventJobAbsentOnReconnect).Scan(&events))
	assert.Equal(t, 1, events, "the report is logged")
	require.NoError(t, p.Ledger.FenceCheck(ctx, gone.Fence), "the hub only reports; the adopter fences and requeues")
}
