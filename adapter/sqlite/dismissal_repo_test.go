package sqlite

import (
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
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

// dismissalRepoSatisfiesPort fails to compile if *DismissalRepo no longer
// implements the domain port.
var _ instance.DismissalRepository = (*DismissalRepo)(nil)
