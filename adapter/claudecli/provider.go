package claudecli

import (
	"context"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
)

// Provider implements engine.LLMProvider by invoking the `claude` CLI in
// headless (`--print`) mode.
type Provider struct {
	binPath      string
	defaultModel string
	timeout      time.Duration
	runner       CommandRunner
}

var _ engine.LLMProvider = (*Provider)(nil)

// New constructs a claude-cli Provider. An empty binaryPath is resolved from
// PATH and common install directories. A zero timeout means no timeout. A nil
// runner uses the production os/exec runner.
func New(binaryPath, defaultModel string, timeout time.Duration, runner CommandRunner) *Provider {
	if runner == nil {
		runner = newExecRunner()
	}
	return &Provider{
		binPath:      resolveBinary(binaryPath),
		defaultModel: defaultModel,
		timeout:      timeout,
		runner:       runner,
	}
}

// spec builds the RunSpec for a request.
func (p *Provider) spec(req engine.LLMRequest) RunSpec {
	return RunSpec{
		Bin:   p.binPath,
		Args:  buildArgs(req, p.defaultModel),
		Stdin: req.Prompt,
		Dir:   req.WorkDir,
		Env:   augmentedEnv(),
	}
}

// withTimeout derives a context honouring the provider timeout.
func (p *Provider) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if p.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, p.timeout)
}

// Call runs claude to completion and returns the terminal result event.
func (p *Provider) Call(ctx context.Context, req engine.LLMRequest) (*engine.LLMResponse, error) {
	runCtx, cancel := p.withTimeout(ctx)
	defer cancel()

	lines, wait, err := p.runner.Run(runCtx, p.spec(req))
	if err != nil {
		return nil, fmt.Errorf("claudecli: call: %w", err)
	}

	var result streamEvent
	var haveResult bool
	var parseErr error
	for line := range lines {
		if parseErr != nil {
			continue // keep draining; don't process further
		}
		ev, ok, err := parseEvent([]byte(line))
		if err != nil {
			parseErr = err
			continue
		}
		if ok && ev.Kind == eventResult {
			result, haveResult = ev, true
		}
	}
	if waitErr := wait(); waitErr != nil {
		return nil, fmt.Errorf("claudecli: claude exited with error: %w", waitErr)
	}
	if parseErr != nil {
		return nil, parseErr
	}
	if !haveResult {
		return nil, fmt.Errorf("claudecli: no result event in claude output")
	}
	if result.IsError {
		return nil, fmt.Errorf("claudecli: claude reported an error: %s", result.Text)
	}

	return &engine.LLMResponse{
		Output:    result.Text,
		SessionID: result.SessionID,
		Model:     req.Model,
		TokensIn:  result.TokensIn,
		TokensOut: result.TokensOut,
	}, nil
}

// StreamCall runs claude and streams assistant text as it arrives. The final
// chunk has Done=true and carries the session id and token counts. Any failure
// is reported as a chunk with a non-nil Error. The internal goroutine always
// drains the runner's lines channel and calls wait(), honouring the
// CommandRunner contract even when an error is encountered mid-stream.
func (p *Provider) StreamCall(ctx context.Context, req engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	runCtx, cancel := p.withTimeout(ctx)

	lines, wait, err := p.runner.Run(runCtx, p.spec(req))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claudecli: stream call: %w", err)
	}

	out := make(chan engine.LLMStreamChunk)
	go func() {
		defer cancel()
		defer close(out)

		var sessionID string
		var tokensIn, tokensOut int
		var failed bool // once true, keep draining lines without processing

		for line := range lines {
			if failed {
				continue
			}
			ev, ok, parseErr := parseEvent([]byte(line))
			if parseErr != nil {
				out <- engine.LLMStreamChunk{Error: parseErr}
				failed = true
				continue
			}
			if !ok {
				continue
			}
			if ev.SessionID != "" {
				sessionID = ev.SessionID
			}
			switch ev.Kind {
			case eventAssistant:
				if ev.Text != "" {
					out <- engine.LLMStreamChunk{Token: ev.Text}
				}
			case eventResult:
				tokensIn, tokensOut = ev.TokensIn, ev.TokensOut
				if ev.IsError {
					out <- engine.LLMStreamChunk{Error: fmt.Errorf("claudecli: claude reported an error: %s", ev.Text)}
					failed = true
					continue
				}
			}
		}

		// lines is fully drained; reap the process.
		waitErr := wait()
		if failed {
			// An error chunk was already emitted; just finish.
			return
		}
		if waitErr != nil {
			out <- engine.LLMStreamChunk{Error: fmt.Errorf("claudecli: claude exited with error: %w", waitErr)}
			return
		}

		out <- engine.LLMStreamChunk{
			Done:      true,
			SessionID: sessionID,
			TokensIn:  tokensIn,
			TokensOut: tokensOut,
		}
	}()

	return out, nil
}
