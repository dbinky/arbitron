package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// newParentListHandler builds the instance handler over the shared mock service
// and points ListEnriched at a stub that captures the filters the handler
// produced and HONOURS the parent narrowing, so a handler that dropped the
// parameter would fail these tests. The returned pointer receives the captured
// filters.
func newParentListHandler(rows []instance.EnrichedInstanceSummary) (*adapthttp.InstanceHandler, *instance.ListFilters) {
	h, _, _, _, svc := newInstanceHandler()
	got := &instance.ListFilters{}
	svc.listEnrichedFn = func(_ context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		*got = filters
		out := []instance.EnrichedInstanceSummary{}
		for _, r := range rows {
			switch filters.ParentScope {
			case instance.ParentScopeRoots:
				if r.ParentInstanceID == nil {
					out = append(out, r)
				}
			case instance.ParentScopeChildren:
				if filters.ParentInstanceID != nil && r.ParentInstanceID != nil &&
					*r.ParentInstanceID == *filters.ParentInstanceID {
					out = append(out, r)
				}
			default:
				out = append(out, r)
			}
		}
		return out, nil
	}
	return h, got
}

// adminListRequest issues GET /instances with the given raw query string as an
// admin and returns the recorder.
func adminListRequest(h *adapthttp.InstanceHandler, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/instances"+query, nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{
		UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin,
	}))
	w := httptest.NewRecorder()
	h.List(w, req)
	return w
}

// doList issues the request and decodes the rows on a 200.
func doList(t *testing.T, h *adapthttp.InstanceHandler, query string) (int, []map[string]any) {
	t.Helper()
	w := adminListRequest(h, query)
	if w.Code != http.StatusOK {
		return w.Code, nil
	}
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	return w.Code, rows
}

// ---------- happy path ----------

func TestInstanceListREST_RootsOnly(t *testing.T) {
	parent := uuid.New()
	root := instance.EnrichedInstanceSummary{
		InstanceSummary: instance.InstanceSummary{ID: parent, WorkflowName: "parent"},
	}
	child := instance.EnrichedInstanceSummary{
		InstanceSummary: instance.InstanceSummary{
			ID: uuid.New(), WorkflowName: "child", ParentInstanceID: &parent,
		},
	}
	h, got := newParentListHandler([]instance.EnrichedInstanceSummary{root, child})

	code, rows := doList(t, h, "?parent=roots")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, instance.ParentScopeRoots, got.ParentScope)
	assert.Nil(t, got.ParentInstanceID)
	require.Len(t, rows, 1)
	assert.Equal(t, parent.String(), rows[0]["id"])
}

func TestInstanceListREST_ChildrenOfAParent(t *testing.T) {
	parent := uuid.New()
	childID := uuid.New()
	h, got := newParentListHandler([]instance.EnrichedInstanceSummary{
		{InstanceSummary: instance.InstanceSummary{ID: parent, WorkflowName: "parent"}},
		{InstanceSummary: instance.InstanceSummary{ID: childID, WorkflowName: "child", ParentInstanceID: &parent}},
	})

	code, rows := doList(t, h, "?parent="+parent.String())
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, instance.ParentScopeChildren, got.ParentScope)
	require.NotNil(t, got.ParentInstanceID)
	assert.Equal(t, parent, *got.ParentInstanceID)
	require.Len(t, rows, 1)
	assert.Equal(t, childID.String(), rows[0]["id"])
	assert.Equal(t, parent.String(), rows[0]["parentInstanceId"],
		"a child row must carry its parent id so the dashboard can nest it")
}

// ---------- success ----------

func TestInstanceListREST_NoParentParamDoesNoNarrowing(t *testing.T) {
	parent := uuid.New()
	h, got := newParentListHandler([]instance.EnrichedInstanceSummary{
		{InstanceSummary: instance.InstanceSummary{ID: parent}},
		{InstanceSummary: instance.InstanceSummary{ID: uuid.New(), ParentInstanceID: &parent}},
	})

	code, rows := doList(t, h, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, instance.ParentScopeAny, got.ParentScope)
	assert.Len(t, rows, 2)
}

func TestInstanceListREST_ChildCountIsSurfaced(t *testing.T) {
	h, _ := newParentListHandler([]instance.EnrichedInstanceSummary{
		{InstanceSummary: instance.InstanceSummary{ID: uuid.New()}, ChildCount: 3},
		{InstanceSummary: instance.InstanceSummary{ID: uuid.New()}},
	})

	code, rows := doList(t, h, "")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, rows, 2)
	assert.Equal(t, float64(3), rows[0]["childCount"], "the expander state must reach the client")
	_, present := rows[1]["childCount"]
	assert.False(t, present, "a zero count is omitted so a childless row renders no expander")
}

func TestInstanceListREST_RootRowOmitsParentInstanceID(t *testing.T) {
	h, _ := newParentListHandler([]instance.EnrichedInstanceSummary{
		{InstanceSummary: instance.InstanceSummary{ID: uuid.New()}},
	})

	code, rows := doList(t, h, "")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, rows, 1)
	_, present := rows[0]["parentInstanceId"]
	assert.False(t, present, "a root row must omit parentInstanceId entirely")
}

func TestInstanceListREST_ParentComposesWithStateFilter(t *testing.T) {
	parent := uuid.New()
	h, got := newParentListHandler([]instance.EnrichedInstanceSummary{})

	code, _ := doList(t, h, "?parent="+parent.String()+"&state=running")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, instance.ParentScopeChildren, got.ParentScope)
	require.NotNil(t, got.State)
	assert.Equal(t, instance.StateRunning, *got.State,
		"the parent selector must compose with the existing filters, not replace them")
}

// ---------- failure ----------

// A malformed selector is REJECTED rather than silently degrading to an
// unfiltered list, which would show a caller far more than they asked for.
// The error code is pinned as INVALID_PARENT (not a generic BAD_REQUEST) so a
// caller can distinguish this failure from any other 400 on the endpoint.
func TestInstanceListREST_MalformedParentIsRejected(t *testing.T) {
	h, _ := newParentListHandler([]instance.EnrichedInstanceSummary{})

	for _, raw := range []string{"not-a-uuid", "ROOTS", "root", "%20"} {
		w := adminListRequest(h, "?parent="+raw)
		require.Equal(t, http.StatusBadRequest, w.Code, "parent=%q must be rejected", raw)
		var body adapthttp.ErrorResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "INVALID_PARENT", body.Code, "parent=%q must report the INVALID_PARENT code", raw)
	}
}

// ---------- edge case ----------

// Naming a run the caller cannot see returns an EMPTY list, identical to a
// parent with no children — existence is not disclosed.
func TestInstanceListREST_UnknownParentReturnsEmptyNot404(t *testing.T) {
	h, _ := newParentListHandler([]instance.EnrichedInstanceSummary{})

	code, rows := doList(t, h, "?parent="+uuid.New().String())
	require.Equal(t, http.StatusOK, code)
	assert.Empty(t, rows)
}
