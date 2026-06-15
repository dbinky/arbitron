package codex

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real exec session must satisfy the port — a rename of either side breaks
// the build here.
var _ mcpSession = (*execSession)(nil)

// --- buildInteractiveExecArgs: fresh vs resume -----------------------------

// HAPPY — a fresh turn builds `codex exec --json --skip-git-repo-check -s
// workspace-write -c sandbox_workspace_write.writable_roots=["<wd>"]
// -c sandbox_workspace_write.network_access=false -m <model> --cd <wd>`; the
// prompt is NOT an arg (it rides stdin).
//
// SECURITY (Must Fix #1): the two -c overrides are the SOLE containment now that
// the approval subsystem is deleted. Bare `-s workspace-write` was VERIFIED against
// codex-cli 0.139 to leave writes to the cwd PARENT chain and /tmp open and reads
// fully unconfined; pinning writable_roots to ONLY the WorkDir closes the
// cross-worktree write escape, and network_access=false pins egress off.
func TestBuildInteractiveExecArgs_Happy_Fresh(t *testing.T) {
	args, err := buildInteractiveExecArgs(turnConfig{Model: "gpt-5", Prompt: "interview me", WorkDir: "/work"})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"exec", "--json", "--skip-git-repo-check",
		"-s", "workspace-write",
		"-c", `sandbox_workspace_write.writable_roots=["/work"]`,
		"-c", "sandbox_workspace_write.network_access=false",
		"-m", "gpt-5", "--cd", "/work",
	}, args)
	assert.NotContains(t, args, "interview me", "the prompt must never reach argv — it rides stdin")
}

// SUCCESS — a resume turn targets `codex exec resume <id> --json
// --skip-git-repo-check -c sandbox_workspace_write.writable_roots=["<wd>"]
// -c sandbox_workspace_write.network_access=false -`; the "-" makes codex read the
// prompt from stdin, and NEITHER -s NOR -m is re-sent (codex exec resume rejects
// both — VERIFIED).
//
// SECURITY (Must Fix #1): resume re-asserts the SAME posture via -c (which resume
// DOES accept, unlike -s). Resume confines writes against the PROCESS cwd
// (RunSpec.Dir == WorkDir — resume rejects --cd too), so the resume-cwd invariant
// is load-bearing for containment, not just rollout resolution.
func TestBuildInteractiveExecArgs_Success_Resume(t *testing.T) {
	args, err := buildInteractiveExecArgs(turnConfig{Model: "gpt-5", Prompt: "continue", WorkDir: "/work", ResumeID: "conv-abc"})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"exec", "resume", "conv-abc",
		"--json", "--skip-git-repo-check",
		"-c", `sandbox_workspace_write.writable_roots=["/work"]`,
		"-c", "sandbox_workspace_write.network_access=false",
		"-",
	}, args)
	assert.NotContains(t, args, "-s", "resume must NOT re-send the sandbox flag (codex exec resume rejects it)")
	assert.NotContains(t, args, "-m", "resume must NOT re-send the model (it carries from the session)")
	assert.NotContains(t, args, "--cd", "resume must NOT send --cd (codex exec resume rejects it); the process cwd is RunSpec.Dir")
	assert.NotContains(t, args, "continue", "the prompt must never reach argv on resume either")
}

// SECURITY (Must Fix #1) — both fresh AND resume turns must carry the writable_roots
// pin and the network-off override, because the sandbox is the SOLE containment
// after the approval subsystem was deleted. A bare `-s workspace-write` was proven
// (against codex-cli 0.139) NOT to confine writes to the worktree.
func TestBuildInteractiveExecArgs_Security_PostureOnBothPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  turnConfig
	}{
		{"fresh", turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/sandbox/root"}},
		{"resume", turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/sandbox/root", ResumeID: "conv-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, err := buildInteractiveExecArgs(tc.cfg)
			require.NoError(t, err)
			assert.Contains(t, args, `sandbox_workspace_write.writable_roots=["/sandbox/root"]`,
				"writable_roots must be pinned to ONLY the WorkDir on the %s path", tc.name)
			assert.Contains(t, args, "sandbox_workspace_write.network_access=false",
				"network egress must be pinned off on the %s path", tc.name)
		})
	}
}

