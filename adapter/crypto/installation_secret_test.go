package crypto

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallationSecretCreateReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-state.key")
	key, err := LoadOrCreateInstallationSecret(path, false)
	require.NoError(t, err)
	require.Len(t, key, 32)
	assert.NotEqual(t, make([]byte, 32), key)
	stored, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, key, stored)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	reloaded, err := LoadOrCreateInstallationSecret(path, true)
	require.NoError(t, err)
	assert.Equal(t, key, reloaded)
	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, info.ModTime(), after.ModTime(), "must not rewrite a valid key")
	other, err := LoadOrCreateInstallationSecret(filepath.Join(t.TempDir(), "key"), false)
	require.NoError(t, err)
	assert.False(t, bytes.Equal(key, other), "installations must not share a hardcoded secret")
}

func TestInstallationSecretMissingRequired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.key")
	key, err := LoadOrCreateInstallationSecret(path, true)
	require.Error(t, err)
	assert.Nil(t, key)
	assert.Contains(t, err.Error(), path, "operators must be told which file to restore")
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "must not rotate a lost key")
}

// A blocking OS lock would hang startup. The watchdog releases the real lease
// before reporting failure so this regression also terminates on the old code.
func TestInstallationSecretLockTimeoutAndRecovery(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(fmt.Sprintf("initialized-%t", initialized), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "model-state.key")
			var original []byte
			if initialized {
				var err error
				original, err = LoadOrCreateInstallationSecret(path, false)
				require.NoError(t, err)
			}
			unlock, err := lockInstallationSecret(path + ".lock")
			require.NoError(t, err)
			held := true
			defer func() {
				if held {
					unlock()
				}
			}()
			type result struct {
				key []byte
				err error
			}
			done := make(chan result, 1)
			started := time.Now()
			go func() {
				key, err := LoadOrCreateInstallationSecret(path, initialized)
				done <- result{key, err}
			}()
			select {
			case got := <-done:
				require.ErrorIs(t, got.err, context.DeadlineExceeded)
				require.Nil(t, got.key, "a timeout must never yield a key")
				assert.GreaterOrEqual(t, time.Since(started), time.Second, "transient contention must be retried")
			case <-time.After(8 * time.Second):
				unlock()
				held = false
				<-done
				t.Fatal("startup waited indefinitely for the model-state key lock")
			}
			if initialized {
				stored, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, original, stored, "timeout must preserve the working key")
			} else {
				_, err := os.Stat(path)
				require.True(t, os.IsNotExist(err), "timeout must not bootstrap a replacement")
			}
			unlock()
			held = false
			key, err := LoadOrCreateInstallationSecret(path, initialized)
			require.NoError(t, err, "timeout must close the waiting handle and allow retry")
			require.Len(t, key, 32)
			if initialized {
				assert.Equal(t, original, key)
			}
		})
	}
}

func TestInstallationSecretCorruptAndWriteFailure(t *testing.T) {
	for _, size := range []int{0, 31, 33} {
		t.Run(fmt.Sprintf("size-%d", size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "corrupt.key")
			data := bytes.Repeat([]byte("x"), size)
			require.NoError(t, os.WriteFile(path, data, 0600))
			key, err := LoadOrCreateInstallationSecret(path, false)
			require.Error(t, err)
			assert.Nil(t, key)
			assert.NotContains(t, err.Error(), string(bytes.Repeat([]byte("x"), 31)))
			assert.Contains(t, err.Error(), path)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, data, after)
		})
	}
	path := filepath.Join(t.TempDir(), "missing-parent", "key")
	key, err := LoadOrCreateInstallationSecret(path, false)
	require.Error(t, err)
	assert.Nil(t, key)
}

func TestInstallationSecretConcurrentProcesses(t *testing.T) {
	if path := os.Getenv("ARBITRON_TEST_INSTALLATION_SECRET_PATH"); path != "" {
		require.NoError(t, os.WriteFile(os.Getenv("ARBITRON_TEST_SECRET_RESULT")+".ready", nil, 0600))
		key, err := LoadOrCreateInstallationSecret(path, false)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(os.Getenv("ARBITRON_TEST_SECRET_RESULT"), []byte(base64.StdEncoding.EncodeToString(key)), 0600))
		return
	}
	root := t.TempDir()
	path := filepath.Join(root, "key")
	unlock, err := lockInstallationSecret(path + ".lock")
	require.NoError(t, err)
	released := false
	defer func() {
		if !released {
			unlock()
		}
	}()
	const processes = 8
	cmds := make([]*exec.Cmd, processes)
	results := make([]string, processes)
	for i := range cmds {
		results[i] = filepath.Join(root, fmt.Sprintf("result-%d", i))
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestInstallationSecretConcurrentProcesses$")
		cmds[i].Env = append(os.Environ(), "ARBITRON_TEST_INSTALLATION_SECRET_PATH="+path, "ARBITRON_TEST_SECRET_RESULT="+results[i])
		require.NoError(t, cmds[i].Start())
	}
	require.Eventually(t, func() bool {
		for _, result := range results {
			if _, err := os.Stat(result + ".ready"); err != nil {
				return false
			}
		}
		return true
	}, 10*time.Second, 5*time.Millisecond, "all competing processes must reach bootstrap")
	// Every child must wait for the shared bootstrap lease, not publish its own
	// key while another process holds it.
	time.Sleep(100 * time.Millisecond)
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "secret publication must honor the cross-process lock")
	unlock()
	released = true
	for _, cmd := range cmds {
		assert.NoError(t, cmd.Wait())
	}
	if t.Failed() {
		return
	}
	stored, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, result := range results {
		got, err := os.ReadFile(result)
		require.NoError(t, err)
		assert.Equal(t, base64.StdEncoding.EncodeToString(stored), string(got))
	}
}
