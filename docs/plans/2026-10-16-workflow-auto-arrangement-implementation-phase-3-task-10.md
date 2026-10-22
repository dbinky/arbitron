# Workflow Auto-Arrangement — Phase 3 Task 10: MCP layout surfaces

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Execution discipline:** Expected outputs below are predictions, never recorded test results.
Where an initial run cannot compile/import a new symbol, add only its declaration and a
minimal compiling non-passing body, then rerun and observe the named behavioral assertion
fail before implementing behavior. Keep each subtask green before committing; do not commit
empty success stubs or knowingly failing tests. Test count examples are illustrative: record
the actual command/output at execution. Each data path needs an assertion at its consumer
and an absent/nil/empty/wrong-input negative control; constructor/call-count checks alone
cannot establish that the graph, status, evidence or retry was fed correctly.

**Alignment requirements:** Read contracts §6 (6.6) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** MCP clients see each workflow's saved revision and arrangement status in `workflow_get`, and can retry arrangement with `workflow_layout_retry` and `instance_layout_retry`. Both go through the same authorised `layout.QueryService` that HTTP uses.

**Architecture:** `adapter/mcp/deps.go` declares the consumer-owned `LayoutQueryService` port (the same four methods as HTTP) and the `Deps.Layouts` field. `workflow_get` keeps its manifest fields and adds an `"arrangement"` object with a fixed key set and no geometry. When the service fails or is unwired, that object reports unavailable; it is never silently absent. The two retry tools live in a new `adapter/mcp/tools_layout.go`, registered from `RegisterAllTools`. `cmd/arbitron/main.go:mcpDeps` feeds `Deps.Layouts` from `ds.layoutQuery()` (P3T8), and an e2e test drives `workflow_get` through the real `mcpDeps`.

**Tech Stack:** Go 1.25.5, mark3labs/mcp-go v0.53.0 (`gomcp`, `server`), testify, `domain/layout` (P2T5/P3T8).

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` §3 "MCP surfaces"; contracts §3 "MCP (P3T10)".

Line numbers are from the base checkout. P2T9, P3T8 and P3T9 edit `cmd/arbitron/main.go` first, so find each edit by its quoted anchor.

---

## Interfaces

**Consumes:**

```go
// domain/layout (P2T5 + P3T8)
type View struct {
	SavedRevision string; JobGeneration int64; ResultGeneration int64; State State; DisplayStatus DisplayStatus; Stage Stage
	AttemptsUsed int; AttemptLimit int; Findings []Finding; FailureStage Stage; FailureReason string
	Evidence *Evidence; Contract *RendererContract; Geometry *Geometry; Retained *workflow.RetainedLayout
	Provenance Provenance; Unreviewed bool; RetryEligible bool; Available bool; AvailabilityError string
}
type RetryAck struct { SavedRevision string `json:"savedRevision"`; JobGeneration int64 `json:"jobGeneration"`; State State `json:"state"`; DisplayStatus DisplayStatus `json:"displayStatus"` }
type Finding struct { Criterion Criterion `json:"criterion"`; Severity Severity `json:"severity"`; Source FindingSource `json:"source"`; NodeIDs []string `json:"nodeIds,omitempty"`; EdgeOrdinals []int `json:"edgeOrdinals,omitempty"`; Evidence string `json:"evidence"` }
var ErrRevisionConflict, ErrLegacyRevision, ErrRetryConflict, ErrStoreUnavailable error
const DisplayUnavailable DisplayStatus = "unavailable"
const ProvenanceRetainedPositions Provenance = "retained_positions"; const ProvenanceLegacy Provenance = "legacy"
// domain/workflow (P2T2)
func (w *Workflow) SavedRevision() string
// adapter/mcp (existing)
func safeWorkflowName(name string) bool                                         // helpers.go:16
func MarshalResult(v any) (*gomcp.CallToolResult, error)                         // helpers.go:23
func ParseUUID(request gomcp.CallToolRequest, key string) (uuid.UUID, error)     // helpers.go:34
func workflowSvc(srv *Server) *workflow.Service                                  // tools_workflow.go:26
// adapter/mcp test helpers (existing, package mcp_test): stubWorkflowRepo, stubFileReader (tools_workflow_test.go:28-143),
// callTool / callToolWithIdentity / extractText / mustMarshal (tools_workflow_test.go:157-216), mcpTestUserID
// cmd/arbitron (P3T8)
func (ds *domainStack) layoutQuery() *layout.QueryService
func bootQueryStack(t *testing.T) *domainStack; func queryE2EWorkflow(name, label string) *workflow.Workflow
func manifestRevisionOnDisk(t *testing.T, ds *domainStack, name string) string; func snapshotRevision(t *testing.T, ds *domainStack, id uuid.UUID) string
func adminCtxForLocalUser(t *testing.T, ds *domainStack) context.Context // spawn_e2e_test.go:87
```

**Produces:**

```go
// adapter/mcp/deps.go
type LayoutQueryService interface {
	WorkflowLayout(ctx context.Context, name string) (layout.View, error)
	InstanceLayout(ctx context.Context, id uuid.UUID) (layout.View, error)
	RetryWorkflow(ctx context.Context, name, expectedRevision string) (layout.RetryAck, error)
	RetryInstance(ctx context.Context, id uuid.UUID, expectedRevision string) (layout.RetryAck, error)
}
// Deps gains: Layouts LayoutQueryService
// adapter/mcp/tools_layout.go
func RegisterLayoutTools(srv *Server) // workflow_layout_retry(name, expectedRevision), instance_layout_retry(id, expectedRevision) → RetryAck JSON
```

`workflow_get` result = the manifest object (unchanged keys) plus `"arrangement"` with **exactly** these keys:
`savedRevision, displayStatus, state, stage, attemptsUsed, attemptLimit, findings, failureStage, failureReason, retryEligible, available, availabilityError, provenance`.
Geometry, contract, evidence and retained positions are never included. `findings` is always a JSON array, never `null`. When the query fails or `Deps.Layouts` is nil, the same keys describe an unavailable view: `available:false`, `displayStatus:"unavailable"`, `availabilityError` set to the error text or `"layout service not configured"`, and `savedRevision` taken from the loaded manifest.

Retry tool error messages (tool errors, `IsError: true`):

| Sentinel | Message |
|---|---|
| `layout.ErrRevisionConflict` | `layout revision conflict: expected <expectedRevision> is not current` |
| `layout.ErrRetryConflict` | `layout is not eligible for retry` |
| `layout.ErrLegacyRevision` | `layout revision has no arrangement request (saved before automatic arrangement); save the workflow to arrange it` |
| `workflow.ErrNotFound` / `instance.ErrNotFound` | `workflow not found: <name>` / `instance not found: <id>` |
| `workflow.ErrSharedReadOnly` | `workflow is shared and read-only: <name>` |
| `identity.ErrForbidden`, `workflow.ErrUnauthorized` | `not authorized: <err>` |
| `layout.ErrStoreUnavailable` | `layout status store unavailable; retry later` |
| nil `Deps.Layouts` | `layout service not configured` |
| other | `failed to retry layout: <err>` |

---

### Task 10.1: `Deps.Layouts` + `workflow_get` arrangement summary

**Files:**
- Modify: `adapter/mcp/deps.go:3-18` (imports: add `"github.com/arbitron/arbitron/domain/layout"`), `:41` (declare `LayoutQueryService` after `EngineService`), `:59-170` (`Deps`: add `Layouts` after `BackendHealth`)
- Modify: `adapter/mcp/tools_workflow.go:259-283` (`workflowGetHandler`: final `return MarshalResult(wf)`)
- Create: `adapter/mcp/tools_layout.go` (summary helpers; retry tools are added in 10.2)
- Test:   `adapter/mcp/tools_layout_test.go`

- [x] **Step 1: Write the failing test**

Create `adapter/mcp/tools_layout_test.go`:

```go
package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

