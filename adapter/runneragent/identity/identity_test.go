package identity

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStoreCreatesKeyOnceAndReusesIt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "machine_ed25519")
	k1, err := New(p).Key(context.Background())
	require.NoError(t, err)
	require.Len(t, k1, ed25519.PrivateKeySize)

	info, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	k2, err := New(p).Key(context.Background())
	require.NoError(t, err)
	assert.True(t, k1.Equal(k2), "a restarted runner proves the same machine")

	other, err := New(filepath.Join(t.TempDir(), "machine_ed25519")).Key(context.Background())
	require.NoError(t, err)
	assert.False(t, k1.Equal(other), "a second machine has a different keypair")
}

func TestFileStoreConcurrentFirstUseYieldsOneKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "machine_ed25519")
	keys := make([]ed25519.PrivateKey, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k, err := New(p).Key(context.Background())
			assert.NoError(t, err)
			keys[i] = k
		}(i)
	}
	wg.Wait()
	for _, k := range keys[1:] {
		assert.True(t, keys[0].Equal(k))
	}
}

func TestFileStoreRefusesLooseModeAndGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "machine_ed25519")
	_, err := New(p).Key(context.Background())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(p, 0o644))
	_, err = New(p).Key(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0600")

	g := filepath.Join(t.TempDir(), "machine_ed25519")
	require.NoError(t, os.WriteFile(g, []byte("not a key"), 0o600))
	_, err = New(g).Key(context.Background())
	require.Error(t, err)
}
