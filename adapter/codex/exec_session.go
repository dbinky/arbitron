package codex

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// exec-subcommand vocabulary for the interactive path (VERIFIED against
// codex-cli 0.139 by driving live `codex exec` / `codex exec resume` turns).
const (
	execResumeSubcommand = "resume"
	// execSandboxFlag + execSandboxWorkspaceWrite confine every write to the cwd
	// worktree (codex's own sandbox is the containment; no approval loop).
	execSandboxFlag           = "-s"
	execSandboxWorkspaceWrite = "workspace-write"
	// execSkipGitRepoCheckFlag lets codex run non-interactively inside the isolated
	// arbitron-created git worktree without a trust prompt (exec cannot answer one).
	execSkipGitRepoCheckFlag = "--skip-git-repo-check"
	// execStdinPromptArg is the "-" placeholder that makes `codex exec resume` read
	// the prompt from stdin (so a resume prompt stays off argv, like a fresh turn).
	execStdinPromptArg = "-"
	// execConfigFlag is codex's `-c key=value` config override. It is accepted by
	// BOTH `codex exec` AND `codex exec resume` (unlike -s, which resume rejects),
	// so it is the one mechanism that can re-assert the sandbox posture on resume.
	execConfigFlag = "-c"
	// execWritableRootsKeyPrefix pins codex's workspace-write writable roots to a
	// single root (the WorkDir). SECURITY: a bare `-s workspace-write` was VERIFIED
	// against codex-cli 0.139 to also permit writes to the cwd PARENT chain and
	// /tmp; under the production layout (<base>/instances/<id>/) that lets one turn
	// write into a SIBLING instance's workspace. Pinning writable_roots to ONLY the
	// WorkDir closes that cross-worktree write escape. (Reads remain unconfined —
	// see codex-backend-spec.md; the worktree must hold no secrets.)
	execWritableRootsKeyPrefix = "sandbox_workspace_write.writable_roots="
	// execNetworkOff pins workspace-write network egress off. SECURITY: without it,
	// a prompt-injected interview turn could exfiltrate the worktree (or any
	// readable host file) over the network; pinning it off removes that channel.
	execNetworkOff = "sandbox_workspace_write.network_access=false"
)

// execSession is the production mcpSession for the codex INTERACTIVE path. Despite
// the historical port name (mcpSession), it drives `codex exec --json` (a fresh
// conversation) and `codex exec resume <id> --json` (a turn-by-turn resume), NOT
// `codex mcp-server`.
//
// exec is the correct transport now that the interactive posture is workspace-write
// with no approval/elicitation loop: `codex exec resume` resumes a conversation
// from its ON-DISK rollout, so the engine's spawn-per-turn model (a fresh process
// per human turn, the conversation id round-tripped through the engine/DB) works
// across server restarts and long WAITs — exactly like `claude --resume`. The
// mcp-server `codex-reply` tool, by contrast, resumes only conversations live in
// the SAME process ("Session not found for thread_id" cross-process), which
// spawn-per-turn can never satisfy.
//
// Spawn-per-turn: each Start launches a fresh `codex exec[ resume]` subprocess,
// folds its JSONL event stream (parse.go's verified decodeLine + foldEvent) into
// turnEvents, and reaps the child when the stream is exhausted or ctx is cancelled.
//
// SECURITY / fail-closed: it launches under codexEnv() (NO OPENAI_API_KEY,
// HOME/CODEX_HOME preserved), gates on the shared memoized version probe before any
// turn, delivers the prompt via STDIN (never argv), and confines writes to the
// WorkDir worktree via -s workspace-write. An empty WorkDir or model fails closed
// with zero events.
type execSession struct {
	core *core
}

var _ mcpSession = (*execSession)(nil)

// newExecSession builds a session bound to the shared core (resolved binary,
// memoized probe, timeout, response cap). It is the single construction seam for
// the real interactive transport; the provider injects the core.
func newExecSession(c *core) *execSession { return &execSession{core: c} }

// Start launches one codex exec turn (fresh when cfg.ResumeID == "", a
// `codex exec resume` otherwise) and returns a turnStream the caller folds. The
// prompt rides stdin (never argv). A failed WorkDir guard, probe, arg build, or
// launch yields a typed error and NO stream (no half-open turn).
func (s *execSession) Start(ctx context.Context, cfg turnConfig) (turnStream, error) {
	// WorkDir guard FIRST: an empty cwd would inherit the engine launch directory
	// (arbitron.db / config.yaml). Same invariant the exec/agent path enforces.
	if cfg.WorkDir == "" {
		return nil, ErrWorkDirRequired
	}

	// Version probe (memoized, shared with the batch/exec paths): a missing or
	// incompatible binary fails closed here, before any turn launches.
	if err := s.core.ensureCompatible(ctx); err != nil {
		return nil, err
	}

	args, err := buildInteractiveExecArgs(cfg)
	if err != nil {
		return nil, err
	}

	spec := claudecli.RunSpec{
		Bin:              s.core.binPath,
		Args:             args,
		Stdin:            cfg.Prompt, // prompt via stdin — never argv (SECURITY)
		Dir:              cfg.WorkDir,
		Env:              codexEnv(), // NO OPENAI_API_KEY; HOME/CODEX_HOME preserved
		MaxResponseBytes: s.core.maxResponseBytes,
	}

	lines, wait, err := s.core.runner.Run(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("codex exec: stream call: %w", err)
	}

	// events buffers folded turn events; the fold goroutine feeds it, the returned
	// stream drains it. Buffered so a normal burst does not stall the reader; when
	// the buffer fills, sendTurnEvent applies real backpressure rather than dropping
	// transcript content (it only gives up, with a slog.Warn, once ctx is done).
	events := make(chan turnEvent, 64)
	go foldExecStream(ctx, lines, wait, events, slog.Default())
	return newEventStream(events, &terminalError{}), nil
}

