package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

// InstanceService defines the engine methods needed by instance handlers.
type InstanceService interface {
	ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error)
	// ListEnriched returns the authorized page with additive enriched fields.
	// When the enricher is unwired, rows carry the run name only. Implementations
	// apply the same authz as ListInstances.
	ListEnriched(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error)
	GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error)
	GetExecutionStatus(ctx context.Context, id uuid.UUID) ([]instance.ExecutionStatus, error)
	GetTrace(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error)
	GetNodeTrace(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error)
	GetVariables(ctx context.Context, id uuid.UUID) (map[string]any, error)
	GetVariableHistory(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error)
	GetInterventions(ctx context.Context, id uuid.UUID) ([]instance.Intervention, error)
	Pause(ctx context.Context, id uuid.UUID) error
	Resume(ctx context.Context, id uuid.UUID) error
	Cancel(ctx context.Context, id uuid.UUID) error
	Retry(ctx context.Context, id uuid.UUID, variableEdits map[string]any) error
	Rerun(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	SetPriority(ctx context.Context, id uuid.UUID, priority string) error
}

// GraphStateService reads the authorized aggregate state of a run and its
// descendants under the request identity.
type GraphStateService interface {
	GraphState(ctx context.Context, id uuid.UUID) (*engine.GraphState, error)
}

// InstanceHandler handles REST API requests for instance operations.
type InstanceHandler struct {
	instances   instance.InstanceRepository
	nodeExecs   instance.NodeExecutionRepository
	snapshots   instance.VariableSnapshotRepository
	service     InstanceService
	dismissals  instance.DismissalRepository
	currentUser CurrentUserResolver
	layouts     LayoutQueryService
	graphState  GraphStateService
}

// NewInstanceHandler creates a new InstanceHandler with its dependencies.
func NewInstanceHandler(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	snapshots instance.VariableSnapshotRepository,
	service InstanceService,
) *InstanceHandler {
	return &InstanceHandler{
		instances: instances,
		nodeExecs: nodeExecs,
		snapshots: snapshots,
		service:   service,
		// Default to Null Objects so the handler body never nil-checks the
		// dismissal seam. When the feature is enabled, SetDismissals swaps in the
		// real repo + resolver; route registration decides endpoint exposure.
		dismissals:  nullDismissalRepository{},
		currentUser: nullUserResolver{},
	}
}

// SetDismissals wires the per-user dismissal repository and acting-user seam so
// the Dismiss/Restore/DismissAll handlers and the per-user List can operate.
// Kept as a setter (rather than constructor args) so existing NewInstanceHandler
// call sites compile unchanged. When unset, the Null Object defaults apply and
// dismissal routes are not registered.
func (h *InstanceHandler) SetDismissals(dismissals instance.DismissalRepository, currentUser CurrentUserResolver) {
	h.dismissals = dismissals
	h.currentUser = currentUser
}

// SetLayouts wires the authorized snapshot-revision layout query seam.
func (h *InstanceHandler) SetLayouts(l LayoutQueryService) {
	h.layouts = l
}

// SetGraphState wires the aggregate read model before serving requests.
func (h *InstanceHandler) SetGraphState(gs GraphStateService) {
	h.graphState = gs
}

// instanceResponse is a JSON-friendly representation of an instance.
type instanceResponse struct {
	Execution         []instance.ExecutionStatus `json:"execution,omitempty"`
	ID                string                     `json:"id"`
	OwnerID           string                     `json:"ownerId"`
	WorkflowName      string                     `json:"workflowName"`
	State             string                     `json:"state"`
	Priority          string                     `json:"priority"`
	Department        string                     `json:"department,omitempty"`
	Visibility        string                     `json:"visibility,omitempty"`
	Variables         map[string]any             `json:"variables"`
	OriginalVariables map[string]any             `json:"originalVariables"`
	CurrentNodeID     *string                    `json:"currentNodeId,omitempty"`
	FailedNodeID      *string                    `json:"failedNodeId,omitempty"`
	CreatedAt         string                     `json:"createdAt"`
	UpdatedAt         string                     `json:"updatedAt"`
	CompletedAt       *string                    `json:"completedAt,omitempty"`
	QueuedAt          *string                    `json:"queuedAt,omitempty"`
	WaitingUntil      *string                    `json:"waitingUntil,omitempty"`
	WebhookURL        *string                    `json:"webhookUrl,omitempty"`
	WebhookReceived   bool                       `json:"webhookReceived,omitempty"`
	WorkflowSnapshot  map[string]any             `json:"workflowSnapshot,omitempty"`
	InteractiveNodeID *string                    `json:"interactiveNodeId"`
	InteractiveActive bool                       `json:"interactiveActive"`
	Layout            *layout.View               `json:"layout,omitempty"`
}

