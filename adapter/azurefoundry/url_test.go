package azurefoundry

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
)

// endpointURL is the single place topology selection lives. These table tests
// exercise it directly for both Azure topologies, driven purely by the binding
// fields (Extras / BaseURL / Model) with NO provider-name branching.
func TestEndpointURL(t *testing.T) {
	cases := []struct {
		name    string
		binding provider.ResolvedBinding
		want    string
		wantErr error
	}{
		{
			name: "resource topology builds deployment path with api-version",
			binding: provider.ResolvedBinding{
				BaseURL: "https://my-resource.openai.azure.com",
				Model:   "gpt-4o-deploy",
				Extras:  map[string]string{"api-version": "2024-08-01-preview"},
			},
			want: "https://my-resource.openai.azure.com/openai/deployments/gpt-4o-deploy/chat/completions?api-version=2024-08-01-preview",
		},
		{
			name: "resource topology trims trailing slash on base-url",
			binding: provider.ResolvedBinding{
				BaseURL: "https://my-resource.openai.azure.com/",
				Model:   "gpt-4o-deploy",
				Extras:  map[string]string{"api-version": "2024-08-01-preview"},
			},
			want: "https://my-resource.openai.azure.com/openai/deployments/gpt-4o-deploy/chat/completions?api-version=2024-08-01-preview",
		},
		{
			name: "serverless topology appends chat-completions suffix",
			binding: provider.ResolvedBinding{
				BaseURL: "https://llama-serverless.eastus.models.ai.azure.com",
			},
			want: "https://llama-serverless.eastus.models.ai.azure.com/chat/completions",
		},
		{
			name: "serverless topology trims trailing slash before suffix",
			binding: provider.ResolvedBinding{
				BaseURL: "https://llama-serverless.eastus.models.ai.azure.com/",
			},
			want: "https://llama-serverless.eastus.models.ai.azure.com/chat/completions",
		},
		{
			name: "serverless topology does not double-suffix",
			binding: provider.ResolvedBinding{
				BaseURL: "https://llama-serverless.eastus.models.ai.azure.com/chat/completions",
			},
			want: "https://llama-serverless.eastus.models.ai.azure.com/chat/completions",
		},
		{
			name: "resource topology missing api-version errors",
			binding: provider.ResolvedBinding{
				BaseURL: "https://my-resource.openai.azure.com",
				Model:   "gpt-4o-deploy",
				Extras:  map[string]string{"api-version": ""},
			},
			// api-version present but empty in a resource-shaped binding: treated
			// as serverless because the version is what triggers resource mode.
			want: "https://my-resource.openai.azure.com/chat/completions",
		},
		{
			name: "resource topology with api-version but empty model errors",
			binding: provider.ResolvedBinding{
				BaseURL: "https://my-resource.openai.azure.com",
				Model:   "",
				Extras:  map[string]string{"api-version": "2024-08-01-preview"},
			},
			wantErr: ErrMissingDeployment,
		},
		{
			name: "empty base-url errors",
			binding: provider.ResolvedBinding{
				BaseURL: "",
			},
			wantErr: ErrMissingBaseURL,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := endpointURL(tc.binding)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected error %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("endpointURL = %q, want %q", got, tc.want)
			}
		})
	}
}
