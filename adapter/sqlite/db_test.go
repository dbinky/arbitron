package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Opening a second stack must apply the busy policy before the first pragma
// that can encounter a departing connection's lock, not only after WAL setup.
func TestOpen_WaitsForStartupLockBeforeEnablingWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.db")
	blocker, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer blocker.Close()
	blocker.SetMaxOpenConns(1)
	_, err = blocker.Exec("BEGIN EXCLUSIVE")
	require.NoError(t, err)

	type result struct {
		db  *sql.DB
		err error
	}
	done := make(chan result, 1)
	go func() {
		db, err := Open(path)
		done <- result{db, err}
	}()
	received := false
	defer func() {
		// Also unblock and join Open when an assertion exits the test early.
		_, _ = blocker.Exec("ROLLBACK")
		if !received {
			select {
			case got := <-done:
				if got.db != nil {
					got.db.Close()
				}
			case <-time.After(10 * time.Second):
				t.Error("Open did not stop during cleanup")
			}
		}
	}()
	// Keep the real lock held long enough to expose an immediate SQLITE_BUSY.
	// This is not a performance assertion: success is checked after releasing it.
	select {
	case got := <-done:
		received = true
		if got.db != nil {
			got.db.Close()
		}
		t.Fatalf("Open returned before the startup lock was released: %v", got.err)
	case <-time.After(200 * time.Millisecond):
	}
	_, err = blocker.Exec("ROLLBACK")
	require.NoError(t, err)

	select {
	case got := <-done:
		received = true
		require.NoError(t, got.err)
		defer got.db.Close()
		var mode string
		require.NoError(t, got.db.QueryRow("PRAGMA journal_mode").Scan(&mode))
		assert.Equal(t, "wal", mode)
		var busy, foreignKeys int
		require.NoError(t, got.db.QueryRow("PRAGMA busy_timeout").Scan(&busy))
		require.NoError(t, got.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys))
		assert.Equal(t, 5000, busy, "keep the existing production lock budget")
		assert.Equal(t, 1, foreignKeys)
	case <-time.After(10 * time.Second):
		t.Fatal("Open did not finish after the startup lock was released")
	}
}

// A peer that keeps the startup lock must exhaust the busy budget and fail
// closed. Ignoring a WAL error or omitting the busy policy breaks this test.
func TestOpen_StartupLockTimeoutFailsClosedAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup-timeout.db")
	blocker, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer blocker.Close()
	blocker.SetMaxOpenConns(1)
	_, err = blocker.Exec("BEGIN EXCLUSIVE")
	require.NoError(t, err)
	defer blocker.Exec("ROLLBACK")

	// Keep the lock held throughout the real production Open call. No timer
	// releases it early, so a nil error could only mean WAL setup was skipped.
	started := time.Now()
	db, err := Open(path)
	elapsed := time.Since(started)
	if db != nil {
		defer db.Close()
	}
	require.ErrorContains(t, err, "setting PRAGMA journal_mode=WAL")
	require.ErrorContains(t, err, "SQLITE_BUSY")
	require.Nil(t, db, "a failed WAL setup must not expose a partially configured DB")
	// Allow driver timer granularity while distinguishing the five-second
	// policy from immediate refusal or an unbounded wait.
	require.GreaterOrEqual(t, elapsed, 4500*time.Millisecond)
	require.Less(t, elapsed, 10*time.Second)
	var mode string
	require.NoError(t, blocker.QueryRow("PRAGMA journal_mode").Scan(&mode))
	require.Equal(t, "delete", mode, "failed startup must leave the locked DB in its prior mode")

	_, err = blocker.Exec("ROLLBACK")
	require.NoError(t, err)
	reopened, err := Open(path)
	require.NoError(t, err, "the expired attempt must not prevent recovery after the peer releases its lock")
	defer reopened.Close()
	require.NoError(t, reopened.QueryRow("PRAGMA journal_mode").Scan(&mode))
	require.Equal(t, "wal", mode)
	var busy, foreignKeys int
	require.NoError(t, reopened.QueryRow("PRAGMA busy_timeout").Scan(&busy))
	require.NoError(t, reopened.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys))
	require.Equal(t, 5000, busy)
	require.Equal(t, 1, foreignKeys)
	_, err = reopened.Exec("CREATE TABLE recovered (value TEXT); INSERT INTO recovered VALUES ('after timeout')")
	require.NoError(t, err)
	var value string
	require.NoError(t, reopened.QueryRow("SELECT value FROM recovered").Scan(&value))
	require.Equal(t, "after timeout", value)
}