// buildInteractiveExecArgs assembles the `codex exec[ resume]` argv for one
// interactive turn (verified against codex-cli 0.139). The prompt is NEVER an arg —
// it rides stdin (RunSpec.Stdin): a fresh turn supplies no positional prompt (codex
// reads stdin), and a resume turn supplies the "-" placeholder that makes codex read
// the prompt from stdin.
//
//	fresh:  codex exec --json --skip-git-repo-check -s workspace-write -c writable_roots=["<wd>"] -c network_access=false -m <model> --cd <workdir> [-c model_reasoning_effort=<e>]
//	resume: codex exec resume <id> --json --skip-git-repo-check -c writable_roots=["<wd>"] -c network_access=false -
//
// SECURITY (Must Fix #1): the approval subsystem is deleted, so the sandbox is the
// SOLE containment. A bare `-s workspace-write` was VERIFIED against codex-cli 0.139
// NOT to confine writes to the worktree (writes to the cwd parent chain and /tmp
// succeed; reads are fully unconfined). So BOTH paths pin writable_roots to ONLY the
// WorkDir and pin network egress off, via -c overrides. Resume re-asserts the SAME
// posture rather than trusting the persisted session to carry it: `codex exec resume`
// rejects -s and --cd (VERIFIED) but ACCEPTS -c.
//
// RESUME-CWD INVARIANT (Should Fix #1): resume sends neither --cd (rejected) nor the
// writable_roots-defining cwd as anything but the PROCESS cwd, so the resume process
// cwd (RunSpec.Dir == cfg.WorkDir) MUST equal the originating turn's WorkDir — codex
// resolves both the on-disk rollout AND the workspace-write sandbox against it. The
// engine round-trips the same WorkDir per instance, so this holds; Start passes
// cfg.WorkDir as RunSpec.Dir on every turn (tested), making the invariant explicit.
//
// Resume carries the model from the persisted on-disk session, so it re-sends none
// (`codex exec resume` needs no -m). A fresh turn with an empty model fails closed
// (ErrModelRequired) — never a default-model run. Effort is emitted only on the
// FRESH path: it is immutable across a conversation (codex carries it from the
// persisted session on resume), so a resume turn that sets Effort intentionally
// drops it.
func buildInteractiveExecArgs(cfg turnConfig) ([]string, error) {
	posture := []string{
		execConfigFlag, execWritableRootsKeyPrefix + writableRootsValue(cfg.WorkDir),
		execConfigFlag, execNetworkOff,
	}
	if cfg.ResumeID != "" {
		args := []string{
			execSubcommand, execResumeSubcommand, cfg.ResumeID,
			jsonFlag, execSkipGitRepoCheckFlag,
		}
		args = append(args, posture...)
		return append(args, execStdinPromptArg), nil
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, ErrModelRequired
	}
	args := []string{
		execSubcommand, jsonFlag, execSkipGitRepoCheckFlag,
		execSandboxFlag, execSandboxWorkspaceWrite,
	}
	args = append(args, posture...)
	args = append(args, modelFlag, model, cdFlag, cfg.WorkDir)
	if cfg.Effort != "" {
		args = append(args, execConfigFlag, codexEffortConfigKey+"="+cfg.Effort)
	}
	return args, nil
}

