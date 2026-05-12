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

var _ engine.LLMProvider = (*Provider)(nil)

// Provider is a stub implementation of engine.LLMProvider for the Anthropic API.
type Provider struct {
	apiKey string
}

// New creates a new Anthropic Provider. If the API key is empty, all calls
// will return ErrNotConfigured.
func New(apiKey string) *Provider {
	return &Provider{apiKey: apiKey}
}

// Call makes a synchronous LLM request. Currently returns ErrNotConfigured
// if no API key is set; a full implementation will be added in a future phase.
func (p *Provider) Call(_ context.Context, _ engine.LLMRequest) (*engine.LLMResponse, error) {
	if p.apiKey == "" {
		return nil, ErrNotConfigured
	}
	return nil, errors.New("anthropic provider: full implementation not yet available")
}

// StreamCall makes a streaming LLM request. Currently returns ErrNotConfigured
// if no API key is set; a full implementation will be added in a future phase.
func (p *Provider) StreamCall(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	if p.apiKey == "" {
		return nil, ErrNotConfigured
	}
	return nil, errors.New("anthropic provider: full implementation not yet available")
}
