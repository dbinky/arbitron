package probe

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// InteractiveChatLaneDeps are the real collaborators of an
// InteractiveChatUsabilityLane: the stream selector interactive_llm turns use,
// and the path inventory's WithBinding for the path's endpoint and lazy key.
type InteractiveChatLaneDeps struct {
	Streams     engine.InteractiveStreamSelector
	WithBinding func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error)
	// WorkDir is the scratch directory the Claude Code subscription stream runs
	// in; without it the claude-cli route is unsupported and reports unknown.
	WorkDir string
}

// InteractiveChatUsabilityLane checks an interactive_llm route by streaming one
// bounded, tool-free request on the explicit candidate spelling through the same
// stream selector a live turn uses. The streaming adapters verify the executed
// model themselves when RequireExecutedModel is set, without pin-version grammar,
// and report a mismatch, a missing identity or a truncated stream as an error
// chunk; the lane confirms only a stream that ends with a clean terminal Done.
type InteractiveChatUsabilityLane struct{ d InteractiveChatLaneDeps }

var _ UsabilityLane = (*InteractiveChatUsabilityLane)(nil)

func NewInteractiveChatUsabilityLane(d InteractiveChatLaneDeps) (*InteractiveChatUsabilityLane, error) {
	if d.Streams == nil || d.WithBinding == nil {
		return nil, errors.New("probe: interactive chat usability lane needs a stream selector and a binding source")
	}
	return &InteractiveChatUsabilityLane{d: d}, nil
}

// Supports admits interactive, backend-less chat routes on an evidenced
// provider. Interactive agent streams (claude-code, codex) are tool-capable and
// need their own exact-model proof.
func (l *InteractiveChatUsabilityLane) Supports(p modelcatalog.ExecutionPath) bool {
	prov := domprovider.Canonicalize(p.Binding.Provider)
	if prov == claudeCLIChatProvider && l.d.WorkDir == "" {
		return false
	}
	return p.Kind == string(domprovider.ProfileKindChat) && p.Mode == modelcatalog.ExecutionModeInteractive &&
		p.Backend == "" && chatLaneProviders[prov]
}

func (l *InteractiveChatUsabilityLane) Check(ctx context.Context, p modelcatalog.ExecutionPath, e modelidentity.Entry) (LaneResult, error) {
	model := candidateSpelling(e)
	b, err := l.d.WithBinding(ctx, p, model)
	if err != nil {
		return LaneResult{}, fmt.Errorf("%w: binding", ErrUsabilityNotConfirmed)
	}
	b.Model, b.Pinned, b.OpaquePinRoute = model, false, ""
	stream, err := l.d.Streams.StreamProviderFor(b)
	if err != nil {
		return LaneResult{}, fmt.Errorf("%w: no adapter", ErrUsabilityNotConfirmed)
	}
	req := engine.ChatRequest{Model: model, Prompt: chatUsabilityPrompt, MaxTokens: 16, RequireExecutedModel: true}
	if domprovider.Canonicalize(b.Provider) == claudeCLIChatProvider {
		workDir, clean, err := probeWorkDir(l.d.WorkDir)
		if err != nil {
			return LaneResult{}, err
		}
		defer clean()
		req.WorkDir, req.SuppressTools = workDir, true
	}
	res, err := confirmStream(ctx, stream, req, "chat-stream:"+domprovider.Canonicalize(b.Provider)+":"+model, model)
	if err != nil {
		return LaneResult{}, err
	}
	return confirmCurrentBinding(ctx, l.d.WithBinding, p, model, res)
}

