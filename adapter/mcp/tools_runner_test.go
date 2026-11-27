package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
)

type stubRunnerService struct {
	views    []runner.RunnerView
	detail   runner.RunnerDetail
	events   []runner.PoolEvent
	gotLim   int
	gotPat   runner.RunnerConfigPatch
	gotSrc   runner.SourceID
	gotID    runner.RunnerID
	cordons  []bool
	drain    runner.DrainProgress
	gotDrain runner.RunnerID
}

func stubAdmin(ctx context.Context) error {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.ErrUnauthenticated
	}
	if !id.IsAdmin() {
		return identity.ErrForbidden
	}
	return nil
}

func (s *stubRunnerService) List(context.Context) ([]runner.RunnerView, error) { return s.views, nil }
func (s *stubRunnerService) Get(_ context.Context, id runner.RunnerID) (runner.RunnerDetail, error) {
	if id != runner.BuiltInRunnerID {
		return runner.RunnerDetail{}, runner.ErrRunnerNotFound
	}
	return s.detail, nil
}
func (s *stubRunnerService) Events(_ context.Context, limit int) ([]runner.PoolEvent, error) {
	s.gotLim = limit
	return s.events, nil
}
func (s *stubRunnerService) Update(ctx context.Context, id runner.RunnerID, p runner.RunnerConfigPatch) (runner.Runner, error) {
	if err := stubAdmin(ctx); err != nil {
		return runner.Runner{}, err
	}
	if p.StepLimit != nil && !runner.ValidStepLimit(*p.StepLimit) {
		return runner.Runner{}, runner.ErrInvalidStepLimit
	}
	s.gotID, s.gotPat = id, p
	return runner.Runner{ID: id, StepLimit: *p.StepLimit}, nil
}
func (s *stubRunnerService) Cordon(ctx context.Context, id runner.RunnerID) error {
	if err := stubAdmin(ctx); err != nil {
		return err
	}
	s.gotID = id
	s.cordons = append(s.cordons, true)
	return nil
}
func (s *stubRunnerService) Uncordon(ctx context.Context, id runner.RunnerID) error {
	if err := stubAdmin(ctx); err != nil {
		return err
	}
	s.gotID = id
	s.cordons = append(s.cordons, false)
	return nil
}
func (s *stubRunnerService) ProbeSource(ctx context.Context, id runner.SourceID) (runner.SourceHealth, error) {
	if err := stubAdmin(ctx); err != nil {
		return runner.SourceHealth{}, err
	}
	s.gotSrc = id
	return runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}, nil
}

func (s *stubRunnerService) Drain(ctx context.Context, id runner.RunnerID) (runner.DrainProgress, error) {
	if err := stubAdmin(ctx); err != nil {
		return runner.DrainProgress{}, err
	}
	s.gotDrain = id
	return s.drain, nil
}

func sampleRunnerStub() *stubRunnerService {
	src := runner.Source{ID: "builtin/anthropic_subscription/-", RunnerID: runner.BuiltInRunnerID, Kind: runner.CredAnthropicSubscription,
		Families: []modelidentity.Family{"anthropic"}, Health: runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}}
	view := runner.RunnerView{Runner: runner.Runner{ID: runner.BuiltInRunnerID, Name: "server", BuiltIn: true, StepLimit: 8}, Sources: []runner.Source{src}, SlotsUsed: 2}
	vis := uuid.New()
	return &stubRunnerService{
		views: []runner.RunnerView{view},
		detail: runner.RunnerDetail{RunnerView: view, Assignments: []runner.AssignmentView{
			{JobID: uuid.New(), RunID: &vis, NodeID: "n1", State: runner.JobRunning, SourceID: src.ID},
			{JobID: uuid.New(), State: runner.JobRunning, SourceID: src.ID, Hidden: true},
		}},
		events: []runner.PoolEvent{{ID: uuid.New(), Kind: runner.EventSourceLoggedOut, RunnerID: runner.BuiltInRunnerID}},
	}
}

func runnerToolServer(svc adaptmcp.RunnerService) *adaptmcp.Server {
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{Runners: svc})
	adaptmcp.RegisterRunnerTools(srv)
	return srv
}

var (
	runnerUser  = identity.Identity{UserID: uuid.New(), Username: "u", Role: identity.RoleUser}
	runnerAdmin = identity.Identity{UserID: uuid.New(), Username: "root", Role: identity.RoleAdmin}
)

