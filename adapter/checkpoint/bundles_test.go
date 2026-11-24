// adapter/checkpoint/bundles_test.go
package checkpoint_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ runner.BundleStore = (*checkpoint.BundleStore)(nil)

func refFor(data []byte) engine.ScriptBundleRef {
	sum := sha256.Sum256(data)
	return engine.ScriptBundleRef{Hash: hex.EncodeToString(sum[:]), Entrypoint: "run.sh"}
}

func TestBundleStore_PutOpenHas(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "bundles")
	s, err := checkpoint.NewBundleStore(dir)
	require.NoError(t, err)
	data := []byte("bundle-bytes")
	ref := refFor(data)

	ok, err := s.Has(ctx, ref.Hash)
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, s.Put(ctx, ref, data))
	require.NoError(t, s.Put(ctx, ref, data), "re-putting identical content is idempotent")
	ok, err = s.Has(ctx, ref.Hash)
	require.NoError(t, err)
	assert.True(t, ok)

	rc, err := s.Open(ctx, ref.Hash)
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	assert.Equal(t, data, got)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp files left behind")
	assert.Equal(t, ref.Hash+".tar.gz", entries[0].Name())
}

func TestBundleStore_RejectsHashMismatchAndBadHashes(t *testing.T) {
	ctx := context.Background()
	s, err := checkpoint.NewBundleStore(t.TempDir())
	require.NoError(t, err)
	ref := refFor([]byte("one"))
	require.ErrorIs(t, s.Put(ctx, ref, []byte("two")), runner.ErrBundleHashMismatch)

	_, err = s.Open(ctx, strings.Repeat("0", 64))
	require.ErrorIs(t, err, runner.ErrBundleNotFound)
	_, err = s.Open(ctx, "../../secret")
	require.ErrorIs(t, err, runner.ErrBundleNotFound, "a malformed hash never reaches the filesystem")
}