func toInstanceResponse(inst *instance.Instance) instanceResponse {
	resp := instanceResponse{
		ID:                inst.ID.String(),
		OwnerID:           inst.OwnerID.String(),
		WorkflowName:      inst.WorkflowName,
		State:             string(inst.State),
		Priority:          string(inst.Priority),
		Variables:         inst.Variables,
		OriginalVariables: inst.OriginalVariables,
		CurrentNodeID:     inst.CurrentNodeID,
		FailedNodeID:      inst.FailedNodeID,
		CreatedAt:         inst.CreatedAt.Format(APITimeFormat),
		UpdatedAt:         inst.UpdatedAt.Format(APITimeFormat),
		Department:        inst.Department,
		Visibility:        string(inst.Visibility),
	}
	resp.WorkflowSnapshot = inst.WorkflowSnapshot
	if inst.CompletedAt != nil {
		s := inst.CompletedAt.Format(APITimeFormat)
		resp.CompletedAt = &s
	}
	if inst.QueuedAt != nil {
		s := inst.QueuedAt.Format(APITimeFormat)
		resp.QueuedAt = &s
	}
	if inst.WaitingUntil != nil {
		s := inst.WaitingUntil.Format(APITimeFormat)
		resp.WaitingUntil = &s
	}
	if inst.WebhookToken != nil {
		url := fmt.Sprintf("/api/v1/webhooks/wait/%s", *inst.WebhookToken)
		resp.WebhookURL = &url
	}
	if inst.WebhookReceivedAt != nil {
		resp.WebhookReceived = true
	}

	// Derive interactive state fields.
	if inst.State == instance.StateWaiting && inst.CurrentNodeID != nil && *inst.CurrentNodeID != "" {
		if isInteractiveNode(inst.WorkflowSnapshot, *inst.CurrentNodeID) {
			nodeID := *inst.CurrentNodeID
			resp.InteractiveNodeID = &nodeID
			resp.InteractiveActive = true
		}
	}

	return resp
}

// isInteractiveNode checks if the specified node in the workflow snapshot is
// of type "interactive". Uses the shared engine.FindNodeByID helper.
func isInteractiveNode(snapshot map[string]any, nodeID string) bool {
	node, err := engine.FindNodeByID(snapshot, nodeID)
	if err != nil {
		return false
	}
	return workflow.IsInteractive(node.Type)
}

