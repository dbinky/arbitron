package opencode

import "testing"

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
