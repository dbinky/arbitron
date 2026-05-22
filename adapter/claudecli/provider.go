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
	for line := range lines {
		ev, ok, parseErr := parseEvent([]byte(line))
		if parseErr != nil {
			return nil, parseErr
		}
		if ok && ev.Kind == eventResult {
			result, haveResult = ev, true
		}
	}
	if waitErr := wait(); waitErr != nil {
		return nil, fmt.Errorf("claudecli: claude exited with error: %w", waitErr)
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

// StreamCall is implemented in Task 6.
func (p *Provider) StreamCall(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	return nil, fmt.Errorf("claudecli: StreamCall not yet implemented")
}
