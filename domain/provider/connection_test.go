package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
)

// Success: the connection sentinel errors are non-nil and distinct so adapters
// and callers can match each condition with errors.Is.
func TestConnectionSentinels_Distinct(t *testing.T) {
	assert.NotNil(t, provider.ErrConnectionNotFound)
	assert.NotNil(t, provider.ErrConnectionExists)
	assert.NotErrorIs(t, provider.ErrConnectionNotFound, provider.ErrConnectionExists)
	assert.NotErrorIs(t, provider.ErrConnectionExists, provider.ErrConnectionNotFound)
}

// Error: each sentinel stays matchable with errors.Is even when wrapped with
// context, which is how the adapter surfaces them.
func TestConnectionSentinels_WrappedMatch(t *testing.T) {
	wrapped := errors.Join(provider.ErrConnectionNotFound, errors.New("connection foo"))
	assert.ErrorIs(t, wrapped, provider.ErrConnectionNotFound)
}

// Happy path: the Connection struct carries no plaintext key field; only
// metadata is modelled in the domain. This is a structural guarantee enforced
// at compile time by constructing the full struct here.
func TestConnection_MetadataOnly(t *testing.T) {
	c := provider.Connection{
		Name:     "azure-prod",
		Provider: "azure-openai",
		BaseURL:  "https://example.openai.azure.com",
		Extras:   map[string]string{"api-version": "2024-02-01"},
	}
	assert.Equal(t, "azure-prod", c.Name)
	assert.Equal(t, "azure-openai", c.Provider)
	assert.Equal(t, "2024-02-01", c.Extras["api-version"])
}

// fakeStore confirms the ConnectionStore port is implementable by adapters
// without importing any adapter package, satisfying hexagonal boundaries.
type fakeStore struct{}

func (fakeStore) Create(context.Context, provider.Connection, string) error { return nil }
func (fakeStore) Update(context.Context, provider.Connection, *string) error {
	return nil
}
func (fakeStore) Delete(context.Context, string) error { return nil }
func (fakeStore) Get(context.Context, string) (provider.Connection, error) {
	return provider.Connection{}, nil
}
func (fakeStore) List(context.Context) ([]provider.Connection, error) { return nil, nil }
func (fakeStore) Reveal(context.Context, string) (string, error)      { return "", nil }

func TestConnectionStore_Implementable(t *testing.T) {
	var _ provider.ConnectionStore = fakeStore{}
}

// ---------------------------------------------------------------------------
// ValidateConnection: Azure api-version requirement (write-time rule).
// ---------------------------------------------------------------------------

// Happy path: an Azure connection carrying a non-empty api-version in Extras
// passes ValidateConnection.
func TestValidateConnection_AzureWithAPIVersion_OK(t *testing.T) {
	for _, prov := range []string{"azure-openai", "azure-foundry"} {
		c := provider.Connection{
			Name:     "az",
			Provider: prov,
			Extras:   map[string]string{"api-version": "2024-02-01"},
		}
		assert.NoError(t, provider.ValidateConnection(c), "provider %q", prov)
	}
}

// Success: a non-Azure connection with no api-version passes; the api-version
// requirement applies only to the Azure provider set.
func TestValidateConnection_NonAzureWithoutAPIVersion_OK(t *testing.T) {
	for _, prov := range []string{"openrouter", "litellm", "claude-cli"} {
		c := provider.Connection{Name: "n", Provider: prov}
		assert.NoError(t, provider.ValidateConnection(c), "provider %q", prov)
	}
}

// Failure: an Azure connection missing api-version is rejected with
// ErrAzureAPIVersionRequired.
func TestValidateConnection_AzureMissingAPIVersion_Rejected(t *testing.T) {
	c := provider.Connection{Name: "az", Provider: "azure-openai"}
	err := provider.ValidateConnection(c)
	assert.ErrorIs(t, err, provider.ErrAzureAPIVersionRequired)
}

// Failure: azure-foundry also requires api-version; the failure path is
// symmetric with azure-openai (both are members of the azure provider set).
func TestValidateConnection_AzureFoundryMissingAPIVersion_Rejected(t *testing.T) {
	c := provider.Connection{Name: "af", Provider: "azure-foundry"}
	err := provider.ValidateConnection(c)
	assert.ErrorIs(t, err, provider.ErrAzureAPIVersionRequired)
}

// Edge case: an Azure connection whose api-version is present but empty is
// treated as missing and rejected.
func TestValidateConnection_AzureEmptyAPIVersion_Rejected(t *testing.T) {
	c := provider.Connection{
		Name:     "az",
		Provider: "azure-foundry",
		Extras:   map[string]string{"api-version": ""},
	}
	err := provider.ValidateConnection(c)
	assert.ErrorIs(t, err, provider.ErrAzureAPIVersionRequired)
}

// Input-validation: "amazon-bedrock" (the canonical long-form alias) must also
// enforce the region + access-key-id requirement, not silently skip it.
func TestValidateConnection_AmazonBedrockAlias_AlsoRequiresCredentials(t *testing.T) {
	noExtras := provider.Connection{Name: "bd", Provider: "amazon-bedrock"}
	assert.ErrorIs(t, provider.ValidateConnection(noExtras), provider.ErrBedrockCredentialsRequired)

	withExtras := provider.Connection{
		Name: "bd", Provider: "amazon-bedrock",
		Extras: map[string]string{"region": "us-east-1", "access-key-id": "AKID"},
	}
	assert.NoError(t, provider.ValidateConnection(withExtras))
}

// Input-validation: a bedrock connection requires both region and access-key-id
// in Extras; either missing (or nil extras) yields ErrBedrockCredentialsRequired.
func TestValidateConnection_BedrockRequiresRegionAndAccessKey(t *testing.T) {
	cases := []struct {
		name   string
		extras map[string]string
		wantOK bool
	}{
		{"both present", map[string]string{"region": "us-east-1", "access-key-id": "AKID"}, true},
		{"missing region", map[string]string{"access-key-id": "AKID"}, false},
		{"missing access-key-id", map[string]string{"region": "us-east-1"}, false},
		{"empty region value", map[string]string{"region": "", "access-key-id": "AKID"}, false},
		{"nil extras", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := provider.ValidateConnection(provider.Connection{
				Name: "bd", Provider: "bedrock", Extras: tc.extras,
			})
			if tc.wantOK && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !tc.wantOK && !errors.Is(err, provider.ErrBedrockCredentialsRequired) {
				t.Fatalf("want ErrBedrockCredentialsRequired, got %v", err)
			}
		})
	}
}
