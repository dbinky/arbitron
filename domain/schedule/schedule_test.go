package schedule_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
)

// fakeRepo is a no-op implementation used only to prove ScheduleRepository is a
// satisfiable port with the full CRUD + ListEnabled + SetLastFired method set.
type fakePortRepo struct{}

func (fakePortRepo) Create(ctx context.Context, s *schedule.Schedule) error { return nil }
func (fakePortRepo) Get(ctx context.Context, id uuid.UUID) (*schedule.Schedule, error) {
	return nil, nil
}
func (fakePortRepo) List(ctx context.Context, f schedule.ListFilters) ([]schedule.Schedule, error) {
	return nil, nil
}
func (fakePortRepo) ListEnabled(ctx context.Context) ([]schedule.Schedule, error) { return nil, nil }
func (fakePortRepo) Update(ctx context.Context, s *schedule.Schedule) error       { return nil }
func (fakePortRepo) Delete(ctx context.Context, id uuid.UUID) error               { return nil }
func (fakePortRepo) SetLastFired(ctx context.Context, id uuid.UUID, at time.Time) error {
	return nil
}

// --- Integration (port shape) ---

var _ schedule.ScheduleRepository = (*fakePortRepo)(nil)

func TestScheduleRepository_PortShape(t *testing.T) {
	var r schedule.ScheduleRepository = fakePortRepo{}
	_ = r
}
