package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/adapter/runneragent/transfer"
	"github.com/arbitron/arbitron/domain/runner"
)

// BundleCache keeps verified bundle archives by hash under dir
// (~/.arbitron-runner/bundles) and unpacks a fresh copy per job, so a script
// that writes into its cwd never alters the cached bundle.
type BundleCache struct {
	dir string
	t   transfer.Transfer
	mu  sync.Mutex
}

func NewBundleCache(dir string, t transfer.Transfer) (*BundleCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &BundleCache{dir: dir, t: t}, nil
}

func (c *BundleCache) archive(ctx context.Context, hash, token string) (string, error) {
	if !runner.ValidBundleHash(hash) {
		return "", errors.New("bundle cache: malformed hash")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p := filepath.Join(c.dir, hash+".tar.gz")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	rc, err := c.t.FetchBundle(ctx, token, hash)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, process.MaxBundleBytes+1))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return "", fmt.Errorf("bundle cache: %w", runner.ErrBundleHashMismatch)
	}
	tmp, err := os.CreateTemp(c.dir, ".bundle-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return p, os.Rename(tmp.Name(), p)
}

// Unpack returns a fresh per-job directory holding the bundle's files.
func (c *BundleCache) Unpack(ctx context.Context, hash, token string) (string, func(), error) {
	p, err := c.archive(ctx, hash, token)
	if err != nil {
		return "", nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(c.dir, "run-"+hash[:12]+"-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := process.ExtractScriptBundle(bytes.NewReader(data), dir); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}
