package mcp_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- in-test schedule repo + canTrigger ---

type mcpSchedRepo struct {
	m map[uuid.UUID]*schedule.Schedule
}

func newMCPSchedRepo() *mcpSchedRepo { return &mcpSchedRepo{m: map[uuid.UUID]*schedule.Schedule{}} }

func (r *mcpSchedRepo) Create(_ context.Context, s *schedule.Schedule) error {
	r.m[s.ID] = s
	return nil
}
func (r *mcpSchedRepo) Get(_ context.Context, id uuid.UUID) (*schedule.Schedule, error) {
	s, ok := r.m[id]
	if !ok {
		return nil, schedule.ErrNotFound
	}
	cp := *s
	return &cp, nil
}
func (r *mcpSchedRepo) List(_ context.Context, f schedule.ListFilters) ([]schedule.Schedule, error) {
	var out []schedule.Schedule
	for _, s := range r.m {
		if f.CreatorID != nil && s.CreatorID != *f.CreatorID {
			continue
		}
		out = append(out, *s)
	}
	return out, nil
}
func (r *mcpSchedRepo) ListEnabled(context.Context) ([]schedule.Schedule, error) { return nil, nil }
func (r *mcpSchedRepo) Update(_ context.Context, s *schedule.Schedule) error {
	if _, ok := r.m[s.ID]; !ok {
		return schedule.ErrNotFound
	}
	r.m[s.ID] = s
	return nil
}
func (r *mcpSchedRepo) Delete(_ context.Context, id uuid.UUID) error             { delete(r.m, id); return nil }
func (r *mcpSchedRepo) SetLastFired(context.Context, uuid.UUID, time.Time) error { return nil }

type mcpCanTrigger struct{ allow bool }

func (c mcpCanTrigger) CanTrigger(context.Context, identity.Identity, string) (bool, error) {
	return c.allow, nil
}

func newScheduleServer(repo schedule.ScheduleRepository, allow bool) *adaptmcp.Server {
	svc := schedule.NewService(repo, mcpCanTrigger{allow: allow})
	deps := &adaptmcp.Deps{ScheduleService: svc}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterScheduleTools(srv)
	return srv
}

func callSchedule(t *testing.T, srv *adaptmcp.Server, ctx context.Context, name string, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	req := gomcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	result := srv.MCPServer().HandleMessage(ctx, mustMarshalSched(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": req.Params,
	}))
	data, err := json.Marshal(result)
	require.NoError(t, err)
	var resp struct {
		Result gomcp.CallToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(data, &resp))
	return &resp.Result
}

func mustMarshalSched(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func schedTextOf(t *testing.T, r *gomcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, r.Content)
	tc, ok := r.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	return tc.Text
}

func userCtx(id uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: id, Username: "u", Role: identity.RoleUser})
}

// --- Tests ---

func TestScheduleTools_Create_And_List_MCP(t *testing.T) {
	repo := newMCPSchedRepo()
	srv := newScheduleServer(repo, true)

	u1 := uuid.New()
	res := callSchedule(t, srv, userCtx(u1), "schedule_create", map[string]any{
		"workflowName": "report", "cronExpr": "0 9 * * *", "timezone": "UTC",
	})
	require.False(t, res.IsError, schedTextOf(t, res))

	// u1 lists their own.
	res = callSchedule(t, srv, userCtx(u1), "schedule_list", nil)
	require.False(t, res.IsError)
	assert.Contains(t, schedTextOf(t, res), "report")

	// A different non-admin user sees none of u1's schedules.
	u2 := uuid.New()
	res = callSchedule(t, srv, userCtx(u2), "schedule_list", nil)
	require.False(t, res.IsError)
	var body struct {
		Schedules []map[string]any `json:"schedules"`
	}
	require.NoError(t, json.Unmarshal([]byte(schedTextOf(t, res)), &body))
	assert.Len(t, body.Schedules, 0, "non-admin must only list their own schedules")
}

func TestScheduleTools_Create_Forbidden_MCP(t *testing.T) {
	srv := newScheduleServer(newMCPSchedRepo(), false) // canTrigger=false
	res := callSchedule(t, srv, userCtx(uuid.New()), "schedule_create", map[string]any{
		"workflowName": "report", "cronExpr": "0 9 * * *",
	})
	assert.True(t, res.IsError, "scheduling a workflow you cannot trigger must be an MCP error")
}

func TestScheduleTools_Create_InvalidCron_MCP(t *testing.T) {
	srv := newScheduleServer(newMCPSchedRepo(), true)
	res := callSchedule(t, srv, userCtx(uuid.New()), "schedule_create", map[string]any{
		"workflowName": "report", "cronExpr": "nope",
	})
	assert.True(t, res.IsError)
}
