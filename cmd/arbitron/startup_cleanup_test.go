//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// openFileFDs counts descriptors for the stack's actual files, not unrelated
// asynchronous work in this test process. Use file identities rather than FD
// numbers, which can be reused (Darwin's /dev/fd lists only the standard three).
func openFileFDs(t *testing.T, paths ...string) map[string]int {
	t.Helper()
	files := make(map[string]unix.Stat_t, len(paths))
	counts := make(map[string]int, len(paths))
	for _, path := range paths {
		var st unix.Stat_t
		require.NoError(t, unix.Stat(path, &st))
		files[path] = st
		counts[path] = 0
	}
	var lim unix.Rlimit
	require.NoError(t, unix.Getrlimit(unix.RLIMIT_NOFILE, &lim))
	maxFD := lim.Cur
	if maxFD > 1<<16 {
		maxFD = 1 << 16
	}
	for fd := 0; fd < int(maxFD); fd++ {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil {
			continue
		}
		for path, file := range files {
			if st.Dev == file.Dev && st.Ino == file.Ino {
				counts[path]++
			}
		}
	}
	return counts
}

// chainedRBACConfig boots past logging, the database, migrations and the
// model-state key, then fails at the RBAC mapping guard.
func chainedRBACConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	cfg := "auth:\n  mode: local\n  rbac:\n    mapping:\n      raw-a: dept-b\n      dept-b: dept-c\n" +
		"database:\n  path: " + strconv.Quote(filepath.Join(root, "arbitron.db")) +
		"\nworkspace:\n  path: " + filepath.Join(root, "workspace") +
		"\nsamples:\n  seed: false\nlogging:\n  file: " + filepath.Join(root, "arbitron.log") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0600))
	return path
}

func TestInitDomainStartupErrorReleasesDatabaseAndLog(t *testing.T) {
	ctx := context.Background()
	secret := bytes.Repeat([]byte{0x3a}, 32)
	path := chainedRBACConfig(t)

	// Run through creation of the actual database and log files before counting.
	ds, err := initDomainWithModelStateSecret(ctx, path, secret)
	require.ErrorContains(t, err, "invalid RBAC group mapping")
	require.Nil(t, ds)

	files := []string{filepath.Join(filepath.Dir(path), "arbitron.db"), filepath.Join(filepath.Dir(path), "arbitron.log")}
	wantClosed := map[string]int{files[0]: 0, files[1]: 0}
	assert.Equal(t, wantClosed, openFileFDs(t, files...))
	for i := 0; i < 3; i++ {
		ds, err = initDomainWithModelStateSecret(ctx, path, secret)
		require.ErrorContains(t, err, "invalid RBAC group mapping")
		require.Nil(t, ds)
	}
	assert.Equal(t, wantClosed, openFileFDs(t, files...), "a failed boot must close the database and log file it opened")
}

func TestInitDomainSuccessfulBootKeepsResourcesUntilClose(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ARBITRON_MASTER_KEY", "")
	path := modelStateConfig(t, filepath.Join(t.TempDir(), "arbitron.db"))
	secret := bytes.Repeat([]byte{0x3a}, 32)

	ds, err := initDomainWithModelStateSecret(ctx, path, secret)
	require.NoError(t, err)
	files := []string{ds.cfg.Database.Path, ds.cfg.Logging.File}
	ds.Close(ctx)
	wantClosed := map[string]int{files[0]: 0, files[1]: 0}
	assert.Equal(t, wantClosed, openFileFDs(t, files...))

	// An unrelated descriptor may close while this root is live; it must not
	// mask a leak or make correct root cleanup fail the ownership assertion.
	unrelated, err := os.CreateTemp(t.TempDir(), "unrelated-")
	require.NoError(t, err)
	t.Cleanup(func() { unrelated.Close() })
	ds, err = initDomainWithModelStateSecret(ctx, path, secret)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(ctx) })
	require.NoError(t, ds.db.PingContext(ctx), "a successful boot hands an open database to the stack")
	for file, count := range openFileFDs(t, files...) {
		assert.Positive(t, count, "the live root must own an open descriptor for %s", file)
	}
	require.NoError(t, unrelated.Close())
	ds.Close(ctx)
	assert.Equal(t, wantClosed, openFileFDs(t, files...), "Close releases every database/log descriptor owned by the root")
}
