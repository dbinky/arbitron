package layout

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/arbitron/arbitron/domain/workflow"
)

// DisplayStatus is the read-only user-facing state of a saved revision.
type DisplayStatus string

const (
	DisplayPending        DisplayStatus = "pending"
	DisplayArranging      DisplayStatus = "arranging"
	DisplayRendering      DisplayStatus = "rendering"
	DisplayReviewing      DisplayStatus = "reviewing"
	DisplayComplete       DisplayStatus = "complete"
	DisplayNeedsAttention DisplayStatus = "needs_attention"
	DisplayFailed         DisplayStatus = "failed"
	DisplayLegacy         DisplayStatus = "legacy"
	DisplayUnavailable    DisplayStatus = "unavailable"
)

// Provenance identifies the source of the displayed geometry or positions.
type Provenance string

const (
	ProvenanceArranged          Provenance = "arranged"
	ProvenanceRetainedGeometry  Provenance = "retained_geometry"
	ProvenanceRetainedPositions Provenance = "retained_positions"
	ProvenanceLegacy            Provenance = "legacy"
)

// View is the common workflow and frozen-instance layout response.
type View struct {
	SavedRevision     string                   `json:"savedRevision"`
	JobGeneration     int64                    `json:"jobGeneration"`
	ResultGeneration  int64                    `json:"resultGeneration"`
	State             State                    `json:"state,omitempty"`
	DisplayStatus     DisplayStatus            `json:"displayStatus"`
	Stage             Stage                    `json:"stage,omitempty"`
	AttemptsUsed      int                      `json:"attemptsUsed"`
	AttemptLimit      int                      `json:"attemptLimit"`
	Findings          []Finding                `json:"findings"`
	FailureStage      Stage                    `json:"failureStage,omitempty"`
	FailureReason     string                   `json:"failureReason,omitempty"`
	Evidence          *Evidence                `json:"evidence,omitempty"`
	Contract          *RendererContract        `json:"contract,omitempty"`
	Geometry          *Geometry                `json:"geometry,omitempty"`
	Groups            *workflow.GroupGraph     `json:"groups,omitempty"`
	Retained          *workflow.RetainedLayout `json:"retained,omitempty"`
	Provenance        Provenance               `json:"provenance"`
	Unreviewed        bool                     `json:"unreviewed"`
	RetryEligible     bool                     `json:"retryEligible"`
	Available         bool                     `json:"available"`
	AvailabilityError string                   `json:"availabilityError,omitempty"`
}

// Reader resolves immutable results, with explicitly unreviewed captured fallbacks.
type Reader struct {
	store   Store
	journal workflow.RevisionJournal
	cache   CacheReader
}

func NewReader(store Store, journal workflow.RevisionJournal, cache CacheReader) *Reader {
	return &Reader{store: store, journal: journal, cache: cache}
}

// retainedFor copies only coordinates belonging to the displayed manifest.
func retainedFor(wf *workflow.Workflow) workflow.RetainedLayout {
	out := workflow.RetainedLayout{Source: workflow.RetainedNone}
	if wf == nil {
		return out
	}
	if wf.Presentation != nil {
		out = wf.Presentation.Retained
	}
	positions := map[string]workflow.NodePosition{}
	for _, n := range wf.Nodes {
		if p, ok := out.Positions[n.ID]; ok {
			positions[n.ID] = p
		} else if wf.Presentation == nil && n.Position != nil {
			positions[n.ID] = *n.Position
			out.Source = workflow.RetainedFromManifest
		}
	}
	out.Positions = positions
	return out
}
func (r *Reader) fallback(ctx context.Context, wf *workflow.Workflow, v *View) {
	retained := retainedFor(wf)
	v.Retained = &retained
	v.Unreviewed = true
	v.Provenance = ProvenanceRetainedPositions
	if retained.Source != workflow.RetainedFromLayout || retained.FromRevision == "" || retained.FromGeneration < 1 {
		return
	}
	prior, err := r.store.Result(ctx, retained.FromRevision, retained.FromGeneration)
	if err != nil {
		return
	}
	gr, err := BuildGraph(wf)
	if err != nil || prior.Revision != retained.FromRevision || prior.Generation != retained.FromGeneration || !Compatible(prior.Geometry, gr) {
		return
	}
	v.Geometry = &prior.Geometry
	c := prior.Geometry.Contract
	v.Contract = &c
	v.Provenance = ProvenanceRetainedGeometry
}
func displayFor(row Record) DisplayStatus {
	switch row.State {
	case StatePending:
		return DisplayPending
	case StateInProgress:
		switch row.Stage {
		case StageRendering:
			return DisplayRendering
		case StageReviewing, StagePublishing:
			return DisplayReviewing
		default:
			return DisplayArranging
		}
	case StatePassed:
		return DisplayComplete
	case StateNeedsAttention:
		return DisplayNeedsAttention
	case StateFailed:
		return DisplayFailed
	case StateSuperseded:
		if row.Verdict == VerdictPassed {
			return DisplayComplete
		}
		return DisplayNeedsAttention
	default:
		return DisplayUnavailable
	}
}
func rejectView(v *View, reason string, unavailable bool) {
	// A readable active row owns the status even if its retained best is
	// unusable. A missing row has no generation and still needs recovery Retry.
	active := v.JobGeneration > 0 && (v.State == StatePending || v.State == StateInProgress)
	if !active {
		v.DisplayStatus = DisplayNeedsAttention
		if unavailable {
			v.DisplayStatus = DisplayUnavailable
		}
	}
	v.Available = false
	v.FailureReason = reason
	v.AvailabilityError = reason
	v.RetryEligible = !active
	v.Evidence = nil
}

