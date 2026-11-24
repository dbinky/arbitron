package codex

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real exec session must satisfy the port — a rename of either side breaks
// the build here.
var _ turnSession = (*execSession)(nil)

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
// --skip-git-repo-check -c sandbox_mode="workspace-write" -c writable_roots=["<wd>"]
// -c network_access=false -m <model> -`; the "-" makes codex read the prompt from
// stdin. Resume rejects -s and --cd (VERIFIED), so the sandbox MODE rides -c too.
//
// WHY EVERYTHING IS RE-SENT (verified 2026-09-29, codex-cli 0.159.0): a resumed
// turn does NOT inherit the session's model, effort or sandbox mode — it falls back
// to ~/.codex/config.toml. A feature-pipeline brainstorm-spec session started on
// gpt-6.1-sol/workspace-write ran all 27 resumed turns on the config's gpt-6-sol in
// the default READ-ONLY sandbox, and could not write the spec its human approved.
//
// SECURITY (Must Fix #1): resume re-asserts the SAME posture as a fresh turn.
// Resume confines writes against the PROCESS cwd (RunSpec.Dir == WorkDir — resume
// rejects --cd), so the resume-cwd invariant is load-bearing for containment.
func TestBuildInteractiveExecArgs_Success_Resume(t *testing.T) {
	args, err := buildInteractiveExecArgs(turnConfig{Model: "gpt-5", Prompt: "continue", WorkDir: "/work", ResumeID: "conv-abc"})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"exec", "resume", "conv-abc",
		"--json", "--skip-git-repo-check",
		"-c", `sandbox_mode="workspace-write"`,
		"-c", `sandbox_workspace_write.writable_roots=["/work"]`,
		"-c", "sandbox_workspace_write.network_access=false",
		"-m", "gpt-5",
		"-",
	}, args)
	assert.NotContains(t, args, "-s", "resume must NOT send -s (codex exec resume rejects it); the mode rides -c")
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

// FAILURE — a resume turn re-sends the pinned model (-m) and an empty model fails
// closed: a pinned run must never continue on whatever model the persisted session
// recorded, so there is no "carry from the session" default.
func TestBuildInteractiveExecArgs_Failure_ResumeRequiresModel(t *testing.T) {
	for _, m := range []string{"", "  "} {
		_, err := buildInteractiveExecArgs(turnConfig{Model: m, Prompt: "p", WorkDir: "/w", ResumeID: "conv-1"})
		assert.ErrorIs(t, err, ErrModelRequired, "resume with model %q must fail closed", m)
	}
}

