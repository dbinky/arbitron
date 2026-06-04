package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const seededLocalUserID = "00000000-0000-0000-0000-000000000001"

// ---------------------------------------------------------------------------
// Migration: the instance_dismissals table exists after RunMigrations.
// ---------------------------------------------------------------------------

func TestInstanceDismissalsMigration_TableExists(t *testing.T) {
	db := setupTestDB(t)
	var count int
	require.NoError(t, db.DB.QueryRow("SELECT COUNT(*) FROM instance_dismissals").Scan(&count))
	assert.Equal(t, 0, count)
}
