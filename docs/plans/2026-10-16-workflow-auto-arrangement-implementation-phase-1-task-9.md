# Workflow Auto-Arrangement — Phase 1 Task 9: Bounded arrange → render → review attempt coordinator

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Alignment requirements:** Read contracts §6 (6.4/6.5) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Run up to the configured number of total attempts for one saved revision — arrange a candidate, preflight it, render it with the production renderer, review the rendering, refine from findings — and return the best candidate with **its own** evidence and findings, a truthful verdict (`passed` only after a successful rendered review of that exact candidate), attempts used, and a separate failure/cancellation indication.

**Architecture:** `domain/layout/coordinator.go`. `Coordinator` holds the `Renderer` and `Reviewer` ports plus contract/options; `Run` is synchronous and context-driven (the worker in P2T8 owns concurrency, deadlines and persistence). Attempts are counted when arranging starts; measuring happens once at the start of attempt 1. Every produced candidate — including ones failing preflight — is rendered and reviewed. Candidates are ranked by `Score.Less`, except that any candidate with a rendered review ranks above every unreviewed one. Stops on pass, budget, cancellation, a stage error, or `Refine` reporting no possible change (early `needs_attention`).

**Tech Stack:** Go 1.25.5 (`context`, `errors`), testify.

**Spec / Design:** spec "Bound refinement" (default five **total** attempts), "Handle incomplete results", "A result that exhausts its attempt limit without meeting the criteria is marked as needing attention"; `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` §4 (stop conditions, counting, failure retains best-so-far and states whether it is unreviewed, worker must not turn it into passed), §3 pass rule.

---

### Task 9.1: `Coordinator.Run`

**Files:**
- Create: `domain/layout/coordinator.go`
- Test:   `domain/layout/coordinator_test.go`

**Interfaces:**
- Consumes: `BuildGraph` (P1T1); `GeometryDigest`, `RendererContract`, `DefaultContract` (P1T2); `MeasureRequestFor`, `Measurements`, `metricForText`, `Options`, `DefaultOptions` (P1T4); `Arrange` (P1T6); `Preflight`, `Finding`, `BlockingCount`, `Score`, `ScoreOf` (P1T7); `Renderer`, `Reviewer`, `NewRenderRequest`, `ReviewRequest`, `Review`, `Rendering`, `RenderReport`, `Refine` (P1T8); `workflow.Workflow`.
- Produces (exact, contracts §1):
  ```go
  type Stage string; const StageQueued, StageArranging, StageRendering, StageReviewing, StagePublishing, StageDone
  type Verdict string; const VerdictPassed, VerdictNeedsAttention, VerdictFailed
  type Job struct { Revision string; Manifest *workflow.Workflow; MaxAttempts int }
  type Progress struct { Stage Stage; Attempt int }
  type Candidate struct { Attempt int; Geometry Geometry; Preflight []Finding; Review *Review; Score Score; Passed bool } // JSON tags in contracts
  type Failure struct { Stage Stage; Reason string }
  type Result struct { Verdict Verdict; Best *Candidate; Findings []Finding; AttemptsUsed int; Failure *Failure; Cancelled bool; Unreviewed bool; ExhaustedReason string }
  type CoordinatorOption func(*Coordinator)
  func WithContract(c RendererContract) CoordinatorOption
  func WithOptions(o Options) CoordinatorOption
  func WithStageTimeout(d time.Duration) CoordinatorOption
  func HasRenderedEvidence(geo Geometry, review *Review) bool
  var ErrNilRenderer, ErrNilReviewer error
  func NewCoordinator(renderer Renderer, reviewer Reviewer, opts ...CoordinatorOption) (*Coordinator, error)
  func (c *Coordinator) Run(ctx context.Context, job Job, progress func(Progress)) Result
  ```

- [x] **Step 1: Write the failing test**

Create `domain/layout/coordinator_test.go`:

