package codex

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// InteractiveConfig configures an InteractiveProvider — the conversational codex
// backend that runs each interactive turn over `codex exec --json` (fresh) /
// `codex exec resume --json` (resume) via the mcpSession seam. It is the
// interactive sibling of codex.Config: it shares the same construction inputs
// (binding, binary, per-turn timeout, capability rules, response cap), and it owns
// NO Runner (the mcpSession factory wraps the subprocess runner seam — real
// codex exec in prod, a fake in tests).
//
// SECURITY: like the exec Config there is no per-provider credential — codex is
// subscription-only (env.go), so Binding.RevealKey is never consulted and no API
// key ever reaches env or argv.
type InteractiveConfig struct {
	// Binding is the resolved backend selection. Only Binding.Model / Backend /
	// Effort are consumed (the codex path is subscription-only, so RevealKey /
	// BaseURL / provider-keyed env are deliberately NOT used).
	Binding provider.ResolvedBinding
	// BinaryPath is the codex binary; "" resolves from PATH / StandardBinDirs.
	BinaryPath string
	// Timeout is the default per-turn timeout; <=0 means no default (the turn is
	// governed solely by the engine ctx). It is wired through procenv.WithTimeout
	// in startTurn, so a wedged turn that never emits a terminal frame is bounded.
	Timeout time.Duration
	// CapabilityRules drives the per-turn effort emission: the binding's resolved
	// Effort is floored and applied as turnConfig.Effort via the SAME pipeline the
	// exec path uses, so interactive and exec never disagree on the effective
	// value. A zero-value (Empty) ruleset emits nothing.
	CapabilityRules provider.CapabilityRules
	// MaxResponseBytes caps per-turn `codex exec` stdout (0 means no cap; production
	// wires cfg.LLM.MaxResponseBytes). The exec stream is a real stdout pipe, so
	// this bounds a runaway turn alongside the per-turn deadline.
	MaxResponseBytes int64
}

// InteractiveProvider implements engine.ChatProvider (Chat + StreamChat) by
// running one codex interview turn over the mcpSession seam, folding the turn
// stream into ChatChunks, and round-tripping the conversation id for resume.
//
// It holds NO in-memory session state between turns (spawn-per-turn): the
// terminal ChatChunk.SessionID carries codex's conversation id, the engine
// persists it, and a later req.SessionID flows back as the turn's ResumeID — so
// resume survives WAITING, server restarts, and long human delays.
//
// SECURITY / fail-closed: it shares the exec adapters' core (binding, binary
// resolution, memoized version probe), so a missing/incompatible binary or an
// empty WorkDir fails closed with zero events; the prompt rides stdin (never
// argv); and no OPENAI_API_KEY ever enters env/argv. The turn runs under codex's
// workspace-write sandbox (confined to the WorkDir worktree — see
// buildInteractiveExecArgs), so codex's own sandbox is the write containment;
// there is no per-mutation approval loop. A failed turn or transport break yields
// a terminal Error chunk and NEVER a clean Done.
type InteractiveProvider struct {
	core
	// newSession builds one mcpSession per turn (real execSession in prod, a fake
	// in tests). It mirrors codex.Config.Runner's subprocess injection seam.
	newSession func() mcpSession
}

var _ engine.ChatProvider = (*InteractiveProvider)(nil)

// NewInteractiveProvider constructs an InteractiveProvider from cfg. It builds
// the shared core via newCore exactly as the exec adapters do (binding/binary
// resolution + the memoized version probe), so construction and compatibility
// cannot drift. A nil session factory defaults to the real execSession built from
// the core — the composition root (Phase 3) calls this.
func NewInteractiveProvider(cfg InteractiveConfig) *InteractiveProvider {
	// Build the provider with the shared core inlined into the literal (a move, not
	// a copy of the lock-bearing core — see the exec adapters' &Provider{core:
	// newCore(cfg)} precedent). The default session factory is set AFTER so it can
	// capture &p.core, sharing the memoized version probe with construction.
	p := &InteractiveProvider{
		core: newCore(interactiveCoreConfig(cfg)),
	}
	p.newSession = func() mcpSession { return newExecSession(&p.core) }
	return p
}