// cachedFallback is allowed only on an outage, never to hide corrupt authoritative data.
func (r *Reader) cachedFallback(ctx context.Context, wf *workflow.Workflow, v *View) {
	if r.cache == nil || wf.Presentation == nil {
		return
	}
	e, ok, err := r.cache.ReadCache(ctx, workflow.ScopeFromPresentation(wf.Presentation), wf.Presentation.Name)
	gr, graphErr := BuildGraph(wf)
	if err != nil || !ok || graphErr != nil || e.Format != CacheFormatV1 || e.Revision != v.SavedRevision || e.Generation < 1 || e.JobGeneration < 1 || e.Contract != DefaultContract() || e.Contract != e.Geometry.Contract || !Compatible(e.Geometry, gr) {
		return
	}
	// When the row is readable, its exact fences are authoritative even during a result outage.
	if v.JobGeneration > 0 && (v.JobGeneration != e.JobGeneration || v.ResultGeneration != e.Generation) {
		return
	}
	v.Geometry = &e.Geometry
	c := e.Contract
	v.Contract = &c
	v.Provenance = ProvenanceArranged
	v.JobGeneration = e.JobGeneration
	v.ResultGeneration = e.Generation
	v.Unreviewed = true
	v.AvailabilityError = "status store unavailable; showing the last published layout for this revision"
}

// View reads immutable revision history only; it never repairs, requests or invalidates work.
func (r *Reader) View(ctx context.Context, wf *workflow.Workflow) View {
	v, _ := r.view(ctx, wf)
	return v
}

