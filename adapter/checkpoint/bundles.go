// adapter/checkpoint/bundles.go
package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
)

// BundleStore is the filesystem runner.BundleStore: <dir>/<hash>.tar.gz,
// written via temp file + rename so a reader never sees a partial bundle.
type BundleStore struct{ dir string }

var _ runner.BundleStore = (*BundleStore)(nil)

// NewBundleStore creates dir (0700) if needed.
func NewBundleStore(dir string) (*BundleStore, error) {
	if dir == "" {
		return nil, errors.New("checkpoint: bundle directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("checkpoint: bundle directory: %w", err)
	}
	return &BundleStore{dir: dir}, nil
}

func (s *BundleStore) path(hash string) string { return filepath.Join(s.dir, hash+".tar.gz") }

func (s *BundleStore) Put(_ context.Context, ref engine.ScriptBundleRef, data []byte) error {
	if !runner.ValidBundleHash(ref.Hash) {
		return fmt.Errorf("%w: malformed hash", runner.ErrBundleHashMismatch)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != ref.Hash {
		return runner.ErrBundleHashMismatch
	}
	if _, err := os.Stat(s.path(ref.Hash)); err == nil {
		return nil
	}
	tmp, err := os.CreateTemp(s.dir, ".bundle-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(ref.Hash))
}

func (s *BundleStore) Open(_ context.Context, hash string) (io.ReadCloser, error) {
	if !runner.ValidBundleHash(hash) {
		return nil, runner.ErrBundleNotFound
	}
	f, err := os.Open(s.path(hash))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, runner.ErrBundleNotFound
	}
	return f, err
}

func (s *BundleStore) Has(_ context.Context, hash string) (bool, error) {
	if !runner.ValidBundleHash(hash) {
		return false, nil
	}
	_, err := os.Stat(s.path(hash))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
