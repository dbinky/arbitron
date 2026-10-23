package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// G6: the key file's length check alone lets a corrupted or substituted
// same-length key boot, silently invalidating every encrypted attempt and
// fingerprint. The database records a keyed verifier and refuses any other key.
func TestModelStateKeyVerifierRejectsSameLengthSubstitution(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "")
	database := filepath.Join(t.TempDir(), "arbitron.db")
	cfgPath := modelStateConfig(t, database)
	ctx := context.Background()
	ds, err := initDomain(ctx, cfgPath)
	require.NoError(t, err)
	ds.Close(ctx)

	keyPath := database + ".model-state.key"
	original, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	substitute := bytes.Repeat([]byte{0xa5}, len(original))
	require.NotEqual(t, original, substitute)
	require.NoError(t, os.WriteFile(keyPath, substitute, 0600))

	ds, err = initDomain(ctx, cfgPath)
	require.Error(t, err, "a same-length key that is not this database's key must not boot")
	assert.Nil(t, ds)
	assert.Contains(t, err.Error(), "does not match this database")
	assert.NotContains(t, err.Error(), string(substitute))
	after, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, substitute, after, "a mismatched key is reported, never rewritten")

	// Restoring the original key from backup recovers the installation.
	require.NoError(t, os.WriteFile(keyPath, original, 0600))
	ds, err = initDomain(ctx, cfgPath)
	require.NoError(t, err)
	ds.Close(ctx)
}

func TestModelStateKeyVerifierBackfillsLegacyInstallation(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "")
	database := filepath.Join(t.TempDir(), "arbitron.db")
	cfgPath := modelStateConfig(t, database)
	ctx := context.Background()
	ds, err := initDomain(ctx, cfgPath)
	require.NoError(t, err)
	ds.Close(ctx)

	// An installation initialized before the verifier existed has no row.
	db, err := adaptsqlite.Open(database)
	require.NoError(t, err)
	_, err = db.Exec("DELETE FROM model_state_key_verifier")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	ds, err = initDomain(ctx, cfgPath)
	require.NoError(t, err, "the first boot after upgrade records the existing key's verifier")
	var verifier string
	require.NoError(t, ds.db.QueryRow("SELECT verifier FROM model_state_key_verifier WHERE id = 1").Scan(&verifier))
	assert.Len(t, verifier, 64)
	stored, err := os.ReadFile(database + ".model-state.key")
	require.NoError(t, err)
	assert.NotContains(t, verifier, string(stored))
	ds.Close(ctx)

	require.NoError(t, os.WriteFile(database+".model-state.key", bytes.Repeat([]byte{0x11}, len(stored)), 0600))
	ds, err = initDomain(ctx, cfgPath)
	require.Error(t, err, "the backfilled verifier protects later boots")
	assert.Nil(t, ds)
}

func TestModelStateKeyVerifierReadFaultFailsClosed(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "")
	database := filepath.Join(t.TempDir(), "arbitron.db")
	cfgPath := modelStateConfig(t, database)
	db, err := adaptsqlite.Open(database)
	require.NoError(t, err)
	require.NoError(t, adaptsqlite.RunMigrations(db))
	_, err = db.Exec("DROP TABLE model_state_key_verifier")
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE model_state_key_verifier (id INTEGER PRIMARY KEY, verifier BLOB)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO model_state_key_verifier (id, verifier) VALUES (1, NULL)")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	ds, err := initDomain(context.Background(), cfgPath)
	require.Error(t, err, "an unreadable verifier must not be treated as absent")
	assert.Nil(t, ds)
	db, err = adaptsqlite.Open(database)
	require.NoError(t, err)
	defer db.Close()
	var initialized bool
	require.NoError(t, db.QueryRow("SELECT secret_initialized FROM model_state_meta WHERE id = 1").Scan(&initialized))
	assert.False(t, initialized, "a verifier fault leaves the initialization flag unset")
}

func TestModelStateKeyVerifierInMemoryInjectedSecret(t *testing.T) {
	ctx := context.Background()
	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	require.NoError(t, adaptsqlite.RunMigrations(db))
	_, _, err = initModelState(ctx, db, ":memory:", bytes.Repeat([]byte{0x3a}, 32))
	require.NoError(t, err)
	_, _, err = initModelState(ctx, db, ":memory:", bytes.Repeat([]byte{0x3a}, 32))
	require.NoError(t, err, "the same injected secret re-verifies")
	_, _, err = initModelState(ctx, db, ":memory:", bytes.Repeat([]byte{0x3b}, 32))
	require.Error(t, err, "a different injected secret is refused on the same database")
}
