# Phase 1 — Task 6: MCP list path adoption of the enriched read

**Goal:** Switch the MCP `instance_list` tool from `Engine.ListInstances` (lean summaries) to `Engine.ListEnriched` so MCP clients receive the same enriched fields (run name, submitter, friendly step, model, backend, retry count) the HTTP path now returns. Authorization is unchanged — `ListEnriched` applies the same service-set filters.

**Architecture:** The MCP server already routes `instance_list` through `srv.deps.Engine.ListInstances` (`adapter/mcp/tools_instance.go:115`). This task switches that single call site to `ListEnriched` and updates the `deps.Engine` interface to include the method. The MCP path does not carry the `dismissed` decoration (that is an HTTP/view concern); it returns the enriched domain rows directly via `MarshalResult`. No new component — just adopting the shared enriched read path for parity, as the design's "Deferred to plans" item requires.

**Tech Stack:** Go. Tests with `testing` + `testify`, following `adapter/mcp/tools_instance_test.go`'s `stubEngine` pattern.

---

### Task 6: MCP enriched list

**Files:**
- Modify: `adapter/mcp/deps.go:25` (extend the `Engine` interface with `ListEnriched`)
- Modify: `adapter/mcp/tools_instance.go:102-123` (switch `instanceListHandler` to `ListEnriched`)
- Modify: `adapter/mcp/tools_instance_test.go` (extend `stubEngine` + add enriched assertion)

- [x] Step 1: Write the failing test asserting the MCP list tool returns enriched fields

Append to `adapter/mcp/tools_instance_test.go`:

```go
func TestInstanceList_ReturnsEnrichedFields(t *testing.T) {
	srv := newTestServer(t)
	id := uuid.New()
	eng := srv.deps.Engine.(*stubEngine)
	eng.listEnrichedFn = func(_ context.Context, _ instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		return []instance.EnrichedInstanceSummary{{
			InstanceSummary: instance.InstanceSummary{ID: id, WorkflowName: "wf", State: instance.StateRunning, Priority: instance.PriorityNormal, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			RunName:         "MyTitle — wf",
			Submitter:       instance.Submitter{Name: "alice"},
			FriendlyStep:    "Generate",
			Model:           "gpt-4o",
			Backend:         "claude-code",
			RetryCount:      2,
		}}, nil
	}

	res, err := callTool(t, srv, "instance_list", nil)
	require.NoError(t, err)
	require.False(t, res.IsError, "tool must not error")
	body := toolResultText(t, res)
	assert.Contains(t, body, "MyTitle — wf", "run name reaches the MCP result")
	assert.Contains(t, body, "Generate", "friendly step reaches the MCP result")
	assert.Contains(t, body, "gpt-4o", "model reaches the MCP result")
	assert.Contains(t, body, "claude-code", "backend reaches the MCP result")
}
```

Note: `callTool` and `toolResultText` are existing helpers in `adapter/mcp/tools_instance_test.go`. Confirm their exact names:

```bash
rg -n "func callTool|func toolResultText|func newTestServer" adapter/mcp/tools_instance_test.go adapter/mcp/server_test.go | head
```

If the helpers are named differently (e.g. `callToolResult`, `resultText`), use those exact names in the test above.

- [x] Step 2: Run it, confirm it fails

```bash
go test ./adapter/mcp/ -run TestInstanceList_ReturnsEnrichedFields
```

Expected: build failure — `undefined: eng.listEnrichedFn` (the `stubEngine` does not yet have a `ListEnriched` method), and the tool still calls `ListInstances` so the enriched fields are absent.

- [x] Step 3: Extend the `deps.Engine` interface and `stubEngine`

In `adapter/mcp/deps.go`, add to the `Engine` interface (after `ListInstances`):

```go
	// ListEnriched returns the authorized page with additive enriched fields
	// (run name, submitter, friendly step, model, backend, retry count,
	// queue position). Same authz as ListInstances.
	ListEnriched(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error)
```

In `adapter/mcp/tools_instance_test.go`, add to `stubEngine`:

```go
	listEnrichedFn func(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error)
```

and the method:

```go
func (s *stubEngine) ListEnriched(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
	if s.listEnrichedFn != nil {
		return s.listEnrichedFn(ctx, filters)
	}
	// Fallback: wrap the plain list so existing tests that only seed
	// listFn/listEnrichedFn keep working.
	summaries, err := s.ListInstances(ctx, filters)
	if err != nil {
		return nil, err
	}
	out := make([]instance.EnrichedInstanceSummary, len(summaries))
	for i, sm := range summaries {
		out[i] = instance.EnrichedInstanceSummary{InstanceSummary: sm}
	}
	return out, nil
}
```

If `stubEngine` is defined in a different test file (e.g. `adapter/mcp/parity_test.go`), add the field + method there instead. Confirm:

```bash
rg -n "type stubEngine struct" adapter/mcp/
```

- [x] Step 4: Switch the `instanceListHandler` to call `ListEnriched`

In `adapter/mcp/tools_instance.go`, replace the `instanceListHandler` body (lines 102-123) with:

```go
func instanceListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()

		var filters instance.ListFilters
		if stateStr, _ := args["state"].(string); stateStr != "" {
			s := instance.State(stateStr)
			filters.State = &s
		}
		if wfName, _ := args["workflowName"].(string); wfName != "" {
			filters.WorkflowName = &wfName
		}

		enriched, err := srv.deps.Engine.ListEnriched(ctx, filters)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to list instances: %s", err)), nil
		}
		if enriched == nil {
			enriched = []instance.EnrichedInstanceSummary{}
		}
		return MarshalResult(enriched)
	}
}
```

- [x] Step 5: Run the test, confirm it passes

```bash
go test ./adapter/mcp/ -run TestInstanceList_ReturnsEnrichedFields
```

Expected:

```
ok  	github.com/arbitron/arbitron/adapter/mcp	0.00s
```

- [x] Step 6: Confirm the full test gate passes (the concrete `*engine.Engine` satisfies the widened `deps.Engine` interface because Task 4 added `ListEnriched` to it)

```bash
go build ./... && go test ./...
```

Expected: no build errors; all package tests pass.

- [x] Step 7: Commit

```bash
git add adapter/mcp/deps.go adapter/mcp/tools_instance.go adapter/mcp/tools_instance_test.go
git commit -m "feat(mcp): instance_list returns enriched fields (run name, submitter, step, model, backend, retry)"
```