// HYGIENE (Consider #3) — a resume turn with a blank/whitespace ResumeID fails
// closed before the malformed id reaches argv (a non-blank id passes verbatim).
func TestBuildInteractiveExecArgs_Hygiene_BlankResumeIDFailsClosed(t *testing.T) {
	for _, id := range []string{" ", "\t", "   \n"} {
		_, err := buildInteractiveExecArgs(turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w", ResumeID: id})
		assert.ErrorIs(t, err, ErrMalformedResumeID, "blank ResumeID %q must fail closed", id)
	}
	// A non-blank id is accepted and passed through verbatim.
	args, err := buildInteractiveExecArgs(turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w", ResumeID: "conv-abc"})
	require.NoError(t, err)
	assert.Equal(t, "conv-abc", args[2])
}

// CONTRACT — a resume turn re-sends the profile's effort. codex does NOT carry it
// from the persisted session (verified 2026-09-29: a session started at high effort
// resumed at the config's medium), so dropping it silently changes the effort.
func TestBuildInteractiveExecArgs_Contract_ResumeReassertsEffort(t *testing.T) {
	args, err := buildInteractiveExecArgs(turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w", ResumeID: "conv-1", Effort: "high"})
	require.NoError(t, err)
	assert.Contains(t, args, codexEffortConfigKey+"=high", "resume must re-send the profile's effort")
	assert.Equal(t, "-", args[len(args)-1], "the stdin placeholder must stay last")
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
		Model: "gpt-5", Prompt: "more", WorkDir: "/work/instance-A", ResumeID: "conv-1",
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
		`{"type":"turn.completed","usage":{"input_tokens":` + strconv.Itoa(in) + `,"output_tokens":` + strconv.Itoa(out) + `}}`,
	}
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
		"-c", `sandbox_mode="workspace-write"`,
		"-c", `sandbox_workspace_write.writable_roots=["/w"]`,
		"-c", "sandbox_workspace_write.network_access=false",
		"-m", "gpt-5",
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
	s := newEventStream(ch)

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
	s := newEventStream(ch)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Next(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

// EDGE — a closed (drained) channel reports the clean errStreamDone sentinel: the
// exec transport surfaces faults in-band as turnFailed events, so a channel close
// is always a clean done (there is no out-of-band transport error to record).
func TestEventStream_Edge_CloseReportsStreamDone(t *testing.T) {
	ch := make(chan turnEvent)
	close(ch)
	s := newEventStream(ch)

	_, err := s.Next(context.Background())
	assert.ErrorIs(t, err, errStreamDone)
}

// runFold drives foldExecStream over a canned stdout stream and returns the
// terminal event it emitted (the last one on the channel).
func runFold(t *testing.T, ctx context.Context, lines []string, waitErr error) turnEvent {
	t.Helper()
	in := make(chan string)
	go func() {
		defer close(in)
		for _, l := range lines {
			in <- l
		}
	}()
	events := make(chan turnEvent, len(lines)+4)
	foldExecStream(ctx, in, func() error { return waitErr }, events, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil, nil)
	var last turnEvent
	got := false
	for ev := range events {
		last, got = ev, true
	}
	require.True(t, got, "foldExecStream must emit a terminal event")
	return last
}

// HAPPY (the bug, interactive path) — a non-zero exit whose stdout carried a
// `turn.failed` must surface that message. The interactive path had the SAME
// inverted precedence as the batch path: the exit branch returned first, so the
// folded cause was never reported.
func TestFoldExecStream_NonZeroExitSurfacesFoldedCause(t *testing.T) {
	last := runFold(t, context.Background(), []string{
		`{"type":"thread.started","thread_id":"thr-limited"}`,
		`{"type":"turn.failed","error":{"message":"You've hit your usage limit."}}`,
	}, errors.New("exit status 1"))

	require.Equal(t, turnFailed, last.Kind)
	require.Error(t, last.Err)
	assert.Contains(t, last.Err.Error(), "You've hit your usage limit.",
		"the folded turn.failed cause must reach the consumer, not just the exit status")
	assert.Contains(t, last.Err.Error(), "exit status 1", "the process exit must stay visible")
}

// FAILURE — a non-zero exit with no folded cause still reports the bare exit
// error (unchanged behaviour).
func TestFoldExecStream_NonZeroExitWithoutCauseStaysBare(t *testing.T) {
	last := runFold(t, context.Background(), []string{
		`{"type":"item.completed","item":{"type":"agent_message","text":"Done."}}`,
	}, errors.New("exit status 1"))

	require.Equal(t, turnFailed, last.Kind)
	require.Error(t, last.Err)
	assert.Contains(t, last.Err.Error(), "exit status 1")
	assert.NotContains(t, last.Err.Error(), "Done.", "assistant text is not a failure cause")
}

// SUCCESS — a clean exit with a folded failure still reports the folded cause
// (unchanged behaviour), and a clean exit with no failure is a clean turnDone.
func TestFoldExecStream_CleanExitKeepsExistingOutcomes(t *testing.T) {
	failedTurn := runFold(t, context.Background(), []string{
		`{"type":"turn.failed","error":{"message":"model refused"}}`,
	}, nil)
	require.Equal(t, turnFailed, failedTurn.Kind)
	assert.Contains(t, failedTurn.Err.Error(), "model refused")

	okTurn := runFold(t, context.Background(), []string{
		`{"type":"thread.started","thread_id":"thr-1"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Done."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":7,"output_tokens":3}}`,
	}, nil)
	require.Equal(t, turnDone, okTurn.Kind)
	assert.Equal(t, "thr-1", okTurn.SessionID)
	assert.Equal(t, 7, okTurn.TokensIn)
}

// ERROR — a malformed line still yields exactly one terminal parse failure and
// the exit error does not add a second (unchanged fail-closed behaviour).
func TestFoldExecStream_MalformedLineStillFailsClosed(t *testing.T) {
	last := runFold(t, context.Background(), []string{`{"type":"item.completed", BROKEN`}, errors.New("exit status 1"))
	require.Equal(t, turnFailed, last.Kind)
	assert.ErrorIs(t, last.Err, ErrParse)
}

// EDGE — a cancelled ctx must NOT emit a spurious turnFailed for the process the
// deadline just killed, even when stdout carried a cause. The consumer learns of
// the cancel via ctx.Done, so surfacing the cause here would report the kill as
// the turn's own failure. Recovering the cause on the exit path must not breach
// this guard.
// The guard covers the EXIT branch only: a turn that genuinely folded a
// turn.failed before the cancel still reports it (that is the turn's own
// failure, not an artifact of the kill).
func TestFoldExecStream_CancelledCtxEmitsNoTurnFailedForTheExitCode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// No failure event on stdout — the ONLY failure signal is the killed
	// process's exit code, which the guard must not report as a turn failure.
	last := runFold(t, ctx, []string{
		`{"type":"item.completed","item":{"type":"agent_message","text":"Done."}}`,
	}, errors.New("signal: killed"))

	assert.NotEqual(t, turnFailed, last.Kind,
		"a cancelled ctx must not report the killed process's exit code as the turn's failure")
}

// A D10 restricted probe runs under codex's
// read-only sandbox with no writable root, fresh or resumed, and keeps egress off.
func TestBuildInteractiveExecArgs_ReadOnlyTurnGrantsNoWritableRoot(t *testing.T) {
	fresh, err := buildInteractiveExecArgs(turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/work", ReadOnly: true})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"exec", "--json", "--skip-git-repo-check",
		"-s", "read-only",
		"--strict-config", "-c", "features.plugins=false", "-c", "features.apps=false",
		"-c", `web_search="disabled"`, "-c", "sandbox_workspace_write.network_access=false",
		"-m", "gpt-5", "--cd", "/work",
	}, fresh)

	resume, err := buildInteractiveExecArgs(turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/work", ResumeID: "conv-abc", ReadOnly: true})
	require.NoError(t, err)
	joined := strings.Join(resume, " ")
	assert.Contains(t, joined, `sandbox_mode="read-only"`)
	assert.NotContains(t, joined, "workspace-write")
	assert.NotContains(t, joined, "writable_roots")
}

func TestBuildInteractiveExecArgs_RestrictedTurnReassertsPostureOnResume(t *testing.T) {
	pol, err := codexToolPolicy([]string{"Read", "Bash"})
	require.NoError(t, err)
	cfg := turnConfig{Model: "gpt-5", WorkDir: "/work", Tools: pol, MCPOverride: `mcp_servers={"a"={enabled=false}}`,
		ContextArgs: []string{"-c", "model_context_window=200000"}}
	fresh, err := buildInteractiveExecArgs(cfg)
	require.NoError(t, err)
	assert.Contains(t, fresh, "read-only")
	cfg.ResumeID = "conv-1"
	resume, err := buildInteractiveExecArgs(cfg)
	require.NoError(t, err)
	assert.Contains(t, resume, `sandbox_mode="read-only"`)
	for _, args := range [][]string{fresh, resume} {
		for _, want := range []string{"--strict-config", "features.plugins=false", "features.apps=false", `web_search="disabled"`,
			`mcp_servers={"a"={enabled=false}}`, "model_context_window=200000"} {
			assert.Contains(t, args, want)
		}
	}
}
