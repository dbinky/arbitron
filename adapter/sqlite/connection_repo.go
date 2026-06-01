package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/provider"
)

// ConnectionRepo implements provider.ConnectionStore backed by SQLite. Every
// API key is sealed via the injected provider.Crypto before it touches storage;
// the plaintext key is never written to a non-cipher column and never logged.
type ConnectionRepo struct {
	db      *sql.DB
	queries *generated.Queries
	crypto  provider.Crypto
}

// Compile-time assertion that ConnectionRepo satisfies the domain port.
var _ provider.ConnectionStore = (*ConnectionRepo)(nil)

// NewConnectionRepo creates a ConnectionRepo using the given database
// connection and Crypto. The Crypto seals keys on write and is the only path
// that decrypts them on Reveal.
func NewConnectionRepo(db *sql.DB, crypto provider.Crypto) *ConnectionRepo {
	return &ConnectionRepo{
		db:      db,
		queries: generated.New(db),
		crypto:  crypto,
	}
}

// isUniqueViolation reports whether err is a SQLite primary-key / unique
// constraint failure, used to map a duplicate name to ErrConnectionExists.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "PRIMARY KEY")
}

func (r *ConnectionRepo) Create(ctx context.Context, c provider.Connection, apiKey string) error {
	extrasJSON, err := extrasToJSON(c.Extras)
	if err != nil {
		return fmt.Errorf("marshalling extras: %w", err)
	}
	// apiKey is consumed only by Seal here; it is never placed in any other
	// parameter and never logged.
	cipher, nonce, err := r.crypto.Seal([]byte(apiKey))
	if err != nil {
		return fmt.Errorf("sealing api key for connection %q: %w", c.Name, err)
	}

	err = r.queries.CreateConnection(ctx, generated.CreateConnectionParams{
		Name:         c.Name,
		Provider:     c.Provider,
		BaseURL:      c.BaseURL,
		Extras:       extrasJSON,
		APIKeyCipher: cipher,
		APIKeyNonce:  nonce,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("connection %q: %w", c.Name, provider.ErrConnectionExists)
		}
		return fmt.Errorf("creating connection %q: %w", c.Name, err)
	}
	return nil
}

func (r *ConnectionRepo) Update(ctx context.Context, c provider.Connection, apiKey *string) error {
	// Pre-check existence so a missing connection maps to the sentinel rather
	// than silently affecting zero rows.
	if _, err := r.queries.GetConnectionMeta(ctx, c.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("connection %q: %w", c.Name, provider.ErrConnectionNotFound)
		}
		return fmt.Errorf("checking connection %q: %w", c.Name, err)
	}

	extrasJSON, err := extrasToJSON(c.Extras)
	if err != nil {
		return fmt.Errorf("marshalling extras: %w", err)
	}

	if apiKey == nil {
		// nil key: leave the sealed key columns untouched.
		err = r.queries.UpdateConnectionMetadata(ctx, generated.UpdateConnectionMetadataParams{
			Provider: c.Provider,
			BaseURL:  c.BaseURL,
			Extras:   extrasJSON,
			Name:     c.Name,
		})
		if err != nil {
			return fmt.Errorf("updating connection %q: %w", c.Name, err)
		}
		return nil
	}

	// *apiKey is consumed only by Seal here; never logged or placed elsewhere.
	cipher, nonce, err := r.crypto.Seal([]byte(*apiKey))
	if err != nil {
		return fmt.Errorf("sealing api key for connection %q: %w", c.Name, err)
	}
	err = r.queries.UpdateConnectionWithKey(ctx, generated.UpdateConnectionWithKeyParams{
		Provider:     c.Provider,
		BaseURL:      c.BaseURL,
		Extras:       extrasJSON,
		APIKeyCipher: cipher,
		APIKeyNonce:  nonce,
		Name:         c.Name,
	})
	if err != nil {
		return fmt.Errorf("updating connection %q: %w", c.Name, err)
	}
	return nil
}

func (r *ConnectionRepo) Delete(ctx context.Context, name string) error {
	// Pre-check existence so deleting a missing connection maps to the sentinel.
	if _, err := r.queries.GetConnectionMeta(ctx, name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("connection %q: %w", name, provider.ErrConnectionNotFound)
		}
		return fmt.Errorf("checking connection %q: %w", name, err)
	}
	if err := r.queries.DeleteConnection(ctx, name); err != nil {
		return fmt.Errorf("deleting connection %q: %w", name, err)
	}
	return nil
}

func (r *ConnectionRepo) Get(ctx context.Context, name string) (provider.Connection, error) {
	row, err := r.queries.GetConnectionMeta(ctx, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return provider.Connection{}, fmt.Errorf("connection %q: %w", name, provider.ErrConnectionNotFound)
		}
		return provider.Connection{}, fmt.Errorf("getting connection %q: %w", name, err)
	}
	extras, err := extrasFromJSON(row.Extras)
	if err != nil {
		return provider.Connection{}, fmt.Errorf("unmarshalling extras for connection %q: %w", name, err)
	}
	return provider.Connection{
		Name:     row.Name,
		Provider: row.Provider,
		BaseURL:  row.BaseURL,
		Extras:   extras,
	}, nil
}

func (r *ConnectionRepo) List(ctx context.Context) ([]provider.Connection, error) {
	rows, err := r.queries.ListConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing connections: %w", err)
	}
	conns := make([]provider.Connection, len(rows))
	for i, row := range rows {
		extras, err := extrasFromJSON(row.Extras)
		if err != nil {
			return nil, fmt.Errorf("unmarshalling extras for connection %q: %w", row.Name, err)
		}
		conns[i] = provider.Connection{
			Name:     row.Name,
			Provider: row.Provider,
			BaseURL:  row.BaseURL,
			Extras:   extras,
		}
	}
	return conns, nil
}

// Reveal is the sole decryption entry point. The returned plaintext is never
// logged here and never persisted; callers hand it straight to a request
// builder or subprocess env.
func (r *ConnectionRepo) Reveal(ctx context.Context, name string) (string, error) {
	row, err := r.queries.GetConnectionCipher(ctx, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("connection %q: %w", name, provider.ErrConnectionNotFound)
		}
		return "", fmt.Errorf("getting cipher for connection %q: %w", name, err)
	}
	plaintext, err := r.crypto.Open(row.APIKeyCipher, row.APIKeyNonce)
	if err != nil {
		// Reference the connection name only; never log the cipher or key.
		return "", fmt.Errorf("revealing connection %q: %w", name, err)
	}
	return string(plaintext), nil
}

// Count returns the number of stored connections. It feeds the boot gate that
// requires a master key whenever any connection exists.
func (r *ConnectionRepo) Count(ctx context.Context) (int, error) {
	n, err := r.queries.CountConnections(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting connections: %w", err)
	}
	return int(n), nil
}
