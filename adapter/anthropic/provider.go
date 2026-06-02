// Package anthropic provides a stub LLM provider adapter for the Anthropic API.
// A full implementation will be added in a future phase.
package anthropic

import (
	"context"
	"errors"

	"github.com/arbitron/arbitron/domain/engine"
)

// ErrNotConfigured is returned when the Anthropic provider is called without
// a valid API key.
var ErrNotConfigured = errors.New("anthropic provider not configured: set ANTHROPIC_API_KEY")

var _ engine.ChatProvider = (*Provider)(nil)

// Provider is a stub implementation of engine.ChatProvider for the Anthropic API.
type Provider struct {
	apiKey string
}

// New creates a new Anthropic Provider. If the API key is empty, all calls
// will return ErrNotConfigured.
func New(apiKey string) *Provider {
	return &Provider{apiKey: apiKey}
}

// Chat makes a synchronous LLM request. Currently returns ErrNotConfigured
// if no API key is set; a full implementation will be added in a future phase.
func (p *Provider) Chat(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
	if p.apiKey == "" {
		return nil, ErrNotConfigured
	}
	return nil, errors.New("anthropic provider: full implementation not yet available")
}

// StreamChat makes a streaming LLM request. Currently returns ErrNotConfigured
// if no API key is set; a full implementation will be added in a future phase.
func (p *Provider) StreamChat(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	if p.apiKey == "" {
		return nil, ErrNotConfigured
	}
	return nil, errors.New("anthropic provider: full implementation not yet available")
}