// SUCCESS — a non-empty effort rides the fresh turn as `-c model_reasoning_effort=<v>`.
func TestBuildInteractiveExecArgs_Success_Effort(t *testing.T) {
	args, err := buildInteractiveExecArgs(turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w", Effort: "high"})
	require.NoError(t, err)
	assert.Contains(t, args, "-c")
	assert.Contains(t, args, "model_reasoning_effort=high")
}

// EDGE — an empty effort emits no model_reasoning_effort override (default/empty
// effort passes nothing). The -c flag is still present for the security posture
// overrides (writable_roots + network_access); only the effort key is absent.
func TestBuildInteractiveExecArgs_Edge_NoEffort(t *testing.T) {
	args, err := buildInteractiveExecArgs(turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w"})
	require.NoError(t, err)
	for _, a := range args {
		assert.NotContains(t, a, codexEffortConfigKey, "no effort must emit no model_reasoning_effort override")
	}
}

// ERROR — a fresh turn with an empty model fails closed (never a default-model run).
func TestBuildInteractiveExecArgs_Error_EmptyModelFresh(t *testing.T) {
	_, err := buildInteractiveExecArgs(turnConfig{Prompt: "p", WorkDir: "/w"})
	assert.ErrorIs(t, err, ErrModelRequired)
}

// EDGE — a resume turn does NOT require a model (it carries from the session), so an
// empty model is fine on the resume path.
func TestBuildInteractiveExecArgs_Edge_ResumeNeedsNoModel(t *testing.T) {
	args, err := buildInteractiveExecArgs(turnConfig{Prompt: "p", WorkDir: "/w", ResumeID: "conv-1"})
	require.NoError(t, err)
	assert.Equal(t, "resume", args[1])
	assert.Equal(t, "conv-1", args[2])
}

// CONTRACT (Should Fix #1) — Effort is IMMUTABLE across a conversation: a resume
// turn that sets Effort silently drops it (codex carries the effort from the
// persisted on-disk session). Asserting it here converts the unstated assumption
// into a documented, tested contract.
func TestBuildInteractiveExecArgs_Contract_ResumeDropsEffort(t *testing.T) {
	args, err := buildInteractiveExecArgs(turnConfig{Prompt: "p", WorkDir: "/w", ResumeID: "conv-1", Effort: "high"})
	require.NoError(t, err)
	assert.NotContains(t, args, codexEffortConfigKey+"=high",
		"resume must NOT emit effort — it is immutable across a conversation and carried by the persisted session")
}

// CONTRACT (Should Fix #1) — the resume-cwd invariant: Start MUST pass cfg.WorkDir
// as the process cwd (RunSpec.Dir) on a resume turn, because `codex exec resume`
// rejects --cd and resolves BOTH the on-disk rollout AND the workspace-write sandbox
// against the process cwd. If the resume process cwd ever diverged from the
// originating turn's WorkDir, codex would resolve the wrong tree (silent, confusing)
// and the writable_roots pin would confine against the wrong root.
func TestExecSession_Contract_ResumePassesWorkDirAsProcessCwd(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: execLines("conv-2", "ok", 1, 1)},
	}}
	c := newCore(Config{BinaryPath: "/x/codex", Runner: mr})
	s := newExecSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{
		Prompt: "more", WorkDir: "/work/instance-A", ResumeID: "conv-1",
	})
	require.NoError(t, err)
	_, _ = drainStream(t, context.Background(), stream)

	require.Len(t, mr.calls, 2)
	assert.Equal(t, "/work/instance-A", mr.calls[1].Dir,
		"resume's process cwd (RunSpec.Dir) MUST be the WorkDir — codex resume resolves the rollout + sandbox against it")
	assert.NotContains(t, mr.calls[1].Args, "--cd",
		"resume must not pass --cd (codex exec resume rejects it); the WorkDir rides RunSpec.Dir")
}

// --- execSession.Start: folds the exec JSONL stream into turnEvents ---------

