package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration044_LeaseHolderAndSupersessionTables(t *testing.T) {
	db := setupTestDB(t).DB
	holder := `INSERT INTO workspace_lease_holders(resource_key,generation,token,holder,owner,job_id,job_runner_id,job_generation,boot_id,claimed_at)
		VALUES(?,?,'11111111-1111-1111-1111-111111111111','h','builtin',?,?,?,?,'t')`
	_, err := db.Exec(holder, "run:a", 1, "job-1", "builtin", 1, "")
	require.NoError(t, err, "a job holder")
	_, err = db.Exec(holder, "run:a", 2, nil, nil, nil, "boot-1")
	require.NoError(t, err, "a server-side holder")
	_, err = db.Exec(holder, "run:a", 2, nil, nil, nil, "boot-2")
	require.Error(t, err, "one holder row per generation")
	_, err = db.Exec(holder, "run:a", 3, nil, nil, nil, "")
	require.Error(t, err, "a server-side holder must name its boot")
	_, err = db.Exec(holder, "run:a", 4, "job-1", "builtin", 1, "boot-1")
	require.Error(t, err, "a job holder carries no boot")
	_, err = db.Exec(holder, "run:a", 5, "job-1", nil, 1, "")
	require.Error(t, err, "a job holder names its runner")
	_, err = db.Exec(holder, "run:a", 0, nil, nil, nil, "boot-1")
	require.Error(t, err, "generation starts at 1")

	sup := `INSERT INTO workspace_lease_supersessions(resource_key,old_generation,old_token,new_generation,kind,superseded_at) VALUES(?,?,'tok',?,?,'t')`
	for _, kind := range []string{"runner_lost", "revoked", "stopped", "server_restart"} {
		_, err = db.Exec(`DELETE FROM workspace_lease_supersessions`)
		require.NoError(t, err)
		_, err = db.Exec(sup, "run:a", 1, 2, kind)
		require.NoError(t, err, kind)
	}
	_, err = db.Exec(sup, "run:b", 1, 2, "suspect")
	require.Error(t, err, "suspect is never a takeover kind")
	_, err = db.Exec(sup, "run:a", 1, 3, "revoked")
	require.Error(t, err, "one receipt per superseded generation")

	// workspace_leases is unchanged: the landed positional insert still fits.
	var cols int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pragma_table_info('workspace_leases')`).Scan(&cols))
	assert.Equal(t, 6, cols)

	// Down then up again is clean.
	down, err := migrationsFS.ReadFile("migrations/044_workspace_mobility.down.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	for _, tbl := range []string{"workspace_lease_holders", "workspace_lease_supersessions"} {
		_, err = db.Exec(`SELECT * FROM ` + tbl)
		require.Error(t, err, tbl)
	}
	_, err = db.Exec(`DELETE FROM schema_migrations WHERE version=44`)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	for _, tbl := range []string{"workspace_lease_holders", "workspace_lease_supersessions"} {
		_, err = db.Exec(`SELECT count(*) FROM ` + tbl)
		require.NoError(t, err, tbl)
	}
}