// summaryDTO is the JSON projection of a domain InstanceSummary (the list-item
// shape). It is deliberately the lean list shape — NOT the heavy single-instance
// instanceResponse used by Get. toSummaryDTO owns the timestamp/nil-pointer
// Format(APITimeFormat) mapping so there is a single place that maps a summary,
// killing the silent-field-drop trap of two parallel mappers.
type summaryDTO struct {
	ID            string  `json:"id"`
	OwnerID       string  `json:"ownerId"`
	WorkflowName  string  `json:"workflowName"`
	State         string  `json:"state"`
	Priority      string  `json:"priority"`
	Department    string  `json:"department,omitempty"`
	Visibility    string  `json:"visibility,omitempty"`
	CurrentNodeID *string `json:"currentNodeId,omitempty"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
	CompletedAt   *string `json:"completedAt,omitempty"`
	QueuedAt      *string `json:"queuedAt,omitempty"`
	TriggerSource string  `json:"triggerSource"`
	ScheduleID    *string `json:"scheduleId,omitempty"`
	// ParentInstanceID is the spawning parent run; omitted entirely for a root
	// run so the dashboard can treat presence as "this row nests under something".
	ParentInstanceID *string `json:"parentInstanceId,omitempty"`
}

func toSummaryDTO(s instance.InstanceSummary) summaryDTO {
	dto := summaryDTO{
		ID:            s.ID.String(),
		OwnerID:       s.OwnerID.String(),
		WorkflowName:  s.WorkflowName,
		State:         string(s.State),
		Priority:      string(s.Priority),
		CurrentNodeID: s.CurrentNodeID,
		CreatedAt:     s.CreatedAt.Format(APITimeFormat),
		UpdatedAt:     s.UpdatedAt.Format(APITimeFormat),
		Department:    s.Department,
		Visibility:    string(s.Visibility),
		TriggerSource: string(s.TriggerSource),
	}
	if s.CompletedAt != nil {
		v := s.CompletedAt.Format(APITimeFormat)
		dto.CompletedAt = &v
	}
	if s.QueuedAt != nil {
		v := s.QueuedAt.Format(APITimeFormat)
		dto.QueuedAt = &v
	}
	if s.ScheduleID != nil {
		v := s.ScheduleID.String()
		dto.ScheduleID = &v
	}
	if s.ParentInstanceID != nil {
		v := s.ParentInstanceID.String()
		dto.ParentInstanceID = &v
	}
	return dto
}

// summaryResponse is the list-item JSON: the summary DTO plus a per-user
// "dismissed" flag. The flag is HTTP/view state and is intentionally not part of
// the domain InstanceSummary. The DTO is embedded so its fields flatten into the
// same JSON object the dashboard already consumes.
type summaryResponse struct {
	summaryDTO
	Dismissed bool `json:"dismissed"`
}

func toSummaryResponse(s instance.InstanceSummary, dismissed bool) summaryResponse {
	return summaryResponse{
		summaryDTO: toSummaryDTO(s),
		Dismissed:  dismissed,
	}
}

// enrichedSummaryDTO is the JSON projection of instance.EnrichedInstanceSummary.
// It embeds summaryDTO so the lean fields flatten into the same JSON object;
// the enriched fields are additive. submitterName/submitterIdentity carry the
// resolved owner (the HTTP layer renders "you" vs the name from authMode in
// the dashboard, not here — the dashboard reads authMode from /me).
type enrichedSummaryDTO struct {
	summaryDTO
	RunName           string  `json:"runName"`
	FriendlyStep      string  `json:"friendlyStep,omitempty"`
	Model             string  `json:"model,omitempty"`
	Effort            string  `json:"effort,omitempty"`
	Backend           string  `json:"backend,omitempty"`
	Provider          string  `json:"provider,omitempty"`
	NodeType          string  `json:"nodeType,omitempty"`
	GitRef            string  `json:"gitRef,omitempty"`
	RetryCount        int     `json:"retryCount"`
	QueuePosition     int     `json:"queuePosition,omitempty"`
	SubmitterName     string  `json:"submitterName,omitempty"`
	SubmitterIdentity string  `json:"submitterIdentity,omitempty"`
	FailedNodeID      *string `json:"failedNodeId,omitempty"`
	// ChildCount is the number of spawned children the CALLER may see. Omitted
	// when zero so a childless row renders no expander.
	ChildCount int                        `json:"childCount,omitempty"`
	Execution  []instance.ExecutionStatus `json:"execution,omitempty"`
}

// toEnrichedSummaryDTO projects an enriched summary to its JSON DTO. The
// submitter's account identity (email) is PII and is only included when
// revealIdentity is true (the viewer owns the run or is an admin); otherwise it
// is withheld so a public/department-peer viewer cannot read another user's
// account email.
func toEnrichedSummaryDTO(e instance.EnrichedInstanceSummary, revealIdentity bool) enrichedSummaryDTO {
	dto := enrichedSummaryDTO{
		summaryDTO:    toSummaryDTO(e.InstanceSummary),
		RunName:       e.RunName,
		FriendlyStep:  e.FriendlyStep,
		Model:         e.Model,
		Effort:        e.Effort,
		Backend:       e.Backend,
		Provider:      e.Provider,
		NodeType:      e.NodeType,
		GitRef:        e.GitRef,
		RetryCount:    e.RetryCount,
		QueuePosition: e.QueuePosition,
		SubmitterName: e.Submitter.Name,
		FailedNodeID:  e.FailedNodeID,
		ChildCount:    e.ChildCount,
		Execution:     e.Execution,
	}
	if revealIdentity {
		dto.SubmitterIdentity = e.Submitter.AccountIdentity
	}
	return dto
}

// enrichedResponse is the enriched list-item JSON: the enriched DTO plus the
// per-user "dismissed" flag, mirroring summaryResponse.
type enrichedResponse struct {
	enrichedSummaryDTO
	Dismissed bool `json:"dismissed"`
}

func toEnrichedResponse(e instance.EnrichedInstanceSummary, dismissed bool, revealIdentity bool) enrichedResponse {
	return enrichedResponse{
		enrichedSummaryDTO: toEnrichedSummaryDTO(e, revealIdentity),
		Dismissed:          dismissed,
	}
}

// retryRequest is the optional JSON body for retrying an instance.
type retryRequest struct {
	Variables map[string]any `json:"variables"`
}

// parseInstanceID extracts and validates the instance UUID from the URL.
func parseInstanceID(r *http.Request) (uuid.UUID, error) {
	idStr := chi.URLParam(r, "id")
	return uuid.Parse(idStr)
}

// mapEngineError maps an engine error to the appropriate HTTP status code.
func mapEngineError(err error) int {
	if errors.Is(err, instance.ErrNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, instance.ErrForbidden) {
		return http.StatusForbidden
	}
	if errors.Is(err, instance.ErrNotRunning) ||
		errors.Is(err, instance.ErrNotPaused) ||
		errors.Is(err, instance.ErrNotFailed) ||
		errors.Is(err, instance.ErrNotQueued) ||
		errors.Is(err, instance.ErrNotWaiting) ||
		errors.Is(err, instance.ErrCannotCancel) ||
		errors.Is(err, instance.ErrCannotRetry) ||
		errors.Is(err, instance.ErrCannotRerun) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// mapEngineErrorCode maps an engine error to an error code string.
func mapEngineErrorCode(err error) string {
	status := mapEngineError(err)
	switch status {
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusConflict:
		return "CONFLICT"
	default:
		return "INTERNAL_ERROR"
	}
}

const (
	// sharedWindowLimit is the single row cap for the category base window that
	// BOTH List and DismissAll operate on. It lives in exactly one place so the
	// two endpoints provably act on the same set: "Dismiss All dismisses exactly
	// what is listed." A client may request a smaller limit on List, but the
	// default window — and the set DismissAll dismisses — is this constant.
	sharedWindowLimit = 500
	maxListLimit      = sharedWindowLimit
	maxRetryBodyBytes = 1 << 20 // 1 MiB
)

// baseWindowFilters builds the category base window shared by List and
// DismissAll: a state filter, an optional since cutoff, and the one shared row
// limit. Per-call-site concerns (includeDismissed filtering, paging offset, and
// the per-user dismissed-set subtraction) are deliberately NOT part of the base
// window and stay explicit at each call site.
func baseWindowFilters(state *instance.State, since *time.Time) instance.ListFilters {
	return instance.ListFilters{
		State: state,
		Since: since,
		Limit: sharedWindowLimit,
	}
}

// List handles GET /instances with optional query params for filtering.
func (h *InstanceHandler) List(w http.ResponseWriter, r *http.Request) {
	var state *instance.State
	if s := r.URL.Query().Get("state"); s != "" {
		st := instance.State(s)
		state = &st
	}
	var since *time.Time
	if s := r.URL.Query().Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			since = &t
		}
	}

	// Start from the shared category base window so List and DismissAll agree on
	// the limit/category; then layer List-only concerns (workflowName, paging,
	// client limit override).
	filters := baseWindowFilters(state, since)
	if wn := r.URL.Query().Get("workflowName"); wn != "" {
		filters.WorkflowName = &wn
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			if parsed > maxListLimit {
				parsed = maxListLimit
			}
			filters.Limit = parsed
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			filters.Offset = parsed
		}
	}

	// The parent selector is a caller-supplied narrowing on this endpoint.
	// It composes with — and can never replace — the owner/department constraints
	// ListEnriched service-sets from the request identity. Decoding goes through
	// the shared domain parser so REST and the MCP tool cannot drift.
	//
	// A MALFORMED selector is rejected rather than silently degrading to an
	// unfiltered list, which would show the caller far more than they asked for.
	scope, parentID, perr := instance.ParseParentScope(r.URL.Query().Get("parent"))
	if perr != nil {
		WriteError(w, http.StatusBadRequest, "INVALID_PARENT", perr.Error())
		return
	}
	filters.ParentScope = scope
	filters.ParentInstanceID = parentID

	// This narrowing composes with the service-set visibility scope.
	if r.URL.Query().Has("activeDescendantAncestors") {
		switch r.URL.Query().Get("activeDescendantAncestors") {
		case "true", "1":
			filters.ActiveDescendantAncestors = true
		case "false", "0":
			filters.ActiveDescendantAncestors = false
		default:
			WriteError(w, http.StatusBadRequest, "INVALID_FILTER", "activeDescendantAncestors must be true, false, 1 or 0")
			return
		}
	}

	enriched, err := h.service.ListEnriched(r.Context(), filters)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteJSON(w, http.StatusOK, []instance.EnrichedInstanceSummary{})
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list instances")
		return
	}

	includeDismissed := false
	if v := r.URL.Query().Get("includeDismissed"); v != "" {
		if parsed, perr := strconv.ParseBool(v); perr == nil {
			includeDismissed = parsed
		}
	}

	// The dismissal seam is always non-nil (Null Object when disabled), so no
	// nil-guard here: a disabled feature resolves the zero user and subtracts an
	// empty dismissed set, leaving the list unchanged.
	user, uerr := h.currentUser.CurrentUser(r)
	if uerr != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve user")
		return
	}
	dismissedIDs, err := h.dismissals.DismissedIDs(r.Context(), user)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load dismissals")
		return
	}

	// The submitter's account identity (email) is PII: reveal it only to the run
	// owner or an admin. An absent identity (hasViewer false) reveals nothing.
	viewer, hasViewer := identity.FromContext(r.Context())

	out := make([]enrichedResponse, 0, len(enriched))
	for _, e := range enriched {
		isDismissed := dismissedIDs[e.ID]
		if isDismissed && !includeDismissed {
			continue
		}
		revealIdentity := hasViewer && (viewer.IsAdmin() || viewer.UserID == e.OwnerID)
		out = append(out, toEnrichedResponse(e, isDismissed, revealIdentity))
	}
	WriteJSON(w, http.StatusOK, out)
}

// Get handles GET /instances/{id} and returns the full instance.
func (h *InstanceHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID: "+chi.URLParam(r, "id"))
		return
	}

	inst, err := h.service.GetInstance(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
		return
	}

	execution, err := h.service.GetExecutionStatus(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, "STATUS_UNAVAILABLE", "execution status unavailable")
		return
	}
	resp := toInstanceResponse(inst)
	resp.Execution = execution
	view := h.instanceLayout(r, inst)
	resp.Layout = &view
	WriteJSON(w, http.StatusOK, resp)
}

// instanceLayout preserves the authorized snapshot's fallback when status is unavailable.
func (h *InstanceHandler) instanceLayout(r *http.Request, inst *instance.Instance) layout.View {
	var view layout.View
	var err error
	if h.layouts == nil {
		err = errors.New("layout service not configured")
	} else {
		view, err = h.layouts.InstanceLayout(r.Context(), inst.ID)
	}
	if err == nil {
		return view
	}
	// The instance itself was already authorized. Describe only status
	// availability here; query causes can contain storage or policy details.
	reason := "layout status unavailable; reload or retry"
	switch {
	case h.layouts == nil:
		reason = "layout service not configured"
	case errors.Is(err, workflow.ErrMutationPending):
		reason = "workflow destination is awaiting durable mutation recovery; retry shortly"
	case errors.Is(err, layout.ErrStoreUnavailable):
		reason = "layout status store unavailable; retry later"
	}
	out := layout.View{
		DisplayStatus:     layout.DisplayUnavailable,
		Findings:          []layout.Finding{},
		Provenance:        layout.ProvenanceLegacy,
		Available:         false,
		Unreviewed:        true,
		AvailabilityError: reason,
	}
	if wf, decodeErr := engine.DecodeWorkflowSnapshot(inst.WorkflowSnapshot); decodeErr == nil {
		graph, _ := layout.BuildGraph(wf)
		out.Groups = graph.Groups
		if wf.Presentation != nil {
			retained := wf.Presentation.Retained
			out.SavedRevision = wf.SavedRevision()
			out.RetryEligible = out.SavedRevision != ""
			out.Retained = &retained
			out.Provenance = layout.ProvenanceRetainedPositions
		}
	}
	return out
}

// GraphState handles GET /instances/{id}/graph-state. Error responses mirror
// Get so an unviewable root remains indistinguishable from an absent one.
func (h *InstanceHandler) GraphState(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID: "+chi.URLParam(r, "id"))
		return
	}
	if h.graphState == nil {
		WriteError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "graph state is not available")
		return
	}

	gs, err := h.graphState.GraphState(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
		return
	}
	WriteJSON(w, http.StatusOK, gs)
}

// Pause handles POST /instances/{id}/pause.
func (h *InstanceHandler) Pause(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	if err := h.service.Pause(r.Context(), id); err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// Resume handles POST /instances/{id}/resume.
func (h *InstanceHandler) Resume(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	if err := h.service.Resume(r.Context(), id); err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

// Cancel handles POST /instances/{id}/cancel.
func (h *InstanceHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	if err := h.service.Cancel(r.Context(), id); err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	// The cancel may have succeeded only by stopping descendants of a terminal
	// parent whose own state is unchanged, so report the state it ended in.
	resp := map[string]string{"status": "cancel_requested"}
	if inst, err := h.service.GetInstance(r.Context(), id); err == nil && inst != nil {
		resp["status"] = instance.CancelStatus(inst.State)
		resp["state"] = string(inst.State)
	}
	WriteJSON(w, http.StatusOK, resp)
}

// Retry handles POST /instances/{id}/retry with optional variable edits.
func (h *InstanceHandler) Retry(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	var req retryRequest
	if r.Body != nil && r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxRetryBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
			return
		}
	}

	if err := h.service.Retry(r.Context(), id, req.Variables); err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "retrying"})
}

// Rerun handles POST /instances/{id}/rerun.
func (h *InstanceHandler) Rerun(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	newID, err := h.service.Rerun(r.Context(), id)
	if err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"instanceId": newID.String(),
		"state":      "running",
	})
}

// isDismissibleState reports whether a state is dismissible (completed or
// failed). Note: this is narrower than stream_handler's isTerminalState, which
// also includes cancelled; only completed/failed items can be dismissed.
func isDismissibleState(s instance.State) bool {
	return s == instance.StateCompleted || s == instance.StateFailed
}

// Dismiss handles POST /instances/{id}/dismiss. Per-user; terminal-only.
func (h *InstanceHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}
	inst, err := h.instances.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
		return
	}
	if !isDismissibleState(inst.State) {
		WriteError(w, http.StatusConflict, "CONFLICT", "only completed or failed instances can be dismissed")
		return
	}
	user, err := h.currentUser.CurrentUser(r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve user")
		return
	}
	if err := h.dismissals.Dismiss(r.Context(), user, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to dismiss instance")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Restore handles POST /instances/{id}/restore. Per-user; idempotent no-op if
// not currently dismissed.
func (h *InstanceHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}
	user, err := h.currentUser.CurrentUser(r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve user")
		return
	}
	if err := h.dismissals.Restore(r.Context(), user, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to restore instance")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DismissAll handles POST /instances/dismiss-all?state=failed|completed. It
// dismisses every instance currently listed in that category for the acting
// user (the same window the dashboard shows). Empty category is a no-op.
func (h *InstanceHandler) DismissAll(w http.ResponseWriter, r *http.Request) {
	stateParam := r.URL.Query().Get("state")
	if stateParam != string(instance.StateFailed) && stateParam != string(instance.StateCompleted) {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "state must be 'failed' or 'completed'")
		return
	}
	user, err := h.currentUser.CurrentUser(r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve user")
		return
	}
	state := instance.State(stateParam)
	var since *time.Time
	if s := r.URL.Query().Get("since"); s != "" {
		if t, perr := time.Parse(time.RFC3339, s); perr == nil {
			since = &t
		}
	}
	// Same base window as List, so DismissAll dismisses exactly the set the
	// dashboard lists. DismissAll deliberately omits List's includeDismissed
	// filtering and dismissed-set subtraction: it dismisses everything in the
	// window regardless of current per-user dismissed state.
	//
	// It routes through the SERVICE, not the raw repository: the repository's
	// visibility predicate fails closed, and the service is what derives the
	// caller's scope from the request identity. Reading the repository directly
	// relied on the repository inferring "admin" from an unscoped filter set —
	// the exact inference that made an unscoped list return the full table.
	summaries, err := h.service.ListInstances(r.Context(), baseWindowFilters(&state, since))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list instances")
		return
	}
	// SECURITY: the id-set is derived 100% server-side from instances.List —
	// never from client input — so DismissMany cannot be turned into a
	// write-IDOR. One atomic multi-row write (no partial-commit-then-500 path).
	ids := make([]uuid.UUID, 0, len(summaries))
	for _, s := range summaries {
		ids = append(ids, s.ID)
	}
	if err := h.dismissals.DismissMany(r.Context(), user, ids); err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to dismiss instances")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// nodeExecutionResponse is a JSON-friendly representation of a node execution.
type placementRecordResponse struct {
	JobID      string `json:"jobId"`
	RunnerID   string `json:"runnerId"`
	SourceID   string `json:"sourceId,omitempty"`
	Reason     string `json:"reason"`
	Outcome    string `json:"outcome"`
	Generation int64  `json:"generation"`
	At         string `json:"at"`
}

type nodeExecutionResponse struct {
	RunnerID             *string                      `json:"runnerId,omitempty"`
	SourceID             *string                      `json:"sourceId,omitempty"`
	PlacementReason      *string                      `json:"placementReason,omitempty"`
	PlacementHistory     []placementRecordResponse    `json:"placementHistory,omitempty"`
	ID                   string                       `json:"id"`
	InstanceID           string                       `json:"instanceId"`
	NodeID               string                       `json:"nodeId"`
	Attempt              int                          `json:"attempt"`
	Status               string                       `json:"status"`
	ResolvedInputs       map[string]any               `json:"resolvedInputs,omitempty"`
	Output               map[string]any               `json:"output,omitempty"`
	Error                map[string]any               `json:"error,omitempty"`
	LLMConversation      map[string]any               `json:"llmConversation,omitempty"`
	LLMModel             *string                      `json:"llmModel,omitempty"`
	LLMTokensIn          *int                         `json:"llmTokensIn,omitempty"`
	LLMTokensOut         *int                         `json:"llmTokensOut,omitempty"`
	ScriptStdout         *string                      `json:"scriptStdout,omitempty"`
	ScriptStderr         *string                      `json:"scriptStderr,omitempty"`
	ScriptExitCode       *int                         `json:"scriptExitCode,omitempty"`
	StartedAt            string                       `json:"startedAt"`
	CompletedAt          *string                      `json:"completedAt,omitempty"`
	SessionInvocationID  *string                      `json:"sessionInvocationId,omitempty"`
	SessionCheckpointRef *string                      `json:"sessionCheckpointRef,omitempty"`
	SessionPinRunnerID   *string                      `json:"sessionPinRunnerId,omitempty"`
	SessionGeneration    int64                        `json:"sessionGeneration,omitempty"`
	SessionMoves         []instance.SessionMoveRecord `json:"sessionMoves,omitempty"`
}

func toNodeExecutionResponse(exec *instance.NodeExecution) nodeExecutionResponse {
	resp := nodeExecutionResponse{
		RunnerID: exec.RunnerID, SourceID: exec.SourceID, PlacementReason: exec.PlacementReason,
		ID:                   exec.ID.String(),
		InstanceID:           exec.InstanceID.String(),
		NodeID:               exec.NodeID,
		Attempt:              exec.Attempt,
		Status:               string(exec.Status),
		ResolvedInputs:       exec.ResolvedInputs,
		Output:               exec.Output,
		Error:                exec.Error,
		LLMConversation:      exec.LLMConversation,
		LLMModel:             exec.LLMModel,
		LLMTokensIn:          exec.LLMTokensIn,
		LLMTokensOut:         exec.LLMTokensOut,
		ScriptStdout:         exec.ScriptStdout,
		ScriptStderr:         exec.ScriptStderr,
		ScriptExitCode:       exec.ScriptExitCode,
		StartedAt:            exec.StartedAt.Format(APITimeFormat),
		SessionCheckpointRef: exec.SessionCheckpointRef,
		SessionPinRunnerID:   exec.SessionPinRunner,
		SessionGeneration:    exec.SessionGeneration,
		SessionMoves:         exec.SessionMoves,
	}
	if exec.SessionInvocationID != uuid.Nil {
		s := exec.SessionInvocationID.String()
		resp.SessionInvocationID = &s
	}
	for _, p := range exec.PlacementHistory {
		resp.PlacementHistory = append(resp.PlacementHistory, placementRecordResponse{JobID: p.JobID, RunnerID: p.RunnerID, SourceID: p.SourceID, Reason: p.Reason, Outcome: p.Outcome, Generation: p.Generation, At: p.At.UTC().Format(APITimeFormat)})
	}
	if exec.CompletedAt != nil {
		s := exec.CompletedAt.Format(APITimeFormat)
		resp.CompletedAt = &s
	}
	return resp
}

// GetTrace handles GET /instances/{id}/trace and returns ordered execution records.
func (h *InstanceHandler) GetTrace(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	execs, err := h.service.GetTrace(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list trace records")
		return
	}

	resp := make([]nodeExecutionResponse, len(execs))
	for i, exec := range execs {
		resp[i] = toNodeExecutionResponse(&exec)
	}
	WriteJSON(w, http.StatusOK, resp)
}

// GetNodeTrace handles GET /instances/{id}/trace/{nodeId} and returns a single node execution.
func (h *InstanceHandler) GetNodeTrace(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	exec, err := h.service.GetNodeTrace(r.Context(), id, nodeID)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			// Covers both an unauthorized/absent instance and a missing node
			// execution; the message stays node-oriented to match prior behavior.
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "node execution not found: "+nodeID)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get node execution")
		return
	}

	WriteJSON(w, http.StatusOK, toNodeExecutionResponse(exec))
}

// variableSnapshotResponse is a JSON-friendly representation of a variable snapshot.
type variableSnapshotResponse struct {
	ID         string         `json:"id"`
	InstanceID string         `json:"instanceId"`
	NodeID     string         `json:"nodeId"`
	Variables  map[string]any `json:"variables"`
	CapturedAt string         `json:"capturedAt"`
}

func toVariableSnapshotResponse(snap *instance.VariableSnapshot) variableSnapshotResponse {
	return variableSnapshotResponse{
		ID:         snap.ID.String(),
		InstanceID: snap.InstanceID.String(),
		NodeID:     snap.NodeID,
		Variables:  snap.Variables,
		CapturedAt: snap.CapturedAt.Format(APITimeFormat),
	}
}

// GetVariables handles GET /instances/{id}/variables and returns current instance variables.
func (h *InstanceHandler) GetVariables(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	vars, err := h.service.GetVariables(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
		return
	}

	WriteJSON(w, http.StatusOK, vars)
}

// GetVariableHistory handles GET /instances/{id}/variables/history and returns
// the ordered history of variable snapshots.
func (h *InstanceHandler) GetVariableHistory(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	snaps, err := h.service.GetVariableHistory(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list variable history")
		return
	}

	resp := make([]variableSnapshotResponse, len(snaps))
	for i, snap := range snaps {
		resp[i] = toVariableSnapshotResponse(&snap)
	}
	WriteJSON(w, http.StatusOK, resp)
}

// interventionResponse is a JSON-friendly representation of an intervention.
type interventionResponse struct {
	ID          string  `json:"id"`
	InstanceID  string  `json:"instanceId"`
	ActorUserID string  `json:"actorUserId"`
	Action      string  `json:"action"`
	Detail      *string `json:"detail,omitempty"`
	CreatedAt   string  `json:"createdAt"`
}

func toInterventionResponse(iv *instance.Intervention) interventionResponse {
	return interventionResponse{
		ID:          iv.ID.String(),
		InstanceID:  iv.InstanceID.String(),
		ActorUserID: iv.ActorUserID.String(),
		Action:      iv.Action,
		Detail:      iv.Detail,
		CreatedAt:   iv.CreatedAt.Format(APITimeFormat),
	}
}

// GetInterventions handles GET /instances/{id}/interventions and returns the
// intervention audit trail. Visible to the instance owner and admins; a
// non-viewer gets 404 (existence not leaked).
func (h *InstanceHandler) GetInterventions(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	ivs, err := h.service.GetInterventions(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list interventions")
		return
	}

	resp := make([]interventionResponse, len(ivs))
	for i := range ivs {
		resp[i] = toInterventionResponse(&ivs[i])
	}
	WriteJSON(w, http.StatusOK, resp)
}

// priorityRequest is the JSON body for changing instance priority.
type priorityRequest struct {
	Priority string `json:"priority"`
}

// SetPriority handles POST /instances/{id}/priority.
func (h *InstanceHandler) SetPriority(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	var req priorityRequest
	if r.Body == nil || r.ContentLength == 0 {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "request body required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRetryBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}

	// Validate priority value.
	switch req.Priority {
	case "high", "normal", "low":
		// valid
	default:
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid priority: must be high, normal, or low")
		return
	}

	if err := h.service.SetPriority(r.Context(), id, req.Priority); err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "priority_updated"})
}
