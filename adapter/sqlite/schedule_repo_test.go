package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newScheduleRepo(t *testing.T) (*ScheduleRepo, testDB, uuid.UUID) {
	t.Helper()
	db := setupTestDB(t)
	repo := NewScheduleRepo(db.DB)
	creator := seedUser(t, db, "creator-"+uuid.NewString()[:8])
	return repo, db, creator
}

func newSchedule(creator uuid.UUID) *schedule.Schedule {
	return &schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "report",
		CronExpr:     "0 9 * * 1-5",
		Timezone:     "America/New_York",
		CreatorID:    creator,
		Department:   "eng",
		Enabled:      true,
		Variables:    map[string]any{"x": float64(1)},
	}
}

// --- Happy-path ---

func TestScheduleRepo_CreateThenGet_RoundTrip(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	ctx := context.Background()
	s := newSchedule(creator)
	require.NoError(t, repo.Create(ctx, s))

	got, err := repo.Get(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, s.ID, got.ID)
	assert.Equal(t, "report", got.WorkflowName)
	assert.Equal(t, "0 9 * * 1-5", got.CronExpr)
	assert.Equal(t, "America/New_York", got.Timezone)
	assert.Equal(t, creator, got.CreatorID)
	assert.Equal(t, "eng", got.Department)
	assert.True(t, got.Enabled)
	assert.Equal(t, map[string]any{"x": float64(1)}, got.Variables)
	assert.Nil(t, got.LastFiredAt)
	assert.False(t, got.CreatedAt.IsZero())
	assert.False(t, got.UpdatedAt.IsZero())
}

func TestScheduleRepo_List_FiltersByCreator(t *testing.T) {
	repo, db, u1 := newScheduleRepo(t)
	ctx := context.Background()
	u2 := seedUser(t, db, "u2-"+uuid.NewString()[:8])

	s1 := newSchedule(u1)
	s2 := newSchedule(u2)
	require.NoError(t, repo.Create(ctx, s1))
	require.NoError(t, repo.Create(ctx, s2))

	own, err := repo.List(ctx, schedule.ListFilters{CreatorID: &u1})
	require.NoError(t, err)
	require.Len(t, own, 1)
	assert.Equal(t, s1.ID, own[0].ID)

	all, err := repo.List(ctx, schedule.ListFilters{})
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestScheduleRepo_ListEnabled_OmitsDisabled(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	ctx := context.Background()
	on := newSchedule(creator)
	off := newSchedule(creator)
	off.Enabled = false
	require.NoError(t, repo.Create(ctx, on))
	require.NoError(t, repo.Create(ctx, off))

	enabled, err := repo.ListEnabled(ctx)
	require.NoError(t, err)
	require.Len(t, enabled, 1)
	assert.Equal(t, on.ID, enabled[0].ID)
}

// --- Input-validation (persistence-level) ---

func TestScheduleRepo_Create_DuplicateID_ReturnsErrExists(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	ctx := context.Background()
	s := newSchedule(creator)
	require.NoError(t, repo.Create(ctx, s))
	err := repo.Create(ctx, s)
	assert.ErrorIs(t, err, schedule.ErrExists)
}

func TestScheduleRepo_List_FilterByEnabledAndWorkflow(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	ctx := context.Background()
	a := newSchedule(creator)
	a.WorkflowName = "report"
	b := newSchedule(creator)
	b.WorkflowName = "cleanup"
	c := newSchedule(creator)
	c.WorkflowName = "report"
	c.Enabled = false
	require.NoError(t, repo.Create(ctx, a))
	require.NoError(t, repo.Create(ctx, b))
	require.NoError(t, repo.Create(ctx, c))

	enabled := true
	wf := "report"
	got, err := repo.List(ctx, schedule.ListFilters{Enabled: &enabled, WorkflowName: &wf})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, a.ID, got[0].ID)
}

// --- Error-handling ---