```go
package layout

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedRenderer measures with fakeMeasurer and "renders" by echoing the
// request's geometry digest and contract — it honours the load-bearing
// argument (the candidate geometry) so evidence can be checked against it.
type scriptedRenderer struct {
	mu         sync.Mutex
	measurer   *fakeMeasurer
	measureErr error
	renderErr  map[int]error // by attempt
	block      bool          // Render waits for ctx cancellation
	rendered   []string      // geometry digests in call order
	options    []Options     // options of each rendered candidate
}

func newScriptedRenderer() *scriptedRenderer {
	return &scriptedRenderer{measurer: newFakeMeasurer(), renderErr: map[int]error{}}
}

func (r *scriptedRenderer) Measure(ctx context.Context, req MeasureRequest) (Measurements, error) {
	if r.measureErr != nil {
		return Measurements{}, r.measureErr
	}
	return r.measurer.Measure(ctx, req)
}

func (r *scriptedRenderer) Render(ctx context.Context, req RenderRequest) (Rendering, error) {
	r.mu.Lock()
	r.rendered = append(r.rendered, GeometryDigest(req.Geometry))
	r.options = append(r.options, req.Geometry.Options)
	err := r.renderErr[req.Attempt]
	block := r.block
	r.mu.Unlock()
	if block {
		<-ctx.Done()
		return Rendering{}, ctx.Err()
	}
	if err != nil {
		return Rendering{}, err
	}
	return Rendering{
		Revision: req.Revision, Attempt: req.Attempt, GeometryDigest: GeometryDigest(req.Geometry),
		Artifacts: []Artifact{
			{Kind: "overview", Path: "/unit/"+GeometryDigest(req.Geometry)+"-overview.png", SHA256: GeometryDigest(req.Geometry), Region: req.Geometry.Canvas, Scale: req.Geometry.Contract.PracticalZoom},
			{Kind: "tile", Path: "/unit/"+GeometryDigest(req.Geometry)+"-tile.png", SHA256: GeometryDigest(req.Geometry), Region: req.Geometry.Canvas, Scale: req.Geometry.Contract.PracticalZoom},
		},
		Report: unitReport(req.Geometry),
		RenderedAt: time.Unix(0, 0).UTC(),
	}, nil
}

type reviewStep struct {
	findings    []Finding
	err         error
	notRendered bool
	wrongDigest bool
	fontsLate   bool
}

// scriptedReviewer returns per-attempt scripted findings and copies evidence
// from the rendering it was given (so a mismatch is only produced on purpose).
type scriptedReviewer struct {
	steps    map[int]reviewStep
	fallback reviewStep
	seen     []ReviewRequest
}

func (r *scriptedReviewer) Review(ctx context.Context, req ReviewRequest) (Review, error) {
	if err := ctx.Err(); err != nil { return Review{}, err }
	if req.Revision == "" || req.Rendering.Revision != req.Revision || req.Rendering.Attempt != req.Attempt || req.Rendering.GeometryDigest != GeometryDigest(req.Geometry) {
		return Review{Rendered: false, Findings: []Finding{{Criterion: CriterionRenderMismatch, Severity: SeverityBlocking, Source: SourceReview, Evidence: "unit rendering request binding mismatch"}}}, nil
	}
	r.seen = append(r.seen, req)
	step, ok := r.steps[req.Attempt]
	if !ok {
		step = r.fallback
	}
	if step.err != nil {
		return Review{}, step.err
	}
	ev := Evidence{
		GeometryDigest: req.Rendering.GeometryDigest, RendererVersion: req.Rendering.Report.SurfaceVersion,
		Contract: req.Rendering.Report.Contract, FontsReady: req.Rendering.Report.FontsReady && req.Rendering.Report.FontFamilyResolved, Zoom: req.Rendering.Report.Zoom, Tiles: 1,
		Artifacts: append([]Artifact(nil), req.Rendering.Artifacts...),
		ReviewedAt: time.Unix(0, 0).UTC(),
	}
	if step.wrongDigest {
		ev.GeometryDigest = "0000"
	}
	if step.fontsLate {
		ev.FontsReady = false
	}
	return Review{Rendered: !step.notRendered, Findings: append([]Finding(nil), step.findings...), Evidence: ev}, nil
}

func reviewFinding(c Criterion) Finding {
	return Finding{Criterion: c, Severity: SeverityBlocking, Source: SourceReview, Evidence: "scripted " + string(c)}
}

func newCoord(t *testing.T, r Renderer, rv Reviewer, opts ...CoordinatorOption) *Coordinator {
	t.Helper()
	c, err := NewCoordinator(r, rv, opts...)
	require.NoError(t, err)
	return c
}

func prReviewJob(t *testing.T, attempts int) Job {
	return Job{Revision: "rev-1", Manifest: loadSample(t, "pr-review"), MaxAttempts: attempts}
}

func TestNewCoordinator_RequiresBothPorts(t *testing.T) {
	_, err := NewCoordinator(nil, &scriptedReviewer{})
	assert.True(t, errors.Is(err, ErrNilRenderer))
	_, err = NewCoordinator(newScriptedRenderer(), nil)
	assert.True(t, errors.Is(err, ErrNilReviewer))
}

func TestRun_PassesOnFirstCleanRenderedReview(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{}
	var stages []Progress
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), func(p Progress) { stages = append(stages, p) })

	assert.Equal(t, VerdictPassed, res.Verdict)
	assert.Equal(t, 1, res.AttemptsUsed)
	require.NotNil(t, res.Best)
	assert.True(t, res.Best.Passed)
	assert.False(t, res.Unreviewed)
	assert.Nil(t, res.Failure)
	assert.Equal(t, 0, BlockingCount(res.Findings))
	assert.Equal(t, GeometryDigest(res.Best.Geometry), res.Best.Review.Evidence.GeometryDigest, "evidence belongs to the returned candidate")
	assert.Equal(t, []Progress{{StageArranging, 1}, {StageRendering, 1}, {StageReviewing, 1}}, stages)
	assert.Equal(t, 1, r.measurer.calls, "text is measured once per job")
}

func TestRun_VisualOnlyClippingIsRefinedThenPasses(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{steps: map[int]reviewStep{1: {findings: []Finding{reviewFinding(CriterionTextClipped)}}}}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), nil)

	assert.Equal(t, VerdictPassed, res.Verdict)
	assert.Equal(t, 2, res.AttemptsUsed)
	require.Len(t, r.rendered, 2)
	assert.NotEqual(t, r.rendered[0], r.rendered[1], "refinement produced a different candidate")
	assert.Equal(t, 8.0, r.options[1].NodeTextSlack, "text_clipped grows boxes")
	assert.Equal(t, 2, res.Best.Attempt)
}

func TestRun_BudgetExhaustionReturnsBestWithItsFindings(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{
		steps:    map[int]reviewStep{1: {findings: []Finding{reviewFinding(CriterionAmbiguousCrossing)}}},
		fallback: reviewStep{findings: []Finding{reviewFinding(CriterionAmbiguousCrossing), reviewFinding(CriterionMergedLines)}},
	}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), nil)

	assert.Equal(t, VerdictNeedsAttention, res.Verdict)
	assert.Equal(t, "budget", res.ExhaustedReason)
	assert.Equal(t, 5, res.AttemptsUsed)
	assert.Len(t, r.rendered, 5, "five attempts means exactly five at most")
	require.NotNil(t, res.Best)
	assert.Equal(t, 1, res.Best.Attempt, "attempt 1 had fewer blocking findings and wins the ranking")
	assert.False(t, res.Best.Passed)
	assert.Contains(t, res.Findings, reviewFinding(CriterionAmbiguousCrossing))
	assert.NotContains(t, res.Findings, reviewFinding(CriterionMergedLines), "findings come from the returned candidate only")
	assert.Nil(t, res.Failure)
}

func TestRun_BudgetOneAndNonPositiveBudgets(t *testing.T) {
	for _, budget := range []int{1, 0, -3} {
		r := newScriptedRenderer()
		rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionNodeOverlap)}}}
		res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, budget), nil)
		assert.Equal(t, VerdictNeedsAttention, res.Verdict, "budget %d", budget)
		assert.Equal(t, 1, res.AttemptsUsed, "budget %d is at least one attempt", budget)
		assert.Len(t, r.rendered, 1)
	}
}

func TestRun_EarlyExhaustionWhenNothingCanChange(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionTextUnreadable)}}}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, VerdictNeedsAttention, res.Verdict)
	assert.Equal(t, "no_refinement", res.ExhaustedReason)
	assert.Equal(t, 1, res.AttemptsUsed)
}

func TestRun_PreflightFailuresAreStillRenderedAndReviewed(t *testing.T) {
	c := DefaultContract()
	c.NodeTypeFontPx = 8 // unreadable at practical zoom → blocking preflight finding
	r := newScriptedRenderer()
	rv := &scriptedReviewer{}
	res := newCoord(t, r, rv, WithContract(c)).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Len(t, r.rendered, 1, "a candidate failing preflight is still rendered")
	assert.Len(t, rv.seen, 1, "…and reviewed")
	assert.NotEqual(t, VerdictPassed, res.Verdict, "a clean review never overrides blocking preflight findings")
	assert.Greater(t, BlockingCount(res.Findings), 0)
}

func TestRun_EvidenceMustMatchTheCandidate(t *testing.T) {
	for name, step := range map[string]reviewStep{
		"wrong digest":       {wrongDigest: true},
		"not rendered":       {notRendered: true},
		"fonts not ready":    {fontsLate: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newScriptedRenderer()
			rv := &scriptedReviewer{fallback: step}
			res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 2), nil)
			assert.NotEqual(t, VerdictPassed, res.Verdict, "missing or mismatched render evidence never implies a pass")
			found := false
			for _, f := range res.Findings {
				if f.Criterion == CriterionRenderMismatch && f.Severity == SeverityBlocking {
					found = true
				}
			}
			assert.True(t, found)
		})
	}
}

func TestRun_RenderFailureAfterAReviewedAttemptKeepsReviewedBest(t *testing.T) {
	r := newScriptedRenderer()
	r.renderErr[2] = errors.New("browser crashed")
	rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionNodeOverlap)}}}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
	require.NotNil(t, res.Failure)
	assert.Equal(t, StageRendering, res.Failure.Stage)
	assert.Contains(t, res.Failure.Reason, "browser crashed")
	assert.Equal(t, 2, res.AttemptsUsed, "the failed attempt still counts")
	require.NotNil(t, res.Best)
	assert.Equal(t, 1, res.Best.Attempt, "the reviewed candidate outranks the unreviewed one")
	assert.False(t, res.Unreviewed)
	assert.False(t, res.Cancelled)
}

func TestRun_RenderFailureOnFirstAttemptRetainsUnreviewedGeometry(t *testing.T) {
	r := newScriptedRenderer()
	r.renderErr[1] = errors.New("no browser")
	res := newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
	require.NotNil(t, res.Best)
	assert.Nil(t, res.Best.Review)
	assert.True(t, res.Unreviewed, "the retained geometry is clearly marked unreviewed")
	assert.False(t, res.Best.Passed)
}

func TestRun_ReviewerErrorNeverPasses(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{fallback: reviewStep{err: errors.New("reviewer unavailable")}}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
	assert.Equal(t, StageReviewing, res.Failure.Stage)
	assert.True(t, res.Unreviewed)
}

func TestRun_MeasureFailure(t *testing.T) {
	r := newScriptedRenderer()
	r.measureErr = errors.New("measure surface failed")
	res := newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
	assert.Equal(t, StageRendering, res.Failure.Stage)
	assert.Equal(t, 1, res.AttemptsUsed)
	assert.Nil(t, res.Best)
	assert.Empty(t, r.rendered)
}

func TestRun_InvalidManifest(t *testing.T) {
	r := newScriptedRenderer()
	res := newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), Job{Revision: "r", Manifest: &workflow.Workflow{}, MaxAttempts: 5}, nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
	assert.Equal(t, StageArranging, res.Failure.Stage)
	assert.Contains(t, res.Failure.Reason, "no nodes")
	assert.Equal(t, 0, res.AttemptsUsed)
	assert.Nil(t, res.Best)
	assert.Empty(t, r.rendered)

	res = newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), Job{Revision: "r", Manifest: nil, MaxAttempts: 5}, nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
}

func TestRun_CancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := newScriptedRenderer()
	res := newCoord(t, r, &scriptedReviewer{}).Run(ctx, prReviewJob(t, 5), nil)
	assert.True(t, res.Cancelled)
	assert.Equal(t, VerdictFailed, res.Verdict)
	assert.Equal(t, 0, res.AttemptsUsed)
	assert.Empty(t, r.rendered)
}

func TestRun_CancelledMidRenderRetainsBestSoFar(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionNodeOverlap)}}}
	ctx, cancel := context.WithCancel(context.Background())
	attempt := 0
	progress := func(p Progress) {
		if p.Stage == StageRendering {
			attempt = p.Attempt
			if attempt == 2 {
				r.mu.Lock()
				r.block = true
				r.mu.Unlock()
				go cancel()
			}
		}
	}
	res := newCoord(t, r, rv).Run(ctx, prReviewJob(t, 5), progress)
	assert.True(t, res.Cancelled)
	assert.Equal(t, VerdictFailed, res.Verdict)
	require.NotNil(t, res.Best)
	assert.Equal(t, 1, res.Best.Attempt)
	assert.False(t, res.Unreviewed)
}

func TestRun_IsDeterministic(t *testing.T) {
	run := func() Result {
		rv := &scriptedReviewer{steps: map[int]reviewStep{1: {findings: []Finding{reviewFinding(CriterionMergedLines)}}}}
		return newCoord(t, newScriptedRenderer(), rv).Run(context.Background(), Job{Revision: "r", Manifest: loadSample(t, "feature-pipeline"), MaxAttempts: 5}, nil)
	}
	a, b := run(), run()
	require.NotNil(t, a.Best)
	assert.Equal(t, GeometryDigest(a.Best.Geometry), GeometryDigest(b.Best.Geometry))
	assert.Equal(t, a.Verdict, b.Verdict)
	assert.Equal(t, a.AttemptsUsed, b.AttemptsUsed)
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run 'TestNewCoordinator|TestRun_' -v`
Expected: build failure `undefined: NewCoordinator`, `undefined: Job`, `undefined: VerdictPassed`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/coordinator.go`:

```go
package layout

