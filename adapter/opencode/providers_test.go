package opencode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustDesc resolves a provider's descriptor for a test, failing if it is unknown.
// The builders (buildArgs/buildConfig/opencodeEnv) now take a pre-resolved
// descriptor — RunAgent resolves the provider ONCE and threads it down — so the
// per-builder tests resolve through this helper instead of passing a provider
// string. Unknown-provider fail-closed behaviour is covered at the RunAgent level
// (provider_test.go) and via descriptorFor (TestDescriptorFor_UnknownFailsClosed).
func mustDesc(t *testing.T, prov string) providerDescriptor {
	t.Helper()
	d, ok := descriptorFor(prov)
	require.True(t, ok, "descriptorFor(%q) must resolve in test", prov)
	return d
}

func TestDescriptorFor(t *testing.T) {
	cases := []struct {
		arbitron     string
		opencodeID   string
		credEnvVar   string
		alwaysConfig bool
		keyless      bool
		aws          bool
	}{
		{"anthropic", "anthropic", "ANTHROPIC_API_KEY", false, false, false},
		{"anthropic-api", "anthropic", "ANTHROPIC_API_KEY", false, false, false},
		{"openai", "openai", "OPENAI_API_KEY", false, false, false},
		{"litellm", "litellm", "OPENAI_API_KEY", true, false, false},
		{"openrouter", "openrouter", "OPENROUTER_API_KEY", false, false, false},
		{"azure", "azure", "AZURE_API_KEY", true, false, false},
		{"azure-openai", "azure", "AZURE_API_KEY", true, false, false},
		{"bedrock", "amazon-bedrock", "", false, false, true},
		{"amazon-bedrock", "amazon-bedrock", "", false, false, true},
		{"ollama", "ollama", "", true, true, false},
		{"ollama-cloud", "ollama-cloud", "OLLAMA_API_KEY", false, false, false},
		{"  Ollama-Cloud ", "ollama-cloud", "OLLAMA_API_KEY", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.arbitron, func(t *testing.T) {
			d, ok := descriptorFor(tc.arbitron)
			if !ok {
				t.Fatalf("provider %q must map", tc.arbitron)
			}
			if d.opencodeID != tc.opencodeID || d.credEnvVar != tc.credEnvVar ||
				d.alwaysConfig != tc.alwaysConfig || d.keyless != tc.keyless || d.aws != tc.aws {
				t.Fatalf("descriptor mismatch for %q: %+v", tc.arbitron, d)
			}
		})
	}
}

func TestDescriptorFor_UnknownFailsClosed(t *testing.T) {
	for _, p := range []string{"", "mystery", "claude-cli"} {
		if _, ok := descriptorFor(p); ok {
			t.Fatalf("provider %q must NOT map (fail-closed)", p)
		}
	}
}

// SECURITY — the shipped descriptor table must contain no fail-OPEN auth
// combinations: a `keyless:true` row with a non-empty credEnvVar would launch a
// supposedly-keyed provider unauthenticated (EmptyKeyAllowBaseURLOnly), and an
// `aws:true` row must not carry a credEnvVar. This guards the one-line table edit
// the architect/security reviewers flagged.
func TestValidateDescriptors_ShippedTableIsWellFormed(t *testing.T) {
	require.NoError(t, validateDescriptors(), "shipped descriptor table must have no fail-open auth rows")
}

// validateDescriptors must REJECT the illegal combinations so the guard is proven
// to catch a regressing edit, not vacuously pass.
func TestValidateDescriptors_RejectsIllegalRows(t *testing.T) {
	cases := map[string]providerDescriptor{
		"keyless+credEnvVar": {opencodeID: "x", credEnvVar: "X_API_KEY", keyless: true},
		"aws+credEnvVar":     {opencodeID: "x", credEnvVar: "X_API_KEY", aws: true},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			orig := providerDescriptors
			restore := make(map[string]providerDescriptor, len(orig))
			for k, v := range orig {
				restore[k] = v
			}
			providerDescriptors = map[string]providerDescriptor{"__bad__": bad}
			t.Cleanup(func() { providerDescriptors = restore })

			err := validateDescriptors()
			require.Error(t, err, "illegal row must be rejected")
			assert.Contains(t, err.Error(), "__bad__")
		})
	}
}
