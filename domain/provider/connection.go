package provider

import (
	"context"
	"errors"
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

// azureProviders is the set of providers that require an api-version in Extras.
// Centralized so later phases can extend the Azure set in one place.
var azureProviders = map[string]bool{
	"azure-openai":  true,
	"azure-foundry": true,
}

// ValidateConnection enforces provider-specific requirements at create/update
// time (not at workflow validate time). Azure providers ("azure-openai",
// "azure-foundry") require a non-empty "api-version" entry in Extras; an absent
// or empty value returns ErrAzureAPIVersionRequired. Non-Azure providers pass
// with no extras requirement.
func ValidateConnection(c Connection) error {
	if azureProviders[c.Provider] {
		if c.Extras["api-version"] == "" {
			return ErrAzureAPIVersionRequired
		}
	}
	return nil
}
