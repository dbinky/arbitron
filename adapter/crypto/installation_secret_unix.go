//go:build unix

package crypto

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// The stable lock file is never unlinked: replacing it would allow concurrent
// processes to lock different inodes. The kernel releases the lease on exit.
func lockInstallationSecret(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := waitForInstallationSecretLock(func() (bool, error) {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			return false, nil
		}
		return err == nil, err
	}); err != nil {
		file.Close()
		return nil, err
	}
	return func() { file.Close() }, nil
}

func publishInstallationSecret(temp, path string) error {
	if err := os.Rename(temp, path); err != nil {
		return err
	}
	return finishInstallationSecretPublication(path)
}

// A rename can succeed even when the subsequent directory sync fails. Repeat
// that sync on load without rewriting the key or changing its identity.
func finishInstallationSecretPublication(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return syncInstallationSecret(dir)
}
