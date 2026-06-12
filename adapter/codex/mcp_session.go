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

// mcpSession is one codex mcp-server conversation turn over stdio. It is the
// narrow, codex-shaped transport seam the interactive provider depends on: the
// real impl (Task 1.2) wraps the mcp-go stdio client driving `codex mcp-server`;
// the fake replays canned frames. It references NO domain/engine type — it is a
// pure adapter-local port, consumed only by the Phase-2 provider, which alone
// imports engine (hexagonal).
//
// SECURITY / fail-closed: the real impl launches under the subscription env
// codexEnv() builds (NO OPENAI_API_KEY, HOME/CODEX_HOME preserved) and gates on
// the memoized version probe, so a missing or incompatible binary fails closed
// before any conversation starts — exactly the posture of the exec path.
type mcpSession interface {
	// Start begins a NEW codex conversation (cfg.ResumeID == "") OR resumes an
	// existing one (cfg.ResumeID != ""). It sends the prompt and returns a
	// turnStream the caller folds. A failed launch / broken stdio yields a typed
	// error and NO stream (no half-open turn).
	Start(ctx context.Context, cfg turnConfig) (turnStream, error)
}

// turnConfig carries everything one interview turn needs to start: the model,
// the prompt (delivered over the protocol — never argv/logs), the working
// directory, the optional resume id, the provider's approval policy, and an
// optional reasoning-effort hint. It deliberately holds value types only (no
// domain dependency).
//
// SECURITY: Prompt rides the protocol body, never the command line; WorkDir is
// the codex sandbox root and must be set by the provider (an empty cwd would
// inherit the engine launch directory, which holds arbitron.db / config.yaml —
// the same invariant ErrWorkDirRequired guards on the exec path).
type turnConfig struct {
	// Model is the codex model id for this turn.
	Model string
	// Prompt is the turn's user prompt, delivered over the protocol body.
	Prompt string
	// WorkDir is the codex sandbox root / cwd for this turn. Required.
	WorkDir string
	// ResumeID is the codex conversation id to resume; "" starts a new
	// conversation.
	ResumeID string
	// Approver is the provider's auto-policy, invoked for each server→client
	// approval request. The transport surfaces EVERY request to it and never
	// auto-allows internally, so the guardrail is the provider's, not the
	// transport's.
	Approver approvalFunc
	// Effort is an optional reasoning-effort hint, applied only if the codex
	// surface accepts it; "" applies nothing.
	Effort string
}

// turnStream yields a turn's assistant-text increments and its terminal outcome.
// It mirrors the existing parsed/foldEvent contract so the Phase-2 provider
// reuses the same fold discipline over a live transport.
type turnStream interface {
	// Next returns the next event: an assistant-text increment (turnToken), a
	// terminal success (turnDone, carrying the conversation id + token counts), or
	// a terminal failure (turnFailed, carrying the cause). Once the turn's events
	// are exhausted Next returns errStreamDone. A context cancellation / timeout
	// returns the context error (fail closed — never a silently dropped frame).
	Next(ctx context.Context) (turnEvent, error)
}

// turnEventKind tags a turnEvent. The kinds are mutually exclusive: a turn folds
// to a run of turnToken increments terminated by exactly one of turnDone (clean)
// or turnFailed (a failure is NEVER reported as a clean done).
type turnEventKind int

const (
	// turnToken is an assistant-text increment (codex emits whole agent_message
	// items, not token deltas; each is one turnToken).
	turnToken turnEventKind = iota
	// turnDone is the terminal success carrying the codex conversation id + token
	// counts.
	turnDone
	// turnFailed is the terminal failure (turn.failed / error) carrying the cause.
	turnFailed
)

// turnEvent is the small tagged event a turnStream yields. Which fields are
// populated is driven by Kind: Text for turnToken; SessionID/TokensIn/TokensOut
// for turnDone; Err for turnFailed.
type turnEvent struct {
	// Kind tags which event this is.
	Kind turnEventKind
	// Text is the assistant-text increment (turnToken).
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

// approvalRequest is the structured payload codex sends when it asks the client
// to approve a mutation. Exactly one of Command / WritePaths describes the
// action: Command holds the argv of a command-exec request (WritePaths nil); a
// patch/write request holds the target WritePaths (Command nil). Reason is
// codex's stated justification, recorded to the trace.
type approvalRequest struct {
	// Command is the argv for a command-exec approval request (nil for a patch).
	Command []string
	// WritePaths are the target paths for a patch/write approval request (nil for
	// an exec).
	WritePaths []string
	// Reason is codex's stated reason for the request (recorded to the trace).
	Reason string
}

// approvalDecision is the client's verdict on an approvalRequest. Reason is
// recorded to the trace alongside the request so an operator can audit why the
// policy allowed or denied a given mutation.
type approvalDecision struct {
	// Allow is the verdict: true permits the requested action, false denies it.
	Allow bool
	// Reason is the policy's justification, recorded to the trace.
	Reason string
}

// approvalFunc is the provider's auto-policy, called by the transport for every
// server→client approval request codex raises (command exec / patch apply); it
// returns allow/deny. Phase 1 defines the SHAPE; Phase 2 supplies the policy.
//
// SECURITY / fail-closed: the transport must surface EVERY request to this
// function and never auto-allow internally — the mutation guardrail belongs to
// the provider's policy, not to the transport.
type approvalFunc func(req approvalRequest) approvalDecision
