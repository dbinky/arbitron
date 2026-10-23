package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modelStateConfig(t *testing.T, database string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	cfg := "auth:\n  mode: local\ndatabase:\n  path: " + strconv.Quote(database) + "\nworkspace:\n  path: " + filepath.Join(root, "workspace") + "\nsamples:\n  seed: false\nlogging:\n  file: " + filepath.Join(root, "arbitron.log") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0600))
	return path
}

func TestExecutionRevisionSubscriptionBootSecretReload(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "")
	database := filepath.Join(t.TempDir(), "arbitron.db")
	cfgPath := modelStateConfig(t, database)
	ctx := context.Background()
	ds, err := initDomain(ctx, cfgPath)
	require.NoError(t, err)
	closed := false
	defer func() {
		if !closed {
			ds.Close(ctx)
		}
	}()
	var initialized bool
	require.NoError(t, ds.db.QueryRow("SELECT secret_initialized FROM model_state_meta WHERE id = 1").Scan(&initialized))
	require.True(t, initialized)
	stored, err := os.ReadFile(database + ".model-state.key")
	require.NoError(t, err)
	require.Len(t, stored, 32)
	cipher, nonce, err := ds.modelStateCrypto.Seal([]byte("durable model event payload"))
	require.NoError(t, err)
	ds.Close(ctx)
	closed = true

	ds, err = initDomain(ctx, cfgPath)
	require.NoError(t, err)
	closed = false
	plaintext, err := ds.modelStateCrypto.Open(cipher, nonce)
	require.NoError(t, err)
	assert.Equal(t, "durable model event payload", string(plaintext), "root must use the stored secret, not generate a fresh one")
	after, err := os.ReadFile(database + ".model-state.key")
	require.NoError(t, err)
	assert.Equal(t, stored, after)
	logs, err := os.ReadFile(filepath.Join(filepath.Dir(cfgPath), "arbitron.log"))
	require.NoError(t, err)
	assert.NotContains(t, string(logs), base64.StdEncoding.EncodeToString(stored))
	ds.Close(ctx)
	closed = true

	require.NoError(t, os.Remove(database+".model-state.key"))
	ds, err = initDomain(ctx, cfgPath)
	require.Error(t, err, "persisted initialized flag must prohibit replacement of a lost key")
	assert.Nil(t, ds)
	_, err = os.Stat(database + ".model-state.key")
	assert.True(t, os.IsNotExist(err))
}

func TestExecutionRevisionSecretBootFailureDoesNotMarkInitialized(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "")
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "write-failure", true: "corrupt"}[corrupt], func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "arbitron.db")
			cfgPath := modelStateConfig(t, database)
			if corrupt {
				require.NoError(t, os.WriteFile(database+".model-state.key", []byte("corrupt"), 0600))
			} else {
				require.NoError(t, os.Mkdir(database+".model-state.key.lock", 0700))
			}
			ds, err := initDomain(context.Background(), cfgPath)
			require.Error(t, err)
			assert.Nil(t, ds)
			// Read the real migrated database even though assembly failed.
			db, err := adaptsqlite.Open(database)
			require.NoError(t, err)
			defer db.Close()
			var initialized bool
			require.NoError(t, db.QueryRow("SELECT secret_initialized FROM model_state_meta WHERE id = 1").Scan(&initialized))
			assert.False(t, initialized)
		})
	}
}

