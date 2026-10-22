package layout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
)

// Stage names where a job is (or failed).
type Stage string

const (
	StageQueued     Stage = "queued"
	StageArranging  Stage = "arranging"
	StageRendering  Stage = "rendering"
	StageReviewing  Stage = "reviewing"
	StagePublishing Stage = "publishing"
	StageDone       Stage = "done"
)

// Verdict is the outcome of a run.
type Verdict string

const (
	// VerdictPassed: the returned candidate passed actual rendered review.
	VerdictPassed Verdict = "passed"
	// VerdictNeedsAttention: the budget (or every possible refinement) was
	// exhausted without a pass; Best and Findings describe what remains.
	VerdictNeedsAttention Verdict = "needs_attention"
	// VerdictFailed: a stage errored or the run was cancelled; Best (if any)
	// is the best candidate completed so far.
	VerdictFailed Verdict = "failed"
)

// Job is one immutable saved revision to arrange.
type Job struct {
	Revision    string
	Manifest    *workflow.Workflow
	MaxAttempts int
}

// Progress reports the stage of an attempt as it starts.
type Progress struct {
	Stage   Stage
	Attempt int
}

// Candidate is one attempt's geometry with its own preflight findings and
// rendered review.
type Candidate struct {
	Attempt   int       `json:"attempt"`
	Geometry  Geometry  `json:"geometry"`
	Preflight []Finding `json:"preflight"`
	Review    *Review   `json:"review,omitempty"`
	Score     Score     `json:"score"`
	Passed    bool      `json:"passed"`
}

// Failure records the failing stage and reason.
type Failure struct {
	Stage  Stage  `json:"stage"`
	Reason string `json:"reason"`
}

// Result is the outcome of Run.
type Result struct {
	Verdict         Verdict
	Best            *Candidate
	Findings        []Finding
	AttemptsUsed    int
	Failure         *Failure
	Cancelled       bool
	Unreviewed      bool
	ExhaustedReason string
}

var (
	ErrNilRenderer = errors.New("layout: renderer is required")
	ErrNilReviewer = errors.New("layout: reviewer is required")
)

// Coordinator runs bounded arrange → preflight → render → review attempts.
type Coordinator struct {
	renderer     Renderer
	reviewer     Reviewer
	contract     RendererContract
	options      Options
	stageTimeout time.Duration
}

// CoordinatorOption customises a Coordinator.
type CoordinatorOption func(*Coordinator)

// WithContract overrides the renderer contract (default DefaultContract()).
func WithContract(c RendererContract) CoordinatorOption {
	return func(co *Coordinator) { co.contract = c }
}

// WithOptions overrides attempt 1's options (default DefaultOptions()).
func WithOptions(o Options) CoordinatorOption {
	return func(co *Coordinator) { co.options = o }
}

const defaultStageTimeout = 2 * time.Minute

// WithStageTimeout bounds each Measure, Arrange, Render and Review call separately.
func WithStageTimeout(d time.Duration) CoordinatorOption {
	return func(co *Coordinator) {
		if d > 0 {
			co.stageTimeout = d
		} else {
			co.stageTimeout = defaultStageTimeout
		}
	}
}

