package codex

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// StreamProvider implements engine.ChatProvider (Chat + StreamChat) by driving
// `codex exec` in streaming-JSON mode on the ChatGPT subscription — the first
// non-Claude streaming agent adapter, parallel to claudecli.StreamAgentAdapter.
// It is the interactive_agent (streaming) sibling of the batch Provider: it
// REUSES the same Config, the same `codex exec` argv builder (args.go), the same
// subscription env (env.go — NO API key, HOME preserved), the same memoized
// version probe (version.go), and the SAME JSONL event vocabulary (parse.go's
// decodeLine + foldEvent) so batch RunAgent and streaming StreamChat can never
// diverge on what an event means.
//
// codex emits whole `item.completed` agent_message items rather than token
// deltas, so each agent_message becomes one ChatChunk token; command_execution
// items contribute no text. The terminal chunk (Done=true) carries the codex
// thread id and best-effort token counts. A failed turn (error / turn.failed), a
// malformed line, or a non-zero process exit closes the channel with a typed
// error chunk and NO Done — a failure is never a silent partial success.
//
// SECURITY: like the batch Provider, the prompt is delivered via stdin (never
// argv), the env carries NO OPENAI_API_KEY (codex authenticates via its on-disk
// ~/.codex/auth.json subscription credential), and binding.RevealKey is never
// consulted. `codex exec` runs under the fixed v1 fully-open bypass posture
// (--dangerously-bypass-approvals-and-sandbox, see args.go).
type StreamProvider struct {
	binding          provider.ResolvedBinding
	binPath          string
	runner           claudecli.CommandRunner
	timeout          time.Duration
	maxResponseBytes int64

	// probeOnce memoizes the binary version probe across requests.
	probeOnce sync.Once
	probeErr  error
}

var _ engine.ChatProvider = (*StreamProvider)(nil)

// NewStreamProvider constructs a codex StreamProvider from cfg (the SAME Config
// as the batch Provider). An empty BinaryPath is resolved from PATH and common
// install directories. A nil Runner uses the shared production os/exec runner.
func NewStreamProvider(cfg Config) *StreamProvider {
	runner := cfg.Runner
	if runner == nil {
		runner = claudecli.NewExecRunner()
	}
	return &StreamProvider{
		binding:          cfg.Binding,
		binPath:          resolveBinary(cfg.BinaryPath),
		runner:           runner,
		timeout:          cfg.Timeout,
		maxResponseBytes: cfg.MaxResponseBytes,
	}
}

// launch performs the shared fail-closed prelude for both Chat and StreamChat:
// WorkDir guard → build args (fail-closed on empty model) → build subscription
// env → memoized version probe → start the subprocess. It returns the runner's
// lines channel + wait function plus the run context's cancel, which the caller
// MUST invoke (defer) to release the derived timeout context.
//
// An empty WorkDir, an empty model, or an incompatible binary produces ZERO
// stdout (the error is returned before — or at — the launch), so a failed
// prelude never yields a half-open channel.
//
// resolveModel: a non-empty ChatRequest.Model overrides the binding's model so an
// interactive_agent node can target its profile's model per request; an empty
// request model falls back to the adapter's configured binding model.
func (p *StreamProvider) launch(ctx context.Context, req engine.ChatRequest) (<-chan string, func() error, context.Context, context.CancelFunc, error) {
	if req.WorkDir == "" {
		return nil, nil, nil, nil, ErrWorkDirRequired
	}

	// Build args FIRST so an empty model fails closed with NO launch (never a
	// silent default-model run). A non-empty request model wins over the binding.
	model := req.Model
	if model == "" {
		model = p.binding.Model
	}
	args, err := buildExecArgs(model, req.WorkDir)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("codex stream (model %q): %w", model, err)
	}

	// Subscription env: NO key revealed, HOME preserved (env.go). Built before the
	// probe so the launch ordering matches the batch path.
	env := codexEnv()

	// Version probe (memoized): an incompatible/missing binary fails closed here,
	// before any run launches.
	if err := p.ensureCompatible(ctx); err != nil {
		return nil, nil, nil, nil, err
	}

	// ChatRequest carries no per-request timeout (unlike AgentMission); the
	// adapter's configured default is the only floor. <=0 means no default.
	runCtx, cancel := procenv.WithTimeout(ctx, p.timeout)

	spec := claudecli.RunSpec{
		Bin:              p.binPath,
		Args:             args,
		Stdin:            req.Prompt, // prompt via stdin — never argv (SECURITY)
		Dir:              req.WorkDir,
		Env:              env,
		MaxResponseBytes: p.maxResponseBytes,
	}

	lines, wait, err := p.runner.Run(runCtx, spec)
	if err != nil {
		cancel()
		return nil, nil, nil, nil, fmt.Errorf("codex: stream call (model %q): %w", model, err)
	}
	return lines, wait, runCtx, cancel, nil
}