func TestExecutionRevisionSecretPublicationRecovery(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "")
	database := filepath.Join(t.TempDir(), "arbitron.db")
	cfgPath := modelStateConfig(t, database)
	ctx := context.Background()
	db, err := adaptsqlite.Open(database)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	_, err = db.Exec(`CREATE TRIGGER reject_secret_initialization
		BEFORE UPDATE OF secret_initialized ON model_state_meta
		BEGIN SELECT RAISE(ABORT, 'injected initialization write failure'); END`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// Publication succeeds, but the durable database flag cannot be written.
	ds, err := initDomain(ctx, cfgPath)
	require.Error(t, err)
	assert.Nil(t, ds)
	assert.Contains(t, err.Error(), "recording model-state initialization")
	keyPath := database + ".model-state.key"
	stored, err := os.ReadFile(keyPath)
	require.NoError(t, err, "the published key must survive a metadata write failure")
	require.Len(t, stored, 32)
	info, err := os.Stat(keyPath)
	require.NoError(t, err)
	crypto, err := adaptcrypto.NewAESGCM(stored)
	require.NoError(t, err)
	cipher, nonce, err := crypto.Seal([]byte("payload before metadata commit"))
	require.NoError(t, err)

	db, err = adaptsqlite.Open(database)
	require.NoError(t, err)
	var initialized bool
	require.NoError(t, db.QueryRow("SELECT secret_initialized FROM model_state_meta WHERE id = 1").Scan(&initialized))
	assert.False(t, initialized)
	_, err = db.Exec("DROP TRIGGER reject_secret_initialization")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	ds, err = initDomain(ctx, cfgPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(ctx) })
	require.NoError(t, ds.db.QueryRow("SELECT secret_initialized FROM model_state_meta WHERE id = 1").Scan(&initialized))
	assert.True(t, initialized)
	plaintext, err := ds.modelStateCrypto.Open(cipher, nonce)
	require.NoError(t, err, "retry must retain the key published before the failed metadata commit")
	assert.Equal(t, "payload before metadata commit", string(plaintext))
	after, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, info.ModTime(), after.ModTime(), "retry must not rewrite the published key")
}

func TestExecutionRevisionSecretMigrationRoundTrip(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "")
	database := filepath.Join(t.TempDir(), "arbitron.db")
	cfgPath := modelStateConfig(t, database)
	ctx := context.Background()
	ds, err := initDomain(ctx, cfgPath)
	require.NoError(t, err)
	cipher, nonce, err := ds.modelStateCrypto.Seal([]byte("payload before migration rollback"))
	ds.Close(ctx)
	require.NoError(t, err)

	db, err := adaptsqlite.Open(database)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	// Newest-first on the way down: 028 depends on the 027 column.
	for _, step := range []string{"028_connection_revision_floor.down", "027_execution_revisions.down", "027_execution_revisions.up", "028_connection_revision_floor.up"} {
		migration, err := os.ReadFile("../../adapter/sqlite/migrations/" + step + ".sql")
		require.NoError(t, err)
		_, err = db.Exec(string(migration))
		require.NoError(t, err)
	}
	var initialized bool
	require.NoError(t, db.QueryRow("SELECT secret_initialized FROM model_state_meta WHERE id = 1").Scan(&initialized))
	assert.False(t, initialized, "down/up resets metadata while retaining the key file")
	require.NoError(t, db.Close())

	ds, err = initDomain(ctx, cfgPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(ctx) })
	plaintext, err := ds.modelStateCrypto.Open(cipher, nonce)
	require.NoError(t, err, "boot after down/up must reuse the existing key")
	assert.Equal(t, "payload before migration rollback", string(plaintext))
	require.NoError(t, ds.db.QueryRow("SELECT secret_initialized FROM model_state_meta WHERE id = 1").Scan(&initialized))
	assert.True(t, initialized)
}

