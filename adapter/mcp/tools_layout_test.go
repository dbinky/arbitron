package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/identity"
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

func (f *fakeMCPLayouts) recordedContexts() []context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]context.Context(nil), f.contexts...)
}

func (f *fakeMCPLayouts) record(ctx context.Context, c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	f.contexts = append(f.contexts, ctx)
}

func (f *fakeMCPLayouts) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeMCPLayouts) WorkflowLayout(ctx context.Context, name string) (layout.View, error) {
	f.record(ctx, "WorkflowLayout:"+name)
	if err := ctx.Err(); err != nil {
		return layout.View{}, err
	}
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
	f.record(ctx, "InstanceLayout:"+id.String())
	if err := ctx.Err(); err != nil {
		return layout.View{}, err
	}
	return layout.View{}, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
}

func (f *fakeMCPLayouts) RetryWorkflow(ctx context.Context, name, expected string) (layout.RetryAck, error) {
	f.record(ctx, "RetryWorkflow:"+name+"@"+expected)
	if err := ctx.Err(); err != nil {
		return layout.RetryAck{}, err
	}
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
	f.record(ctx, "RetryInstance:"+id.String()+"@"+expected)
	if err := ctx.Err(); err != nil {
		return layout.RetryAck{}, err
	}
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
		loadFn: func(_ context.Context, n string) (*workflow.Workflow, error) {
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
		FailureStage: layout.StageReviewing, FailureReason: "review failed",
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
	assert.Equal(t, "done", arr["stage"])
	assert.Equal(t, "reviewing", arr["failureStage"])
	assert.Equal(t, "review failed", arr["failureReason"])
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

func TestWorkflowGet_ArrangementFallbackConcealsQueryCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"store", layout.ErrStoreUnavailable, "layout status store unavailable; retry later"},
		{"pending", workflow.ErrMutationPending, "workflow destination is awaiting durable mutation recovery; retry shortly"},
		{"hidden", workflow.ErrNotFound, "layout status unavailable; reload or retry"},
		{"forbidden", identity.ErrForbidden, "layout status unavailable; reload or retry"},
		{"canceled", context.Canceled, "layout status unavailable; reload or retry"},
		{"deadline", context.DeadlineExceeded, "layout status unavailable; reload or retry"},
		{"unexpected", errors.New("disk failure"), "layout status unavailable; reload or retry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layouts := newFakeMCPLayouts()
			layouts.errs["workflow:my-wf"] = fmt.Errorf("/private/secret.db: %w", tc.err)
			res := callTool(t, newLayoutToolServer(stampedRepo("my-wf", "rev-1"), layouts), "workflow_get", map[string]any{"name": "my-wf"})
			require.False(t, res.IsError, "the authorized manifest remains readable")
			require.NotContains(t, extractText(t, res), "/private/secret.db")
			_, arr := decodeArrangement(t, extractText(t, res))
			assert.Equal(t, tc.want, arr["availabilityError"])
			assert.Equal(t, "rev-1", arr["savedRevision"])
			assert.Equal(t, false, arr["available"])
			assert.Equal(t, "unavailable", arr["displayStatus"])
			assert.Empty(t, arr["findings"])
		})
	}
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
		{"pending mutation", fmt.Errorf("/private/registry: %w", workflow.ErrMutationPending), "rev-1", "workflow destination is awaiting durable mutation recovery; retry shortly"},
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

func TestWorkflowGet_PreservesManifestNumbers(t *testing.T) {
	repo := stampedRepo("numeric", "A")
	load := repo.loadFn
	repo.loadFn = func(ctx context.Context, name string) (*workflow.Workflow, error) {
		wf, err := load(ctx, name)
		if err == nil {
			wf.Nodes[0].Config.Operations = []workflow.Operation{{Variable: "large", Operation: "set", Value: int64(9007199254740993)}}
		}
		return wf, err
	}
	res := callTool(t, newLayoutToolServer(repo, nil), "workflow_get", map[string]any{"name": "numeric"})
	require.False(t, res.IsError)
	var out map[string]any
	decoder := json.NewDecoder(strings.NewReader(extractText(t, res)))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&out))
	node := out["nodes"].([]any)[0].(map[string]any)
	op := node["config"].(map[string]any)["operations"].([]any)[0].(map[string]any)
	assert.Equal(t, json.Number("9007199254740993"), op["value"], "adding arrangement must not round existing manifest fields")
}