// fakeMCPLayouts honours every load-bearing argument: views/revisions are keyed
// by the exact workflow name or instance id; a retry succeeds only when the
// expected revision matches; errs ("workflow:<name>" / "instance:<id>") is
// returned by every method for that subject. Calls are recorded with args.
type fakeMCPLayouts struct {
	mu            sync.Mutex
	workflowViews map[string]layout.View
	workflowRevs  map[string]string
	instanceRevs  map[uuid.UUID]string
	errs          map[string]error
	calls         []string
	contexts      []context.Context
}

func newFakeMCPLayouts() *fakeMCPLayouts {
	return &fakeMCPLayouts{
		workflowViews: map[string]layout.View{},
		workflowRevs:  map[string]string{},
		instanceRevs:  map[uuid.UUID]string{},
		errs:          map[string]error{},
	}
}

func (f *fakeMCPLayouts) record(ctx context.Context, c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	f.contexts = append(f.contexts, ctx)
}

func (f *fakeMCPLayouts) recordedContexts() []context.Context {
    f.mu.Lock()
    defer f.mu.Unlock()
    return append([]context.Context(nil), f.contexts...)
}

func (f *fakeMCPLayouts) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeMCPLayouts) WorkflowLayout(ctx context.Context, name string) (layout.View, error) {
	if err := ctx.Err(); err != nil { return layout.View{}, err }
	f.record(ctx, "WorkflowLayout:" + name)
	if err := f.errs["workflow:"+name]; err != nil {
		return layout.View{}, err
	}
	v, ok := f.workflowViews[name]
	if !ok {
		return layout.View{}, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	return v, nil
}

func (f *fakeMCPLayouts) InstanceLayout(ctx context.Context, id uuid.UUID) (layout.View, error) {
	if err := ctx.Err(); err != nil { return layout.View{}, err }
	f.record(ctx, "InstanceLayout:" + id.String())
	return layout.View{}, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
}

func (f *fakeMCPLayouts) RetryWorkflow(ctx context.Context, name, expected string) (layout.RetryAck, error) {
	if err := ctx.Err(); err != nil { return layout.RetryAck{}, err }
	f.record(ctx, "RetryWorkflow:" + name + "@" + expected)
	if err := f.errs["workflow:"+name]; err != nil {
		return layout.RetryAck{}, err
	}
	rev, ok := f.workflowRevs[name]
	if !ok {
		return layout.RetryAck{}, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	if expected != rev {
		return layout.RetryAck{}, fmt.Errorf("%w: expected %s", layout.ErrRevisionConflict, expected)
	}
	return layout.RetryAck{SavedRevision: rev, JobGeneration: 2, State: layout.StatePending, DisplayStatus: layout.DisplayPending}, nil
}

func (f *fakeMCPLayouts) RetryInstance(ctx context.Context, id uuid.UUID, expected string) (layout.RetryAck, error) {
	if err := ctx.Err(); err != nil { return layout.RetryAck{}, err }
	f.record(ctx, "RetryInstance:" + id.String() + "@" + expected)
	if err := f.errs["instance:"+id.String()]; err != nil {
		return layout.RetryAck{}, err
	}
	rev, ok := f.instanceRevs[id]
	if !ok {
		return layout.RetryAck{}, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	if expected != rev {
		return layout.RetryAck{}, fmt.Errorf("%w: expected %s", layout.ErrRevisionConflict, expected)
	}
	return layout.RetryAck{SavedRevision: rev, JobGeneration: 4, State: layout.StatePending, DisplayStatus: layout.DisplayPending}, nil
}

var _ adaptmcp.LayoutQueryService = (*fakeMCPLayouts)(nil)

// stampedRepo returns a stub repo whose Load serves ONLY the named workflow,
// stamped with rev.
func stampedRepo(name, rev string) *stubWorkflowRepo {
	return &stubWorkflowRepo{
		loadFn: func(ctx context.Context, n string) (*workflow.Workflow, error) {
			if err := ctx.Err(); err != nil { return nil, err }
			if n != name {
				return nil, fmt.Errorf("%w: %s", workflow.ErrNotFound, n)
			}
			return &workflow.Workflow{
				Name:        name,
				Description: "layout wf",
				Nodes:       []workflow.Node{{ID: "a", Type: workflow.NodeTypeTransform, Entrypoint: true}},
				Presentation: &workflow.Presentation{
					Revision: rev, Reason: workflow.SaveReasonUpdate, Scope: workflow.ScopeLabelPrivate, Name: name,
					Retained: workflow.RetainedLayout{Source: workflow.RetainedNone},
				},
			}, nil
		},
	}
}

func newLayoutToolServer(repo *stubWorkflowRepo, layouts adaptmcp.LayoutQueryService) *adaptmcp.Server {
	deps := &adaptmcp.Deps{Workflows: repo, FileReader: &stubFileReader{}, Layouts: layouts}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterWorkflowTools(srv)
	adaptmcp.RegisterLayoutTools(srv)
	return srv
}

var arrangementKeys = []string{
	"attemptLimit", "attemptsUsed", "availabilityError", "available", "displayStatus", "failureReason",
	"failureStage", "findings", "provenance", "retryEligible", "savedRevision", "stage", "state",
}

func decodeArrangement(t *testing.T, text string) (map[string]any, map[string]any) {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &out))
	arr, ok := out["arrangement"].(map[string]any)
	require.True(t, ok, "workflow_get must always include an arrangement object")
	keys := make([]string, 0, len(arr))
	for k := range arr {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, arrangementKeys, keys, "arrangement must carry exactly the contract keys (no geometry)")
	return out, arr
}

func TestWorkflowGet_IncludesArrangementSummary(t *testing.T) {
	layouts := newFakeMCPLayouts()
	geo := layout.Geometry{Format: layout.FormatV1}
	layouts.workflowViews["my-wf"] = layout.View{
		SavedRevision: "rev-1", JobGeneration: 1, State: layout.StateNeedsAttention,
		DisplayStatus: layout.DisplayNeedsAttention, Stage: layout.StageDone, AttemptsUsed: 5, AttemptLimit: 5,
		Findings: []layout.Finding{{
			Criterion: layout.CriterionTextClipped, Severity: layout.SeverityBlocking, Source: layout.SourceReview,
			NodeIDs: []string{"a"}, Evidence: "label clipped",
		}},
		Geometry: &geo, Provenance: layout.ProvenanceArranged, RetryEligible: true, Available: true,
	}
	srv := newLayoutToolServer(stampedRepo("my-wf", "rev-1"), layouts)

	result := callTool(t, srv, "workflow_get", map[string]any{"name": "my-wf"})
	require.False(t, result.IsError, extractText(t, result))
	out, arr := decodeArrangement(t, extractText(t, result))

	assert.Equal(t, "my-wf", out["name"], "manifest fields stay compatible")
	assert.Equal(t, "layout wf", out["description"])
	assert.Equal(t, "rev-1", out["presentation"].(map[string]any)["revision"])

	assert.Equal(t, "rev-1", arr["savedRevision"])
	assert.Equal(t, "needs_attention", arr["displayStatus"])
	assert.Equal(t, "needs_attention", arr["state"])
	assert.Equal(t, float64(5), arr["attemptsUsed"])
	assert.Equal(t, float64(5), arr["attemptLimit"])
	assert.Equal(t, true, arr["retryEligible"])
	assert.Equal(t, true, arr["available"])
	assert.Equal(t, "arranged", arr["provenance"])
	findings := arr["findings"].([]any)
	require.Len(t, findings, 1)
	assert.Equal(t, "text_clipped", findings[0].(map[string]any)["criterion"])
	assert.Equal(t, []string{"WorkflowLayout:my-wf"}, layouts.recorded())
}

func TestWorkflowGet_ArrangementUnavailableWhenQueryFails(t *testing.T) {
	layouts := newFakeMCPLayouts()
	layouts.errs["workflow:my-wf"] = fmt.Errorf("read: %w", layout.ErrStoreUnavailable)
	srv := newLayoutToolServer(stampedRepo("my-wf", "rev-1"), layouts)

	result := callTool(t, srv, "workflow_get", map[string]any{"name": "my-wf"})
	require.False(t, result.IsError, "a layout failure must not fail the manifest read")
	_, arr := decodeArrangement(t, extractText(t, result))
	assert.Equal(t, "rev-1", arr["savedRevision"], "the saved revision still comes from the manifest")
	assert.Equal(t, "unavailable", arr["displayStatus"])
	assert.Equal(t, false, arr["available"])
	assert.Contains(t, arr["availabilityError"], "status store unavailable")
	assert.Equal(t, []any{}, arr["findings"])
}

func TestWorkflowGet_ArrangementUnavailableWhenUnwired(t *testing.T) {
	srv := newLayoutToolServer(stampedRepo("my-wf", "rev-1"), nil)

	result := callTool(t, srv, "workflow_get", map[string]any{"name": "my-wf"})
	require.False(t, result.IsError)
	_, arr := decodeArrangement(t, extractText(t, result))
	assert.Equal(t, false, arr["available"])
	assert.Equal(t, "layout service not configured", arr["availabilityError"])
	assert.Equal(t, "rev-1", arr["savedRevision"])
}

func TestWorkflowGet_NotFound_DoesNotQueryLayout(t *testing.T) {
	layouts := newFakeMCPLayouts()
	srv := newLayoutToolServer(stampedRepo("my-wf", "rev-1"), layouts)

	result := callTool(t, srv, "workflow_get", map[string]any{"name": "other"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "workflow not found: other")
	assert.Empty(t, layouts.recorded())
}
```

- [x] **Step 2: Run it, confirm it fails**

Observed compile failure, then behavioral failures (missing arrangement object) with declarations only; `/tmp/p3t10-summary-red.log`.

```bash
go test ./adapter/mcp/ -run 'TestWorkflowGet_' -v
```

Expected:

```
# github.com/arbitron/arbitron/adapter/mcp_test [github.com/arbitron/arbitron/adapter/mcp.test]
./tools_layout_test.go:...: undefined: adaptmcp.LayoutQueryService
./tools_layout_test.go:...: unknown field Layouts in struct literal of type mcp.Deps
./tools_layout_test.go:...: undefined: adaptmcp.RegisterLayoutTools
FAIL	github.com/arbitron/arbitron/adapter/mcp [build failed]
```

- [x] **Step 3: Minimal implementation — deps.go**

In `adapter/mcp/deps.go`, add `"github.com/arbitron/arbitron/domain/layout"` to the import block after `"github.com/arbitron/arbitron/domain/instance"`. After the `EngineService` interface (ends at line 41), add:

```go
// LayoutQueryService is the consumer-owned port onto the authorised,
// revision-scoped layout query/retry seam (*layout.QueryService in production —
// the same instance the HTTP adapter uses, so the surfaces cannot drift).
type LayoutQueryService interface {
	WorkflowLayout(ctx context.Context, name string) (layout.View, error)
	InstanceLayout(ctx context.Context, id uuid.UUID) (layout.View, error)
	RetryWorkflow(ctx context.Context, name, expectedRevision string) (layout.RetryAck, error)
	RetryInstance(ctx context.Context, id uuid.UUID, expectedRevision string) (layout.RetryAck, error)
}
```

In `Deps`, after the `BackendHealth BackendHealthChecker` field, add:

```go
	// Layouts powers workflow_get's arrangement summary and the
	// workflow_layout_retry / instance_layout_retry tools. Optional: when nil the
	// summary reports "layout service not configured" and the retry tools return
	// that as a tool error (never a silent omission).
	Layouts LayoutQueryService
```

- [x] **Step 4: Minimal implementation — tools_layout.go (summary part)**

Create `adapter/mcp/tools_layout.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

const layoutServiceNotConfigured = "layout service not configured"

// RegisterLayoutTools registers the arrangement retry tools (filled in by the
// next step of this task).
func RegisterLayoutTools(srv *Server) {}

// layoutService returns the injected layout query seam, or nil.
func layoutService(srv *Server) LayoutQueryService {
	if srv.deps == nil {
		return nil
	}
	return srv.deps.Layouts
}

// workflowWithArrangement renders the manifest exactly as before plus an
// "arrangement" summary for its saved revision.
func workflowWithArrangement(ctx context.Context, srv *Server, name string, wf *workflow.Workflow) (*gomcp.CallToolResult, error) {
	v := workflowArrangement(ctx, srv, name, wf)
    if v.SavedRevision != wf.SavedRevision() {
        // One authorized reload closes an ordinary save/read race.
        newer, err := workflowSvc(srv).Load(ctx, name)
        if err != nil || newer == nil { return gomcp.NewToolResultError("workflow is no longer accessible; reload"), nil }
        wf = newer
        v = workflowArrangement(ctx, srv, name, wf)
        if v.SavedRevision != wf.SavedRevision() { v = unavailableArrangement(wf, "workflow changed while reading arrangement; reload or retry") }
    }
    data, err := json.Marshal(wf)
	if err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("marshal error: %s", err)), nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("marshal error: %s", err)), nil
	}
	out["arrangement"] = arrangementSummary(v)
	return MarshalResult(out)
}