func TestExecutionRevisionRootConnectionMutationPersists(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5c}, 32)))
	database := filepath.Join(t.TempDir(), "arbitron.db")
	path := modelStateConfig(t, database)
	ctx := context.Background()
	ds, err := initDomain(ctx, path)
	require.NoError(t, err)
	closed := false
	defer func() {
		if !closed {
			ds.Close(ctx)
		}
	}()
	admin := identity.WithIdentity(ctx, identity.Identity{Role: identity.RoleAdmin})
	conn := provider.Connection{Name: "root-revision", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}
	require.NoError(t, ds.connSvc.Create(admin, conn, "old-root-key"))
	newKey := "new-root-key"
	require.NoError(t, ds.connSvc.UpdatePatch(admin, conn, nil, &newKey))
	var revision int64
	require.NoError(t, ds.db.QueryRow("SELECT config_revision FROM connections WHERE name = ?", conn.Name).Scan(&revision))
	assert.Equal(t, int64(2), revision, "the wired admin service must increment the real durable row")
	ds.Close(ctx)
	closed = true
	ds, err = initDomain(ctx, path)
	require.NoError(t, err)
	closed = false
	got, err := ds.connSvc.Get(admin, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, revision, got.ConfigRevision)
	key, err := ds.connStore.Reveal(ctx, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, newKey, key)
}

func TestExecutionRevisionMemoryRequiresExplicitSecret(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "")
	path := modelStateConfig(t, ":memory:")
	ds, err := initDomain(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, ds)
	ds, err = initDomainWithModelStateSecret(context.Background(), path, bytes.Repeat([]byte{0x3a}, 32))
	require.NoError(t, err)
	defer ds.Close(context.Background())
	cipher, nonce, err := ds.modelStateCrypto.Seal([]byte("memory payload"))
	require.NoError(t, err)
	plaintext, err := ds.modelStateCrypto.Open(cipher, nonce)
	require.NoError(t, err)
	assert.Equal(t, "memory payload", string(plaintext))
}

func TestModelStateFingerprintClosureRootWiring(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "")
	database := filepath.Join(t.TempDir(), "arbitron.db")
	cfgPath := modelStateConfig(t, database)
	ctx := context.Background()
	ds, err := initDomain(ctx, cfgPath)
	require.NoError(t, err)
	require.NotNil(t, ds.modelFingerprint, "the inventory consumes this closure; initDomain must expose it")
	first := ds.modelFingerprint([]byte("binary:/usr/local/bin/claude:v1"))
	require.NotEmpty(t, first)
	assert.NotEqual(t, first, ds.modelFingerprint([]byte("binary:/usr/local/bin/claude:v2")), "different config input must change the fingerprint")
	assert.Equal(t, first, ds.modelFingerprint([]byte("binary:/usr/local/bin/claude:v1")), "deterministic")
	stored, err := os.ReadFile(database + ".model-state.key")
	require.NoError(t, err)
	assert.NotContains(t, first, string(stored))
	assert.NotContains(t, first, base64.StdEncoding.EncodeToString(stored))
	ds.Close(ctx)

	ds, err = initDomain(ctx, cfgPath)
	require.NoError(t, err)
	defer ds.Close(ctx)
	assert.Equal(t, first, ds.modelFingerprint([]byte("binary:/usr/local/bin/claude:v1")), "same DB and key give the same fingerprint after restart")
}

func TestModelStateFingerprintDependsOnKeyAndDomainSeparation(t *testing.T) {
	ctx := context.Background()
	open := func(secret []byte) func([]byte) string {
		db, err := adaptsqlite.Open(":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { db.Close() })
		require.NoError(t, adaptsqlite.RunMigrations(db))
		_, fp, err := initModelState(ctx, db, ":memory:", secret)
		require.NoError(t, err)
		return fp
	}
	a := open(bytes.Repeat([]byte{1}, 32))
	b := open(bytes.Repeat([]byte{2}, 32))
	assert.NotEqual(t, a([]byte("x")), b([]byte("x")), "a different installation key must give a different fingerprint")
	assert.NotEqual(t, a([]byte("x")), a([]byte("y")))
	assert.Len(t, a(nil), 64, "empty input still yields a keyed digest")
}

func TestInitModelStateRequiresKeyForInMemory(t *testing.T) {
	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, adaptsqlite.RunMigrations(db))
	_, fp, err := initModelState(context.Background(), db, ":memory:", nil)
	require.Error(t, err)
	assert.Nil(t, fp)
}
