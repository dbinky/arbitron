//go:build unix

package crypto

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing either sync, publishing before the file sync, or discarding a sync
// error must fail this fixture. Only Sync is intercepted: writes, permissions,
// rename, file contents, cleanup and the bootstrap lock remain real.
func TestInstallationSecretDurablePublication(t *testing.T) {
	for _, failAt := range []string{"none", "file", "directory"} {
		t.Run(failAt, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "key")
			originalSync := syncInstallationSecret
			t.Cleanup(func() { syncInstallationSecret = originalSync })
			syncFailure := errors.New("injected sync failure")
			var stages []string
			var written []byte
			syncInstallationSecret = func(file *os.File) error {
				info, err := file.Stat()
				require.NoError(t, err)
				if info.IsDir() {
					assert.Equal(t, []string{"file"}, stages, "file data must be synced before rename and directory sync")
					assert.Equal(t, root, file.Name(), "sync the directory containing the published key")
					stored, err := os.ReadFile(path)
					require.NoError(t, err, "rename must precede directory sync")
					assert.Equal(t, written, stored)
					stages = append(stages, "directory")
					if failAt == "directory" {
						return syncFailure
					}
				} else {
					assert.Empty(t, stages)
					_, err := os.Stat(path)
					assert.True(t, os.IsNotExist(err), "key must not be published before its data is durable")
					assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
					written, err = os.ReadFile(file.Name())
					require.NoError(t, err)
					require.Len(t, written, 32, "sync must follow the complete key write")
					stages = append(stages, "file")
					if failAt == "file" {
						return syncFailure
					}
				}
				return originalSync(file)
			}

			key, err := LoadOrCreateInstallationSecret(path, false)
			if failAt == "none" {
				require.NoError(t, err)
				assert.Equal(t, written, key)
			} else {
				require.ErrorIs(t, err, syncFailure, "bootstrap must not accept an undurable key")
				assert.Nil(t, key)
			}
			if failAt == "file" {
				assert.Equal(t, []string{"file"}, stages)
				_, err := os.Stat(path)
				assert.True(t, os.IsNotExist(err), "failed file sync must prevent publication")
			} else {
				assert.Equal(t, []string{"file", "directory"}, stages)
				stored, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, written, stored, "a failed directory sync must not rotate the published key")
			}
			remaining, err := filepath.Glob(filepath.Join(root, ".model-state-key-*"))
			require.NoError(t, err)
			assert.Empty(t, remaining, "bootstrap must clean up unpublished temporary keys")
		})
	}
}

// Returning an existing key before retrying its parent-directory sync would
// accept a publication that a prior boot could not make durable (G7).
func TestInstallationSecretRetriesInterruptedPublication(t *testing.T) {
	for _, required := range []bool{false, true} {
		name := "bootstrap"
		if required {
			name = "initialized"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "key")
			originalSync := syncInstallationSecret
			t.Cleanup(func() { syncInstallationSecret = originalSync })
			syncFailure := errors.New("injected directory sync failure")
			failDirectory := true
			var syncedDirectories int
			syncInstallationSecret = func(file *os.File) error {
				info, err := file.Stat()
				require.NoError(t, err)
				if info.IsDir() {
					assert.Equal(t, root, file.Name())
					syncedDirectories++
					if failDirectory {
						return syncFailure
					}
				}
				return originalSync(file)
			}

			key, err := LoadOrCreateInstallationSecret(path, false)
			require.ErrorIs(t, err, syncFailure)
			require.Nil(t, key)
			stored, err := os.ReadFile(path)
			require.NoError(t, err, "rename published the key before directory sync failed")
			require.Len(t, stored, 32)
			before, err := os.Stat(path)
			require.NoError(t, err)

			key, err = LoadOrCreateInstallationSecret(path, required)
			require.ErrorIs(t, err, syncFailure, "retry must not accept a still-undurable publication")
			require.Nil(t, key)
			assert.Equal(t, 2, syncedDirectories)

			failDirectory = false
			key, err = LoadOrCreateInstallationSecret(path, required)
			require.NoError(t, err)
			assert.Equal(t, stored, key, "recovery must retain the published key")
			assert.Equal(t, 3, syncedDirectories)
			after, err := os.Stat(path)
			require.NoError(t, err)
			assert.True(t, os.SameFile(before, after), "recovery must not replace the key")
			assert.Equal(t, before.ModTime(), after.ModTime())
			assert.Equal(t, os.FileMode(0600), after.Mode().Perm())
			remaining, err := filepath.Glob(filepath.Join(root, ".model-state-key-*"))
			require.NoError(t, err)
			assert.Empty(t, remaining)
		})
	}
}