func workflowArrangement(ctx context.Context, srv *Server, name string, wf *workflow.Workflow) layout.View {
	svc := layoutService(srv)
	if svc == nil {
		return unavailableArrangement(wf, layoutServiceNotConfigured)
	}
	v, err := svc.WorkflowLayout(ctx, name)
	if err != nil {
		return unavailableArrangement(wf, err.Error())
	}
	return v
}

// unavailableArrangement describes a view that could not be read, keyed by the
// manifest's own saved revision.
func unavailableArrangement(wf *workflow.Workflow, reason string) layout.View {
	v := layout.View{
		SavedRevision:     wf.SavedRevision(),
		DisplayStatus:     layout.DisplayUnavailable,
		Findings:          []layout.Finding{},
		Provenance:        layout.ProvenanceLegacy,
		Available:         false,
		AvailabilityError: reason,
	}
	if wf.Presentation != nil {
		v.Provenance = layout.ProvenanceRetainedPositions
        v.Unreviewed = true
        v.RetryEligible = true
	}
	return v
}

// arrangementSummary projects a view onto the MCP contract keys; geometry,
// contract, evidence and retained positions are deliberately omitted.
func arrangementSummary(v layout.View) map[string]any {
	findings := v.Findings
	if findings == nil {
		findings = []layout.Finding{}
	}
	return map[string]any{
		"savedRevision":     v.SavedRevision,
		"displayStatus":     v.DisplayStatus,
		"state":             v.State,
		"stage":             v.Stage,
		"attemptsUsed":      v.AttemptsUsed,
		"attemptLimit":      v.AttemptLimit,
		"findings":          findings,
		"failureStage":      v.FailureStage,
		"failureReason":     v.FailureReason,
		"retryEligible":     v.RetryEligible,
		"available":         v.Available,
		"availabilityError": v.AvailabilityError,
		"provenance":        v.Provenance,
	}
}
```

- [x] **Step 5: Minimal implementation — workflow_get**

In `adapter/mcp/tools_workflow.go` `workflowGetHandler`, replace the final line `return MarshalResult(wf)` (line 281) with:

```go
		return workflowWithArrangement(ctx, srv, name, wf)