// NewCoordinator requires both production ports; there is no default that
// could silently skip rendered review.
func NewCoordinator(renderer Renderer, reviewer Reviewer, opts ...CoordinatorOption) (*Coordinator, error) {
	if renderer == nil {
		return nil, ErrNilRenderer
	}
	if reviewer == nil {
		return nil, ErrNilReviewer
	}
	c := &Coordinator{renderer: renderer, reviewer: reviewer, contract: DefaultContract(), options: DefaultOptions(), stageTimeout: defaultStageTimeout}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// ErrRendererUnavailable and ErrRendererCrashed are the operator-actionable
// categories a renderer wraps when no usable browser exists, or when the one
// it launched died mid-job; they are the only adapter detail a persisted
// failure reason names.
var (
	ErrRendererUnavailable = errors.New("no Chrome/Chromium browser available")
	ErrRendererCrashed     = errors.New("browser exited")
)

// adapterReason is the fixed, viewer-safe failure reason for a Measure, Render
// or Review error. Adapter errors can carry browser paths, candidate lists or
// artifact locations, and the reason is persisted and served to every viewer,
// so the detail is logged server-side only.
func adapterReason(revision string, attempt int, what string, err error) string {
	slog.Error("layout stage failed", "revision", revision, "attempt", attempt, "stage", what, "error", err)
	switch {
	case errors.Is(err, ErrRendererUnavailable):
		return what + " failed: " + ErrRendererUnavailable.Error()
	case errors.Is(err, ErrRendererCrashed):
		return what + " failed: " + ErrRendererCrashed.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return what + " timed out"
	case errors.Is(err, context.Canceled):
		return what + " was cancelled"
	default:
		return what + " failed"
	}
}

// Run arranges, renders and reviews job.Manifest within job.MaxAttempts total
// attempts (at least one). progress may be nil.
func (c *Coordinator) Run(ctx context.Context, job Job, progress func(Progress)) Result {
	if progress == nil {
		progress = func(Progress) {}
	}
	budget := max(job.MaxAttempts, 1)
	if err := ctx.Err(); err != nil {
		return Result{Verdict: VerdictFailed, Cancelled: true, Failure: &Failure{Stage: StageArranging, Reason: err.Error()}}
	}
	if job.Revision == "" {
		return Result{Verdict: VerdictFailed, Failure: &Failure{Stage: StageArranging, Reason: "missing saved revision"}}
	}
	g, err := BuildGraph(job.Manifest)
	if err == nil {
		err = checkArrangeBounds(g) // fail over-cap graphs before measuring
	}
	if err != nil {
		return Result{Verdict: VerdictFailed, Failure: &Failure{Stage: StageArranging, Reason: err.Error()}}
	}

	nodeTypes := make(map[string]string, len(job.Manifest.Nodes))
	for _, n := range job.Manifest.Nodes {
		nodeTypes[n.ID] = string(n.Type)
	}
	var best *Candidate
	// captures holds the rendering artifacts of each reviewed candidate still
	// competing for best; losers are released as soon as they are discarded.
	// (An unreviewed candidate never displaces a reviewed one, so the error
	// paths below only release their own rendering.)
	captures := map[*Candidate][]Artifact{}
	keep := func(cand *Candidate) {
		prev := best
		best = better(best, cand)
		for _, loser := range []*Candidate{prev, cand} {
			if loser != nil && loser != best {
				_ = releaseArtifacts(ctx, c.renderer, captures[loser])
				delete(captures, loser)
			}
		}
	}
	finish := func(r Result) Result {
		r.Best = best
		if best != nil {
			r.Findings = candidateFindings(best)
			r.Unreviewed = !HasRenderedEvidence(best.Geometry, best.Review)
		}
		return r
	}
	fail := func(attempt int, stage Stage, reason string) Result {
		r := Result{Verdict: VerdictFailed, AttemptsUsed: attempt, Failure: &Failure{Stage: stage, Reason: reason}}
		if ctx.Err() != nil {
			r.Cancelled = true
			r.Failure.Reason = "cancelled: " + reason
		}
		return finish(r)
	}

	opts := c.options
	var m Measurements
	for attempt := 1; attempt <= budget; attempt++ {
		if err := ctx.Err(); err != nil {
			return finish(Result{Verdict: VerdictFailed, AttemptsUsed: attempt - 1, Cancelled: true,
				Failure: &Failure{Stage: StageArranging, Reason: "cancelled: " + err.Error()}})
		}
		progress(Progress{Stage: StageArranging, Attempt: attempt})
		if err := ctx.Err(); err != nil {
			return fail(attempt, StageArranging, err.Error())
		}
		if attempt == 1 {
			measureCtx, cancel := context.WithTimeout(ctx, c.stageTimeout)
			measureReq := MeasureRequestFor(g, c.contract)
			m, err = c.renderer.Measure(measureCtx, measureReq)
			if err == nil {
				err = measureCtx.Err()
			}
			cancel()
			if err != nil {
				return fail(attempt, StageRendering, adapterReason(job.Revision, attempt, "measuring text", err))
			}
			if m.Contract != c.contract || !m.FontsReady {
				return fail(attempt, StageRendering, "measurement contract mismatch or fonts not ready")
			}
			for _, item := range measureReq.Items {
				fontPx := c.contract.NodeLabelFontPx
				if item.Style == StyleNodeType {
					fontPx = c.contract.NodeTypeFontPx
				}
				if item.Style == StyleEdgeLabel {
					fontPx = c.contract.EdgeLabelFontPx
				}
				_, metricErr := metricForText(m, item.Key, item.Text, fontPx, c.contract.LineHeight, item.MaxWidth)
				if metricErr != nil {
					return fail(attempt, StageRendering, metricErr.Error())
				}
			}
		}
		arrangeCtx, cancelArrange := context.WithTimeout(ctx, c.stageTimeout)
		geo, err := Arrange(arrangeCtx, g, m, c.contract, opts)
		cancelArrange()
		if err != nil {
			return fail(attempt, StageArranging, err.Error())
		}
		if err := geo.Validate(g); err != nil {
			return fail(attempt, StageArranging, err.Error())
		}
		cand := &Candidate{Attempt: attempt, Geometry: geo, Preflight: Preflight(g, geo, m)}
		cand.Score = unreviewedScore(cand)

		progress(Progress{Stage: StageRendering, Attempt: attempt})
		if err := ctx.Err(); err != nil {
			best = better(best, cand)
			return fail(attempt, StageRendering, err.Error())
		}
		renderReq := NewRenderRequest(job.Revision, attempt, geo, nodeTypes)
		renderReq.WorkflowGroups, err = cloneGroupGraph(g.Groups)
		if err != nil {
			return fail(attempt, StageRendering, err.Error())
		}
		renderCtx, cancelRender := context.WithTimeout(ctx, c.stageTimeout)
		rendering, err := c.renderer.Render(renderCtx, renderReq)
		if err == nil {
			err = renderCtx.Err()
		}
		cancelRender()
		if err != nil {
			// A render that finished as its context expired still wrote a capture.
			_ = releaseArtifacts(ctx, c.renderer, rendering.Artifacts)
			best = better(best, cand)
			return fail(attempt, StageRendering, adapterReason(job.Revision, attempt, "rendering", err))
		}

		progress(Progress{Stage: StageReviewing, Attempt: attempt})
		if err := ctx.Err(); err != nil {
			_ = releaseArtifacts(ctx, c.renderer, rendering.Artifacts)
			best = better(best, cand)
			return fail(attempt, StageReviewing, err.Error())
		}
		reviewReq := ReviewRequest{Revision: job.Revision, Attempt: attempt, Geometry: geo, Rendering: rendering}
		reviewReq.WorkflowGroups, err = cloneGroupGraph(g.Groups)
		if err != nil {
			_ = releaseArtifacts(ctx, c.renderer, rendering.Artifacts)
			return fail(attempt, StageReviewing, err.Error())
		}
		reviewCtx, cancelReview := context.WithTimeout(ctx, c.stageTimeout)
		review, err := c.reviewer.Review(reviewCtx, reviewReq)
		if err == nil {
			err = reviewCtx.Err()
		}
		cancelReview()
		if err != nil {
			_ = releaseArtifacts(ctx, c.renderer, rendering.Artifacts)
			best = better(best, cand)
			return fail(attempt, StageReviewing, adapterReason(job.Revision, attempt, "rendered review", err))
		}
		problem := evidenceProblem(geo, review)
		if issue := reviewFindingsProblem(g, review.Findings); issue != "" {
			problem = issue
			review.Rendered = false
		}
		if !slices.Equal(review.Evidence.Artifacts, rendering.Artifacts) {
			problem = "review evidence does not cite exactly this rendering's artifacts"
			review.Rendered = false
		}
		if rendering.Revision != job.Revision || rendering.Attempt != attempt || rendering.GeometryDigest != GeometryDigest(geo) || rendering.Report.Contract != geo.Contract || rendering.Report.SurfaceVersion != geo.Contract.Version || !rendering.Report.FontsReady || !rendering.Report.FontFamilyResolved || rendering.Report.Zoom != geo.Contract.PracticalZoom || rendering.RenderedAt.IsZero() || review.Evidence.ReviewedAt.Before(rendering.RenderedAt) {
			problem = "rendering metadata does not bind this revision/attempt/candidate and renderer"
			review.Rendered = false
		}
		if problem != "" {
			review.Findings = append(review.Findings, Finding{Criterion: CriterionRenderMismatch, Severity: SeverityBlocking, Source: SourceReview, Evidence: problem})
			// A rejected rendering can never be evidence: release it now.
			_ = releaseArtifacts(ctx, c.renderer, rendering.Artifacts)
		} else {
			captures[cand] = rendering.Artifacts
		}
		cand.Review = &review
		all := candidateFindings(cand)
		cand.Score = ScoreOf(attempt, all, CrossingCount(geo))
		cand.Passed = HasRenderedEvidence(geo, &review) && BlockingCount(all) == 0
		if cand.Passed {
			if best != nil {
				_ = releaseArtifacts(ctx, c.renderer, captures[best])
			}
			best = cand
			return finish(Result{Verdict: VerdictPassed, AttemptsUsed: attempt})
		}
		keep(cand)
		if attempt == budget {
			break
		}
		next, changed := Refine(opts, all)
		if !changed {
			return finish(Result{Verdict: VerdictNeedsAttention, AttemptsUsed: attempt, ExhaustedReason: "no_refinement"})
		}
		opts = next
	}
	return finish(Result{Verdict: VerdictNeedsAttention, AttemptsUsed: budget, ExhaustedReason: "budget"})
}

// evidenceProblem explains why a review cannot count as rendered evidence for
// geo, or returns "".
func evidenceProblem(geo Geometry, r Review) string {
	switch {
	case !r.Rendered:
		return "rendered review did not examine a complete rendering"
	case r.Evidence.GeometryDigest != GeometryDigest(geo):
		return "review evidence belongs to a different candidate"
	case r.Evidence.Contract != geo.Contract:
		return fmt.Sprintf("reviewed with renderer contract %q, candidate uses %q", r.Evidence.Contract.Version, geo.Contract.Version)
	case !r.Evidence.FontsReady:
		return "renderer fonts were not ready during review"
	case !HasRenderedEvidence(geo, &r):
		return "renderer version, zoom or complete readable artifact evidence is missing or invalid"
	}
	return ""
}

func candidateFindings(c *Candidate) []Finding {
	out := append([]Finding{}, c.Preflight...)
	if c.Review != nil {
		out = append(out, c.Review.Findings...)
	}
	return out
}

// unreviewedScore scores a candidate whose rendering or review failed from its
// preflight findings alone.
func unreviewedScore(c *Candidate) Score {
	return ScoreOf(c.Attempt, c.Preflight, CrossingCount(c.Geometry))
}

// better keeps the best candidate: any candidate with a rendered review ranks
// above every unreviewed one (geometry alone is never evidence); otherwise the
// lexicographic Score decides.
func better(best, cand *Candidate) *Candidate {
	if best == nil {
		return cand
	}
	if reviewedB, reviewedC := HasRenderedEvidence(best.Geometry, best.Review), HasRenderedEvidence(cand.Geometry, cand.Review); reviewedB != reviewedC {
		if reviewedC {
			return cand
		}
		return best
	}
	if cand.Score.Less(best.Score) {
		return cand
	}
	return best
}

// HasRenderedEvidence checks the exact candidate's evidence envelope. Opening,
// hashing and inspecting screenshot pixels is the production reviewer's job.
func HasRenderedEvidence(geo Geometry, review *Review) bool {
	if review == nil || !review.Rendered || !positiveRect(geo.Canvas) || geo.Canvas.X != 0 || geo.Canvas.Y != 0 {
		return false
	}
	digest := GeometryDigest(geo)
	ev := review.Evidence
	if !validSHA256(digest) || ev.GeometryDigest != digest || ev.Contract != geo.Contract || ev.RendererVersion != geo.Contract.Version || !ev.FontsReady || !finite(ev.Zoom) || ev.Zoom <= 0 || ev.Zoom != geo.Contract.PracticalZoom || ev.ReviewedAt.IsZero() {
		return false
	}
	overview := false
	var tiles []Rect
	for _, a := range ev.Artifacts {
		if a.Path == "" || !validSHA256(a.SHA256) || !positiveRect(a.Region) || !geo.Canvas.Contains(a.Region) || !finite(a.Scale) || a.Scale <= 0 {
			return false
		}
		switch a.Kind {
		case "overview":
			if a.Region != geo.Canvas || a.Scale > ev.Zoom {
				return false
			}
			overview = true
		case "tile":
			if a.Scale != ev.Zoom {
				return false
			}
			tiles = append(tiles, a.Region)
		default:
			return false
		}
	}
	return overview && len(tiles) > 0 && ev.Tiles == len(tiles) && tileUnionCovers(geo.Canvas, tiles)
}

func validSHA256(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// tileUnionCovers sweeps every tile boundary. In each nonzero X strip, the
// merged vertical intervals must cover the full canvas, not just its corners.
func tileUnionCovers(canvas Rect, tiles []Rect) bool {
	xs := []float64{canvas.X, canvas.Right()}
	for _, tile := range tiles {
		xs = append(xs, tile.X, tile.Right())
	}
	sort.Float64s(xs)
	for i := 1; i < len(xs); i++ {
		left, right := max(xs[i-1], canvas.X), min(xs[i], canvas.Right())
		if right <= left {
			continue
		}
		var intervals []Rect
		for _, tile := range tiles {
			if tile.X <= left && tile.Right() >= right {
				intervals = append(intervals, tile)
			}
		}
		sort.Slice(intervals, func(i, j int) bool { return intervals[i].Y < intervals[j].Y })
		bottom := canvas.Y
		for _, tile := range intervals {
			if tile.Y > bottom+geomEps {
				return false
			}
			bottom = max(bottom, tile.Bottom())
		}
		if bottom < canvas.Bottom()-geomEps {
			return false
		}
	}
	return true
}

// reviewFindingsProblem validates external reviewer output before it can affect
// ranking or pass eligibility. Unknown severity must not evade BlockingCount.
func reviewFindingsProblem(g Graph, fs []Finding) string {
	for i, f := range fs {
		invalid := func(reason string) string { return fmt.Sprintf("review finding %d: %s", i, reason) }
		switch f.Criterion {
		case CriterionCoverage, CriterionNodeOverlap, CriterionEdgeThroughNode, CriterionTextCollision, CriterionTextClipped, CriterionTextUnreadable, CriterionMergedLines, CriterionAmbiguousAttachment, CriterionArrowDirection, CriterionAmbiguousCrossing, CriterionUpwardProgression, CriterionIndistinctLoop, CriterionIndistinctBranch, CriterionRenderMismatch, CriterionCrossings:
		default:
			return invalid("unknown criterion " + string(f.Criterion))
		}
		if f.Severity != SeverityBlocking && f.Severity != SeverityAdvisory {
			return invalid("unknown severity " + string(f.Severity))
		}
		if f.Criterion == CriterionCrossings && f.Severity != SeverityAdvisory {
			return invalid("crossings must be advisory")
		}
		if f.Source != SourceReview {
			return invalid("source must be review")
		}
		if f.Evidence == "" {
			return invalid("missing evidence")
		}
		for _, id := range f.NodeIDs {
			if _, ok := g.NodeIndex(id); !ok {
				return invalid("unknown node " + id)
			}
		}
		for _, ordinal := range f.EdgeOrdinals {
			if ordinal < 0 || ordinal >= len(g.Edges) {
				return invalid(fmt.Sprintf("unknown edge ordinal %d", ordinal))
			}
		}
	}
	return ""
}