// view also reports a pass reviewed under a previous renderer contract, which
// the query service re-arranges; the View itself never writes.
func (r *Reader) view(ctx context.Context, wf *workflow.Workflow) (v View, stale bool) {
	v = View{SavedRevision: wf.SavedRevision(), DisplayStatus: DisplayLegacy, Provenance: ProvenanceLegacy, Unreviewed: true, Available: true, Findings: []Finding{}}
	graph, graphErr := BuildGraph(wf)
	v.Groups = graph.Groups
	// Ownership comes from this exact authorized input even before a result
	// exists or while the store is unavailable. Invalid historical groups remain
	// inspectable, but neither a stale result nor a missing row can bless them.
	if errors.Is(graphErr, ErrInvalidGroups) {
		defer func() {
			active := v.JobGeneration > 0 && (v.State == StatePending || v.State == StateInProgress)
			if !active {
				v.DisplayStatus = DisplayFailed
			}
			v.FailureStage = StageArranging
			v.FailureReason = graphErr.Error()
			v.Geometry, v.Contract, v.Evidence = nil, nil, nil
			v.Unreviewed = true
			v.RetryEligible = v.SavedRevision != "" && !active
		}()
	}
	if v.SavedRevision == "" {
		retained := retainedFor(wf)
		v.Retained = &retained
		return v, stale
	}
	r.fallback(ctx, wf, &v)
	row, err := r.store.Get(ctx, v.SavedRevision)
	if errors.Is(err, ErrRecordNotFound) {
		v.State = StatePending
		v.Stage = StageQueued
		v.DisplayStatus = DisplayPending
		v.RetryEligible = true
		v.AvailabilityError = "awaiting recovery"
		if r.journal != nil {
			entries, jerr := r.journal.List(ctx)
			if jerr != nil {
				rejectView(&v, storeReason(v.SavedRevision, "revision journal", jerr), true)
				return v, stale
			}
			for _, entry := range entries {
				if entry.Revision == v.SavedRevision {
					v.AvailabilityError = ""
					break
				}
			}
		}
		return v, stale
	}
	if err != nil {
		rejectView(&v, storeReason(v.SavedRevision, "stored status", err), !errors.Is(err, ErrStoreCorrupt))
		if errors.Is(err, ErrStoreUnavailable) {
			r.cachedFallback(ctx, wf, &v)
		}
		return v, stale
	}
	v.JobGeneration = row.JobGeneration
	v.ResultGeneration = row.ResultGeneration
	v.State = row.State
	v.Stage = row.Stage
	v.AttemptsUsed = row.AttemptsUsed
	v.AttemptLimit = row.AttemptLimit
	v.FailureStage = row.FailureStage
	v.FailureReason = row.FailureReason
	v.Findings = StoredResult{Preflight: row.Findings}.UnresolvedFindings()
	v.DisplayStatus = displayFor(row)
	v.RetryEligible = row.State == StateNeedsAttention || row.State == StateFailed || row.State == StateSuperseded && row.Verdict != VerdictPassed
	if row.Revision != v.SavedRevision {
		rejectView(&v, "stored row identity does not match revision", false)
		return v, stale
	}
	if row.ResultGeneration < 1 {
		if row.State == StatePassed || row.State == StateSuperseded {
			rejectView(&v, "stored revision has no reviewed result", false)
		}
		return v, stale
	}
	result, err := r.store.Result(ctx, v.SavedRevision, row.ResultGeneration)
	if err != nil {
		rejectView(&v, storeReason(v.SavedRevision, "stored result", err), errors.Is(err, ErrStoreUnavailable))
		if errors.Is(err, ErrStoreUnavailable) {
			r.cachedFallback(ctx, wf, &v)
		}
		return v, stale
	}
	if graphErr == nil && result.Revision == v.SavedRevision && result.Generation == row.ResultGeneration && contractStale(result.Geometry, graph) {
		return v, retainStale(&v, row, result)
	}
	if graphErr != nil || result.Revision != v.SavedRevision || result.Generation != row.ResultGeneration || !Compatible(result.Geometry, graph) {
		rejectView(&v, "stored geometry is incompatible with this revision", false)
		return v, stale
	}
	v.Geometry = &result.Geometry
	c := result.Geometry.Contract
	v.Contract = &c
	v.Provenance = ProvenanceArranged
	v.Findings = result.UnresolvedFindings()
	v.Unreviewed = result.IsUnreviewed()
	if !v.Unreviewed {
		evidence := result.Review.Evidence.ForViewer()
		v.Evidence = &evidence
	}
	if row.State == StatePassed || row.State == StateSuperseded && row.Verdict == VerdictPassed {
		problem := passProblem(wf, result)
		if row.Verdict != VerdictPassed {
			problem = "stored verdict does not support a pass"
		}
		if problem != "" {
			rejectView(&v, problem, false)
		}
	}
	return v, stale
}

// contractStale is geometry that is valid for its graph but was arranged and
// reviewed under a previous renderer contract of this product. Any other
// contract is not stale but incompatible.
func contractStale(g Geometry, gr Graph) bool {
	return slices.Contains(PreviousContracts(), g.Contract) && g.Validate(gr) == nil
}

// retainStale shows a result from an older renderer contract as unreviewed
// retained geometry, drawn with the contract it was reviewed under. It is not
// this build's arrangement, so a stale pass is flagged for re-arrangement and
// is not reported complete; while that job runs the old layout stays visible.
func retainStale(v *View, row Record, result StoredResult) bool {
	v.Geometry = &result.Geometry
	c := result.Geometry.Contract
	v.Contract = &c
	v.Provenance = ProvenanceRetainedGeometry
	v.Unreviewed = true
	v.Evidence = nil
	if row.State == StatePassed || row.State == StateSuperseded && row.Verdict == VerdictPassed {
		rejectView(v, "stored layout predates the current renderer contract", false)
		return true
	}
	return false
}

// storeReason is the fixed, viewer-safe category for a storage failure; the
// underlying error can carry SQLite or filesystem detail, so it is logged only.
func storeReason(revision, subject string, err error) string {
	slog.Error("layout view storage failure", "revision", revision, "subject", subject, "error", err)
	switch {
	case errors.Is(err, ErrStoreCorrupt):
		return subject + " is corrupt"
	case errors.Is(err, ErrRecordNotFound):
		return subject + " is missing"
	default:
		return subject + " unavailable"
	}
}

func IsActive(s DisplayStatus) bool {
	return s == DisplayPending || s == DisplayArranging || s == DisplayRendering || s == DisplayReviewing
}
