package crypto

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Keep the real filesystem sync as the default; tests inject sync failures
// without replacing key writes, publication, or the cross-process lock.
var syncInstallationSecret = (*os.File).Sync

// Bootstrap contention must not stall startup indefinitely. The stable lock
// file stays in place on timeout; only the waiting handle is closed.
const installationSecretLockTimeout = 5 * time.Second

func waitForInstallationSecretLock(tryLock func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), installationSecretLockTimeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out waiting for model-state key lock: %w", ctx.Err())
		}
		if acquired, err := tryLock(); acquired || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for model-state key lock: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// LoadOrCreateInstallationSecret loads the installation's model-state key.
// Once the database records initialization, required prevents replacing a lost
// key. Backups must retain both the database and this file. Existing keys are
// never rewritten; corrupt keys fail closed without exposing their contents.
// Lock contention returns context.DeadlineExceeded after five seconds, without
// creating or changing the key; a later startup may retry safely.
func LoadOrCreateInstallationSecret(path string, required bool) ([]byte, error) {
	unlock, err := lockInstallationSecret(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("locking model-state key bootstrap: %w", err)
	}
	defer unlock()

	key, err := readInstallationSecret(path)
	if err == nil {
		// A previous boot may have published the key but failed the final
		// durability step. Finish it under the same lock before accepting it.
		if err := finishInstallationSecretPublication(path); err != nil {
			return nil, fmt.Errorf("completing model-state key publication: %w", err)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading model-state key %s: %w", path, err)
	}
	if required {
		return nil, fmt.Errorf("model-state key %s is missing; restore it from the same backup as the database", path)
	}

	key = make([]byte, masterKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating model-state key: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".model-state-key-*")
	if err != nil {
		return nil, fmt.Errorf("creating model-state key: %w", err)
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(0600); err != nil {
		return nil, fmt.Errorf("securing model-state key: %w", err)
	}
	if _, err := temp.Write(key); err != nil {
		return nil, fmt.Errorf("writing model-state key: %w", err)
	}
	if err := syncInstallationSecret(temp); err != nil {
		return nil, fmt.Errorf("syncing model-state key: %w", err)
	}
	if err := temp.Close(); err != nil {
		return nil, fmt.Errorf("closing model-state key: %w", err)
	}
	if err := publishInstallationSecret(temp.Name(), path); err != nil {
		return nil, fmt.Errorf("publishing model-state key: %w", err)
	}
	return key, nil
}

func readInstallationSecret(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	key, err := io.ReadAll(io.LimitReader(file, masterKeyLen+1))
	if err != nil {
		return nil, err
	}
	if len(key) != masterKeyLen {
		return nil, fmt.Errorf("corrupt model-state key %s; expected %d bytes", path, masterKeyLen)
	}
	return key, nil
}
