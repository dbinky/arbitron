package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// NodeBinding is the display-level description of what a node runs on: the
// queue row's "model (effort) · harness · provider" cell. It deliberately
// carries no endpoint, extras or key material — only the provider family name
// crosses into list and event payloads.
type NodeBinding struct {
	NodeType string // always set
	Model    string // configured model; "" when unknown or a non-model node
	Effort   string // "" = default effort
	Backend  string // agent harness; set only for agent-capable nodes
	Provider string // provider family, e.g. "claude-cli", "litellm"
}

// NoopProfileResolver is the Null Object for provider.ProfileResolver: every
// alias is unknown, so bindings degrade to what the node config holds. Used as
// the default until the composition root wires the real resolver.
type NoopProfileResolver struct{}

// Resolve always reports provider.ErrUnknownProfile.
func (NoopProfileResolver) Resolve(context.Context, string) (provider.ResolvedBinding, error) {
	return provider.ResolvedBinding{}, provider.ErrUnknownProfile
}

// resolveNodeBinding describes the CONFIGURED binding of node, resolved through
// the same ProfileResolver the executors use. Backend is set only for
// agent-capable nodes (chat nodes have no harness) and mirrors the precedence
// of the executor that will actually run the node — see the switch below. Non-model nodes yield
// only NodeType. A resolver failure still yields the binding degraded to the
// node config (a display field must not sink the row); the error is returned
// so the caller decides how loudly to report it (the enricher logs each failing
// alias once per page, the engine once per node start).
func resolveNodeBinding(ctx context.Context, r provider.ProfileResolver, node workflow.Node, defaultBackend string) (NodeBinding, error) {
	b := NodeBinding{NodeType: string(node.Type)}

	var agent, honorNodeBackend bool
	switch node.Type {
	case workflow.NodeTypeAgentCall:
		agent, honorNodeBackend = true, true
	case workflow.NodeTypeInteractiveAgent:
		agent = true
	case workflow.NodeTypeLLMCall, workflow.NodeTypeInteractiveLLM:
	default:
		return b, nil
	}

	var resolved provider.ResolvedBinding
	var resolveErr error
	if alias := node.Config.ModelProfile; alias != "" {
		rb, err := r.Resolve(ctx, alias)
		if err != nil {
			resolveErr = fmt.Errorf("resolving profile %q: %w", alias, err)
		} else {
			resolved = rb
		}
	}

	b.Model = firstNonEmpty(resolved.Model, node.Config.Model)
	b.Effort = string(resolved.Effort)
	b.Provider = resolved.Provider
	// The two agent paths differ, and the row must show what runs:
	//   - agent_call: AgentCallExecutor applies node > profile > default.
	//   - interactive_agent: InteractiveExecutor hands the resolved profile
	//     straight to InteractiveStreamSelector, which applies profile > default;
	//     node.Config.Backend never reaches the interactive path.
	switch {
	case honorNodeBackend:
		b.Backend = firstNonEmpty(node.Config.Backend, resolved.Backend, defaultBackend)
	case agent:
		b.Backend = firstNonEmpty(resolved.Backend, defaultBackend)
	}
	return b, resolveErr
}

// pageProfileResolver memoizes Resolve by alias for the lifetime of one
// enrichment page, so a page of N rows on the same profile costs one lookup
// (connection-backed profiles hit the database) and a failing alias is logged
// once per page instead of once per row. Not safe for concurrent use; build
// one per Enrich call.
type pageProfileResolver struct {
	inner provider.ProfileResolver
	memo  map[string]pageResolveResult
}

type pageResolveResult struct {
	binding provider.ResolvedBinding
	err     error
}

func newPageProfileResolver(inner provider.ProfileResolver) *pageProfileResolver {
	return &pageProfileResolver{inner: inner, memo: map[string]pageResolveResult{}}
}

// Resolve returns the memoized result for alias, resolving (and, on failure,
// logging) only on the first request.
func (p *pageProfileResolver) Resolve(ctx context.Context, alias string) (provider.ResolvedBinding, error) {
	if r, ok := p.memo[alias]; ok {
		return r.binding, r.err
	}
	b, err := p.inner.Resolve(ctx, alias)
	if err != nil {
		slog.Warn("enricher: profile resolve failed; queue rows show node config", "profile", alias, "error", err)
	}
	p.memo[alias] = pageResolveResult{binding: b, err: err}
	return b, err
}