// newInteractiveProviderWithSession is the in-package test seam: it injects a
// mcpSession factory (the fake transport) instead of the real stdio session,
// mirroring how codex.Config.Runner accepts a fake runner. External callers use
// NewInteractiveProvider and get the real session.
func newInteractiveProviderWithSession(cfg InteractiveConfig, newSession func() mcpSession) *InteractiveProvider {
	return &InteractiveProvider{
		core:       newCore(interactiveCoreConfig(cfg)),
		newSession: newSession,
	}
}

// interactiveCoreConfig projects an InteractiveConfig onto the exec adapters'
// Config so the shared core is built by the SAME newCore path — the interactive
// provider owns no Runner (the mcpSession factory replaces that seam), so it is
// left at its zero value (nil → the production os/exec runner, used by the version
// probe AND the `codex exec` turns). MaxResponseBytes IS wired through (the exec
// stream is a real stdout pipe that a runaway turn could flood).
func interactiveCoreConfig(cfg InteractiveConfig) Config {
	return Config{
		Binding:          cfg.Binding,
		BinaryPath:       cfg.BinaryPath,
		Timeout:          cfg.Timeout,
		CapabilityRules:  cfg.CapabilityRules,
		MaxResponseBytes: cfg.MaxResponseBytes,
	}
}

// startTurn performs the shared fail-closed prelude for both Chat and StreamChat:
// it guards an empty WorkDir, resolves the model (req.Model overrides the binding
// model) and the floored effort, derives the per-turn deadline, and starts ONE
// codex turn over a fresh mcpSession. An empty WorkDir, empty model, or a failed
// Start yields NO stream (no half-open turn) — the error is returned before any
// events flow.
//
// It returns the per-turn context (which the caller MUST use to fold the stream so
// the deadline reaches Next) and its CancelFunc (which the caller MUST call when
// the turn ends, releasing the deadline timer + reaping the transport child). The
// timeout wrap honours p.timeout via procenv.WithTimeout — the SAME per-call
// deadline plumbing the exec path uses — so a wedged turn (no terminal frame) is
// killed by the deadline instead of parking the node in WAITING forever. A
// non-positive timeout yields the parent ctx unchanged (no deadline), so the turn
// is governed solely by the engine ctx, exactly as before.
func (p *InteractiveProvider) startTurn(ctx context.Context, req engine.ChatRequest) (turnStream, context.Context, context.CancelFunc, string, error) {
	// WorkDir guard FIRST: an empty cwd would inherit the engine launch directory
	// (arbitron.db / config.yaml). Same invariant the exec path enforces — fail
	// closed before any session starts.
	if req.WorkDir == "" {
		return nil, nil, nil, "", ErrWorkDirRequired
	}

	// Single model source per turn: req.Model wins over the binding so an
	// interactive node can target its profile's model per request; an empty
	// request model falls back to the configured binding model.
	model := req.Model
	if model == "" {
		model = p.binding.Model
	}

	// Effort emission: the binding's resolved Effort is floored + mapped through
	// the SAME capability pipeline the exec path uses (codexEffortGrade — the shared
	// graded-value core), then carried as the turn's model_reasoning_effort. A
	// default/empty effort or an Empty ruleset emits "". The MCP path consumes the
	// bare grade directly, so the exec `-c` flag format is not load-bearing here.
	effort, err := codexEffortGrade(p.binding, p.rules)
	if err != nil {
		return nil, nil, nil, model, fmt.Errorf("codex interactive (model %q): %w", model, err)
	}

	// Per-turn deadline: derive a turn context honouring p.timeout. This is the
	// only deadline in the conversation.go → StreamChat → startTurn path, so a
	// wedged codex turn that never emits a terminal frame is bounded here.
	turnCtx, cancel := procenv.WithTimeout(ctx, p.timeout)

	stream, err := p.newSession().Start(turnCtx, turnConfig{
		Model:    model,
		Prompt:   req.Prompt,
		WorkDir:  req.WorkDir,
		ResumeID: req.SessionID,
		Effort:   effort,
	})
	if err != nil {
		cancel()
		return nil, nil, nil, model, err
	}
	return stream, turnCtx, cancel, model, nil
}

