package provider_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
)

// Canonicalize collapses the documented provider aliases to ONE canonical id so
// every selector (chat/agent/probe factories, opencode) dispatches on the same
// vocabulary and cannot drift apart. It normalizes case/whitespace first.
func TestCanonicalize(t *testing.T) {
	cases := []struct{ in, want string }{
		// bedrock family
		{"bedrock", "bedrock"},
		{"amazon-bedrock", "bedrock"},
		{"  Amazon-Bedrock ", "bedrock"},
		// anthropic-api family
		{"anthropic-api", "anthropic-api"},
		{"anthropic", "anthropic-api"},
		{"ANTHROPIC", "anthropic-api"},
		// azure trio
		{"azure-openai", "azure"},
		{"azure-foundry", "azure"},
		{"azure", "azure"},
		{" Azure-Foundry ", "azure"},
		// pass-through (normalized) for ids with no alias collapse
		{"openai", "openai"},
		{"openrouter", "openrouter"},
		{"litellm", "litellm"},
		{"ollama", "ollama"},
		{"ollama-cloud", "ollama-cloud"},
		{"claude-cli", "claude-cli"},
		{"  OpenAI ", "openai"},
		{"", ""},
		{"   ", ""},
		{"mystery", "mystery"},
	}
	for _, tc := range cases {
		if got := provider.Canonicalize(tc.in); got != tc.want {
			t.Errorf("Canonicalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
