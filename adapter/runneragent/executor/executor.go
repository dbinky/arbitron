// Package executor runs hermetic chat jobs on the runner through the
// existing provider factory, each in a fresh scratch directory.
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

const maxErrorText = 1024

// LunaRoute is this runner's own LunaRoute connection and key.
type LunaRoute struct {
	ConnectionRef string
	APIKey        string
}

// Config configures the executor.
type Config struct {
	ScratchRoot string
	LunaRoute   *LunaRoute // nil: no LunaRoute on this runner
}

// Executor implements runneragent.JobExecutor.
type Executor struct {
	sel engine.ChatProviderSelector
	cfg Config
}

var _ runneragent.JobExecutor = (*Executor)(nil)

// New validates and returns an executor.
func New(sel engine.ChatProviderSelector, cfg Config) (*Executor, error) {
	if sel == nil {
		return nil, errors.New("executor: chat provider selector is required")
	}
	if !filepath.IsAbs(cfg.ScratchRoot) {
		return nil, fmt.Errorf("executor: scratch root must be absolute, got %q", cfg.ScratchRoot)
	}
	return &Executor{sel: sel, cfg: cfg}, nil
}

// Supports reports whether this runner can run d.
func (e *Executor) Supports(d runneragent.Dispatch) (bool, string) {
	if d.Chat == nil || (d.Op != runneragent.OpChat && d.Op != runneragent.OpStream) {
		return false, fmt.Sprintf("operation %q is not supported by this runner", d.Op)
	}
	switch runner.CredentialKind(d.Chat.CredentialKind) {
	case runner.CredAnthropicSubscription:
		if b := d.Chat.Backend; b != "" && b != "claude-code" {
			return false, fmt.Sprintf("anthropic_subscription chat runs only on the claude harness, not %q", b)
		}
		return true, ""
	case runner.CredLunaRoute:
		if e.cfg.LunaRoute == nil {
			return false, "no LunaRoute key is configured on this runner"
		}
		return true, ""
	case runner.CredOpenAISubscription:
		return false, "openai_subscription chat is not supported on remote runners in this release"
	default:
		return false, fmt.Sprintf("credential kind %q cannot run on this runner", d.Chat.CredentialKind)
	}
}

// Execute runs d in a fresh scratch directory that is removed afterwards.
func (e *Executor) Execute(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
	if ok, reason := e.Supports(d); !ok {
		return failure(runner.SourceOutcome{Class: runner.OutcomeOther}, reason)
	}
	dir, err := os.MkdirTemp(e.cfg.ScratchRoot, fmt.Sprintf("job-%s-g%d-", d.JobID, d.Generation))
	if err != nil {
		return failure(runner.SourceOutcome{Class: runner.OutcomeOther}, "runner scratch directory unavailable")
	}
	defer func() { _ = os.RemoveAll(dir) }()

	p, err := e.sel.ChatProviderFor(e.binding(d.Chat))
	if err != nil {
		return e.errResult(err)
	}
	c := d.Chat
	req := engine.ChatRequest{
		Model: c.Model, SystemPrompt: c.SystemPrompt, Prompt: c.Prompt, MaxTokens: c.MaxTokens,
		Temperature: c.Temperature, WorkDir: dir, SessionID: c.SessionID, RequireExecutedModel: c.RequireExecutedModel,
	}
	if len(c.OutputSchema) > 0 {
		var schema any
		if err := json.Unmarshal(c.OutputSchema, &schema); err != nil {
			return failure(runner.SourceOutcome{Class: runner.OutcomeOther}, "invalid output schema")
		}
		req.OutputSchema = schema
	}
	if d.Op == runneragent.OpStream || c.Stream {
		return e.stream(ctx, p, req, emit)
	}
	resp, err := p.Chat(ctx, req)
	if err != nil {
		return e.errResult(err)
	}
	out, err := outputText(resp.Output)
	if err != nil {
		return failure(runner.SourceOutcome{Class: runner.OutcomeOther}, "chat output is not serializable")
	}
	model := resp.Model
	if model == "" {
		model = c.Model
	}
	return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: out, Model: model, SessionID: resp.SessionID, TokensIn: resp.TokensIn, TokensOut: resp.TokensOut}}
}

func (e *Executor) stream(ctx context.Context, p engine.ChatProvider, req engine.ChatRequest, emit runneragent.EmitFunc) runneragent.Result {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := p.StreamChat(sctx, req)
	if err != nil {
		return e.errResult(err)
	}
	drain := func() {
		cancel()
		for range ch {
		}
	}
	var sb strings.Builder
	var final engine.ChatChunk
	for c := range ch {
		if c.Error != nil {
			drain()
			return e.errResult(c.Error)
		}
		if c.Token != "" {
			sb.WriteString(c.Token)
			if err := emit(sctx, c.Token); err != nil {
				drain()
				return failure(runner.SourceOutcome{Class: runner.OutcomeOther}, "output delivery stopped")
			}
		}
		if c.Done {
			final = c
		}
	}
	return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: sb.String(), Model: req.Model, SessionID: final.SessionID, TokensIn: final.TokensIn, TokensOut: final.TokensOut}}
}

func (e *Executor) binding(c *runneragent.ChatJob) provider.ResolvedBinding {
	b := provider.ResolvedBinding{
		Kind: provider.ProfileKindChat, Model: c.Model,
		ContextWindow: provider.ContextWindow(c.ContextWindow), Effort: provider.Effort(c.Effort),
	}
	if runner.CredentialKind(c.CredentialKind) == runner.CredLunaRoute && e.cfg.LunaRoute != nil {
		key := e.cfg.LunaRoute.APIKey
		b.Provider = provider.CanonicalLunaRoute
		b.ConnectionRef = e.cfg.LunaRoute.ConnectionRef
		b.RevealKey = func(context.Context) (string, error) { return key, nil }
	}
	return b
}

func (e *Executor) errResult(err error) runneragent.Result {
	text := err.Error()
	if e.cfg.LunaRoute != nil && e.cfg.LunaRoute.APIKey != "" {
		text = strings.ReplaceAll(text, e.cfg.LunaRoute.APIKey, "[redacted]")
	}
	return failure(runner.ClassifyError(err), text)
}

func failure(o runner.SourceOutcome, text string) runneragent.Result {
	if o.Class == runner.OutcomeNone {
		o.Class = runner.OutcomeOther
	}
	if len(text) > maxErrorText {
		text = text[:maxErrorText]
	}
	return runneragent.Result{OK: false, Outcome: o, ErrorText: text}
}

func outputText(v any) (string, error) {
	switch o := v.(type) {
	case nil:
		return "", nil
	case string:
		return o, nil
	default:
		b, err := json.Marshal(o)
		return string(b), err
	}
}
