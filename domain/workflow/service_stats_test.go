package workflow_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStatsReader is an in-memory workflow.StatsReader that records the name it
// was asked for, so the service's authorization gate (the repo is only reached
// when the caller is allowed to see the workflow) is observable.
type fakeStatsReader struct {
	stats  *instance.WorkflowStats
	called bool
	name   string
}

func (f *fakeStatsReader) Stats(_ context.Context, workflowName string, _ time.Time, _ time.Time) (*instance.WorkflowStats, error) {
	f.called = true
	f.name = workflowName
	if f.stats == nil {
		return &instance.WorkflowStats{}, nil
	}
	return f.stats, nil
}

func statsWindow() (time.Time, time.Time) {
	to := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	from := to.AddDate(0, 0, -30)
	return from, to
}

// --- Authorization: the service is the boundary, not the repo ---

func TestService_Stats_OwnerOfPrivate_Delegates(t *testing.T) {
	repo := newFakeRepo()
	reader := &fakeStatsReader{stats: &instance.WorkflowStats{TotalRuns: 7}}
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetStatsReader(reader)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "mine", wf("mine")))

	from, to := statsWindow()
	got, err := svc.Stats(ctxWith(owner, false), "mine", from, to)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 7, got.TotalRuns)
	assert.True(t, reader.called, "authorized caller reaches the repo")
	assert.Equal(t, "mine", reader.name)
}

func TestService_Stats_PublicShared_Delegates(t *testing.T) {
	repo := newFakeRepo()
	pubID := uuid.New()
	pubs := &fakePublishers{pub: map[string]workflow.Publication{
		"shared-public": {Publisher: pubID, Visibility: workflow.VisibilityPublic},
	}}
	reader := &fakeStatsReader{stats: &instance.WorkflowStats{TotalRuns: 3}}
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetStatsReader(reader)
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "shared-public", wf("shared-public")))

	from, to := statsWindow()
	got, err := svc.Stats(ctxWith(uuid.New(), false), "shared-public", from, to)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 3, got.TotalRuns)
	assert.True(t, reader.called)
}

func TestService_Stats_DepartmentShared_NonMember_NotFound(t *testing.T) {
	repo := newFakeRepo()
	pubID := uuid.New()
	pubs := &fakePublishers{pub: map[string]workflow.Publication{
		"dept-only": {Publisher: pubID, Visibility: workflow.VisibilityDepartment, Department: "finance"},
	}}
	reader := &fakeStatsReader{}
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetStatsReader(reader)
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "dept-only", wf("dept-only")))

	// Caller is in "marketing", not "finance" — must not see the publication.
	from, to := statsWindow()
	_, err := svc.Stats(ctxWithDepts(uuid.New(), false, "marketing"), "dept-only", from, to)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound, "denial is 404-mapping, not an existence oracle")
	assert.False(t, reader.called, "the repo is never reached for a denied caller")
}

func TestService_Stats_DepartmentShared_Member_Delegates(t *testing.T) {
	repo := newFakeRepo()
	pubID := uuid.New()
	pubs := &fakePublishers{pub: map[string]workflow.Publication{
		"dept-only": {Publisher: pubID, Visibility: workflow.VisibilityDepartment, Department: "finance"},
	}}
	reader := &fakeStatsReader{stats: &instance.WorkflowStats{TotalRuns: 5}}
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetStatsReader(reader)
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "dept-only", wf("dept-only")))

	from, to := statsWindow()
	got, err := svc.Stats(ctxWithDepts(uuid.New(), false, "finance"), "dept-only", from, to)
	require.NoError(t, err)
	assert.Equal(t, 5, got.TotalRuns)
	assert.True(t, reader.called)
}

func TestService_Stats_OtherUsersPrivate_NotFound(t *testing.T) {
	repo := newFakeRepo()
	reader := &fakeStatsReader{}
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetStatsReader(reader)
	a := uuid.New()
	b := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(a), "a-secret", wf("a-secret")))

	from, to := statsWindow()
	_, err := svc.Stats(ctxWith(b, false), "a-secret", from, to)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
	assert.False(t, reader.called, "another user's private workflow stats are never read")
}

func TestService_Stats_NoIdentity_Rejected(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetStatsReader(&fakeStatsReader{})

	from, to := statsWindow()
	_, err := svc.Stats(context.Background(), "mine", from, to)
	require.Error(t, err)
}

// --- Range validation (defense-in-depth) ---

func TestService_Stats_InvertedRange_Rejected(t *testing.T) {
	repo := newFakeRepo()
	reader := &fakeStatsReader{}
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetStatsReader(reader)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "mine", wf("mine")))

	from, to := statsWindow()
	// from after to.
	_, err := svc.Stats(ctxWith(owner, false), "mine", to, from)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidStatsRange)
	assert.False(t, reader.called)
}

func TestService_Stats_WindowTooLarge_Rejected(t *testing.T) {
	repo := newFakeRepo()
	reader := &fakeStatsReader{}
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetStatsReader(reader)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "mine", wf("mine")))

	to := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	from := to.Add(-workflow.MaxStatsWindow - time.Hour)
	_, err := svc.Stats(ctxWith(owner, false), "mine", from, to)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidStatsRange)
	assert.False(t, reader.called)
}
