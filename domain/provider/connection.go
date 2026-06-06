package provider

import (
	"context"
	"errors"
	"regexp"
)

// Connection binds an endpoint to its credential. model-id is deliberately
// excluded so one connection fronts many models/profiles. The plaintext API key
// is never carried on this struct: it enters only through ConnectionStore.Create
// /Update and leaves only through ConnectionStore.Reveal.
type Connection struct {
	Name     string            // primary key
	Provider string            // "azure-openai" | "azure-foundry" | "openrouter" | "litellm" | ...
	BaseURL  string            //
	Extras   map[string]string // e.g. api-version (Azure), region
	// No APIKey field: the plaintext key is never carried on this struct.
}

// ConnectionStore is the driven port for persisting connections and their
// sealed API keys. Implementations encrypt the key before it touches storage;
// Get and List return metadata only, and Reveal is the sole decryption path.
type ConnectionStore interface {
	// Create persists a new connection, sealing apiKey before it is stored.
	// A duplicate name returns ErrConnectionExists.
	Create(ctx context.Context, c Connection, apiKey string) error
	// Update changes a connection's metadata. A nil apiKey leaves the stored
	// key unchanged; a non-nil apiKey re-seals and replaces it. A missing
	// connection returns ErrConnectionNotFound.
	Update(ctx context.Context, c Connection, apiKey *string) error
	// Delete removes a connection. A missing connection returns
	// ErrConnectionNotFound.
	Delete(ctx context.Context, name string) error
	// Get returns connection metadata only; it never returns key material. A
	// missing connection returns ErrConnectionNotFound.
	Get(ctx context.Context, name string) (Connection, error)
	// List returns all connections' metadata only, ordered by name.
	List(ctx context.Context) ([]Connection, error)
	// Reveal is the sole decryption entry point: it returns the plaintext API
	// key for the named connection. A missing connection returns
	// ErrConnectionNotFound; a tampered ciphertext returns ErrDecrypt.
	Reveal(ctx context.Context, name string) (apiKey string, err error)
}

// ErrConnectionNotFound is returned when a named connection does not exist.
var ErrConnectionNotFound = errors.New("connection not found")

// ErrConnectionExists is returned when creating a connection whose name is
// already taken.
var ErrConnectionExists = errors.New("connection already exists")

// ErrAzureAPIVersionRequired is returned by ValidateConnection when an Azure
// provider connection lacks a non-empty "api-version" entry in Extras.
var ErrAzureAPIVersionRequired = errors.New("azure connections require an api-version in extras")

// ErrBedrockCredentialsRequired is returned by ValidateConnection when a bedrock
// connection lacks a non-empty "region" or "access-key-id" entry in Extras.
var ErrBedrockCredentialsRequired = errors.New("bedrock connections require region and access-key-id in extras")

// ErrBedrockInvalidRegion is returned when a bedrock connection's "region" is
// not a well-formed AWS region. SECURITY: the region is interpolated into the
// request host (bedrock-runtime.{region}.amazonaws.com) on a path the BaseURL
// SSRF policy does not cover, so a malformed/injection value could redirect a
// credential-bearing request to an attacker host.
var ErrBedrockInvalidRegion = errors.New("bedrock region is not a well-formed AWS region")

// bedrockRegionRE anchors an AWS region: two lowercase letters, one or more
// lowercase-letter segments (covers gov/iso multi-segment forms such as
// us-gov-west-1), and a trailing numeric suffix — e.g. us-east-1,
// ap-southeast-2, us-gov-west-1. Anchored end-to-end so an embedded dot, slash,
// space, or newline (host/path/SSRF smuggling) is rejected.
var bedrockRegionRE = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d+$`)

// ValidBedrockRegion reports whether region is a well-formed AWS region. It is
// the single source of truth for the rule enforced at connection write time
// (ValidateConnection); adapters reuse it as a defensive guard immediately
// before interpolating the region into a request host.
func ValidBedrockRegion(region string) bool {
	return bedrockRegionRE.MatchString(region)
}

// azureProviders is the set of providers that require an api-version in Extras.
// Centralized so later phases can extend the Azure set in one place.
var azureProviders = map[string]bool{
	"azure-openai":  true,
	"azure-foundry": true,
}

// bedrockProviders is the set of providers that require region and
// access-key-id in Extras. Includes both the short alias "bedrock" and the
// canonical long-form "amazon-bedrock" so either spelling enforces the rule.
var bedrockProviders = map[string]bool{
	"bedrock":        true,
	"amazon-bedrock": true,
}

// ValidateConnection enforces provider-specific requirements at create/update
// time (not at workflow validate time). Azure providers ("azure-openai",
// "azure-foundry") require a non-empty "api-version" entry in Extras; an absent
// or empty value returns ErrAzureAPIVersionRequired. Bedrock providers require a
// non-empty "region" + "access-key-id" (ErrBedrockCredentialsRequired) and a
// well-formed region (ErrBedrockInvalidRegion) since the region is interpolated
// into the request host. Other providers pass with no extras requirement.
func ValidateConnection(c Connection) error {
	if azureProviders[c.Provider] {
		if c.Extras["api-version"] == "" {
			return ErrAzureAPIVersionRequired
		}
	}
	if bedrockProviders[c.Provider] {
		if c.Extras["region"] == "" || c.Extras["access-key-id"] == "" {
			return ErrBedrockCredentialsRequired
		}
		// SECURITY: the region is interpolated into the request host on a path the
		// BaseURL SSRF policy does not cover; reject anything that is not a
		// well-formed AWS region at write time.
		if !ValidBedrockRegion(c.Extras["region"]) {
			return ErrBedrockInvalidRegion
		}
	}
	return nil
}
