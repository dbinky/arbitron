package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// parentToolEnv is an instance_list harness over ONE root and ONE child owned by
// the same user, wired through a real engine so the service-set scoping runs.
// captured() exposes the ListFilters the repository actually received, so a
// handler that dropped the parent argument fails rather than passing silently.
type parentToolEnv struct {
	srv     *adaptmcp.Server
	ctx     context.Context
	repo    *parityInstanceRepo
	rootID  uuid.UUID
	childID uuid.UUID
}

func (e *parentToolEnv) captured() instance.ListFilters { return e.repo.gotFilters }

func newParentToolEnv(t *testing.T) *parentToolEnv {
	t.Helper()
	owner := uuid.New()
	root := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "root", State: instance.StateRunning}
	rootID := root.ID
	child := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "child", State: instance.StateRunning,
		ParentInstanceID: &rootID,
	}
	repo := &parityInstanceRepo{instances: map[uuid.UUID]*instance.Instance{root.ID: root, child.ID: child}}

	eng := engine.NewEngine(nil, repo, nil, nil, nil, map[workflow.NodeType]engine.NodeExecutor{}, nil, nil, nil, "")
	deps := &adaptmcp.Deps{Engine: eng, Instances: repo}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterInstanceTools(srv)

	return &parentToolEnv{
		srv: srv,
		ctx: identity.WithIdentity(context.Background(), identity.Identity{
			UserID: owner, Username: "o", Role: identity.RoleUser,
		}),
		repo:    repo,
		rootID:  root.ID,
		childID: child.ID,
	}
}

// callListToolResult invokes instance_list with args and returns the raw result.
func callListToolResult(t *testing.T, srv *adaptmcp.Server, ctx context.Context, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	req := gomcp.CallToolRequest{}
	req.Params.Name = "instance_list"
	req.Params.Arguments = args
	result := srv.MCPServer().HandleMessage(ctx, mustMarshal(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  req.Params,
	}))
	data, err := json.Marshal(result)
	require.NoError(t, err)
	var resp struct {
		Result gomcp.CallToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(data, &resp))
	return &resp.Result
}

func callListToolWithArgs(t *testing.T, srv *adaptmcp.Server, ctx context.Context, args map[string]any) []map[string]any {
	t.Helper()
	result := callListToolResult(t, srv, ctx, args)
	require.False(t, result.IsError, extractText(t, result))
	var summaries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &summaries))
	return summaries
}

func callListToolExpectError(t *testing.T, srv *adaptmcp.Server, ctx context.Context, args map[string]any) bool {
	t.Helper()
	return callListToolResult(t, srv, ctx, args).IsError
}

// ---------- happy path ----------

func TestMCPInstanceList_RootsOnly(t *testing.T) {
	env := newParentToolEnv(t)

	rows := callListToolWithArgs(t, env.srv, env.ctx, map[string]any{"parent": "roots"})

	assert.Equal(t, instance.ParentScopeRoots, env.captured().ParentScope)
	assert.Nil(t, env.captured().ParentInstanceID)
	require.Len(t, rows, 1)
	assert.Equal(t, env.rootID.String(), rows[0]["id"])
}

func TestMCPInstanceList_ChildrenOfAParent(t *testing.T) {
	env := newParentToolEnv(t)

	rows := callListToolWithArgs(t, env.srv, env.ctx, map[string]any{"parent": env.rootID.String()})

	assert.Equal(t, instance.ParentScopeChildren, env.captured().ParentScope)
	require.NotNil(t, env.captured().ParentInstanceID)
	assert.Equal(t, env.rootID, *env.captured().ParentInstanceID)
	require.Len(t, rows, 1)
	assert.Equal(t, env.childID.String(), rows[0]["id"])
}

// ---------- success ----------

func TestMCPInstanceList_NoParentArgDoesNoNarrowing(t *testing.T) {
	env := newParentToolEnv(t)

	rows := callListToolWithArgs(t, env.srv, env.ctx, map[string]any{})

	assert.Equal(t, instance.ParentScopeAny, env.captured().ParentScope)
	assert.Len(t, rows, 2)
}

func TestMCPInstanceList_ParentComposesWithStateArg(t *testing.T) {
	env := newParentToolEnv(t)

	_ = callListToolWithArgs(t, env.srv, env.ctx, map[string]any{
		"parent": env.rootID.String(),
		"state":  "running",
	})

	f := env.captured()
	assert.Equal(t, instance.ParentScopeChildren, f.ParentScope)
	require.NotNil(t, f.State)
	assert.Equal(t, instance.StateRunning, *f.State,
		"the parent selector must compose with the existing filters, not replace them")
}

// ---------- failure ----------

// A malformed selector is an ERROR result, never a silent unfiltered list.
func TestMCPInstanceList_MalformedParentIsAnError(t *testing.T) {
	env := newParentToolEnv(t)

	for _, raw := range []string{"not-a-uuid", "ROOTS", "root"} {
		isErr := callListToolExpectError(t, env.srv, env.ctx, map[string]any{"parent": raw})
		assert.True(t, isErr, "parent=%q must produce a tool error", raw)
	}
}

// ---------- edge case ----------

func TestMCPInstanceList_UnknownParentReturnsEmpty(t *testing.T) {
	env := newParentToolEnv(t)

	rows := callListToolWithArgs(t, env.srv, env.ctx, map[string]any{"parent": uuid.New().String()})
	assert.Empty(t, rows, "naming a run the caller cannot see returns an empty list, "+
		"identical to a parent with no children — existence is not disclosed")
}

// ---------- paging ----------

// Before explicit paging, the parent scope inherited the repository's unspoken
// 50-row default, so a run with more children than that returned a TRUNCATED
// list indistinguishable from a complete one — an agent reading it would
// conclude the missing children do not exist. The limit is now stated in the
// tool schema and set on every call.
func TestMCPInstanceList_SetsAnExplicitDefaultLimit(t *testing.T) {
	env := newParentToolEnv(t)

	callListToolWithArgs(t, env.srv, env.ctx, map[string]any{"parent": "roots"})

	assert.Equal(t, 50, env.captured().Limit,
		"the page size must be explicit on the filters, not an unspoken repository default")
	assert.Equal(t, 0, env.captured().Offset)
}

func TestMCPInstanceList_HonoursLimitAndOffset(t *testing.T) {
	env := newParentToolEnv(t)

	callListToolWithArgs(t, env.srv, env.ctx, map[string]any{
		"parent": "roots", "limit": float64(10), "offset": float64(20),
	})

	assert.Equal(t, 10, env.captured().Limit)
	assert.Equal(t, 20, env.captured().Offset)
}

// An unbounded limit would let one tool call pull the whole table into a model
// context; the cap is what keeps paging the only way to read a large tree.
func TestMCPInstanceList_ClampsAnOversizedLimit(t *testing.T) {
	env := newParentToolEnv(t)

	callListToolWithArgs(t, env.srv, env.ctx, map[string]any{
		"parent": "roots", "limit": float64(100000),
	})

	assert.Equal(t, 500, env.captured().Limit)
}

// A nonsensical limit falls back to the default rather than to zero, which the
// repository would silently turn back into its own default.
func TestMCPInstanceList_IgnoresANonPositiveLimit(t *testing.T) {
	env := newParentToolEnv(t)

	callListToolWithArgs(t, env.srv, env.ctx, map[string]any{
		"parent": "roots", "limit": float64(0),
	})

	assert.Equal(t, 50, env.captured().Limit)
}