```

Also update the tool description (line 57) so MCP clients know about the new field:

```go
			gomcp.WithDescription("Load a workflow definition by name. The result also carries an \"arrangement\" object with the saved revision's automatic-arrangement status (savedRevision, displayStatus, attempts, findings, failure reason, retryEligible); pass arrangement.savedRevision as expectedRevision to workflow_layout_retry."),
```

- [x] **Step 6: Run it, confirm it passes**

Observed all focused tests PASS; `/tmp/p3t10-summary-green.log`. The revision-race guard is implemented in the explicit alignment RED→GREEN cycle below.

```bash
go test ./adapter/mcp/ -run 'TestWorkflowGet' -v
```

Expected: the four new `TestWorkflowGet_*` tests and the existing `TestWorkflowGet` subtests `--- PASS`. The existing tests assert `Contains "my-wf"` and the nil/not-found guards, so they are unaffected. Output ends `ok  	github.com/arbitron/arbitron/adapter/mcp`.

- [x] **Step 7: Commit**

Committed with the complete retry/root/alignment slice in this pass, rather than committing the temporary empty RegisterLayoutTools declaration.

```bash
git add adapter/mcp/deps.go adapter/mcp/tools_layout.go adapter/mcp/tools_workflow.go adapter/mcp/tools_layout_test.go && git commit -m "feat(mcp): report saved-revision arrangement status in workflow_get" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10.2: `workflow_layout_retry` + `instance_layout_retry` tools and registry

**Files:**
- Modify: `adapter/mcp/tools_layout.go` (replace the empty `RegisterLayoutTools`; add handlers and error mapping)
- Modify: `adapter/mcp/register.go:5-17` (call `RegisterLayoutTools`)
- Modify: `adapter/mcp/register_test.go:25-110` (`expectedTools`: add the "Layout (2)" group)
- Test:   `adapter/mcp/tools_layout_test.go` (append)

- [x] **Step 1: Write the failing tests**

Add `"errors"` (after `"encoding/json"`) and `"github.com/arbitron/arbitron/domain/identity"` (after the `adaptmcp` import) to the import block of `adapter/mcp/tools_layout_test.go`, then append:

```go
// --- workflow_layout_retry ---

func TestWorkflowLayoutRetry_Accepted(t *testing.T) {
	layouts := newFakeMCPLayouts()
	layouts.workflowRevs["my-wf"] = "rev-1"
	srv := newLayoutToolServer(&stubWorkflowRepo{}, layouts)

	result := callTool(t, srv, "workflow_layout_retry", map[string]any{"name": "my-wf", "expectedRevision": "rev-1"})
	require.False(t, result.IsError, extractText(t, result))
	var ack map[string]any
	require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &ack))
	assert.Equal(t, "rev-1", ack["savedRevision"])
	assert.Equal(t, float64(2), ack["jobGeneration"])
	assert.Equal(t, "pending", ack["state"])
	assert.Equal(t, "pending", ack["displayStatus"])
	assert.Equal(t, []string{"RetryWorkflow:my-wf@rev-1"}, layouts.recorded())
}

func TestWorkflowLayoutRetry_ValidationErrors_NeverReachService(t *testing.T) {
	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"missing name":     {map[string]any{"expectedRevision": "rev-1"}, "name is required"},
		"unsafe name":      {map[string]any{"name": "../x", "expectedRevision": "rev-1"}, "invalid workflow name"},
		"missing expected": {map[string]any{"name": "my-wf"}, "expectedRevision is required"},
		"empty expected":   {map[string]any{"name": "my-wf", "expectedRevision": ""}, "expectedRevision is required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			layouts := newFakeMCPLayouts()
			layouts.workflowRevs["my-wf"] = "rev-1"
			srv := newLayoutToolServer(&stubWorkflowRepo{}, layouts)

			result := callTool(t, srv, "workflow_layout_retry", tc.args)
			assert.True(t, result.IsError)
			assert.Contains(t, extractText(t, result), tc.want)
			assert.Empty(t, layouts.recorded())
		})
	}
}

func TestWorkflowLayoutRetry_ErrorMessages(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected string
		want     string
	}{
		{"stale revision", nil, "stale", "layout revision conflict: expected stale is not current"},
		{"retry conflict", layout.ErrRetryConflict, "rev-1", "layout is not eligible for retry"},
		{"legacy", layout.ErrLegacyRevision, "rev-1", "no arrangement request"},
		{"not found", workflow.ErrNotFound, "rev-1", "workflow not found: my-wf"},
		{"shared read-only", workflow.ErrSharedReadOnly, "rev-1", "workflow is shared and read-only: my-wf"},
		{"forbidden", identity.ErrForbidden, "rev-1", "not authorized"},
		{"unauthorized", workflow.ErrUnauthorized, "rev-1", "not authorized"},
		{"store unavailable", layout.ErrStoreUnavailable, "rev-1", "layout status store unavailable; retry later"},
		{"unexpected", errors.New("boom"), "rev-1", "failed to retry layout: boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layouts := newFakeMCPLayouts()
			layouts.workflowRevs["my-wf"] = "rev-1"
			if tc.err != nil {
				layouts.errs["workflow:my-wf"] = tc.err
			}
			srv := newLayoutToolServer(&stubWorkflowRepo{}, layouts)

			result := callTool(t, srv, "workflow_layout_retry", map[string]any{"name": "my-wf", "expectedRevision": tc.expected})
			assert.True(t, result.IsError)
			assert.Contains(t, extractText(t, result), tc.want)
		})
	}
}

// --- instance_layout_retry ---

func TestInstanceLayoutRetry_Accepted(t *testing.T) {
	layouts := newFakeMCPLayouts()
	id := uuid.New()
	layouts.instanceRevs[id] = "rev-snap"
	srv := newLayoutToolServer(&stubWorkflowRepo{}, layouts)

	result := callTool(t, srv, "instance_layout_retry", map[string]any{"id": id.String(), "expectedRevision": "rev-snap"})
	require.False(t, result.IsError, extractText(t, result))
	var ack map[string]any
	require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &ack))
	assert.Equal(t, "rev-snap", ack["savedRevision"])
	assert.Equal(t, float64(4), ack["jobGeneration"])
	assert.Equal(t, []string{"RetryInstance:" + id.String() + "@rev-snap"}, layouts.recorded())
}

func TestInstanceLayoutRetry_Errors(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name  string
		args  map[string]any
		err   error
		want  string
		calls int
	}{
		{"invalid id", map[string]any{"id": "nope", "expectedRevision": "rev-snap"}, nil, "invalid id", 0},
		{"missing expected", map[string]any{"id": id.String()}, nil, "expectedRevision is required", 0},
		{"stale", map[string]any{"id": id.String(), "expectedRevision": "rev-current"}, nil, "layout revision conflict: expected rev-current is not current", 1},
		{"forbidden peer", map[string]any{"id": id.String(), "expectedRevision": "rev-snap"}, instance.ErrForbidden, "not authorized", 1},
		{"not found", map[string]any{"id": id.String(), "expectedRevision": "rev-snap"}, instance.ErrNotFound, "instance not found: " + id.String(), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layouts := newFakeMCPLayouts()
			layouts.instanceRevs[id] = "rev-snap"
			if tc.err != nil {
				layouts.errs["instance:"+id.String()] = fmt.Errorf("instance %s: %w", id, tc.err)
			}
			srv := newLayoutToolServer(&stubWorkflowRepo{}, layouts)

			result := callTool(t, srv, "instance_layout_retry", tc.args)
			assert.True(t, result.IsError)
			assert.Contains(t, extractText(t, result), tc.want)
			assert.Len(t, layouts.recorded(), tc.calls)
		})
	}
}

func TestLayoutRetryTools_UnwiredService(t *testing.T) {
	srv := newLayoutToolServer(&stubWorkflowRepo{}, nil)

	r1 := callTool(t, srv, "workflow_layout_retry", map[string]any{"name": "my-wf", "expectedRevision": "rev-1"})
	assert.True(t, r1.IsError)
	assert.Contains(t, extractText(t, r1), "layout service not configured")

	r2 := callTool(t, srv, "instance_layout_retry", map[string]any{"id": uuid.New().String(), "expectedRevision": "rev-1"})
	assert.True(t, r2.IsError)
	assert.Contains(t, extractText(t, r2), "layout service not configured")
}

// RegisterAllTools must advertise both retry tools.
func TestRegisterAllTools_IncludesLayoutRetryTools(t *testing.T) {
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{})
	adaptmcp.RegisterAllTools(srv)
	names := map[string]bool{}
	for _, tool := range srv.ListTools() {
		names[tool.Name] = true
	}
	assert.True(t, names["workflow_layout_retry"])
	assert.True(t, names["instance_layout_retry"])
}
```

