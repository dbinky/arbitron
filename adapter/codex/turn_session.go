package codex

import (
	"context"
	"errors"
)

// errStreamDone is the terminal sentinel a turnStream returns from Next once its
// events are exhausted — the io.EOF analogue for the codex turn transport. The
// caller folds events until Next returns an error that errors.Is(errStreamDone);
// any OTHER error from Next is a real transport/context failure (fail closed —
// the stream never silently stops without a terminal signal).
var errStreamDone = errors.New("codex: turn stream done")

// turnSession is one codex interview turn over `codex exec[ resume]`. It is the
// narrow, codex-shaped transport seam the interactive provider depends on: the
// real impl (execSession) drives the `codex exec` subprocess; the fake replays
// canned frames. It references NO domain/engine type — it is a pure adapter-local
// port, consumed only by the interactive provider, which alone imports engine
// (hexagonal).
//
// SECURITY / fail-closed: the real impl launches under the subscription env
// codexEnv() builds (NO OPENAI_API_KEY, HOME/CODEX_HOME preserved) and gates on
// the memoized version probe, so a missing or incompatible binary fails closed
// before any conversation starts — exactly the posture of the exec path.
type turnSession interface {
	// Start begins a NEW codex conversation (cfg.ResumeID == "") OR resumes an
	// existing one (cfg.ResumeID != ""). It sends the prompt and returns a
	// turnStream the caller folds. A failed launch / broken stream yields a typed
	// error and NO stream (no half-open turn).
	Start(ctx context.Context, cfg turnConfig) (turnStream, error)
}

// turnConfig carries everything one interview turn needs to start: the model,
// the prompt (delivered over stdin — never argv/logs), the working directory, the
// optional resume id, and an optional reasoning-effort hint. It deliberately holds
// value types only (no domain dependency).
//
// SECURITY: Prompt rides stdin (RunSpec.Stdin), never the command line; WorkDir is
// the codex sandbox root and must be set by the provider (an empty cwd would
// inherit the engine launch directory, which holds arbitron.db / config.yaml —
// the same invariant ErrWorkDirRequired guards on the exec path).
type turnConfig struct {
	// Model is the codex model id for this turn.
	Model string
	// Prompt is the turn's user prompt, delivered via stdin (RunSpec.Stdin).
	Prompt string
	// WorkDir is the codex sandbox root / cwd for this turn. Required.
	WorkDir string
	// ResumeID is the codex conversation id to resume; "" starts a new
	// conversation.
	ResumeID string
	// Effort is an optional reasoning-effort hint emitted on EVERY turn as
	// `-c model_reasoning_effort=<e>` (buildInteractiveExecArgs); "" emits nothing.
	// Resume turns re-send it: codex does not carry it from the persisted session.
	Effort string
}

// turnStream yields a turn's assistant-text increments and its terminal outcome.
// It mirrors the existing parsed/foldEvent contract so the Phase-2 provider
// reuses the same fold discipline over a live transport.
type turnStream interface {
	// Next returns the next event: an assistant-text increment (turnText), a
	// terminal success (turnDone, carrying the conversation id + token counts), or
	// a terminal failure (turnFailed, carrying the cause). Once the turn's events
	// are exhausted Next returns errStreamDone. A context cancellation / timeout
	// returns the context error (fail closed — never a silently dropped frame).
	Next(ctx context.Context) (turnEvent, error)
}

// turnEventKind tags a turnEvent. The kinds are mutually exclusive: a turn folds
// to a run of turnText increments terminated by exactly one of turnDone (clean)
// or turnFailed (a failure is NEVER reported as a clean done).
type turnEventKind int

const (
	// turnText is an assistant-text increment. The name deliberately does NOT say
	// "token": codex emits whole agent_message items, not token deltas — each
	// turnText carries one complete message increment, so a reader must not build a
	// per-token delta-streaming model (buffering, partial-word UI) around it.
	turnText turnEventKind = iota
	// turnDone is the terminal success carrying the codex conversation id + token
	// counts.
	turnDone
	// turnFailed is the terminal failure (turn.failed / error) carrying the cause.
	turnFailed
)

// turnEvent is the small tagged event a turnStream yields. Which fields are
// populated is driven by Kind: Text for turnText; SessionID/TokensIn/TokensOut
// for turnDone; Err for turnFailed.
type turnEvent struct {
	// Kind tags which event this is.
	Kind turnEventKind
	// Text is the assistant-text increment (turnText).
	Text string
	// SessionID is the codex conversation id (turnDone) — surfaced for the trace
	// and for a later resume.
	SessionID string
	// TokensIn / TokensOut are the best-effort token counts (turnDone).
	TokensIn  int
	TokensOut int
	// Err is the failure cause (turnFailed).
	Err error
}