// StreamChat runs one codex interview turn and streams assistant text as it
// arrives. Each turnText becomes a ChatChunk token in order; the terminal
// turnDone becomes a ChatChunk{Done} carrying the conversation id + token counts.
// A turnFailed event or any non-sentinel transport error becomes a terminal
// ChatChunk{Error} with NO Done — a failure is never a silent partial success.
// The internal goroutine ALWAYS drains the turn stream to its terminal signal so
// the child (real transport) is reaped; a cancelled/timed-out consumer never
// blocks the drainer (the turnCtx-raced makeSendChunk send). The per-turn
// deadline (startTurn's turnCtx) bounds a wedged turn that never emits a terminal
// frame, and the deferred cancel releases it on every exit path.
func (p *InteractiveProvider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	stream, turnCtx, cancel, _, err := p.startTurn(ctx, req)
	if err != nil {
		return nil, err
	}

	out := make(chan engine.ChatChunk)
	go func() {
		defer close(out)
		// Release the per-turn deadline + reap the transport child once the drain
		// reaches its terminal, on every exit path.
		defer cancel()

		// turnCtx (the deadline-bearing context) governs both the drain and the
		// non-blocking send, so a cancelled consumer, an elapsed engine ctx, OR the
		// per-turn timeout unblocks the sender while the drain loop runs to terminal.
		sendChunk := makeSendChunk(turnCtx, out)

		var failed bool
		for {
			ev, nerr := stream.Next(turnCtx)
			if nerr != nil {
				// errStreamDone is the clean terminal sentinel; any OTHER error is a
				// real transport/context failure (fail closed — a terminal error chunk,
				// never a clean Done).
				if isStreamDone(nerr) {
					return
				}
				if !failed {
					sendChunk(engine.ChatChunk{Error: fmt.Errorf("codex interactive: stream: %w", nerr)})
				}
				return
			}
			if failed {
				continue // already surfaced a failure; keep draining to reach the terminal
			}
			switch ev.Kind {
			case turnText:
				if !sendChunk(engine.ChatChunk{Token: ev.Text}) {
					failed = true // consumer gone / timed out; stop emitting, keep draining
				}
			case turnDone:
				sendChunk(engine.ChatChunk{
					Done:      true,
					SessionID: ev.SessionID,
					TokensIn:  ev.TokensIn,
					TokensOut: ev.TokensOut,
				})
				return
			case turnFailed:
				// A failed turn is NOT a silent success: surface a terminal error
				// chunk, never a Done. Keep draining afterwards to reap the child.
				sendChunk(engine.ChatChunk{Error: fmt.Errorf("codex interactive: turn failed: %w", ev.Err)})
				failed = true
			}
		}
	}()

	return out, nil
}

// Chat runs one codex interview turn to completion and returns the terminal
// result, folding the SAME turn stream the StreamChat path folds. A failed turn,
// a transport break, or a cancelled context is a hard error (no silent empty
// result) — mirroring the exec adapter's Chat/StreamChat symmetry.
func (p *InteractiveProvider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	stream, turnCtx, cancel, model, err := p.startTurn(ctx, req)
	if err != nil {
		return nil, err
	}
	// Release the per-turn deadline + reap the transport child when the fold ends.
	defer cancel()

	var b strings.Builder
	var done turnEvent
	var sawDone bool
	for {
		ev, nerr := stream.Next(turnCtx)
		if nerr != nil {
			if isStreamDone(nerr) {
				break
			}
			// A non-sentinel error (transport break / ctx cancel) is a hard error.
			return nil, fmt.Errorf("codex interactive (model %q): stream: %w", model, nerr)
		}
		switch ev.Kind {
		case turnText:
			b.WriteString(ev.Text)
		case turnDone:
			done = ev
			sawDone = true
		case turnFailed:
			return nil, fmt.Errorf("codex interactive (model %q): turn failed: %w", model, ev.Err)
		}
	}
	if !sawDone {
		// The stream ended without a terminal done (e.g. an empty/aborted turn). Fail
		// closed rather than return a silent partial result.
		return nil, fmt.Errorf("codex interactive (model %q): turn ended without completion", model)
	}

	return &engine.ChatResponse{
		Output:    b.String(),
		SessionID: done.SessionID,
		Model:     model,
		TokensIn:  done.TokensIn,
		TokensOut: done.TokensOut,
	}, nil
}

// isStreamDone reports whether err is the turn stream's clean terminal sentinel
// (the io.EOF analogue) — any OTHER error is a real transport/context failure.
func isStreamDone(err error) bool {
	return errors.Is(err, errStreamDone)
}
