package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// List carries each row's pin record so the enricher can show a pinned run's
// model (G23). Snapshot, empty-snapshot, native-route and legacy rows read as
// Get reads them; a corrupt record lists as unreadable without failing the page.
func TestPinsListCarriesPinRecord(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	pinned := pinnedInstance(map[string]string{"worker": "vendor/version-x"}, 1)
	empty := pinnedInstance(map[string]string{}, 1)
	opaque := pinnedInstance(map[string]string{"azure": "my-deployment"}, 1)
	opaque.ModelPinRoutes = map[string]string{"azure": "azure"}
	legacy := pinnedInstance(map[string]string{}, 1)
	corrupt := pinnedInstance(map[string]string{"worker": "vendor/version-x"}, 1)
	badRoute := pinnedInstance(map[string]string{"worker": "vendor/version-x"}, 1)
	for _, inst := range []*instance.Instance{pinned, empty, opaque, legacy, corrupt, badRoute} {
		require.NoError(t, repo.Create(ctx, inst))
	}
	_, err := db.DB.Exec(`UPDATE instances SET model_pins = NULL, model_pins_version = 0 WHERE id = ?`, legacy.ID.String())
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE instances SET model_pins = '{not json' WHERE id = ?`, corrupt.ID.String())
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE instances SET model_pin_routes = '{"worker":"openai"}' WHERE id = ?`, badRoute.ID.String())
	require.NoError(t, err)

	rows, err := repo.List(ctx, instance.ListFilters{Unconstrained: true, Limit: 50})
	require.NoError(t, err, "one corrupt pin record must not fail the page")
	byID := map[uuid.UUID]instance.InstanceSummary{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	require.Len(t, byID, 6)

	assert.Equal(t, instance.ModelPinsVersionSnapshot, byID[pinned.ID].ModelPinsVersion)
	assert.Equal(t, map[string]string{"worker": "vendor/version-x"}, byID[pinned.ID].ModelPins)
	assert.Empty(t, byID[pinned.ID].ModelPinRoutes)

	assert.Equal(t, instance.ModelPinsVersionSnapshot, byID[empty.ID].ModelPinsVersion)
	require.NotNil(t, byID[empty.ID].ModelPins, "an empty snapshot is not a legacy record")
	assert.Empty(t, byID[empty.ID].ModelPins)

	assert.Equal(t, map[string]string{"azure": "azure"}, byID[opaque.ID].ModelPinRoutes)

	assert.Equal(t, instance.ModelPinsVersionLegacy, byID[legacy.ID].ModelPinsVersion)
	assert.Nil(t, byID[legacy.ID].ModelPins)

	for name, id := range map[string]uuid.UUID{"invalid pins": corrupt.ID, "invalid route": badRoute.ID} {
		assert.Equal(t, instance.ModelPinsVersionUnreadable, byID[id].ModelPinsVersion, name)
		assert.Nil(t, byID[id].ModelPins, name)
		assert.Nil(t, byID[id].ModelPinRoutes, name)
	}
}
