package layout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// WorkflowAccess owns manifest visibility and Update-policy retry authorization.
// Neither operation writes a manifest or allocates an identity.
type WorkflowAccess interface {
	LoadForLayout(context.Context, string) (*workflow.Workflow, workflow.Scope, error)
	AuthorizeLayoutRetry(context.Context, string) (*workflow.Workflow, workflow.Scope, error)
	// LayoutRetryTargets is every destination AuthorizeLayoutRetry may resolve
	// for the caller. A held lock set cannot grow, so a retry locks all of them.
	LayoutRetryTargets(context.Context, string) ([]workflow.MutationTarget, error)
}

// InstanceAccess returns only the authorized immutable run snapshot.
type InstanceAccess interface {
	InstanceLayoutSnapshot(context.Context, uuid.UUID) (*workflow.Workflow, error)
	AuthorizeInstanceLayoutRetry(context.Context, uuid.UUID) (*workflow.Workflow, error)
}

// RetryAck acknowledges a queued generation without waiting for review.
type RetryAck struct {
	SavedRevision string        `json:"savedRevision"`
	JobGeneration int64         `json:"jobGeneration"`
	State         State         `json:"state"`
	DisplayStatus DisplayStatus `json:"displayStatus"`
}

var (
	ErrRevisionConflict = errors.New("layout: expected revision is not current")
	ErrLegacyRevision   = errors.New("layout: revision has no arrangement request")
)

// QueryService is the authorized, revision-scoped seam shared by adapters.
type QueryService struct {
	reader    *Reader
	requester *Requester
	workflows WorkflowAccess
	instances InstanceAccess
	mutations workflow.MutationCoordinator
	now       func() time.Time
}

func NewQueryService(reader *Reader, requester *Requester, workflows WorkflowAccess, instances InstanceAccess, mutations workflow.MutationCoordinator) *QueryService {
	return &QueryService{reader: reader, requester: requester, workflows: workflows, instances: instances, mutations: mutations, now: time.Now}
}

func queryNil(v any) bool {
	return v == nil || reflect.ValueOf(v).Kind() == reflect.Pointer && reflect.ValueOf(v).IsNil()
}

func (q *QueryService) ready(access any, retry bool) error {
	if q == nil || q.reader == nil || queryNil(q.reader.store) || queryNil(access) || retry && (q.requester == nil || queryNil(q.requester.store)) {
		return fmt.Errorf("%w: query service is not configured", ErrStoreUnavailable)
	}
	return nil
}

func queryDefinition(wf *workflow.Workflow) error {
	_, err := BuildGraph(wf)
	// Group validation belongs to arrangement, not read authorization: saved
	// legacy input still needs its failure, captured fallback and Retry shown.
	if errors.Is(err, ErrInvalidGroups) {
		return nil
	}
	return err
}

func (q *QueryService) WorkflowLayout(ctx context.Context, name string) (View, error) {
	var access WorkflowAccess
	if q != nil {
		access = q.workflows
	}
	if err := q.ready(access, false); err != nil {
		return View{}, err
	}
	wf, scope, err := access.LoadForLayout(ctx, name)
	if err != nil {
		return View{}, err
	}
	if wf == nil {
		return View{}, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	if err := queryDefinition(wf); err != nil {
		return View{}, err
	}
	v, stale := q.reader.view(ctx, wf)
	return q.refreshStale(ctx, wf, v, stale, func() error {
		return q.rearrangeWorkflow(ctx, access, wf, scope, name)
	}), nil
}

func (q *QueryService) InstanceLayout(ctx context.Context, id uuid.UUID) (View, error) {
	var access InstanceAccess
	if q != nil {
		access = q.instances
	}
	if err := q.ready(access, false); err != nil {
		return View{}, err
	}
	wf, err := access.InstanceLayoutSnapshot(ctx, id)
	if err != nil {
		return View{}, err
	}
	if err := queryDefinition(wf); err != nil {
		return View{}, err
	}
	v, stale := q.reader.view(ctx, wf)
	return q.refreshStale(ctx, wf, v, stale, func() error {
		snapshot := workflow.MutationTarget{Scope: workflow.ScopeFromPresentation(wf.Presentation), Name: wf.Presentation.Name}
		_, err := q.retryHistorical(ctx, wf, snapshot, false)
		return err
	}), nil
}

// refreshStale re-arranges a pass reviewed under an older renderer contract,
// once, when it is viewed, and returns the refreshed view. It is a system
// action: rearrange skips user retry authorization but takes the same locks and
// revision checks as a Retry. Only a pass is stale, so a re-arrange that fails
// settles as ordinary needs-attention and is never queued again; a concurrent
// viewer's requeue simply wins.
func (q *QueryService) refreshStale(ctx context.Context, wf *workflow.Workflow, v View, stale bool, rearrange func() error) View {
	if !stale || q.requester == nil || queryNil(q.requester.store) || queryNil(q.mutations) {
		return v
	}
	rev := wf.SavedRevision()
	if err := rearrange(); err != nil {
		if !errors.Is(err, ErrRetryConflict) && !errors.Is(err, ErrRevisionConflict) {
			slog.Warn("layout: re-arranging a layout from an older renderer contract failed", "revision", rev, "error", err)
		}
	} else {
		slog.Info("layout: re-arranging a layout from an older renderer contract", "revision", rev)
	}
	return q.reader.View(ctx, wf)
}

// rearrangeWorkflow requeues the viewed revision under its resolved
// destination lock, provided the name still resolves to the same scope.
func (q *QueryService) rearrangeWorkflow(ctx context.Context, access WorkflowAccess, wf *workflow.Workflow, scope workflow.Scope, name string) error {
	return q.mutations.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: name}}, func(locked context.Context) error {
		current, resolved, err := access.LoadForLayout(locked, name)
		if err != nil {
			return err
		}
		if current == nil || resolved != scope {
			return ErrRevisionConflict
		}
		_, err = q.retry(locked, current, scope, name, wf.SavedRevision(), false)
		return err
	})
}

