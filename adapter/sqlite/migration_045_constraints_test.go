package sqlite

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMigration045_IntentKindConstraintWithValidRequest(t *testing.T) {
	db := setupTestDB(t)
	insert := `INSERT INTO turn_intents (id, request_id, lease_key, run_id, invocation_id, node_id, turn_index, kind, created_at) VALUES (?,?,'run:x','r','i','n',1,?,0)`
	_, err := db.DB.Exec(insert, "valid", "req-1", "first")
	require.NoError(t, err)
	_, err = db.DB.Exec(insert, "invalid", "req-2", "bogus")
	require.ErrorContains(t, err, "CHECK constraint failed")
}