// writableRootsValue renders the codex `-c sandbox_workspace_write.writable_roots`
// value: a TOML array with the single WorkDir root. codex parses the -c value as
// TOML, so the root is a quoted TOML string. WorkDir is an arbitron-allocated
// absolute path (no embedded quotes in practice); a defensive backslash-escape keeps
// the TOML well-formed regardless.
func writableRootsValue(workDir string) string {
	escaped := strings.ReplaceAll(workDir, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `["` + escaped + `"]`
}

// foldExecStream folds the `codex exec --json` JSONL line stream into turnEvents on
// the events channel (drained by the eventStream), reusing parse.go's VERIFIED
// decodeLine + foldEvent. Each new agent_message item's text becomes a turnText; the
// terminal outcome is exactly one turnDone (thread id + best-effort tokens) or one
// turnFailed (a malformed line, a folded error/turn.failed event, or a non-zero
// exit) — a failure is NEVER a silent clean done. It ALWAYS drains `lines` and calls
// wait() so the subprocess is reaped, then closes events so the stream reaches its
// terminal signal.
func foldExecStream(ctx context.Context, lines <-chan string, wait func() error, events chan<- turnEvent, logger *slog.Logger) {
	defer close(events)

	acc := &parsed{}
	var failed bool
	for line := range lines {
		if failed {
			continue // keep draining so the subprocess can be reaped
		}
		ev, ok, derr := decodeLine(line)
		if derr != nil {
			sendTurnEvent(ctx, events, turnEvent{Kind: turnFailed, Err: fmt.Errorf("codex exec: %w", derr)}, logger)
			failed = true
			continue
		}
		if !ok {
			continue
		}
		if newText := foldEvent(acc, ev); newText != "" {
			sendTurnEvent(ctx, events, turnEvent{Kind: turnText, Text: newText}, logger)
		}
	}

	waitErr := wait()
	if failed {
		return // a terminal turnFailed was already emitted
	}
	// A ctx cancel/timeout surfaces to the consumer via the stream's Next (ctx.Done);
	// don't also emit a spurious turnFailed for the process the deadline just killed.
	if waitErr != nil && ctx.Err() == nil {
		sendTurnEvent(ctx, events, turnEvent{Kind: turnFailed, Err: fmt.Errorf("codex exec: exited with error: %w", waitErr)}, logger)
		return
	}
	if acc.failed {
		sendTurnEvent(ctx, events, turnEvent{Kind: turnFailed, Err: fmt.Errorf("codex exec: turn failed: %s", acc.summary)}, logger)
		return
	}
	sendTurnEvent(ctx, events, turnEvent{
		Kind:      turnDone,
		SessionID: acc.sessionID,
		TokensIn:  acc.tokensIn,
		TokensOut: acc.tokensOut,
	}, logger)
}

// ---------------------------------------------------------------------------
// Turn-stream plumbing shared by the interactive transport. (Relocated from the
// removed mcp_stdio_session.go; the eventStream is pure and unit-tested with an
// in-memory channel.)
// ---------------------------------------------------------------------------

// sendTurnEvent delivers one folded turn event to the buffered events channel with
// real backpressure: a non-blocking send first (the common, free-buffer path), and
// on a full buffer it BLOCKS the producer (the fold goroutine) until a slot frees —
// slowing the producer rather than discarding transcript content — or the turn ctx
// is done. It returns true when the frame was delivered, false on a true drop.
//
// A drop happens ONLY when ctx is cancelled/timed-out (the consumer has torn down
// and nothing will ever drain again); blocking past that point would leak the
// goroutine. That genuine drop is logged at WARN so the lost frame is observable
// (the transcript that becomes the deliverable and audit record must not lose
// content silently).
func sendTurnEvent(ctx context.Context, events chan<- turnEvent, ev turnEvent, logger *slog.Logger) bool {
	select {
	case events <- ev:
		return true
	default:
	}
	select {
	case events <- ev:
		return true
	case <-ctx.Done():
		logger.Warn("codex interactive: dropping turn event under backpressure after context done",
			"kind", ev.Kind,
			"err", ctx.Err(),
		)
		return false
	}
}

// terminalError holds a turn's terminal transport error, written before the events
// channel closes and read only after the close — so the channel close establishes
// the happens-before edge and no mutex is needed. A nil value means the stream
// drained to a clean errStreamDone (the exec path's turnFailed events carry turn
// faults; term is reserved for an out-of-band transport break).
type terminalError struct {
	err error
}

func (t *terminalError) set(err error) { t.err = err }

// get returns the recorded terminal error, or the clean errStreamDone sentinel.
func (t *terminalError) get() error {
	if t != nil && t.err != nil {
		return t.err
	}
	return errStreamDone
}

// eventStream is the turnStream backing the real session: it drains the buffered
// turnEvent channel the fold goroutine feeds, honours ctx cancellation, and reports
// the terminal errStreamDone (or a recorded transport error) once the channel
// closes. The thin subprocess glue (Start) wires this to the live run; the stream
// itself is pure.
type eventStream struct {
	events <-chan turnEvent
	term   *terminalError
	// done caches the terminal error once the channel has drained, so repeated Next
	// calls keep returning it rather than reading a closed channel as a zero event.
	done error
}

// newEventStream builds a stream over the given event channel. term carries any
// terminal transport error (set before the channel closes); a nil term, or a nil
// recorded error, reports the clean errStreamDone sentinel at drain time.
func newEventStream(events <-chan turnEvent, term *terminalError) *eventStream {
	return &eventStream{events: events, term: term}
}

// Next returns the next buffered event, the terminal errStreamDone once the channel
// closes (or a recorded transport error), or the context error on
// cancellation/timeout — never a silently dropped frame.
func (s *eventStream) Next(ctx context.Context) (turnEvent, error) {
	if s.done != nil {
		return turnEvent{}, s.done
	}
	select {
	case <-ctx.Done():
		return turnEvent{}, ctx.Err()
	case ev, ok := <-s.events:
		if !ok {
			s.done = s.term.get()
			return turnEvent{}, s.done
		}
		return ev, nil
	}
}