// Chat runs `codex exec` to completion and returns the terminal result, folding
// the JSONL event stream via the SHARED decodeLine + foldEvent primitives (the
// same fold the batch Provider and StreamChat use). A malformed line, a failed
// turn, or a non-zero exit is a hard error (no silent empty result).
func (p *StreamProvider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	lines, wait, runCtx, cancel, err := p.launch(ctx, req)
	if err != nil {
		return nil, err
	}
	defer cancel()

	out := &parsed{}
	var parseErr error
	for line := range lines {
		if parseErr != nil {
			continue // keep draining so the subprocess can be reaped
		}
		ev, ok, derr := decodeLine(line)
		if derr != nil {
			parseErr = derr
			continue
		}
		if ok {
			foldEvent(out, ev)
		}
	}
	if waitErr := wait(); waitErr != nil {
		// Prefer a context error so callers can errors.Is on cancel/deadline
		// (runCtx folds in both the parent ctx and the derived per-call timeout).
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("codex: stream mission failed (model %q): %w", req.Model, waitErr)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("codex: parse stream output (model %q): %w", req.Model, parseErr)
	}
	if out.failed {
		return nil, fmt.Errorf("codex: turn failed (model %q): %s", req.Model, out.summary)
	}

	return &engine.ChatResponse{
		Output:    out.summary,
		SessionID: out.sessionID,
		Model:     req.Model,
		TokensIn:  out.tokensIn,
		TokensOut: out.tokensOut,
	}, nil
}

// StreamChat runs `codex exec` and streams assistant text as it arrives. Each
// agent_message item is emitted as a ChatChunk token in order; the terminal
// chunk (Done=true) carries the codex thread id and token counts. Any failure
// (malformed line, failed turn, non-zero exit) closes the channel with a chunk
// carrying a non-nil Error and NO Done. The internal goroutine ALWAYS drains the
// runner's lines channel and calls wait() so the subprocess is reaped.
func (p *StreamProvider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	lines, wait, runCtx, cancel, err := p.launch(ctx, req)
	if err != nil {
		return nil, err
	}

	out := make(chan engine.ChatChunk)
	go func() {
		defer cancel()
		defer close(out)

		// runCtx (parent ctx + derived per-call timeout) governs the non-blocking
		// send, so a cancelled consumer OR an elapsed timeout unblocks the sender.
		sendChunk := makeSendChunk(runCtx, out)

		acc := &parsed{}
		var failed bool
		for line := range lines {
			if failed {
				continue // already failed; keep draining to reap the subprocess
			}
			ev, ok, derr := decodeLine(line)
			if derr != nil {
				failed = true
				sendChunk(engine.ChatChunk{Error: fmt.Errorf("codex: parse stream output: %w", derr)})
				continue
			}
			if !ok {
				continue
			}
			// Fold into the shared accumulator; emit any NEW assistant text as a
			// token (codex agent_message items are whole, not deltas).
			if newText := foldEvent(acc, ev); newText != "" {
				if !sendChunk(engine.ChatChunk{Token: newText}) {
					failed = true // consumer gone / timed out; stop emitting, keep draining
				}
			}
		}

		waitErr := wait()
		// A mid-stream failure (parse error, or consumer gone) already closed or
		// abandoned the stream; do not emit a terminal chunk over the top of it.
		if failed {
			return
		}
		if waitErr != nil {
			sendChunk(engine.ChatChunk{
				Error: fmt.Errorf("codex: stream exited with error: %w", waitErr),
			})
			return
		}
		if acc.failed {
			// A failed turn (error / turn.failed) is NOT a silent success: surface
			// the failure message as the terminal error chunk, never a Done.
			sendChunk(engine.ChatChunk{
				Error: fmt.Errorf("codex: turn failed: %s", acc.summary),
			})
			return
		}
		sendChunk(engine.ChatChunk{
			Done:      true,
			SessionID: acc.sessionID,
			TokensIn:  acc.tokensIn,
			TokensOut: acc.tokensOut,
		})
	}()

	return out, nil
}

// ensureCompatible runs the version probe at most once per StreamProvider. A
// failure is memoized so every subsequent request fails closed without
// re-probing.
func (p *StreamProvider) ensureCompatible(ctx context.Context) error {
	p.probeOnce.Do(func() {
		p.probeErr = probeVersion(ctx, p.runner, p.binPath)
	})
	return p.probeErr
}

// makeSendChunk returns the non-blocking send closure used by StreamChat: it
// races the channel send against ctx cancellation so a gone consumer (cancelled
// ctx) never blocks the draining goroutine.
func makeSendChunk(ctx context.Context, out chan<- engine.ChatChunk) func(engine.ChatChunk) bool {
	return func(c engine.ChatChunk) bool {
		select {
		case out <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}
}
