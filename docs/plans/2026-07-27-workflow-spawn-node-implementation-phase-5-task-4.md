# Phase 5 — Task 4: MCP parent selector at REST parity

**Goal:** Add the same three-state parent selector to the MCP `instance_list` tool and extend the existing REST/MCP parity test to cover it.

**Architecture:** `instanceListHandler` decodes the `parent` argument through the same `instance.ParseParentScope` decoder the REST handler uses, so the two surfaces share ONE wire-format definition and cannot drift. Both continue to route through `Engine.ListEnriched` rather than touching the repository, honouring the service-layer authorization boundary `AGENTS.md` states directly. The repository already asserts REST/MCP parity with a dedicated test; the parent filter is brought under the same discipline.

**Tech Stack:** Go 1.25.5, `mark3labs/mcp-go`, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 4: MCP parent selector

**Files:**
- Modify: `adapter/mcp/tools_instance.go:23-29` (tool schema), `:102-124` (`instanceListHandler`)
- Modify: `adapter/mcp/parity_test.go` (extend the parity assertion)
- Test:   `adapter/mcp/tools_instance_parent_test.go`

---

- [x] **Step 1: Write the failing test**

Create `adapter/mcp/tools_instance_parent_test.go`:

```go
package mcp_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- happy path ----------

func TestMCPInstanceList_RootsOnly(t *testing.T) {
	env := newParentToolEnv(t)

	rows := callListToolWithArgs(t, env.srv, env.ctx, map[string]any{"parent": "roots"})

	assert.Equal(t, instance.ParentScopeRoots, env.captured().ParentScope)
	assert.Nil(t, env.captured().ParentInstanceID)
	require.Len(t, rows, 1)
	assert.Equal(t, env.rootID.String(), rows[0]["ID"])
}

func TestMCPInstanceList_ChildrenOfAParent(t *testing.T) {
	env := newParentToolEnv(t)

	rows := callListToolWithArgs(t, env.srv, env.ctx, map[string]any{"parent": env.rootID.String()})

	assert.Equal(t, instance.ParentScopeChildren, env.captured().ParentScope)
	require.NotNil(t, env.captured().ParentInstanceID)
	assert.Equal(t, env.rootID, *env.captured().ParentInstanceID)
	require.Len(t, rows, 1)
	assert.Equal(t, env.childID.String(), rows[0]["ID"])
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

var _ = context.Background
```

> **Adapting the harness.** `adapter/mcp/parity_test.go` already contains
> `callListTool(t, srv, ctx)` and a `parityInstanceRepo` that satisfies
> `instance.InstanceRepository`. Build this file's helpers on those:
> - `newParentToolEnv(t)` — construct a `parityInstanceRepo` holding ONE root and
>   ONE child (`ParentInstanceID` set to the root), owned by the same user; build
>   a real `engine.NewEngine(nil, repo, nil, nil, nil, map[workflow.NodeType]engine.NodeExecutor{}, nil, nil, nil, "")`;
>   register the instance tools on a `NewServerWithDeps` server; return the
>   server, an `identity.WithIdentity` context for that owner, the two ids, and a
>   `captured()` accessor over the `instance.ListFilters` the repo's `List` was
>   called with (add a `gotFilters instance.ListFilters` field to
>   `parityInstanceRepo.List` and HONOUR `ParentScope`/`ParentInstanceID` there,
>   so a handler that dropped the argument would fail).
> - `callListToolWithArgs` / `callListToolExpectError` — copy `callListTool`'s
>   body and set `req.Params.Arguments = args`; the error variant asserts on
>   `resp.Result.IsError` instead of decoding rows.
>
> The MCP surface marshals the DOMAIN struct, so its JSON keys are PascalCase
> (`"ID"`, `"WorkflowName"`) — that is why the assertions above read `rows[0]["ID"]`
> while the REST test reads `rows[0]["id"]`. The existing parity test documents
> this difference; do not "fix" it.

> **Correction applied during implementation.** `instance.InstanceSummary` carries
> lowerCamelCase `json` tags (`json:"id"`, `json:"workflowName"`), so the MCP rows
> use `rows[0]["id"]` too — matching the existing `TestMCPRestParity_InstanceList`,
> which already asserts `mcpSummaries[0]["id"]`. The tests were written against the
> real serialization, not the PascalCase note above. (Non-tagged fields promoted from
> `EnrichedInstanceSummary`, e.g. `RunName`, do keep PascalCase keys.)

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./adapter/mcp/ -run TestMCPInstanceList
```

Expected output:

```
--- FAIL: TestMCPInstanceList_RootsOnly (0.00s)
    tools_instance_parent_test.go:...: Not equal: expected: "roots" actual: ""
--- FAIL: TestMCPInstanceList_MalformedParentIsAnError (0.00s)
    tools_instance_parent_test.go:...: parent="not-a-uuid" must produce a tool error