// execLines is a canned `codex exec --json` event sequence (parse.go's verified
// vocabulary): thread.started → turn.started → an agent_message item → turn.completed.
func execLines(threadID, text string, in, out int) []string {
	return []string{
		`{"type":"thread.started","thread_id":"` + threadID + `"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"` + text + `"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) + `}}`,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// HAPPY — Start folds the exec stream into a turnText (the agent_message) then a
// terminal turnDone carrying the thread id + token counts, and the exec invocation
// (the second runner call, after the version probe) carries the verified argv +
// the prompt on stdin.
func TestExecSession_Happy_FoldsStreamAndArgs(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: execLines("conv-1", "hello world", 11, 4)},
	}}
	c := newCore(Config{BinaryPath: "/x/codex", Runner: mr})
	s := newExecSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "hi", WorkDir: "/w"})
	require.NoError(t, err)

	events, err := drainStream(t, context.Background(), stream)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, turnText, events[0].Kind)
	assert.Equal(t, "hello world", events[0].Text)
	assert.Equal(t, turnDone, events[1].Kind)
	assert.Equal(t, "conv-1", events[1].SessionID)
	assert.Equal(t, 11, events[1].TokensIn)
	assert.Equal(t, 4, events[1].TokensOut)

	// The exec invocation is the SECOND runner call (calls[0] is the version probe).
	require.Len(t, mr.calls, 2)
	exec := mr.calls[1]
	assert.Equal(t, []string{
		"exec", "--json", "--skip-git-repo-check", "-s", "workspace-write",
		"-c", `sandbox_workspace_write.writable_roots=["/w"]`,
		"-c", "sandbox_workspace_write.network_access=false",
		"-m", "gpt-5", "--cd", "/w",
	}, exec.Args)
	assert.Equal(t, "hi", exec.Stdin, "the prompt must be delivered via stdin")
}

// SUCCESS — a resume turn (ResumeID set) drives `codex exec resume <id>` with the
// prompt on stdin, and folds the same way.
func TestExecSession_Success_Resume(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: execLines("conv-2", "continuing", 3, 2)},
	}}
	c := newCore(Config{BinaryPath: "/x/codex", Runner: mr})
	s := newExecSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "more", WorkDir: "/w", ResumeID: "conv-1"})
	require.NoError(t, err)
	events, err := drainStream(t, context.Background(), stream)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "continuing", events[0].Text)
	assert.Equal(t, turnDone, events[1].Kind)

	require.Len(t, mr.calls, 2)
	assert.Equal(t, []string{
		"exec", "resume", "conv-1", "--json", "--skip-git-repo-check",
		"-c", `sandbox_workspace_write.writable_roots=["/w"]`,
		"-c", "sandbox_workspace_write.network_access=false",
		"-",
	}, mr.calls[1].Args)
	assert.Equal(t, "more", mr.calls[1].Stdin)
}

// FAILURE — a turn.failed event in the stream folds to a terminal turnFailed
// carrying the cause, never a clean turnDone.
func TestExecSession_Failure_TurnFailed(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: []string{
			`{"type":"thread.started","thread_id":"conv-1"}`,
			`{"type":"turn.failed","error":{"message":"model refused"}}`,
		}},
	}}
	c := newCore(Config{BinaryPath: "/x/codex", Runner: mr})
	s := newExecSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w"})
	require.NoError(t, err)
	events, drainErr := drainStream(t, context.Background(), stream)
	require.NoError(t, drainErr, "the stream terminates cleanly after surfacing the failure event")
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, turnFailed, last.Kind)
	require.Error(t, last.Err)
	assert.Contains(t, last.Err.Error(), "model refused")
	for _, e := range events {
		assert.NotEqual(t, turnDone, e.Kind, "a failed turn must never emit a clean turnDone")
	}
}

// FAILURE — a non-zero process exit (wait() error) with no terminal event folds to
// a turnFailed (a process crash is never a silent clean done).
func TestExecSession_Failure_NonZeroExit(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: []string{`{"type":"thread.started","thread_id":"conv-1"}`}, waitErr: errors.New("exit status 1")},
	}}
	c := newCore(Config{BinaryPath: "/x/codex", Runner: mr})
	s := newExecSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w"})
	require.NoError(t, err)
	events, drainErr := drainStream(t, context.Background(), stream)
	require.NoError(t, drainErr)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, turnFailed, last.Kind)
	assert.Contains(t, last.Err.Error(), "exit status 1")
}

// ERROR — an empty WorkDir fails closed before the probe even runs: no stream, and
// the runner is never called.
func TestExecSession_Error_EmptyWorkDirFailsClosed(t *testing.T) {
	runner := &fakeRunner{}
	c := newCore(Config{BinaryPath: "/x/codex", Runner: runner})
	s := newExecSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "p"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.Nil(t, stream)
	assert.False(t, runner.runCalled, "WorkDir guard must fire before the probe — the runner must never run")
}

// ERROR — an incompatible-binary probe fails closed: Start returns the probe error
// and NO stream, before any turn launches.
func TestExecSession_Error_IncompatibleProbeFailsClosed(t *testing.T) {
	c := newCore(Config{BinaryPath: "/x/codex", Runner: &fakeRunner{lines: []string{"codex-cli 0.1.0"}}})
	s := newExecSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIncompatibleCodex)
	assert.Nil(t, stream)
}

// PAIRED — the exec turn launches under codexEnv() (the no-key subscription env):
// an OPENAI_API_KEY in the parent env must NOT reach the exec subprocess.
func TestExecSession_SubscriptionEnv_NoAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-should-not-be-forwarded")
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: execLines("c1", "ok", 1, 1)}}}
	c := newCore(Config{BinaryPath: "/x/codex", Runner: mr})
	s := newExecSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w"})
	require.NoError(t, err)
	_, _ = drainStream(t, context.Background(), stream)

	require.Len(t, mr.calls, 2)
	_, hasKey := envValue(mr.calls[1].Env, "OPENAI_API_KEY")
	assert.False(t, hasKey, "the exec turn must use codexEnv() — OPENAI_API_KEY must not be forwarded")
}

// --- sendTurnEvent: backpressure (relocated) -------------------------------

func TestSendTurnEvent_Backpressure_BlocksThenDelivers(t *testing.T) {
	events := make(chan turnEvent, 1)
	events <- turnEvent{Kind: turnText, Text: "first"} // buffer now full

	go func() {
		time.Sleep(10 * time.Millisecond)
		<-events
	}()

	ok := sendTurnEvent(context.Background(), events, turnEvent{Kind: turnText, Text: "second"}, slog.Default())
	assert.True(t, ok, "a full buffer must block until drained, not drop the frame")
	assert.Equal(t, "second", (<-events).Text)
}

func TestSendTurnEvent_ConsumerGone_WarnsAndGivesUp(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	events := make(chan turnEvent, 1)
	events <- turnEvent{Kind: turnText, Text: "stuck"} // full, nobody draining

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok := sendTurnEvent(ctx, events, turnEvent{Kind: turnText, Text: "lost"}, logger)
	assert.False(t, ok, "a send to a gone consumer must give up, not block forever")
	assert.Contains(t, buf.String(), "level=WARN")
	assert.Contains(t, buf.String(), "drop")
}

func TestSendTurnEvent_Happy_DeliversImmediately(t *testing.T) {
	events := make(chan turnEvent, 4)
	ok := sendTurnEvent(context.Background(), events, turnEvent{Kind: turnText, Text: "hi"}, slog.Default())
	assert.True(t, ok)
	assert.Equal(t, "hi", (<-events).Text)
}

// --- eventStream (relocated) -----------------------------------------------

func TestEventStream_Happy_DrainsThenDone(t *testing.T) {
	ch := make(chan turnEvent, 3)
	ch <- turnEvent{Kind: turnText, Text: "a"}
	ch <- turnEvent{Kind: turnText, Text: "b"}
	ch <- turnEvent{Kind: turnDone, SessionID: "c1"}
	close(ch)
	s := newEventStream(ch, &terminalError{})

	ev, err := s.Next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "a", ev.Text)
	ev, err = s.Next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "b", ev.Text)
	ev, err = s.Next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, turnDone, ev.Kind)

	_, err = s.Next(context.Background())
	assert.ErrorIs(t, err, errStreamDone)
}

func TestEventStream_Edge_ContextCancelled(t *testing.T) {
	ch := make(chan turnEvent) // never fed
	s := newEventStream(ch, &terminalError{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Next(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestEventStream_Error_CloseErrSurfaces(t *testing.T) {
	transportErr := errors.New("codex exec: stdio closed")
	ch := make(chan turnEvent)
	close(ch)
	term := &terminalError{}
	term.set(transportErr)
	s := newEventStream(ch, term)

	_, err := s.Next(context.Background())
	assert.ErrorIs(t, err, transportErr)
}
