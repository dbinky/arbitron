package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayoutMigration(t *testing.T) {
	db := setupTestDB(t).DB
	var version int
	require.NoError(t, db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version))
	require.Equal(t, 22, version)
	for _, index := range []string{"idx_layout_destination", "idx_layout_claimable"} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count))
		assert.Equal(t, 1, count)
	}
	var publisher string
	require.NoError(t, db.QueryRow(`SELECT id FROM users WHERE username='local'`).Scan(&publisher))
	_, err := db.Exec(`INSERT INTO published_workflows(name,publisher_id,source_name,saved_revision) VALUES('old',?,'source','proof')`, publisher)
	require.NoError(t, err)
	insert := `INSERT INTO layout_revisions(revision,scope_kind,owner_id,workflow_name,reason,input_manifest,retained,state,job_generation,attempt_limit,requested_at,updated_at) VALUES(?,?,'','wf','create',x'7b7d','{}',?,?,?,?,0)`
	for _, bad := range []struct {
		state, scope string
		gen, budget  int
	}{
		{"bogus", "private", 1, 5}, {"pending", "bogus", 1, 5}, {"pending", "shared", 0, 5}, {"pending", "shared", 1, 0},
	} {
		_, err = db.Exec(insert, "bad", bad.scope, bad.state, bad.gen, bad.budget, 0)
		require.Error(t, err)
	}
	_, err = db.Exec(insert, "r", "shared", "pending", 1, 5, 123456789)
	require.NoError(t, err)
	for _, assignment := range []string{"attempts_used=-1", "result_generation=-1", "cache_generation=-1", "unreviewed=2", "owner_id='noncanonical'"} {
		_, err = db.Exec(`UPDATE layout_revisions SET ` + assignment + ` WHERE revision='r'`)
		require.Error(t, err, assignment)
	}
	result := `INSERT INTO layout_results(revision,generation,geometry,review,preflight,score,verdict,created_at) VALUES(?,?,'{}','null','[]','{}','failed',0)`
	_, err = db.Exec(result, "orphan", 1)
	require.Error(t, err)
	_, err = db.Exec(result, "r", 0)
	require.Error(t, err)
	_, err = db.Exec(result, "r", 1)
	require.NoError(t, err)
	_, err = db.Exec(result, "r", 1)
	require.Error(t, err)
	require.NoError(t, RunMigrations(db))
	down, err := migrationsFS.ReadFile("migrations/022_layout_revisions.down.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	var source string
	require.NoError(t, db.QueryRow(`SELECT source_name FROM published_workflows WHERE name='old'`).Scan(&source))
	assert.Equal(t, "source", source)
	_, err = db.Exec(`SELECT saved_revision FROM published_workflows`)
	require.Error(t, err)
	_, err = db.Exec(`SELECT * FROM layout_revisions`)
	require.Error(t, err)
	_, err = db.Exec(`SELECT * FROM layout_results`)
	require.Error(t, err)
	_, err = db.Exec(`DELETE FROM schema_migrations WHERE version=22`)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
}