func TestScheduleRepo_Get_NotFound(t *testing.T) {
	repo, _, _ := newScheduleRepo(t)
	_, err := repo.Get(context.Background(), uuid.New())
	assert.ErrorIs(t, err, schedule.ErrNotFound)
}

func TestScheduleRepo_Update_NotFound(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	s := newSchedule(creator)
	err := repo.Update(context.Background(), s)
	assert.ErrorIs(t, err, schedule.ErrNotFound)
}

func TestScheduleRepo_Delete_NotFound(t *testing.T) {
	repo, _, _ := newScheduleRepo(t)
	err := repo.Delete(context.Background(), uuid.New())
	assert.ErrorIs(t, err, schedule.ErrNotFound)
}

func TestScheduleRepo_SetLastFired_NotFound(t *testing.T) {
	repo, _, _ := newScheduleRepo(t)
	err := repo.SetLastFired(context.Background(), uuid.New(), time.Now())
	assert.ErrorIs(t, err, schedule.ErrNotFound)
}

// --- Edge-cases / boundary ---

func TestScheduleRepo_Create_NilVariables_StoresEmptyObject(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	ctx := context.Background()
	s := newSchedule(creator)
	s.Variables = nil
	require.NoError(t, repo.Create(ctx, s))

	got, err := repo.Get(ctx, s.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Variables)
	assert.Len(t, got.Variables, 0)
}

func TestScheduleRepo_SetLastFired_ThenGet(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	ctx := context.Background()
	s := newSchedule(creator)
	require.NoError(t, repo.Create(ctx, s))

	at := time.Date(2026, 6, 8, 13, 0, 0, 0, time.UTC)
	require.NoError(t, repo.SetLastFired(ctx, s.ID, at))

	got, err := repo.Get(ctx, s.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastFiredAt)
	assert.True(t, got.LastFiredAt.Equal(at.Truncate(time.Second)))
	assert.Equal(t, time.UTC, got.LastFiredAt.Location())
}

func TestScheduleRepo_Update_TogglesEnabled(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	ctx := context.Background()
	s := newSchedule(creator)
	require.NoError(t, repo.Create(ctx, s))

	s.Enabled = false
	require.NoError(t, repo.Update(ctx, s))
	got, err := repo.Get(ctx, s.ID)
	require.NoError(t, err)
	assert.False(t, got.Enabled)
	en, err := repo.ListEnabled(ctx)
	require.NoError(t, err)
	assert.Len(t, en, 0)

	s.Enabled = true
	require.NoError(t, repo.Update(ctx, s))
	got, err = repo.Get(ctx, s.ID)
	require.NoError(t, err)
	assert.True(t, got.Enabled)
	en, err = repo.ListEnabled(ctx)
	require.NoError(t, err)
	assert.Len(t, en, 1)
}

// --- Integration (survives restart) ---

func TestScheduleRepo_PersistsAcrossReopen(t *testing.T) {
	tmp := t.TempDir() + "/sched.db"
	db, err := Open(tmp)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))

	ctx := context.Background()
	users := NewUserRepo(db)
	creator := uuid.New()
	require.NoError(t, users.Create(ctx, &instance.User{
		ID:       creator,
		Username: "persist-" + uuid.NewString()[:8],
		Role:     identity.RoleUser,
	}))

	repo := NewScheduleRepo(db)
	s := newSchedule(creator)
	require.NoError(t, repo.Create(ctx, s))
	require.NoError(t, db.Close())

	db2, err := Open(tmp)
	require.NoError(t, err)
	t.Cleanup(func() { db2.Close() })
	require.NoError(t, RunMigrations(db2))

	repo2 := NewScheduleRepo(db2)
	got, err := repo2.Get(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "report", got.WorkflowName)
	assert.Equal(t, "America/New_York", got.Timezone)
	assert.Equal(t, "eng", got.Department)
}

func TestScheduleRepo_SatisfiesPort(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	var port schedule.ScheduleRepository = repo
	s := newSchedule(creator)
	require.NoError(t, port.Create(context.Background(), s))
}

var _ schedule.ScheduleRepository = (*ScheduleRepo)(nil)