In `adapter/mcp/register_test.go`, inside `expectedTools`, after the `// User/role management (2)` group (`"user_set_role",`), insert:

```go
	// Layout arrangement retry (2)
	"workflow_layout_retry",
	"instance_layout_retry",
```

- [x] **Step 2: Run them, confirm they fail**

Observed missing-tool/content and registry assertions fail; `/tmp/p3t10-retry-red.log`.

```bash
go test ./adapter/mcp/ -run 'TestWorkflowLayoutRetry|TestInstanceLayoutRetry|TestLayoutRetryTools|TestRegisterAllTools' -v
```

Expected: the build succeeds (the empty `RegisterLayoutTools` exists), but the tools are not registered. The retry tests fail with a JSON-RPC tool-not-found error: `extractText` fails `require.NotEmpty(t, result.Content)`. `TestRegisterAllTools_Count` fails with `"RegisterAllTools must register exactly 72 tools"` and `Should have 72 item(s), but has 70`, because the base checkout registers 70 tools and `expectedTools` now lists 72. `TestRegisterAllTools_APISurface` reports `expected tool "workflow_layout_retry" not found in RegisterAllTools` and the same for `instance_layout_retry`. `TestRegisterAllTools_IncludesLayoutRetryTools` fails. Output ends `FAIL	github.com/arbitron/arbitron/adapter/mcp`.

- [x] **Step 3: Implementation**

In `adapter/mcp/tools_layout.go`, replace the import block and the empty `RegisterLayoutTools` with:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)
```

```go
// RegisterLayoutTools registers the arrangement retry tools. Both route through
// Deps.Layouts — the same authorised seam as the HTTP retry endpoints.
func RegisterLayoutTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("workflow_layout_retry",
			gomcp.WithDescription("Retry automatic arrangement and readability review for a workflow's CURRENT saved revision. "+
				"Allowed when the arrangement failed or needs attention (or was never recorded); returns immediately with the new job generation. "+
				"Requires the same permission as editing the workflow."),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("expectedRevision", gomcp.Required(), gomcp.Description(
				"The saved revision being retried (workflow_get arrangement.savedRevision). A stale value is rejected so a concurrent save is never retried by mistake.")),
		),
		workflowLayoutRetryHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("instance_layout_retry",
			gomcp.WithDescription("Retry automatic arrangement for the HISTORICAL saved revision a workflow instance ran from. "+
				"Never touches the current workflow or the instance's execution. Requires owner, department-lead or admin access to the instance."),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
			gomcp.WithString("expectedRevision", gomcp.Required(), gomcp.Description(
				"The saved revision captured in the instance's workflow snapshot (workflowSnapshot.presentation.revision).")),
		),
		instanceLayoutRetryHandler(srv),
	)
}

func workflowLayoutRetryHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		expected, _ := args["expectedRevision"].(string)
		if expected == "" {
			return gomcp.NewToolResultError("expectedRevision is required"), nil
		}
		svc := layoutService(srv)
		if svc == nil {
			return gomcp.NewToolResultError(layoutServiceNotConfigured), nil
		}
		ack, err := svc.RetryWorkflow(ctx, name, expected)
		if err != nil {
			return layoutToolError("workflow", name, expected, err), nil
		}
		return MarshalResult(ack)
	}
}

func instanceLayoutRetryHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		expected, _ := request.GetArguments()["expectedRevision"].(string)
		if expected == "" {
			return gomcp.NewToolResultError("expectedRevision is required"), nil
		}
		svc := layoutService(srv)
		if svc == nil {
			return gomcp.NewToolResultError(layoutServiceNotConfigured), nil
		}
		ack, err := svc.RetryInstance(ctx, id, expected)
		if err != nil {
			return layoutToolError("instance", id.String(), expected, err), nil
		}
		return MarshalResult(ack)
	}
}

// layoutToolError maps the layout/authorization sentinels to tool errors.
// Not-found wording is identical for absent and inaccessible subjects.
func layoutToolError(kind, subject, expected string, err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, layout.ErrRevisionConflict):
		return gomcp.NewToolResultError(fmt.Sprintf("layout revision conflict: expected %s is not current", expected))
	case errors.Is(err, layout.ErrRetryConflict):
		return gomcp.NewToolResultError("layout is not eligible for retry")
	case errors.Is(err, layout.ErrLegacyRevision):
		return gomcp.NewToolResultError("layout revision has no arrangement request (saved before automatic arrangement); save the workflow to arrange it")
	case errors.Is(err, workflow.ErrNotFound), errors.Is(err, instance.ErrNotFound):
		return gomcp.NewToolResultError(fmt.Sprintf("%s not found: %s", kind, subject))
	case errors.Is(err, workflow.ErrSharedReadOnly):
		return gomcp.NewToolResultError(fmt.Sprintf("workflow is shared and read-only: %s", subject))
	case errors.Is(err, identity.ErrForbidden), errors.Is(err, workflow.ErrUnauthorized):
		return gomcp.NewToolResultError(fmt.Sprintf("not authorized: %s", err))
	case errors.Is(err, workflow.ErrMutationPending):
        return gomcp.NewToolResultError("workflow destination is awaiting durable mutation recovery; retry shortly")
	case errors.Is(err, layout.ErrStoreUnavailable):
		return gomcp.NewToolResultError("layout status store unavailable; retry later")
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("failed to retry layout: %s", err))
	}
}
```

Keep `layoutServiceNotConfigured`, `layoutService`, `workflowWithArrangement`, `workflowArrangement`, `unavailableArrangement` and `arrangementSummary` from Task 10.1 unchanged.

In `adapter/mcp/register.go`, replace the body with:

```go
// RegisterAllTools registers all MCP tools (workflow, instance, trace, system,
// streaming, interactive, and layout retry) with the given server.
func RegisterAllTools(srv *Server) {
	RegisterWorkflowTools(srv)
	RegisterInstanceTools(srv)
	RegisterTraceTools(srv)
	RegisterSystemTools(srv)
	RegisterStreamingTools(srv)
	RegisterInteractiveTools(srv)
	RegisterConnectionTools(srv)
	RegisterProfileTools(srv)
	RegisterTokenTools(srv)
	RegisterUserTools(srv)
	RegisterLayoutTools(srv)
	RegisterScheduleTools(srv)
}
```

- [x] **Step 4: Run them, confirm they pass**

Focused registry/get/retry tests and the full MCP package PASS; `/tmp/p3t10-retry-green.log`.

```bash
go test ./adapter/mcp/ -v -run 'TestWorkflowLayoutRetry|TestInstanceLayoutRetry|TestLayoutRetryTools|TestRegisterAllTools|TestRegisterScheduleTools|TestWorkflowGet'
```

Expected: all listed tests and subtests `--- PASS`, including `TestRegisterAllTools_Count` (now `len(expectedTools)` including the two new names) and `TestRegisterScheduleTools_NotRegisteredWhenUnwired`. Output ends `ok  	github.com/arbitron/arbitron/adapter/mcp`.

```bash
go test ./adapter/mcp/
```

Expected: `ok  	github.com/arbitron/arbitron/adapter/mcp`

- [x] **Step 5: Commit**

```bash
git add adapter/mcp/tools_layout.go adapter/mcp/register.go adapter/mcp/register_test.go adapter/mcp/tools_layout_test.go && git commit -m "feat(mcp): add workflow_layout_retry and instance_layout_retry tools" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10.3: Composition root — `mcpDeps().Layouts` + e2e through the real `mcpDeps`