import (
	"context"
	"errors"
	"fmt"
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
	renderer Renderer
	reviewer Reviewer
	contract RendererContract
	options  Options
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

// WithStageTimeout bounds each Measure, Render and Review call separately.
func WithStageTimeout(d time.Duration) CoordinatorOption {
	return func(co *Coordinator) { if d > 0 { co.stageTimeout = d } else { co.stageTimeout = 2*time.Minute } }
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
	c := &Coordinator{renderer: renderer, reviewer: reviewer, contract: DefaultContract(), options: DefaultOptions(), stageTimeout: 2*time.Minute}
	for _, o := range opts {
		o(c)
	}
	return c, nil
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
	if job.Revision == "" { return Result{Verdict: VerdictFailed, Failure: &Failure{Stage: StageArranging, Reason: "missing saved revision"}} }
	g, err := BuildGraph(job.Manifest)
	if err != nil {
		return Result{Verdict: VerdictFailed, Failure: &Failure{Stage: StageArranging, Reason: err.Error()}}
	}

	nodeTypes := make(map[string]string, len(job.Manifest.Nodes))
	for _, n := range job.Manifest.Nodes { nodeTypes[n.ID] = string(n.Type) }
	var best *Candidate
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
		if attempt == 1 {
			measureCtx, cancel := context.WithTimeout(ctx, c.stageTimeout)
			measureReq := MeasureRequestFor(g, c.contract)
			m, err = c.renderer.Measure(measureCtx, measureReq)
			if err == nil { err = measureCtx.Err() }
			cancel()
			if err != nil {
				return fail(attempt, StageRendering, "measuring text: "+err.Error())
			}
			if m.Contract != c.contract || !m.FontsReady { return fail(attempt, StageRendering, "measurement contract mismatch or fonts not ready") }
			for _, item := range measureReq.Items {
				fontPx := c.contract.NodeLabelFontPx
				if item.Style == StyleNodeType { fontPx = c.contract.NodeTypeFontPx }
				if item.Style == StyleEdgeLabel { fontPx = c.contract.EdgeLabelFontPx }
				tm, metricErr := metricForText(m, item.Key, item.Text, fontPx, c.contract.LineHeight, item.MaxWidth)
				if metricErr != nil { return fail(attempt, StageRendering, metricErr.Error()) }
				if item.Text != "" && (tm.Width <= 0 || tm.Height <= 0 || tm.Lines <= 0) { return fail(attempt, StageRendering, "empty measurement for nonempty text: "+item.Key) }
			}
		}
		geo, err := Arrange(g, m, c.contract, opts)
		if err != nil {
			return fail(attempt, StageArranging, err.Error())
		}
		if err := geo.Validate(g); err != nil { return fail(attempt, StageArranging, err.Error()) }
		cand := &Candidate{Attempt: attempt, Geometry: geo, Preflight: Preflight(g, geo, m)}

		progress(Progress{Stage: StageRendering, Attempt: attempt})
		renderCtx, cancelRender := context.WithTimeout(ctx, c.stageTimeout)
		rendering, err := c.renderer.Render(renderCtx, NewRenderRequest(job.Revision, attempt, geo, nodeTypes))
		if err == nil { err = renderCtx.Err() }
		cancelRender()
		if err != nil {
			cand.Score = unreviewedScore(cand)
			best = better(best, cand)
			return fail(attempt, StageRendering, err.Error())
		}

		progress(Progress{Stage: StageReviewing, Attempt: attempt})
		reviewCtx, cancelReview := context.WithTimeout(ctx, c.stageTimeout)
		review, err := c.reviewer.Review(reviewCtx, ReviewRequest{Revision: job.Revision, Attempt: attempt, Geometry: geo, Rendering: rendering})
		if err == nil { err = reviewCtx.Err() }
		cancelReview()
		if err != nil {
			cand.Score = unreviewedScore(cand)
			best = better(best, cand)
			return fail(attempt, StageReviewing, err.Error())
		}
		problem := evidenceProblem(geo, review)
		if issue := reviewFindingsProblem(g, review.Findings); issue != "" {
			problem = issue
			review.Rendered = false
		}
		if rendering.Revision != job.Revision || rendering.Attempt != attempt || rendering.GeometryDigest != GeometryDigest(geo) || rendering.Report.Contract != geo.Contract || rendering.Report.SurfaceVersion != geo.Contract.Version || !rendering.Report.FontsReady || !rendering.Report.FontFamilyResolved || rendering.Report.Zoom != geo.Contract.PracticalZoom || rendering.RenderedAt.IsZero() || review.Evidence.ReviewedAt.Before(rendering.RenderedAt) {
			problem = "rendering metadata does not bind this revision/attempt/candidate and renderer"
			review.Rendered = false
		}
		if problem != "" {
			review.Findings = append(review.Findings, Finding{Criterion: CriterionRenderMismatch, Severity: SeverityBlocking, Source: SourceReview, Evidence: problem})
		}
		cand.Review = &review
		all := candidateFindings(cand)
		cand.Score = ScoreOf(attempt, all, len(geo.Crossings))
		cand.Passed = HasRenderedEvidence(geo, &review) && BlockingCount(all) == 0
		if cand.Passed {
			best = cand
			return finish(Result{Verdict: VerdictPassed, AttemptsUsed: attempt})
		}
		best = better(best, cand)
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
	return ScoreOf(c.Attempt, c.Preflight, len(c.Geometry.Crossings))
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
```

Pass rule note: `cand.Passed` requires zero blocking findings across preflight **and** review, and `evidenceProblem` turns any missing/mismatched evidence (not rendered, other digest, other contract, fonts not ready) into a blocking finding — exactly the contracts §1 pass rule.

- [x] **Step 4: Run it, confirm it passes**

> Resolved on revisit: the assigned producers/root are installed. `TestCoordinator_RealBrowserBoundedBestReachesSink` drives real Service authoring → exact SQLite input/budget → root Worker/Coordinator → production Chrome measurement/rendering/reviewer → StoredResult/cache/Reader. A narrowly understated actual Unicode branch measurement yields real DOM clipping and real findings. Budget=1 renders/reviews exactly once; a later injected browser interruption retains attempt 1's own reviewed geometry/evidence/findings/score; an interruption before the first rendering retains clearly unreviewed geometry. Existing real-root refinement, unwrapped pr-review, missing-report and missing-browser tests cover pass/refinement/evidence rejection/visible failure. No successful report, artifact or review is fabricated. This closes P1T9 provenance, not P3T7/P3T14's separate complex-sample/UI release acceptance.

Run: `go test ./domain/layout/ -run 'TestNewCoordinator|TestRun_' -v -race`
Expected: `--- PASS` for `TestNewCoordinator_RequiresBothPorts`, `TestRun_PassesOnFirstCleanRenderedReview`, `TestRun_VisualOnlyClippingIsRefinedThenPasses`, `TestRun_BudgetExhaustionReturnsBestWithItsFindings`, `TestRun_BudgetOneAndNonPositiveBudgets`, `TestRun_EarlyExhaustionWhenNothingCanChange`, `TestRun_PreflightFailuresAreStillRenderedAndReviewed`, `TestRun_EvidenceMustMatchTheCandidate` (3 subtests), `TestRun_RenderFailureAfterAReviewedAttemptKeepsReviewedBest`, `TestRun_RenderFailureOnFirstAttemptRetainsUnreviewedGeometry`, `TestRun_ReviewerErrorNeverPasses`, `TestRun_MeasureFailure`, `TestRun_InvalidManifest`, `TestRun_CancelledBeforeStart`, `TestRun_CancelledMidRenderRetainsBestSoFar`, `TestRun_IsDeterministic`; no race reports; `ok`.

Then the full Phase 1 package gate: `go test ./domain/layout/... -race && go vet ./domain/layout/... && go list -deps ./domain/layout/ | grep -c 'arbitron/adapter'`
Expected: `ok`, no vet output, `0` (domain imports no adapters).

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/coordinator.go domain/layout/coordinator_test.go
git commit -m "feat(layout): bounded arrange-render-review coordinator with truthful verdicts" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Composition-root wiring:** `layout.NewCoordinator(renderer, reviewer)` is constructed in `cmd/arbitron/layout_wiring.go:newLayoutStack` (P2T9) and injected into `layout.NewWorker` (P2T8), which `initDomain` starts for **both** `serve` and stdio `mcp`. P2T9's composition-root e2e test drives a real save through that site and asserts the worker reached a terminal state with the saved manifest; P3T7 swaps the renderer/reviewer for the production browser adapter and its acceptance test asserts `passed` on pr-review, feature-pipeline and ralph-loop within 5 attempts.
- **Data sources:** `Job.Manifest` ← `layout_revisions.input_manifest` captured at save (P2T2/P2T3/P2T4 → P2T5 → P2T6), unmarshalled by `Worker.ProcessRevision` (P2T8); `Job.MaxAttempts` ← `layout_revisions.attempt_limit` ← `layout.NewRequester(..., cfg.Layout.MaxAttempts)` (P2T9) ← `layout.maxattempts` YAML/env (P2T1); `Job.Revision` ← server-issued saved revision (P2T2). Renderer/Reviewer ← `adapter/renderreview` (P3T5/P3T6) wired by P3T7.
- **Sink:** `Result` is persisted by the worker (`Store.Complete`, P2T8/P2T6), published to `layout.json` (P2T7) and shown through `QueryService` (P3T8) in both views.
- **Provenance probe:** `TestRun_EvidenceMustMatchTheCandidate` and `TestRun_RenderFailure*` prove a geometry-only or unreviewed result can never be `passed`; P3T7's test with a nonexistent browser path proves the live coordinator really calls the browser renderer.

### Alignment TDD: stage deadlines and trustworthy evidence classification

**Files:** `domain/layout/coordinator.go`, `coordinator_test.go`.

- [x] **RED:** Add TestRun_StageTimeout for blocked Measure/Render/Review: bounded duration,
  correct stage, attempts, best-so-far, context cancellation reaching the fake. Add
  TestHasRenderedEvidence for Rendered=false, wrong digest/contract/version/zoom, FontsReady=false,
  missing overview/tiles and nil Review. A syntactically present but mismatched review is
  Unreviewed and cannot outrank a verified review or pass. Fakes must supply artifacts for
  the actual requested candidate and honor cancellation.
- [x] Run `go test ./domain/layout -run 'TestRun_|TestHasRenderedEvidence' -count=1 -race` and observe the behavioral failure before implementing the correction.
- [x] **GREEN:** Implement WithStageTimeout(d time.Duration) CoordinatorOption, an unexported
  stageTimeout field (2m default; nonpositive value uses default), and HasRenderedEvidence
  exactly as contracts §6.5. Use per-stage contexts and cancel each promptly. WorkerConfig
  overrides the timeout on a per-job copy; never mutate the shared coordinator. Use the
  helper consistently in finish/better/pass checks, including Review.Rendered=false.
- [x] Rerun the command, then the owning package/typecheck gate. Temporarily remove the
  corrected input/wiring/guard and verify the regression assertion fails; restore it.
- [x] Commit the source/tests with this task. These checks are additional to the existing
  happy/success, failure, error and edge cases; fixture-only success is insufficient.

> Resolved on revisit: the real root now constructs NewCoordinator with production ports and Worker makes per-job copies with runtime stage timeout. The new root tests verify distinct bounded Measure/Render/Review contexts, once-per-job measurement, cancellation of the preceding stage before entering the next collaborator, and all persisted geometry/evidence/failure sinks. Deferring cancelRender until Run returns fails the prompt-cleanup assertion; dropping Job.Manifest fails terminal stage/attempt/result assertions; ignoring reviewed-candidate preference loses the real reviewed best; allowing one extra attempt fails the captured budget assertion. Every mutation was restored. The prescribed evidence/deadline/concurrency unit suite and full domain race/vet/no-adapter gates also pass.

**Successful fake evidence:** Extend scriptedRenderer.Render's successful Rendering with
both overview and tile Artifact entries whose Region is req.Geometry.Canvas, Scale is
req.Geometry.Contract.PracticalZoom, and SHA256/path identifiers incorporate
GeometryDigest(req.Geometry). scriptedReviewer copies those artifacts, checks input
revision/attempt/digest against req.Geometry, and honors cancelled contexts. These are
unit evidence envelopes; they cannot be used by the production ReadabilityReviewer,
whose tests require actual PNGs. Test that missing/mismatched envelopes fail the helper.


### Implement HasRenderedEvidence and observe behavioral RED

HasRenderedEvidence is a pure value check, with no file/network I/O. Add its actual body
in coordinator.go before running the GREEN gate; do not leave an undefined helper. It
requires nonnil Review, Rendered=true, a nonempty 64-hex candidate digest, identical
evidence digest/full contract, matching renderer version, FontsReady, exact practical
zoom and nonzero ReviewedAt. Artifacts must include an overview of the full zero-origin
canvas and at least one tile, all with nonempty paths, valid 64-hex SHA256, finite positive
contained regions/scales; tiles have Scale==PracticalZoom, overview 0<Scale<=PracticalZoom.
Evidence.Tiles equals the actual tile count. Verify readable tile **union** covers Canvas:
collect tile X boundaries plus canvas left/right, sweep each nonzero X strip, sort/merge
its covering Y intervals, and require coverage from canvas top to bottom within geomEps.
No one-tile shortcut for a long canvas; a hole in the union rejects evidence. The production
P3T6 reviewer additionally opens/hash-checks/raster-checks those files; domain never does.

Add a complete TestHasRenderedEvidence mutation matrix: nil/not-rendered, wrong digest,
contract/renderer version/zoom, fonts late, missing overview/tile, zero/NaN region or scale,
invalid hash/path/time, incorrect Tiles count, partial tile coverage and overlapping tiles
whose union still leaves a hole. Accept a complete overlapping tile union. Run RED against
a compiling `return false` stub for the success case, then against a permissive stub for
rejections, implement GREEN and mutate each guard. Use this helper consistently for pass,
better, Result.Unreviewed and the future StoredResult.IsUnreviewed consumer (P2T5).

scriptedRenderer must record and validate Revision, Attempt and WorkflowNodeTypes from the
actual immutable manifest; nil/missing/extra/wrong types fail a recording assertion. Its
unit report has matching node/edge coverage constructed from req.Geometry, rather than an
empty production-like success report. scriptedReviewer verifies the rendering/request
binding before applying its script, copies artifacts and fonts-ready conjunction, and
rejects empty/mismatched reports. These fakes are used only in unit tests. Include an
independent fake that ignores the request deliberately; the coordinator must never pass
its stale revision/attempt/digest, even if its reviewer claims Rendered=true.

Stage timeout tests block Measure, first/later Render and first/later Review separately.
Assert exact Failure.Stage, attempts, best-so-far and cancellation reaches the collaborator;
a stage deadline alone sets Failed but not Cancelled, while cancellation of the job context
sets Cancelled. A collaborator returning nil error after its context deadline cannot pass.
Test cancellation after the progress callback but before I/O as well as before Run, and
concurrent Run calls on a shared coordinator with different per-job timeout copies under
-race. Caller manifest bytes, candidate options and evidence remain immutable.

Presentation fingerprints compare actual node/edge/label/path/canvas geometry excluding
Options metadata. Options-only changes are not distinct layouts, but one repeated prefix
does not prove later search cannot improve. Continue within the budget while Refine can
change applicable policy; stop no_refinement only at true no-change/cap. Test a plateau
followed by a better crossing-search prefix and a capped policy. Every produced candidate
still receives render/review; no uncounted speculative Arrange calls or unrendered preview
candidates may evade the attempt contract.

`unitReport(geo Geometry) RenderReport` is a P1T9-owned test helper in coordinator_test.go.
It builds complete node/edge/crossing/state coverage from the exact requested geometry,
with visible full text, finite glyph regions and matching fonts/zoom. It is explicitly a
unit double, never an actual DOM or raster producer. A fake reviewer validates that
coverage rather than accepting an empty report. Include tests for wrong revision/attempt
and nil/missing/extra/wrong WorkflowNodeTypes, font-not-ready/full-contract measurement
results, and invalid reviewer criterion/severity/source/implicated IDs/ordinals. Normalize
invalid reviewer output to a blocking render_mismatch finding; unknown severities must
never evade BlockingCount. Keep that validation in the actual Run implementation before
computing Passed, not only in a fake.

Implement `reviewFindingsProblem(g Graph, fs []Finding) string` locally in coordinator.go:
require a known Criterion, blocking/advisory Severity, SourceReview, nonempty Evidence,
existing node IDs and 0<=ordinal<len(g.Edges). CriterionCrossings must be advisory. Return
a precise reason on the first violation, otherwise empty string. Run calls this before
ranking, adds a blocking render_mismatch and sets Rendered=false for invalid output.

## Execution evidence

- Skipped the previously recorded, still-unresolved production gates in P1T4–P1T8; selected independent P1T9. No later plan task was implemented.
- RED: prescribed missing-symbol failure, then assertion failures against compiling constructor/Run stubs. Added cancellation/deadline, raw-type provenance, invalid-review and immutable-input tests before implementing behavior. HasRenderedEvidence failed its complete-evidence success cases against `return false`, then its rejection/union-hole cases against `return true`.
- GREEN: implemented the exact shared types/options, bounded attempt counting, once-per-job validated measurement, render/review on every produced candidate, targeted refinement, candidate-local findings/evidence, best-so-far ranking and truthful failure/cancellation. Added per-stage deadlines and metadata/findings validation; no domain adapter imports or production fake.
- Tests are grouped in coordinator_test.go (prescribed cases, recording doubles and unitReport), coordinator_evidence_test.go (envelope mutation matrix/long-canvas union), coordinator_regression_test.go (stage/error/cancellation/provenance/ranking/concurrency) and coordinator_search_test.go (presentation plateau followed by actual crossing improvement, visual-only branch clipping, defaults/invalid inputs). Synthetic reports/artifacts are unit doubles only.
- Self-review, in order: (1) checked exact contracts §1/§6.4/§6.5/§6.11/§6.12 and this task's required sinks. Found a pre-review ranking bug; added a behavioral RED where a later unreviewed candidate has worse completed findings, then fixed promotion to happen only after review or a terminal interruption. Kept future store/root/browser work out of this slice and recorded the production blockers above. (2) Ran deliberate mutations removing evidence envelope guards, tile-union coverage, raw-type forwarding, reviewer-output validation, rendering request binding, deadline-error checks, prompt context cancellation and reviewed-candidate preference. Strengthened zoom/digest/redundant-tile cases when overlapping guards initially masked removals. All meaningful guard removals then failed; canvas/artifact positivity was also removed together to test their overlapping protection. All mutations restored. Tests assert Run's actual Result, candidate geometry, branch-label W/H, node H, returned findings/evidence, exact stages/attempt counts, and routed crossings after a plateau. These are not claimed as root acceptance. (3) checked neighboring Go patterns, used the shared rectangle helpers, SHA256 size constant and named default timeout, kept per-job policy local, and formatted all Go files. No stubs, speculative production attempts, second report schema or no-op input remains.
- Final GREEN at the original implementation pass: `go test ./domain/layout/ -run 'TestNewCoordinator|TestRun_' -v -race`, `go test ./domain/layout -run 'TestRun_|TestHasRenderedEvidence' -count=1 -race`, `go test ./domain/layout/... -race`, `go vet ./domain/layout/...`, `gofmt -l domain/layout/` (empty), and `git diff --check` pass. Adapter dependency count is 0. Production completion was blocked then; the revisit below resolves those dependency gates.

## Revisit — real coordinator production provenance

- Selection correction: dependency inspection showed P1T9's formerly missing producers/root now exist. Discarded this pass's premature, uncommitted P2T3 edits; only P1T9 tests/plan are included. Unrelated `docs/visuals/` files remain untouched and excluded.
- Existing behavior characterization followed contracts §6.1. Wrote the new saved-input/terminal-result/cache/Reader assertions first. Behavioral RED: removing reviewed-candidate preference retained the later unreviewed geometry instead of the real reviewed best; increasing budget by one produced two attempts for the configured one; deferring Render context cancellation left it active on entry to Review; replacing the Worker's real captured manifest with nil lost the expected rendering-stage failure/attempt/result. Restored every mutation. No production behavior changes were needed.
- New `cmd/arbitron/layout_coordinator_e2e_test.go` delegates through the actual production renderer/reviewer factory, preserving real DOM reports/PNG artifacts. Its only faults are an explicitly understated real two-line branch metric (shared P1T8 helper) and injected errors at Render call boundaries. Measurements, raw manifest node types, revision/attempt, runtime states, actual findings and candidate-local evidence remain load-bearing. It asserts exact captured bytes/budget, once-only measurement, separate bounded/promptly canceled contexts, first-candidate geometry/preflight/score, real clipping findings, own review/evidence, truthful unreviewed classification, failed/budget state, cache generations and Reader geometry, plus unchanged saved bytes.
- Self-review, in order: (1) limited changes to P1T9's previously blocked completion gates; no new feature, schema, option, runtime policy or unrelated task change. Pass/refinement comes from existing real root tests, not a substituted successful adapter; complex-sample/UI release acceptance stays with P3T7/P3T14. (2) Nil production manifest and the budget/ranking/cancellation guard mutations all fail named regression assertions. Strengthened the stage check to observe preceding-context cancellation on entry to the next collaborator; checking cancellation only after terminal persistence could be masked by parent cleanup. All successful reports/evidence are actual browser/reviewer output and file hashes are checked. (3) reused neighboring root/observer helpers, synchronized snapshots and restored factory/env overrides, required nonnil review before evidence access, and formatted the test. No production mutation/stub remains.
- Post-review GREEN: both prescribed focused coordinator/evidence race commands; full `go test ./domain/layout/... -race`; `go vet ./domain/layout/... ./cmd/arbitron`; `go build ./...`; gofmt and `git diff --check`. Domain adapter dependency count: 0.
- Browser-required post-review GREEN: `ARBITRON_REQUIRE_BROWSER=1 go test -race ./cmd/arbitron -run '^(TestCoordinator_RealBrowser|TestRefine_RealBrowser|TestRenderPorts_|TestLayoutRender_RealBrowserPRReview|TestLayoutRender_MissingBrowser)' -count=1 -v` passes (52.007s), no skips/races. New cases pass in 13.98s; real refinement, unwrapped pr-review and missing-report/browser failures all reach the persisted/display sinks. All P1T9 steps are now checked.
