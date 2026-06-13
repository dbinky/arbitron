package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// InteractiveConfig configures an InteractiveProvider — the conversational codex
// backend that runs over the mcpSession transport seam (not `codex exec`). It is
// the interactive sibling of codex.Config: it shares the same construction
// inputs (binding, binary, per-turn timeout, capability rules) but adds the
// auto-approval policy's WritableRoot, and it owns NO Runner (the mcpSession
// factory replaces the subprocess runner seam — real stdio in prod, fake in
// tests).
//
// Unlike codex.Config there is deliberately NO MaxResponseBytes: the exec path
// enforces a stdout byte cap on a raw pipe, but the interactive turn streams
// discrete JSON-RPC event frames over the mcp-go transport (no raw byte pipe to
// cap), so a byte counter here would be a knob nothing reads — a lying contract.
// The per-turn deadline (Timeout) is the bound that matters on this path.
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
	// WritableRoot is the only directory subtree the auto-policy lets codex write
	// (the deliverable). Empty defaults to "docs" (defaultWritableRoot) — a zero
	// value must never mean "everywhere" (fail closed).
	WritableRoot string
}

// InteractiveProvider implements engine.ChatProvider (Chat + StreamChat) by
// running one codex interview turn over the mcpSession seam, wiring the
// docs/-scoped auto-approval policy as the turn's approvalFunc, folding the turn
// stream into ChatChunks, and round-tripping the conversation id for resume.
//
// It holds NO in-memory session state between turns (spawn-per-turn): the
// terminal ChatChunk.SessionID carries codex's conversation id, the engine
// persists it, and a later req.SessionID flows back as the turn's ResumeID — so
// resume survives WAITING, server restarts, and long human delays.
//
// SECURITY / fail-closed: it shares the exec adapters' core (binding, binary
// resolution, memoized version probe), so a missing/incompatible binary or an
// empty WorkDir fails closed with zero events; the prompt rides the protocol
// body (never argv); no OPENAI_API_KEY ever enters env/argv; and EVERY codex
// approval request is routed through the policy (default-deny). A failed turn or
// transport break yields a terminal Error chunk and NEVER a clean Done.
type InteractiveProvider struct {
	core
	// newSession builds one mcpSession per turn (real mcpStdioSession in prod, a
	// fake in tests). It mirrors codex.Config.Runner's subprocess injection seam.
	newSession func() mcpSession
	// writableRoot is the only writable subtree the policy permits (default docs).
	writableRoot string
}

var _ engine.ChatProvider = (*InteractiveProvider)(nil)

// NewInteractiveProvider constructs an InteractiveProvider from cfg. It builds
// the shared core via newCore exactly as the exec adapters do (binding/binary
// resolution + the memoized version probe), so construction and compatibility
// cannot drift. A nil session factory defaults to the real mcpStdioSession
// (Task 1.2) built from the core — the composition root (Phase 3) calls this.
func NewInteractiveProvider(cfg InteractiveConfig) *InteractiveProvider {
	// Build the provider with the shared core inlined into the literal (a move, not
	// a copy of the lock-bearing core — see the exec adapters' &Provider{core:
	// newCore(cfg)} precedent). The default session factory is set AFTER so it can
	// capture &p.core, sharing the memoized version probe with construction.
	p := &InteractiveProvider{
		core:         newCore(interactiveCoreConfig(cfg)),
		writableRoot: writableRootOrDefault(cfg.WritableRoot),
	}
	p.newSession = func() mcpSession { return newMCPStdioSession(&p.core) }
	return p
}

// newInteractiveProviderWithSession is the in-package test seam: it injects a
// mcpSession factory (the fake transport) instead of the real stdio session,
// mirroring how codex.Config.Runner accepts a fake runner. External callers use
// NewInteractiveProvider and get the real session.
func newInteractiveProviderWithSession(cfg InteractiveConfig, newSession func() mcpSession) *InteractiveProvider {
	return &InteractiveProvider{
		core:         newCore(interactiveCoreConfig(cfg)),
		newSession:   newSession,
		writableRoot: writableRootOrDefault(cfg.WritableRoot),
	}
}

// interactiveCoreConfig projects an InteractiveConfig onto the exec adapters'
// Config so the shared core is built by the SAME newCore path — the interactive
// provider owns no Runner (the mcpSession factory replaces that seam), so it is
// left at its zero value (nil → the production os/exec runner, used only by the
// version probe). MaxResponseBytes is deliberately left at its zero value: the
// exec stdout byte cap has no meaning on the JSON-RPC event-frame transport, so
// the interactive path never sets it (see InteractiveConfig).
func interactiveCoreConfig(cfg InteractiveConfig) Config {
	return Config{
		Binding:         cfg.Binding,
		BinaryPath:      cfg.BinaryPath,
		Timeout:         cfg.Timeout,
		CapabilityRules: cfg.CapabilityRules,
	}
}