**Files:**
- Modify: `cmd/arbitron/main.go:286-315` (`mcpDeps`: build into a local, set `Layouts` from `ds.layoutQuery()` when non-nil)
- Create/Test: `cmd/arbitron/layout_mcp_e2e_test.go`

- [x] **Step 1: Write the failing test**

Create `cmd/arbitron/layout_mcp_e2e_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
)

// callMCPToolE2E dispatches a tools/call JSON-RPC message through the real
// mcp-go server with the given identity context (mirrors the adapter tests'
// callToolWithIdentity; the HTTP /mcp path injects the identity the same way).
func callMCPToolE2E(t *testing.T, srv *adaptmcp.Server, ctx context.Context, name string, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	req := gomcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": req.Params})
	require.NoError(t, err)
	result := srv.MCPServer().HandleMessage(ctx, json.RawMessage(msg))
	data, err := json.Marshal(result)
	require.NoError(t, err)
	var resp struct {
		Result gomcp.CallToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(data, &resp))
	return &resp.Result
}

func mcpE2EText(t *testing.T, res *gomcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(gomcp.TextContent)
	require.True(t, ok, "expected TextContent, got %T", res.Content[0])
	return text.Text
}

func newLayoutE2EMCPServer(t *testing.T, ds *domainStack) *adaptmcp.Server {
	t.Helper()
	deps := ds.mcpDeps()
	require.NotNil(t, deps.Layouts, "mcpDeps must thread the production layout QueryService")
	assert.Same(t, ds.layout.query, deps.Layouts)
	srv := adaptmcp.NewServerWithDeps("t", "0", deps)
	adaptmcp.RegisterAllTools(srv)
	return srv
}

// SINK = the MCP tool result. Fails if mcpDeps drops Layouts (the summary would
// say "layout service not configured") or if the summary is not keyed by the
// revision the save path wrote to disk.
func TestLayoutMCPE2E_WorkflowGetArrangementMatchesManifestRevision(t *testing.T) {
	ds := bootQueryStack(t)
	srv := newLayoutE2EMCPServer(t, ds)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "mcp-wf", queryE2EWorkflow("mcp-wf", "Start")))
	rev := manifestRevisionOnDisk(t, ds, "mcp-wf")

	res := callMCPToolE2E(t, srv, ctx, "workflow_get", map[string]any{"name": "mcp-wf"})
	require.False(t, res.IsError, mcpE2EText(t, res))
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, res)), &out))

	assert.Equal(t, "mcp-wf", out["name"])
	assert.Equal(t, rev, out["presentation"].(map[string]any)["revision"])
	arr, ok := out["arrangement"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, rev, arr["savedRevision"])
	assert.NotEqual(t, "layout service not configured", arr["availabilityError"], "mcpDeps must wire Deps.Layouts")
	assert.NotEqual(t, "unavailable", arr["displayStatus"], "the real SQLite status store answers: %v", arr["availabilityError"])
	assert.NotEqual(t, "legacy", arr["displayStatus"])
	_, hasGeometry := arr["geometry"]
	assert.False(t, hasGeometry)
}

func TestLayoutMCPE2E_RetryToolsThroughCompositionRoot(t *testing.T) {
	ds := bootQueryStack(t)
	srv := newLayoutE2EMCPServer(t, ds)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "mcp-retry", queryE2EWorkflow("mcp-retry", "Start")))

	res := callMCPToolE2E(t, srv, ctx, "workflow_layout_retry", map[string]any{"name": "mcp-retry", "expectedRevision": "stale-revision"})
	assert.True(t, res.IsError)
	assert.Contains(t, mcpE2EText(t, res), "layout revision conflict: expected stale-revision is not current")

	id, _, err := ds.eng.Trigger(ctx, "mcp-retry", map[string]any{}, "normal")
	require.NoError(t, err)
	require.NotEmpty(t, snapshotRevision(t, ds, id))

	res = callMCPToolE2E(t, srv, ctx, "instance_layout_retry", map[string]any{"id": id.String(), "expectedRevision": "stale-revision"})
	assert.True(t, res.IsError)
	assert.Contains(t, mcpE2EText(t, res), "layout revision conflict")
}

// mcpDeps never stores a typed-nil Layouts for a stack without a layout
// subsystem (the existing TestDomainStack_MCPDeps_WiresSecurityFields stack).
func TestDomainStack_MCPDeps_NilLayoutStackLeavesLayoutsNil(t *testing.T) {
	ds := &domainStack{}
	assert.Nil(t, ds.mcpDeps().Layouts)
}
```

- [x] **Step 2: Run it, confirm it fails**

Observed missing production Layouts failure; `/tmp/p3t10-root-red.log`. Final tests also fail at actual summary/ack sinks when the assignment is removed (not merely at constructor assertions).

```bash
go test ./cmd/arbitron/ -run 'TestLayoutMCPE2E|TestDomainStack_MCPDeps_NilLayoutStack' -v
```

Expected: the build succeeds. `TestLayoutMCPE2E_*` fails in `newLayoutE2EMCPServer` with `mcpDeps must thread the production layout QueryService` (`Expected value not to be nil`). `TestDomainStack_MCPDeps_NilLayoutStackLeavesLayoutsNil` passes. Output ends `FAIL	github.com/arbitron/arbitron/cmd/arbitron`.

- [x] **Step 3: Implementation**

In `cmd/arbitron/main.go`, replace the `mcpDeps` method (lines 286-315) with:

