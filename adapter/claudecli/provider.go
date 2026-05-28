package claudecli

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
)

// ErrWorkDirRequired is returned by Call/StreamCall when req.WorkDir is empty.
// SECURITY: an empty Dir on cmd.Exec means "inherit parent cwd" — i.e., the
// engine launch directory containing arbitron.db, config.yaml, and other
// secrets. PR #8 security MEDIUM thread 3291944671 item 1.
var ErrWorkDirRequired = fmt.Errorf("claudecli: WorkDir is required")

// ErrWorkDirEscapesRoot is returned when req.WorkDir resolves outside the
// configured workspace root. SECURITY: PR #8 thread 3291944671 item 2.
var ErrWorkDirEscapesRoot = fmt.Errorf("claudecli: WorkDir is outside the configured workspace root")

// Config configures a claude-cli Provider.
//
// SECURITY:
//   - SkipPermissions defaults to false; setting true emits
//     `--dangerously-skip-permissions`, removing the human-in-the-loop tool
//     gate. Only flip in deployments that trust every prompt input.
//   - AllowedTools is forwarded as `--allowedTools` (comma-joined). When
//     SkipPermissions is false this should be configured because headless
//     `--print` mode cannot prompt the operator for tool approvals.
//   - WorkspaceRoot pins request WorkDir to a subtree of this absolute path.
//     An empty WorkspaceRoot disables the pin check (test convenience only).
//   - MaxResponseBytes caps the per-request total stdout bytes; 0 means no
//     cap (test convenience only).
type Config struct {
	BinaryPath       string
	DefaultModel     string
	WorkspaceRoot    string
	Timeout          time.Duration
	AllowedTools     []string
	SkipPermissions  bool
	MaxResponseBytes int64
	Runner           CommandRunner
}

// Provider implements engine.LLMProvider by invoking the `claude` CLI in
// headless (`--print`) mode.
type Provider struct {
	binPath          string
	defaultModel     string
	workspaceRoot    string
	timeout          time.Duration
	allowedTools     []string
	skipPermissions  bool
	maxResponseBytes int64
	runner           CommandRunner
}

var _ engine.LLMProvider = (*Provider)(nil)

// New constructs a claude-cli Provider from cfg. An empty BinaryPath is
// resolved from PATH and common install directories. A zero Timeout means no
// timeout. A nil Runner uses the production os/exec runner.
//
// SECURITY: when SkipPermissions=false and AllowedTools is empty, a slog.Warn
// is emitted because headless `--print` mode cannot answer interactive
// permission prompts — the agent will fail at its first tool call. Operators
// wanting permissionful runs should configure an AllowedTools allowlist.
func New(cfg Config) *Provider {
	if cfg.Runner == nil {
		cfg.Runner = newExecRunner()
	}
	if !cfg.SkipPermissions && len(cfg.AllowedTools) == 0 {
		slog.Warn(
			"claudecli: SkipPermissions=false with empty AllowedTools; agent runs will fail at the first tool call because --print mode cannot prompt for approvals",
		)
	}
	return &Provider{
		binPath:          resolveBinary(cfg.BinaryPath),
		defaultModel:     cfg.DefaultModel,
		workspaceRoot:    cfg.WorkspaceRoot,
		timeout:          cfg.Timeout,
		allowedTools:     cfg.AllowedTools,
		skipPermissions:  cfg.SkipPermissions,
		maxResponseBytes: cfg.MaxResponseBytes,
		runner:           cfg.Runner,
	}
}

// spec builds the RunSpec for a request.
func (p *Provider) spec(req engine.LLMRequest) RunSpec {
	return RunSpec{
		Bin: p.binPath,
		Args: buildArgs(req, argsPolicy{
			DefaultModel:    p.defaultModel,
			SkipPermissions: p.skipPermissions,
			AllowedTools:    p.allowedTools,
		}),
		Stdin: req.Prompt,
		Dir:   req.WorkDir,
		Env:   augmentedEnv(),
	}
}

// validateWorkDir enforces the per-request WorkDir invariants:
//   - non-empty (avoid the silent "inherit engine cwd" fallback)
//   - resolves to an absolute path under p.workspaceRoot when one is configured
//
// An empty WorkspaceRoot disables the pin check (test convenience only).
func (p *Provider) validateWorkDir(workDir string) error {
	if workDir == "" {
		return ErrWorkDirRequired
	}
	if p.workspaceRoot == "" {
		return nil
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("claudecli: resolve WorkDir: %w", err)
	}
	abs = filepath.Clean(abs)
	root := filepath.Clean(p.workspaceRoot)
	// Allow root itself, or any subdir of root. Use a separator-terminated
	// prefix check to avoid /root/wsX shadowing /root/ws.
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q not under %q", ErrWorkDirEscapesRoot, abs, root)
	}
	return nil
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
	if err := p.validateWorkDir(req.WorkDir); err != nil {
		return nil, err
	}

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
	if err := p.validateWorkDir(req.WorkDir); err != nil {
		return nil, err
	}

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