// writableRootOrDefault defaults an empty writable root to defaultWritableRoot so
// a zero-value config never silently widens the policy's scope to "everywhere".
func writableRootOrDefault(root string) string {
	if root == "" {
		return defaultWritableRoot
	}
	return root
}

// startTurn performs the shared fail-closed prelude for both Chat and StreamChat:
// it guards an empty WorkDir, resolves the model (req.Model overrides the binding
// model) and the floored effort, derives the per-turn deadline, installs the
// docs/-scoped auto-policy (wrapped to trace each decision), and starts ONE codex
// turn over a fresh mcpSession. An empty WorkDir, empty model, or a failed Start
// yields NO stream (no half-open turn) — the error is returned before any events
// flow.
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
	// the SAME capability pipeline the exec path uses, then carried as the turn's
	// model_reasoning_effort. A default/empty effort or an Empty ruleset emits "".
	effort, err := codexEffortValue(p.binding, p.rules)
	if err != nil {
		return nil, nil, nil, model, fmt.Errorf("codex interactive (model %q): %w", model, err)
	}

	// Per-turn deadline: derive a turn context honouring p.timeout. This is the
	// only deadline in the conversation.go → StreamChat → startTurn path, so a
	// wedged codex turn that never emits a terminal frame is bounded here.
	turnCtx, cancel := procenv.WithTimeout(ctx, p.timeout)

	// Install the docs/-scoped auto-policy, wrapped so each decision is traced
	// (slog). The transport routes EVERY codex approval request through this.
	policy := approvalPolicy{workDir: req.WorkDir, writableRoot: p.writableRoot}
	approver := tracingApprover(policy.decide)

	stream, err := p.newSession().Start(turnCtx, turnConfig{
		Model:    model,
		Prompt:   req.Prompt,
		WorkDir:  req.WorkDir,
		ResumeID: req.SessionID,
		Effort:   effort,
		Approver: approver,
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

// tracingApprover wraps the policy's decide so every approval decision is
// recorded via structured logging (slog) — the request summary, allow/deny, and
// reason. Denials are the operator's ONLY signal the guardrail fired; they are
// NEVER surfaced to the human (no ChatChunk field, no engine change — the port
// carries no step channel). The wrapper calls the real decide, logs the verdict,
// and returns it unchanged.
func tracingApprover(decide approvalFunc) approvalFunc {
	return func(req approvalRequest) approvalDecision {
		dec := decide(req)
		slog.Info("codex interactive approval decision",
			"summary", approvalSummary(req),
			"allow", dec.Allow,
			"reason", dec.Reason,
		)
		return dec
	}
}

// approvalSummary renders a short, log-safe description of an approval request:
// the command argv for an exec request, the target paths for a write, or a
// fallback for an unrecognized kind. It never logs the prompt or any secret.
func approvalSummary(req approvalRequest) string {
	switch {
	case len(req.Command) > 0:
		return "exec: " + strings.Join(req.Command, " ")
	case len(req.WritePaths) > 0:
		return "write: " + strings.Join(req.WritePaths, ", ")
	default:
		return "unrecognized approval request"
	}
}

// codexEffortValue resolves the bare codex model_reasoning_effort value (e.g.
// "high", or "minimal" for none) for a binding via the capability rules,
// reusing the exec path's codexEffortArgs so the interactive turn and `codex
// exec` floor + emit the IDENTICAL effective value. An Empty ruleset, a
// default/empty effort, or a backend that yields no graded effort returns "" (the
// turn carries no effort and the protocol body omits the key).
func codexEffortValue(binding provider.ResolvedBinding, rules provider.CapabilityRules) (string, error) {
	args, err := codexEffortArgs(binding, rules)
	if err != nil {
		return "", err
	}
	// codexEffortArgs returns the exec form ["-c", "model_reasoning_effort=<v>"];
	// extract the bare value so the MCP protocol body carries just <v>.
	const prefix = "model_reasoning_effort="
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, prefix); ok {
			return v, nil
		}
	}
	return "", nil
}

// isStreamDone reports whether err is the turn stream's clean terminal sentinel
// (the io.EOF analogue) — any OTHER error is a real transport/context failure.
func isStreamDone(err error) bool {
	return errors.Is(err, errStreamDone)
}
