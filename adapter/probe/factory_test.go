package probe

import (
	"testing"

	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Happy-path: ProbeFor dispatches every known provider string to the right
// concrete probe, purely on binding.Provider (Kind/Model untouched).
func TestProbeFactory_ProbeFor_DispatchesByProvider(t *testing.T) {
	f := NewProbeFactory(Deps{})
	cases := []struct {
		provider string
		want     domprovider.ConnectionProbe
	}{
		{"openai", (*openAICompatProbe)(nil)},
		{"openrouter", (*openAICompatProbe)(nil)},
		{"lunaroute", (*lunaRouteProbe)(nil)},
		{"litellm", (*openAICompatProbe)(nil)},
		{"ollama", (*openAICompatProbe)(nil)},
		{"ollama-cloud", (*openAICompatProbe)(nil)},
		{"azure-openai", (*openAICompatProbe)(nil)},
		{"azure-foundry", (*openAICompatProbe)(nil)},
		{"anthropic-api", (*anthropicProbe)(nil)},
		{"bedrock", (*bedrockProbe)(nil)},
		{"amazon-bedrock", (*bedrockProbe)(nil)},
		{"", (*claudeCLIProbe)(nil)},
		{"claude-cli", (*claudeCLIProbe)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			// Set Kind+Model to prove they are never consulted in dispatch.
			p, err := f.ProbeFor(domprovider.ResolvedBinding{
				Provider: tc.provider,
				Kind:     domprovider.ProfileKindAgent,
				Model:    "should-be-ignored",
			})
			require.NoError(t, err)
			assert.IsType(t, tc.want, p)
		})
	}
}

// Input-validation: an unknown provider returns ErrNoProbeForProvider and names
// the offending provider for operator diagnostics.
func TestProbeFactory_ProbeFor_UnknownProvider_Errors(t *testing.T) {
	f := NewProbeFactory(Deps{})
	p, err := f.ProbeFor(domprovider.ResolvedBinding{Provider: "made-up"})
	require.Error(t, err)
	assert.Nil(t, p)
	assert.ErrorIs(t, err, domprovider.ErrNoProbeForProvider)
	assert.Contains(t, err.Error(), "made-up")
}

// SECURITY — control chars in a provider string must be %q-escaped in the error
// so they cannot inject newlines into logs (same property as factory / env / args).
func TestProbeFactory_ProbeFor_UnknownProvider_ControlCharsSafelyQuoted(t *testing.T) {
	f := NewProbeFactory(Deps{})
	_, err := f.ProbeFor(domprovider.ResolvedBinding{Provider: "foo\nmalicious"})
	require.ErrorIs(t, err, domprovider.ErrNoProbeForProvider)
	assert.NotContains(t, err.Error(), "\n",
		"raw newline must not appear in the error message")
	assert.Contains(t, err.Error(), `\n`,
		"escaped \\n should appear via %q-quoted provider string")
}

// SECURITY — scrubControl replaces all ASCII control chars with spaces so upstream
// snippets cannot inject log escape sequences. These are the same cases tested in
// the bedrock and openrouter adapter scrubControl unit tests.
func TestProbeScrubControl(t *testing.T) {
	cases := []struct {
		input, want, desc string
	}{
		{"", "", "empty"},
		{"hello", "hello", "printable ASCII unchanged"},
		{"a\x00b", "a b", "NUL → space"},
		{"a\tb", "a b", "TAB → space"},
		{"a\nb", "a b", "LF → space"},
		{"a\rb", "a b", "CR → space"},
		{"a\x1bb", "a b", "ESC → space"},
		{"a\x7fb", "a b", "DEL → space"},
		{"\x00\t\n\r\x1b\x7f", "      ", "sequence all → space"},
		{"café", "café", "valid UTF-8 preserved"},
		{"a b", "a b", "0x20 (space) preserved"},
	}
	for _, c := range cases {
		got := scrubControl(c.input)
		if got != c.want {
			t.Errorf("scrubControl(%q) = %q, want %q (%s)", c.input, got, c.want, c.desc)
		}
	}
}

// Input-validation: provider matching is case- and whitespace-insensitive.
func TestProbeFactory_ProbeFor_NormalizesCaseWhitespace(t *testing.T) {
	f := NewProbeFactory(Deps{})

	p, err := f.ProbeFor(domprovider.ResolvedBinding{Provider: "  Anthropic-API "})
	require.NoError(t, err)
	assert.IsType(t, (*anthropicProbe)(nil), p)

	p, err = f.ProbeFor(domprovider.ResolvedBinding{Provider: "  Amazon-Bedrock"})
	require.NoError(t, err)
	assert.IsType(t, (*bedrockProbe)(nil), p)

	p, err = f.ProbeFor(domprovider.ResolvedBinding{Provider: "OPENAI"})
	require.NoError(t, err)
	assert.IsType(t, (*openAICompatProbe)(nil), p)
}
