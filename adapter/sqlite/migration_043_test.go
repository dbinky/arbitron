package sqlite

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration043_RemoteInboxRejectsConflictingReplay(t *testing.T) {
	db := setupTestDB(t).DB
	insert := `INSERT INTO runner_inbox(job_id,generation,runner_id,seq,kind,payload,payload_hash,received_at) VALUES ('j',1,'r',1,'output',?,?,'now')`
	_, err := db.Exec(insert, "hello", "hash-a")
	require.NoError(t, err)
	_, err = db.Exec(insert, "hello", "hash-a")
	require.NoError(t, err)
	_, err = db.Exec(insert, "different", "hash-b")
	require.Error(t, err)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM runner_inbox`).Scan(&n))
	require.Equal(t, 1, n)
	_, err = db.Exec(`INSERT INTO runner_inbox(job_id,generation,runner_id,seq,kind,payload,payload_hash,received_at) VALUES ('j',1,'r',0,'output','x','h','now')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO runner_inbox(job_id,generation,runner_id,seq,kind,payload,payload_hash,received_at) VALUES ('j',1,'r',0,'result','x','h','now')`)
	require.NoError(t, err)
}

func TestMigration043_RoundTrip(t *testing.T) {
	db := setupTestDB(t).DB
	_, err := db.Exec(`INSERT INTO runner_inbox_cursors(job_id,generation,runner_id,last_output_seq,result_received) VALUES ('j',1,'r',2,1)`)
	require.NoError(t, err)
	down, err := os.ReadFile("migrations/043_runner_keys.down.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	for _, table := range []string{"runner_keys", "runner_inbox", "runner_inbox_cursors"} {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n))
		require.Zero(t, n)
	}
	up, err := os.ReadFile("migrations/043_runner_keys.up.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
}
