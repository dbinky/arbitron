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
