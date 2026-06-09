package schedule_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestSchedule_WireMap(t *testing.T) {
	id := uuid.New()
	creator := uuid.New()
	fired := time.Date(2026, 6, 8, 9, 0, 0, 0, time.FixedZone("x", 3600))
	created := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)

	s := schedule.Schedule{
		ID:           id,
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
		CreatorID:    creator,
		Department:   "eng",
		Enabled:      true,
		Variables:    map[string]any{"k": "v"},
		LastFiredAt:  &fired,
		CreatedAt:    created,
		UpdatedAt:    updated,
	}

	m := s.WireMap()

	assert.Equal(t, id.String(), m["id"])
	assert.Equal(t, "report", m["workflowName"])
	assert.Equal(t, "0 9 * * *", m["cronExpr"])
	assert.Equal(t, "UTC", m["timezone"])
	assert.Equal(t, creator.String(), m["creatorId"])
	assert.Equal(t, "eng", m["department"])
	assert.Equal(t, true, m["enabled"])
	assert.Equal(t, map[string]any{"k": "v"}, m["variables"])
	assert.Equal(t, fired.UTC(), m["lastFiredAt"], "lastFiredAt emitted in UTC")
	assert.Equal(t, created.UTC(), m["createdAt"])
	assert.Equal(t, updated.UTC(), m["updatedAt"])
}

func TestSchedule_WireMap_NilLastFired(t *testing.T) {
	s := schedule.Schedule{ID: uuid.New(), CreatorID: uuid.New()}
	m := s.WireMap()
	assert.Nil(t, m["lastFiredAt"], "never-fired schedule emits nil lastFiredAt")
}