func checkExpected(wf *workflow.Workflow, expected string) error {
	if err := queryDefinition(wf); err != nil {
		return err
	}
	if wf.SavedRevision() == "" {
		return ErrLegacyRevision
	}
	if wf.SavedRevision() != expected {
		return ErrRevisionConflict
	}
	return nil
}

// RetryWorkflow linearizes with saves under the resolved destination lock. The
// second authorization prevents a stale token or changed scope from being retried.
func (q *QueryService) RetryWorkflow(ctx context.Context, name, expectedRevision string) (RetryAck, error) {
	var access WorkflowAccess
	if q != nil {
		access = q.workflows
	}
	if err := q.ready(access, true); err != nil {
		return RetryAck{}, err
	}
	wf, scope, err := access.AuthorizeLayoutRetry(ctx, name)
	if err != nil {
		return RetryAck{}, err
	}
	if wf == nil {
		return RetryAck{}, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	if err := checkExpected(wf, expectedRevision); err != nil {
		return RetryAck{}, err
	}
	if queryNil(q.mutations) {
		return RetryAck{}, fmt.Errorf("%w: mutation coordinator is unavailable", ErrStoreUnavailable)
	}
	targets, err := access.LayoutRetryTargets(ctx, name)
	if err != nil {
		return RetryAck{}, err
	}
	if !slices.Contains(targets, workflow.MutationTarget{Scope: scope, Name: name}) {
		return RetryAck{}, fmt.Errorf("layout: retry lock set omits resolved destination for %s", name)
	}
	var ack RetryAck
	err = q.mutations.WithMutations(ctx, targets, func(locked context.Context) error {
		current, resolved, err := access.AuthorizeLayoutRetry(locked, name)
		if err != nil {
			return err
		}
		if current == nil || resolved != scope {
			return ErrRevisionConflict
		}
		ack, err = q.retry(locked, current, scope, name, expectedRevision, false)
		return err
	})
	return ack, err
}

// historicalLockAttempts bounds re-locking while transfers keep moving the
// envelope; a destination that never settles is reported as a conflict.
const historicalLockAttempts = 3

var errHistoricalRelocated = errors.New("layout: historical envelope relocated before its lock was held")

// RetryInstance never requires the current workflow to exist or be visible. It
// locks the snapshot locator and the committed envelope's destination, then
// revalidates that destination: relocation moves both journal and rows under
// those locks, so an insert outside them could strand at a vacated locator.
func (q *QueryService) RetryInstance(ctx context.Context, id uuid.UUID, expectedRevision string) (RetryAck, error) {
	var access InstanceAccess
	if q != nil {
		access = q.instances
	}
	if err := q.ready(access, true); err != nil {
		return RetryAck{}, err
	}
	wf, err := access.AuthorizeInstanceLayoutRetry(ctx, id)
	if err != nil {
		return RetryAck{}, err
	}
	if err := checkExpected(wf, expectedRevision); err != nil {
		return RetryAck{}, err
	}
	if queryNil(q.mutations) {
		return RetryAck{}, fmt.Errorf("%w: mutation coordinator is unavailable", ErrStoreUnavailable)
	}
	snapshot := workflow.MutationTarget{Scope: workflow.ScopeFromPresentation(wf.Presentation), Name: wf.Presentation.Name}
	return q.retryHistorical(ctx, wf, snapshot, true)
}

// retryHistorical retries a snapshot revision under its locator and committed
// destination locks. authorize is false only for a system re-arrangement.
func (q *QueryService) retryHistorical(ctx context.Context, wf *workflow.Workflow, snapshot workflow.MutationTarget, authorize bool) (RetryAck, error) {
	if authorize {
		if err := workflow.AuthorizeRetryDestination(ctx, snapshot); err != nil {
			return RetryAck{}, err
		}
	}
	for range historicalLockAttempts {
		destination, err := q.historicalDestination(ctx, wf, snapshot)
		if err != nil {
			return RetryAck{}, err
		}
		targets := []workflow.MutationTarget{snapshot}
		if destination != snapshot {
			targets = append(targets, destination)
		}
		var ack RetryAck
		err = q.mutations.WithMutations(ctx, targets, func(locked context.Context) error {
			current, err := q.historicalDestination(locked, wf, snapshot)
			if err != nil {
				return err
			}
			if current != destination {
				return errHistoricalRelocated
			}
			if authorize {
				if err := workflow.AuthorizeRetryDestination(locked, current); err != nil {
					return err
				}
			}
			ack, err = q.retry(locked, wf, snapshot.Scope, snapshot.Name, wf.SavedRevision(), true)
			return err
		})
		if !errors.Is(err, errHistoricalRelocated) {
			return ack, err
		}
	}
	return RetryAck{}, fmt.Errorf("%w: historical destination for %s kept relocating", ErrRevisionConflict, wf.SavedRevision())
}

// committedEnvelope is the trusted request a committed save or relocation left
// for rev. Prepared input is not evidence of a committed move.
func (q *QueryService) committedEnvelope(ctx context.Context, rev string) (workflow.ArrangementRequest, bool, error) {
	if q.reader.journal == nil {
		return workflow.ArrangementRequest{}, false, nil
	}
	entries, err := q.reader.journal.Entries(ctx)
	if err != nil {
		return workflow.ArrangementRequest{}, false, err
	}
	for _, entry := range entries {
		if entry.State == workflow.JournalCommitted && entry.Request.Revision == rev {
			return entry.Request, true, nil
		}
	}
	return workflow.ArrangementRequest{}, false, nil
}

func (q *QueryService) historicalDestination(ctx context.Context, wf *workflow.Workflow, snapshot workflow.MutationTarget) (workflow.MutationTarget, error) {
	env, ok, err := q.committedEnvelope(ctx, wf.SavedRevision())
	if err != nil || !ok {
		return snapshot, err
	}
	return workflow.MutationTarget{Scope: env.Scope, Name: env.Name}, nil
}

func (q *QueryService) retry(ctx context.Context, wf *workflow.Workflow, scope workflow.Scope, name, expectedRevision string, historical bool) (RetryAck, error) {
	if err := checkExpected(wf, expectedRevision); err != nil {
		return RetryAck{}, err
	}
	rev := wf.SavedRevision()
	var expectedDestination *Destination
	if !historical {
		expectedDestination = &Destination{Scope: scope, Name: name, WorkflowID: wf.ID}
	}
	rec, err := q.requester.retry(ctx, rev, expectedDestination)
	if err == nil {
		return RetryAck{rev, rec.JobGeneration, rec.State, DisplayPending}, nil
	}
	if !errors.Is(err, ErrRecordNotFound) {
		return RetryAck{}, err
	}
	manifest, err := json.Marshal(wf)
	if err != nil {
		return RetryAck{}, fmt.Errorf("layout: encode manifest for %s: %w", rev, err)
	}
	// The recreated row is the revision's immutable acknowledgement: exact seed
	// resume (RevisionStamper.EnsureStamped) requires RequestedAt == SavedAt.
	requestedAt := wf.Presentation.SavedAt
	if requestedAt.IsZero() {
		requestedAt = q.now()
	}
	req := workflow.ArrangementRequest{
		Revision: rev, Scope: scope, Name: name, WorkflowID: wf.ID,
		Reason: wf.Presentation.Reason, Manifest: manifest, Retained: wf.Presentation.Retained, RequestedAt: requestedAt,
	}
	if historical {
		// Relocation changes the trusted request envelope, never its saved manifest.
		env, ok, err := q.committedEnvelope(ctx, rev)
		if err != nil {
			return RetryAck{}, err
		}
		if ok {
			req = env
		}
	}
	if err := q.requester.requestArrangement(ctx, req, expectedDestination); err != nil {
		return RetryAck{}, err
	}
	v := q.reader.View(ctx, wf)
	if !v.Available || v.JobGeneration < 1 {
		return RetryAck{}, fmt.Errorf("%w: retry queued but acknowledgement could not be read", ErrStoreUnavailable)
	}
	// A fast worker may already be terminal; the ack installs the accepted
	// generation, and polling obtains its authoritative state.
	return RetryAck{rev, v.JobGeneration, StatePending, DisplayPending}, nil
}
