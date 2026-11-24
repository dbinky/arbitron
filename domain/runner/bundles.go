// domain/runner/bundles.go
package runner

import (
	"context"
	"errors"
	"io"

	"github.com/arbitron/arbitron/domain/engine"
)

// BundleStore holds content-addressed script bundles (D9). The server writes a
// bundle at dispatch; runners download it through the bundle transfer route.
type BundleStore interface {
	// Put stores data under ref.Hash; it verifies sha256(data) == ref.Hash and
	// is idempotent for identical content.
	Put(ctx context.Context, ref engine.ScriptBundleRef, data []byte) error
	// Open returns the bundle bytes; ErrBundleNotFound when absent or when hash
	// is not a canonical 64-character lower-case hex digest.
	Open(ctx context.Context, hash string) (io.ReadCloser, error)
	Has(ctx context.Context, hash string) (bool, error)
}

var (
	ErrBundleNotFound     = errors.New("runner: script bundle not found")
	ErrBundleHashMismatch = errors.New("runner: script bundle content does not match its hash")
)

// ValidBundleHash reports whether h is a canonical lower-case SHA-256 hex digest.
func ValidBundleHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