```go
// mcpDeps builds the MCP adapter Deps from the shared domain stack. Layouts is
// assigned only when the layout subsystem exists, so the interface is never a
// typed nil (unit tests build partial stacks).
func (ds *domainStack) mcpDeps() *adaptmcp.Deps {
	deps := &adaptmcp.Deps{
		Engine:              ds.eng,
		Workflows:           ds.workflowRepo,
		WorkflowSvc:         ds.workflowSvc,
		AuditSvc:            ds.auditSvc,
		Instances:           ds.instanceRepo,
		NodeExecs:           ds.nodeExecRepo,
		Snapshots:           ds.snapshotRepo,
		FileReader:          ds.fileReader,
		EventBus:            ds.bus,
		Config:              ds.cfg,
		Settings:            ds.settings,
		QueueManager:        ds.queueMgr,
		QueueRepo:           ds.queueRepo,
		Notifications:       ds.notifRepo,
		ConversationHandler: ds.conversationHandler,
		ProfileChecker:      ds.profileChecker,
		ConnChecker:         ds.connChecker,
		Connections:         ds.connStore,
		ProfileAdmin:        ds.profileSvc,
		ConnectionAdmin:     ds.connSvc,
		Capabilities:        ds.capabilityRules,
		Tokens:              ds.tokenSvc,
		Users:               ds.userSvc,
		ScheduleService:     ds.scheduleSvc,
		Logger:              ds.logger,
		BackendHealth:       ds.backendHealth,
	}
	if q := ds.layoutQuery(); q != nil {
		deps.Layouts = q
	}
	return deps
}
```

If P2T9 already added other fields to this literal, keep them in the new literal. Only the local variable and the trailing `Layouts` assignment are new.

- [x] **Step 4: Run it, confirm it passes**

`go build ./...`, `go vet ./adapter/mcp/ ./cmd/arbitron/` and both full packages PASS (MCP 1.633s; cmd/arbitron 374.583s), `/tmp/p3t10-full.log`. Focused MCP/root tests also PASS under `-race` (1.805s / 12.614s), `/tmp/p3t10-race-detector.log`. Full private-rename/stdio acceptance remains explicitly blocked below, not inferred from these local successes.

```bash
go build ./... && go test ./cmd/arbitron/ -run 'TestLayoutMCPE2E|TestDomainStack_MCPDeps' -v
```

Expected: `--- PASS` for `TestLayoutMCPE2E_WorkflowGetArrangementMatchesManifestRevision`, `TestLayoutMCPE2E_RetryToolsThroughCompositionRoot`, `TestDomainStack_MCPDeps_NilLayoutStackLeavesLayoutsNil`, and the existing `TestDomainStack_MCPDeps_WiresSecurityFields`; `ok  	github.com/arbitron/arbitron/cmd/arbitron`.

```bash
go vet ./adapter/mcp/ ./cmd/arbitron/ && go test ./adapter/mcp/ ./cmd/arbitron/
```

Expected: no vet findings; `ok` for both packages.

- [x] **Step 5: Commit**