func TestRunnerTools_ListAndGet(t *testing.T) {
	srv := runnerToolServer(sampleRunnerStub())
	res := callToolWithIdentity(t, srv, "runner_list", map[string]any{}, runnerUser)
	require.False(t, res.IsError, extractText(t, res))
	var list struct {
		Runners []map[string]any `json:"runners"`
	}
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &list))
	require.Len(t, list.Runners, 1)
	assert.Equal(t, "builtin", list.Runners[0]["id"])
	assert.EqualValues(t, 2, list.Runners[0]["slotsUsed"])

	res = callToolWithIdentity(t, srv, "runner_get", map[string]any{"id": "builtin"}, runnerUser)
	require.False(t, res.IsError)
	var d map[string]any
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &d))
	hidden := d["assignments"].([]any)[1].(map[string]any)
	assert.Equal(t, true, hidden["hidden"])
	assert.Nil(t, hidden["runId"])

	res = callToolWithIdentity(t, srv, "runner_get", map[string]any{"id": "laptop"}, runnerUser)
	assert.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "runner not found")
}

func TestRunnerTools_Events(t *testing.T) {
	stub := sampleRunnerStub()
	srv := runnerToolServer(stub)
	res := callToolWithIdentity(t, srv, "runner_events", map[string]any{"limit": 7}, runnerUser)
	require.False(t, res.IsError)
	assert.Equal(t, 7, stub.gotLim)
	assert.Contains(t, extractText(t, res), `"source.logged_out"`)
}

func TestRunnerTools_WritesAdminOnly(t *testing.T) {
	stub := sampleRunnerStub()
	srv := runnerToolServer(stub)
	for name, args := range map[string]map[string]any{
		"runner_update":       {"id": "builtin", "stepLimit": 12},
		"runner_cordon":       {"id": "builtin"},
		"runner_uncordon":     {"id": "builtin"},
		"runner_source_probe": {"sourceId": "builtin/anthropic_subscription/-"},
	} {
		res := callToolWithIdentity(t, srv, name, args, runnerUser)
		assert.True(t, res.IsError, name)
		assert.Contains(t, extractText(t, res), "admin only", name)
	}
	assert.Empty(t, stub.cordons)
}

func TestRunnerTools_AdminWrites(t *testing.T) {
	stub := sampleRunnerStub()
	srv := runnerToolServer(stub)
	res := callToolWithIdentity(t, srv, "runner_update", map[string]any{"id": "builtin", "stepLimit": 12, "labels": map[string]any{"site": "hq"}}, runnerAdmin)
	require.False(t, res.IsError, extractText(t, res))
	require.NotNil(t, stub.gotPat.StepLimit)
	assert.Equal(t, 12, *stub.gotPat.StepLimit)
	assert.Equal(t, map[string]string{"site": "hq"}, stub.gotPat.Labels)
	assert.Equal(t, runner.BuiltInRunnerID, stub.gotID)

	res = callToolWithIdentity(t, srv, "runner_update", map[string]any{"id": "builtin", "stepLimit": 1.5}, runnerAdmin)
	assert.True(t, res.IsError)
	res = callToolWithIdentity(t, srv, "runner_update", map[string]any{"id": "builtin", "stepLimit": 0}, runnerAdmin)
	assert.Contains(t, extractText(t, res), "between 1 and 256")

	stub.gotID = ""
	require.False(t, callToolWithIdentity(t, srv, "runner_cordon", map[string]any{"id": "builtin"}, runnerAdmin).IsError)
	assert.Equal(t, runner.BuiltInRunnerID, stub.gotID)
	stub.gotID = ""
	require.False(t, callToolWithIdentity(t, srv, "runner_uncordon", map[string]any{"id": "builtin"}, runnerAdmin).IsError)
	assert.Equal(t, []bool{true, false}, stub.cordons)
	assert.Equal(t, runner.BuiltInRunnerID, stub.gotID)

	res = callToolWithIdentity(t, srv, "runner_source_probe", map[string]any{"sourceId": "builtin/anthropic_subscription/-"}, runnerAdmin)
	require.False(t, res.IsError)
	assert.Equal(t, runner.SourceID("builtin/anthropic_subscription/-"), stub.gotSrc)
}

func TestRunnerTools_ServiceUnavailable(t *testing.T) {
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{})
	adaptmcp.RegisterRunnerTools(srv)
	res := callToolWithIdentity(t, srv, "runner_list", map[string]any{}, runnerUser)
	assert.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "runner service not available")
}