// confirmStream reads one probe stream and confirms only one that ends with a
// clean terminal Done and no error chunk. The adapters verify the executed
// model themselves and report a mismatch, a missing identity or a truncated
// stream as an error chunk, so a clean Done is the proof.
func confirmStream(ctx context.Context, stream engine.ChatProvider, req engine.ChatRequest, proofRef, model string) (LaneResult, error) {
	// Cancelling on return releases an adapter goroutine blocked on a send once
	// the lane stops reading.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	chunks, err := stream.StreamChat(ctx, req)
	if err != nil || chunks == nil {
		return LaneResult{}, fmt.Errorf("%w: call failed", ErrUsabilityNotConfirmed)
	}
	// Join the adapter's cancellation drain before removing its scratch files.
	defer func() {
		cancel()
		for range chunks {
		}
	}()
	done := false
	for {
		select {
		case <-ctx.Done():
			return LaneResult{}, fmt.Errorf("%w: cancelled", ErrUsabilityNotConfirmed)
		case c, ok := <-chunks:
			if !ok {
				if !done {
					return LaneResult{}, fmt.Errorf("%w: stream ended without completion", ErrUsabilityNotConfirmed)
				}
				return LaneResult{NativeModel: model, ProofRef: proofRef}, nil
			}
			if c.Error != nil {
				return LaneResult{}, fmt.Errorf("%w: stream failed", ErrUsabilityNotConfirmed)
			}
			done = done || c.Done
		}
	}
}

// InteractiveAgentLaneDeps are the real collaborators of an
// InteractiveAgentUsabilityLane; WorkDir is the scratch directory the stream
// runs in and must lie under the claude adapters' workspace root.
type InteractiveAgentLaneDeps struct {
	Streams     engine.InteractiveStreamSelector
	WithBinding func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error)
	WorkDir     string
}

// InteractiveAgentUsabilityLane checks an interactive_agent route on the
// claude-code or (opted-in) codex backend by streaming one bounded request on the explicit candidate
// spelling through the stream selector a live turn uses. SuppressTools selects
// Claude's tool-free posture or Codex's D10 restrictions. Claude proves the model
// from modelUsage; Codex uses the account's correlated rollout. Every failed or
// missing proof is reported as an error chunk.
type InteractiveAgentUsabilityLane struct{ d InteractiveAgentLaneDeps }

var _ UsabilityLane = (*InteractiveAgentUsabilityLane)(nil)

func NewInteractiveAgentUsabilityLane(d InteractiveAgentLaneDeps) (*InteractiveAgentUsabilityLane, error) {
	if d.Streams == nil || d.WithBinding == nil || d.WorkDir == "" {
		return nil, errors.New("probe: interactive agent usability lane needs a stream selector, a binding source and a work dir")
	}
	return &InteractiveAgentUsabilityLane{d: d}, nil
}

// interactiveAgentBackends are the interactive agent streams whose adapter can
// run a tool-free, executed-model-verified turn.
var interactiveAgentBackends = map[string]bool{"claude-code": true, "codex": true}

func (l *InteractiveAgentUsabilityLane) Supports(p modelcatalog.ExecutionPath) bool {
	return p.Kind == string(domprovider.ProfileKindAgent) && p.Mode == modelcatalog.ExecutionModeInteractive &&
		interactiveAgentBackends[p.Backend]
}

func (l *InteractiveAgentUsabilityLane) Check(ctx context.Context, p modelcatalog.ExecutionPath, e modelidentity.Entry) (LaneResult, error) {
	model := candidateSpelling(e)
	b, err := l.d.WithBinding(ctx, p, model)
	if err != nil {
		return LaneResult{}, fmt.Errorf("%w: binding", ErrUsabilityNotConfirmed)
	}
	b.Model, b.Pinned, b.OpaquePinRoute = model, false, ""
	stream, err := l.d.Streams.StreamProviderFor(b)
	if err != nil {
		return LaneResult{}, fmt.Errorf("%w: no adapter", ErrUsabilityNotConfirmed)
	}
	workDir, clean, err := probeWorkDir(l.d.WorkDir)
	if err != nil {
		return LaneResult{}, err
	}
	defer clean()
	req := engine.ChatRequest{
		Model: model, Prompt: chatUsabilityPrompt, MaxTokens: 16, WorkDir: workDir,
		RequireExecutedModel: true, SuppressTools: true,
	}
	res, err := confirmStream(ctx, stream, req, "agent-stream:"+p.Backend+":"+model, model)
	if err != nil {
		return LaneResult{}, err
	}
	return confirmCurrentBinding(ctx, l.d.WithBinding, p, model, res)
}