```bash
git add cmd/arbitron/main.go cmd/arbitron/layout_mcp_e2e_test.go && git commit -m "feat(mcp): wire the layout QueryService into mcpDeps for serve and stdio MCP" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

| Component | Composition-root wiring | Data source |
|---|---|---|
| `workflow_get` arrangement summary | `Deps.Layouts` set in `cmd/arbitron/main.go:(*domainStack).mcpDeps` (this task, 10.3). `mcpDeps` is used by both `runServe` (HTTP `/mcp`) and `runMCP` (stdio). | `ds.layoutQuery()` → `ds.layout.query` from `(*layoutStack).attachQuery(workflowSvc, eng)` in `initDomain` (**P3T8**). `layout.Reader` reads `adaptsqlite.LayoutRepo` rows (**P2T6**), written by `layout.Requester` (**P2T5**) at every save the stamper commits (**P2T2/P2T3/P2T4**) and by `layout.Worker` (**P2T8**). |
| `workflow_layout_retry` / `instance_layout_retry` | `RegisterLayoutTools` called from `adapter/mcp/register.go:RegisterAllTools` (10.2). `RegisterAllTools` is called by `runServe` and `runMCP`. | Same `QueryService.RetryWorkflow/RetryInstance` → `Requester.Retry` / `RequestArrangement` (**P2T5**) → worker (**P2T8**). Authorization comes from `workflow.Service.AuthorizeLayoutRetry` and `Engine.AuthorizeInstanceLayoutRetry` (**P3T8**). |

Provenance probe: `TestLayoutMCPE2E_WorkflowGetArrangementMatchesManifestRevision` boots `initDomain`, builds the server from the real `ds.mcpDeps()`, calls `workflow_get` through `MCPServer().HandleMessage`, and asserts the tool result's `arrangement.savedRevision` equals the revision in `workflow.json` on disk. If `Layouts` is dropped, the summary reads "layout service not configured" and the test fails.

### Alignment TDD: workflow_get cannot mix manifest and arrangement revisions

**Files:** `adapter/mcp/tools_workflow.go`, `tools_layout_test.go`, `cmd/arbitron/layout_mcp_e2e_test.go`.

- [x] **RED:** Change the saved workflow between manifest Load and WorkflowLayout. Assert
  workflow_get never combines manifest rev A with arrangement rev B. A second race must
  return the manifest with explicit status-unavailable for its token, not a foreign
  complete status. Real mcpDeps test asserts the final tool response, not a mock call.
- [x] Run `go test ./adapter/mcp ./cmd/arbitron -run 'TestWorkflowGet|TestLayoutMCPE2E' -count=1` and observe the behavioral failure before implementing the correction.

Observed mixed A-manifest/B-status and second-race failures in both adapter and real-root response sinks; `/tmp/p3t10-race-red.log`.
- [x] **GREEN:** If revisions differ, reload the authorised manifest and status once. If still
  mismatched, preserve the returned manifest and emit a revision-scoped unavailable
  summary with a reload/retry explanation, omitting foreign status/findings. Never use
  current destination metadata as authorization. All expectedRevision validation remains
  at the shared domain seam.
- [x] Rerun the command, then the owning package/typecheck gate. Temporarily remove the
  corrected input/wiring/guard and verify the regression assertion fails; restore it.
- [x] Commit the source/tests with this task. These checks are additional to the existing
  happy/success, failure, error and edge cases; fixture-only success is insufficient.

### MCP retry generation sink

- [x] Real-root generation and historical retry sinks: `TestLayoutMCPE2E_RetryGenerationSinks` uses actual saves, snapshots, SQLite, production missing-browser failure and worker/query completion. Both tool acknowledgements match persisted increments; historical retry after deletion leaves current/unrelated generations and immutable inputs unchanged. Nil Layouts and empty tokens reject without generation changes. Removing mcpDeps assignment fails the actual summary and retry success sinks (`/tmp/p3t10-wiring-mutation.log`).

Extend the real mcpDeps e2e beyond stale-token errors: create a failed/attention revision,
call each retry tool and assert acknowledgement generation matches the persisted Store
row; poll the real query to terminal. Retry an authorized deleted-workflow historical
snapshot, and verify current/unrelated historical generations remain untouched. A nil
Layouts, empty expectedRevision or dropped mcpDeps assignment must fail success controls.

### Live peer-crash settlement in MCP (part of Task 10.3)

- [x] Shared before-proof/after-proof peer crashes through the real mcpDeps server: committed/restored tool response, manifest, exact publication proof, recovered input and subsequent authoring-to-SQLite sinks pass. Removing production SetReadSettler exposes the candidate revision and fails response assertions (`/tmp/p3t10-settler-mutation.log`); restored afterward. Real durable private/shared pending intents return the safe static error on get/authoring/retry. Wrapped pending query/reload errors cannot leak paths. These are authorized HandleMessage probes, not stdio-command identity acceptance.
- [x] Complete the required surviving **stdio** peer and **private Rename** crash/read/save acceptance.
> Completed in the current revisit: actual race-built Cobra stdio survives shared pre/post-publication crashes (raw/get/save/retry each first) and private/shared pre/post-identity Rename crashes. Raw reads are first for Rename; undecided tools return only the fixed safe error and preserve manifest/Store. Registry-driven recovery precedes successful committed reads and subsequent actual stdio saves/retries. Unfiltered local authoring/EOF and org/edge denials pass. Removing production read settlement and settled FileReader wiring fails the new tool-result sinks; restoration passes (/tmp/p3t10-stdio-red.log, /tmp/p3t10-stdio-green.log).
> Current plan correction (§6.18): active rename context, settled raw FileReader and local stdio identity now exist. Complete the actual MCP raw-read/save/retry crash matrix and local subprocess authoring with org/edge denials; P2T9.8 must fix console stdout and remove filtering from the protocol test. Direct authorized dispatch or a stream parser that drops log lines cannot prove stdio integration.

workflow_get and authoring tool load/save errors matching workflow.ErrMutationPending
return IsError=true with the same safe static recovery reason as HTTP. Query-derived
unavailability may remain a same-manifest arrangement summary only after that manifest
was safely loaded/settled; never return an uncommitted tree with a warning attached.
Test a surviving stdio MCP peer reading/saving after the other process crashes in the
shared/rename commit window. Inspect tool response plus real registry/manifest/Store sink;
real recovery must precede any successful committed revision read. No raw path/error leak.

### Pass review and verification (2026-10-17)

1. **Spec compliance:** exact summary keys, no geometry/evidence leakage, required retry validation/error mappings, shared production QueryService and one bounded authorised reload. The original map[string]any JSON round-trip rounded manifest integers; `TestWorkflowGet_PreservesManifestNumbers` failed (9007199254740993 → 9007199254740992), then passed with an embedded manifest response. No new flag/option or upstream protocol change.
2. **Anti-green-theater:** removing mcpDeps.Layouts fails actual summary and retry-ack sinks; removing the race guard fails adapter and real-root manifest/status sinks; removing SetReadSettler fails the pre-proof peer-crash response sink. Nil/empty retry controls leave generations unchanged. All mutations were restored. Logs: `/tmp/p3t10-wiring-mutation.log`, `/tmp/p3t10-race-mutation.log`, `/tmp/p3t10-settler-mutation.log`. Pending-intent tests first failed the safe error assertions, then passed with shared static mapping (`/tmp/p3t10-recovery-red.log`, `/tmp/p3t10-pending-red.log`).
3. **Code quality:** follows neighboring MCP registration, error mapping and composition patterns; manifest response avoids lossy re-decoding; gofmt and diff whitespace checks clean. No empty tool declaration, no missing-input no-op, no test renderer in production. Build, vet, both owning packages and focused race-detector tests were rerun green after the fixes. Unrelated pre-existing `docs/visuals/` work is preserved and excluded from this commit.

## Identity and cancellation are load-bearing test arguments

- [x] RED→GREEN/mutation: `TestLayoutMCP_ForwardsIdentityAndCancellation` sends a request
  with the actual identity context and a private sentinel value through the handler/tool.
  `fakeMCPLayouts.recordedContexts` must retain that exact identity/value, not a
  Background context. Each fake method rejects canceled context before returning
  successful geometry/RetryAck; canceled requests cannot advance a real Store row.
  Exercise both workflow and instance reads/retries supported by this adapter. Keep
  real-root authorization/concealment and immutable-result sink tests as well.
- [x] Remove context forwarding or the fake cancellation check independently and
  observe behavioral failure, restore, then run the affected adapter tests and the
  existing production router/tool suite. Call-count assertions alone cannot prove
  the user identity reached the service authorization boundary.

## MCP completion acceptance revisit (2026-10-19)

- Selection: worked only P3T10. P3T2's independently controlled second-browser/font inventory and P3T7's repeatedly failing unrelated full-race gates remain recorded blockers; neither was spun on or claimed complete.
- Strict RED→GREEN: the new context test first failed behaviorally because fakeMCPLayouts ignored cancellation. Its four methods now record the supplied context and reject ctx.Err before returning data/acks. Every supported layout tool runs through actual HandleMessage with the caller's complete identity and a private sentinel value. There is no instance-layout read tool, so that fourth shared-port method is checked directly, without adding an unrequested tool. Restored context tests pass. Removing cancellation checks independently fails all canceled cases (/tmp/p3t10-fake-cancellation-red.log); replacing forwarded contexts with Background fails identity/value and actual-root assertions (/tmp/p3t10-context-forwarding-red.log). Retaining identity but dropping cancellation with WithoutCancel causes a real retry generation change and fails the SQLite sink (/tmp/p3t10-real-cancellation-red.log). Every mutation is restored.
- Real transport: cmd/arbitron/layout_mcp_stdio_crash_test.go builds the actual Cobra binary with -race and uses the existing unfiltered JSON-RPC stdout parser. It boots before the crashing writer, exercises shared before/after-publication with raw/get/save/retry independently first, and private/shared Rename before/after-registry identity. Pending tools return exactly the safe static error, preserve bytes and never enqueue the undecided revision. Actual registry/proof-driven settlement recovers exact immutable input; subsequent stdio reads, Unicode saves and retries assert manifest, registry, publication proof, destination/ID, stored input and acknowledged generation. Shared successful reads additionally require durable tree evidence to have been settled, not merely correct visible bytes. Existing subprocess local principal/EOF/org/edge/boot-error tests remain green. Removing settled FileReader alone leaks the prepared graph and fails raw response/pending-error assertions (/tmp/p3t10-raw-wiring-red.log); removing repository read settlement alone fails the manifest response (/tmp/p3t10-repo-wiring-red.log).
- Ordered self-review: **(1) Spec:** completed only the unchecked MCP acceptance/context slice; no new tool, flag, transport identity, recovery bypass, renderer, or production policy was needed. Existing production wiring is exercised with real saved/committed data. **(2) Anti-green-theater:** tests assert final tool content and complete authoritative row/input/proof/generation sinks; missing setters, context forwarding and cancellation each fail independently. The real owner succeeds after cancellation/outsider denials, so an always-denying seam cannot pass. Post-proof recovery preserves the original captured candidate even after a later save. **(3) Quality:** followed neighboring crash/stdio helpers, used bounded subprocess cleanup, copied recorded contexts under the existing mutex, avoided mutating the prior-manifest expectation while decoding a candidate, and ran gofmt/diff checks. No production mutations or generated asset diff remain.
- Final verification after review/restoration: go build ./... and go vet ./adapter/mcp ./cmd/arbitron PASS. Full adapter/mcp PASS (2.327s). Full cmd/arbitron ran to completion (285.028s) but failed only the unrelated TestGroupFilesE2E_DeadParentBeforeGatherRetriesFrozenFailure/periodic/success at group_dead_parent_recovery_e2e_test.go:101 (expected failure, actual success); no assertion, timeout or group lifecycle source was changed. Log: /tmp/p3t10-owning-packages-final.log. Per mission, that unrelated failure is reported rather than blocking this task's own green tests. Focused complete MCP tools/root/stdio plus existing HTTP router regression suite PASS under -race (adapter/mcp 1.637s; cmd/arbitron 80.563s), with no DATA RACE (/tmp/p3t10-focused-race-final.log). Earlier full new-stdio/local/org/edge gates also PASS (35.742s); strengthened post-review crash matrix PASS (26.366s). No complete-module/browser/release pass is newly claimed.