FAIL	github.com/arbitron/arbitron/adapter/mcp	0.4s
```

- [x] **Step 3: Add the tool argument**

In `adapter/mcp/tools_instance.go`, extend the `instance_list` tool declaration:

```go
	srv.AddTool(
		gomcp.NewTool("instance_list",
			gomcp.WithDescription("List workflow instances with optional filters"),
			gomcp.WithString("state", gomcp.Description("Filter by state (queued, running, waiting, completed, failed, cancelled, paused)")),
			gomcp.WithString("workflowName", gomcp.Description("Filter by workflow name")),
			gomcp.WithString("parent", gomcp.Description(
				"Narrow by position in the spawn hierarchy: omit for all runs, "+
					"\"roots\" for top-level runs only, or an instance UUID for that run's children")),
		),
		instanceListHandler(srv),
	)
```

- [x] **Step 4: Decode the selector in the handler**

In `instanceListHandler`, after the `workflowName` block:

```go
		if wfName, _ := args["workflowName"].(string); wfName != "" {
			filters.WorkflowName = &wfName
		}
		// The parent selector is the ONE caller-supplied narrowing here. It
		// composes with — and can never replace — the owner/department constraints
		// ListEnriched service-sets from the request identity. Decoding goes
		// through the SAME shared domain parser the REST handler uses, so the two
		// surfaces cannot drift on the wire format.
		//
		// A malformed selector is an error, never a silent unfiltered list.
		parentRaw, _ := args["parent"].(string)
		scope, parentID, perr := instance.ParseParentScope(parentRaw)
		if perr != nil {
			return gomcp.NewToolResultError(perr.Error()), nil
		}
		filters.ParentScope = scope
		filters.ParentInstanceID = parentID
```

- [x] **Step 5: Extend the parity test**

In `adapter/mcp/parity_test.go`, append to `TestMCPRestParity_InstanceList` — or
add a sibling test — the parent-filtered comparison:

```go
// TestMCPRestParity_InstanceListParentFilter extends the parity discipline to
// the parent selector: for the same identity and the same selector, MCP and
// REST must return the same scoped set. Without this, one surface could widen
// scope through the new parameter while the other did not.
func TestMCPRestParity_InstanceListParentFilter(t *testing.T) {
	owner := uuid.New()
	root := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "root", State: instance.StateRunning}
	rootID := root.ID
	child := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "child", State: instance.StateRunning,
		ParentInstanceID: &rootID,
	}
	repo := &parityInstanceRepo{instances: map[uuid.UUID]*instance.Instance{root.ID: root, child.ID: child}}

	eng := engine.NewEngine(nil, repo, nil, nil, nil, map[workflow.NodeType]engine.NodeExecutor{}, nil, nil, nil, "")
	idCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: owner, Username: "o", Role: identity.RoleUser,
	})

	deps := &adaptmcp.Deps{Engine: eng, Instances: repo}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterInstanceTools(srv)

	mcpRows := callListToolWithArgs(t, srv, idCtx, map[string]any{"parent": "roots"})
	restRows := callListRESTWithQuery(t, eng, repo, idCtx, "?parent=roots")

	require.Len(t, mcpRows, 1)
	require.Len(t, restRows, 1)
	assert.Equal(t, root.ID.String(), mcpRows[0]["ID"])
	assert.Equal(t, root.ID.String(), restRows[0]["id"],
		"MCP and REST must return the same roots-only set for the same identity")
}
```

> `callListRESTWithQuery` is `callListREST` with the query string appended to the
> request URL. Copy the existing helper's body and add a `query string`
> parameter. `parityInstanceRepo.List` must HONOUR
> `filters.ParentScope`/`filters.ParentInstanceID` (roots => `ParentInstanceID ==
> nil`; children => equal to the filter) or this parity test proves nothing.

- [x] **Step 6: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w adapter/mcp/
go test ./adapter/mcp/ -run 'TestMCPInstanceList|TestMCPRestParity' -v
```

Expected output:

```
--- PASS: TestMCPInstanceList_RootsOnly (0.00s)
--- PASS: TestMCPInstanceList_ChildrenOfAParent (0.00s)
--- PASS: TestMCPInstanceList_NoParentArgDoesNoNarrowing (0.00s)
--- PASS: TestMCPInstanceList_ParentComposesWithStateArg (0.00s)
--- PASS: TestMCPInstanceList_MalformedParentIsAnError (0.00s)
--- PASS: TestMCPInstanceList_UnknownParentReturnsEmpty (0.00s)
--- PASS: TestMCPRestParity_InstanceList (0.00s)
--- PASS: TestMCPRestParity_InstanceListParentFilter (0.00s)
PASS
ok  	github.com/arbitron/arbitron/adapter/mcp	0.5s
```

- [x] **Step 7: Full Go gate**

```bash
go test ./...
```

Expected: `ok` for every package, no `FAIL` lines. In particular
`adapter/mcp/register_test.go` (the registered-tool-name list) must still pass —
the tool name is unchanged, only its argument set grew.

- [x] **Step 8: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add adapter/mcp/tools_instance.go adapter/mcp/tools_instance_parent_test.go adapter/mcp/parity_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(mcp): parent selector on instance_list at REST parity"
```
