package layout

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Hooks preserve the real unit inputs unless a test deliberately corrupts one.
type hookedRenderer struct {
	*scriptedRenderer
	measure func(context.Context, MeasureRequest) (Measurements, error)
	render  func(context.Context, RenderRequest) (Rendering, error)
}

func (r hookedRenderer) Measure(ctx context.Context, req MeasureRequest) (Measurements, error) {
	if r.measure != nil {
		return r.measure(ctx, req)
	}
	return r.scriptedRenderer.Measure(ctx, req)
}
func (r hookedRenderer) Render(ctx context.Context, req RenderRequest) (Rendering, error) {
	if r.render != nil {
		return r.render(ctx, req)
	}
	return r.scriptedRenderer.Render(ctx, req)
}

type reviewFunc func(context.Context, ReviewRequest) (Review, error)

func (f reviewFunc) Review(ctx context.Context, req ReviewRequest) (Review, error) {
	return f(ctx, req)
}

func TestRun_StageTimeout(t *testing.T) {
	for _, stage := range []string{"measure", "render", "review"} {
		for _, at := range []int{1, 2} {
			if stage == "measure" && at == 2 {
				continue
			}
			t.Run(stage+string(rune('0'+at)), func(t *testing.T) {
				base := newScriptedRenderer()
				r := hookedRenderer{scriptedRenderer: base}
				rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionNodeOverlap)}}}
				entered := make(chan struct{})
				wait := func(ctx context.Context) {
					assert.NoError(t, ctx.Err(), "the selected stage must enter before its deadline")
					_, bounded := ctx.Deadline()
					assert.True(t, bounded, "each port call must have its own deadline")
					close(entered)
					<-ctx.Done()
					assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
				}
				expectedStage := StageRendering
				if stage == "measure" {
					r.measure = func(ctx context.Context, req MeasureRequest) (Measurements, error) {
						if _, err := base.Measure(ctx, req); err != nil {
							return Measurements{}, err
						}
						wait(ctx)
						return Measurements{}, ctx.Err()
					}
				}
				if stage == "render" {
					r.render = func(ctx context.Context, req RenderRequest) (Rendering, error) {
						if req.Attempt == at {
							if _, err := base.Render(ctx, req); err != nil {
								return Rendering{}, err
							}
							wait(ctx)
							return Rendering{}, ctx.Err()
						}
						return base.Render(ctx, req)
					}
				}
				reviewer := Reviewer(rv)
				if stage == "review" {
					expectedStage = StageReviewing
					reviewer = reviewFunc(func(ctx context.Context, req ReviewRequest) (Review, error) {
						if req.Attempt == at {
							if _, err := rv.Review(ctx, req); err != nil {
								return Review{}, err
							}
							wait(ctx)
							return Review{}, ctx.Err()
						}
						return rv.Review(ctx, req)
					})
				}
				// Healthy measurement/render/review work must not race a 15ms
				// deadline on loaded race-detector runs. Synchronize on entry to
				// the deliberately blocked call, then observe its real timeout.
				const stageTimeout = time.Second
				const testTimeout = 10 * stageTimeout
				co := newCoord(t, r, reviewer, WithStageTimeout(stageTimeout))
				job := prReviewJob(t, 3)
				ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
				done := make(chan Result, 1)
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					done <- co.Run(ctx, job, nil)
				}()
				defer func() {
					cancel()
					<-finished
				}()
				select {
				case <-entered:
				case res := <-done:
					t.Fatalf("coordinator returned before selected stage entry: %+v", res)
				case <-ctx.Done():
					t.Fatal("coordinator never entered the selected stage")
				}
				var res Result
				select {
				case res = <-done:
				case <-ctx.Done():
					t.Fatal("stage timeout did not terminate the coordinator")
				}
				require.NotNil(t, res.Failure)
				assert.Equal(t, expectedStage, res.Failure.Stage)
				assert.Contains(t, res.Failure.Reason, "timed out")
				assert.Equal(t, VerdictFailed, res.Verdict)
				assert.False(t, res.Cancelled)
				assert.Equal(t, at, res.AttemptsUsed)
				if stage == "measure" {
					assert.Nil(t, res.Best)
				} else {
					require.NotNil(t, res.Best)
					assert.Equal(t, 1, res.Best.Attempt)
					assert.Equal(t, at == 1, res.Unreviewed)
				}
			})
		}
	}
}

func TestRun_LateNilErrorCannotPass(t *testing.T) {
	for _, stage := range []string{"measure", "render", "review"} {
		t.Run(stage, func(t *testing.T) {
			base := newScriptedRenderer()
			r := hookedRenderer{scriptedRenderer: base}
			rv := &scriptedReviewer{}
			reviewer := Reviewer(rv)
			if stage == "measure" {
				r.measure = func(ctx context.Context, req MeasureRequest) (Measurements, error) {
					m, err := base.Measure(ctx, req)
					<-ctx.Done()
					return m, err
				}
			}
			if stage == "render" {
				r.render = func(ctx context.Context, req RenderRequest) (Rendering, error) {
					v, err := base.Render(ctx, req)
					<-ctx.Done()
					return v, err
				}
			}
			if stage == "review" {
				reviewer = reviewFunc(func(ctx context.Context, req ReviewRequest) (Review, error) {
					v, err := rv.Review(ctx, req)
					<-ctx.Done()
					return v, err
				})
			}
			res := newCoord(t, r, reviewer, WithStageTimeout(10*time.Millisecond)).Run(context.Background(), prReviewJob(t, 1), nil)
			assert.Equal(t, VerdictFailed, res.Verdict)
			assert.False(t, res.Cancelled)
			require.NotNil(t, res.Failure)
		})
	}
}

func TestRun_CancelAfterProgressBeforeIO(t *testing.T) {
	for _, stage := range []Stage{StageArranging, StageRendering, StageReviewing} {
		t.Run(string(stage), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := newScriptedRenderer()
			r := hookedRenderer{scriptedRenderer: base}
			rv := &scriptedReviewer{}
			r.measure = func(ctx context.Context, req MeasureRequest) (Measurements, error) {
				assert.NoError(t, ctx.Err(), "cancelled progress must stop before Measure")
				return base.Measure(ctx, req)
			}
			r.render = func(ctx context.Context, req RenderRequest) (Rendering, error) {
				assert.NoError(t, ctx.Err(), "cancelled progress must stop before Render")
				return base.Render(ctx, req)
			}
			reviewer := reviewFunc(func(ctx context.Context, req ReviewRequest) (Review, error) {
				assert.NoError(t, ctx.Err(), "cancelled progress must stop before Review")
				return rv.Review(ctx, req)
			})
			res := newCoord(t, r, reviewer).Run(ctx, prReviewJob(t, 3), func(p Progress) {
				if p.Stage == stage {
					cancel()
				}
			})
			assert.True(t, res.Cancelled)
			assert.Equal(t, VerdictFailed, res.Verdict)
			assert.Equal(t, 1, res.AttemptsUsed)
			require.NotNil(t, res.Failure)
			assert.Equal(t, stage, res.Failure.Stage)
			if stage != StageArranging {
				require.NotNil(t, res.Best)
				assert.True(t, res.Unreviewed)
			}
		})
	}
}

func TestRun_MeasurementsAreValidatedAtRenderStage(t *testing.T) {
	for name, mutate := range map[string]func(*Measurements){
		"contract": func(m *Measurements) { m.Contract.NodePaddingX++ }, "fonts": func(m *Measurements) { m.FontsReady = false },
		"nil": func(m *Measurements) { m.Items = nil }, "zero": func(m *Measurements) {
			for k := range m.Items {
				m.Items[k] = TextMetrics{}
			}
		},
		"invalid": func(m *Measurements) {
			for k, v := range m.Items {
				v.Height = math.Inf(1)
				m.Items[k] = v
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			base := newScriptedRenderer()
			r := hookedRenderer{scriptedRenderer: base, measure: func(ctx context.Context, req MeasureRequest) (Measurements, error) {
				m, err := base.Measure(ctx, req)
				mutate(&m)
				return m, err
			}}
			res := newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), prReviewJob(t, 2), nil)
			assert.Equal(t, VerdictFailed, res.Verdict)
			require.NotNil(t, res.Failure)
			assert.Equal(t, StageRendering, res.Failure.Stage)
			assert.Equal(t, 1, res.AttemptsUsed)
			assert.Nil(t, res.Best)
			assert.Empty(t, base.rendered)
		})
	}
}

func TestRun_RejectsInvalidReviewFindings(t *testing.T) {
	for name, mutate := range map[string]func(*Finding){
		"criterion": func(f *Finding) { f.Criterion = "unknown" }, "severity": func(f *Finding) { f.Severity = "unknown" }, "source": func(f *Finding) { f.Source = SourcePreflight },
		"evidence": func(f *Finding) { f.Evidence = "" }, "node": func(f *Finding) { f.NodeIDs = []string{"absent"} }, "ordinal negative": func(f *Finding) { f.EdgeOrdinals = []int{-1} }, "ordinal out of range": func(f *Finding) { f.EdgeOrdinals = []int{999} }, "blocking crossings": func(f *Finding) { f.Criterion = CriterionCrossings },
	} {
		t.Run(name, func(t *testing.T) {
			f := reviewFinding(CriterionNodeOverlap)
			mutate(&f)
			rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{f}}}
			res := newCoord(t, newScriptedRenderer(), rv).Run(context.Background(), prReviewJob(t, 2), nil)
			assert.NotEqual(t, VerdictPassed, res.Verdict)
			assert.True(t, res.Unreviewed)
			require.NotNil(t, res.Best)
			assert.False(t, res.Best.Review.Rendered)
			found := false
			for _, f := range res.Findings {
				if f.Criterion == CriterionRenderMismatch && f.Severity == SeverityBlocking {
					found = true
				}
			}
			assert.True(t, found, "invalid review normalized to a blocker before ranking")
		})
	}
	g := edgeGraph(t, [2]string{"a", "b"})
	assert.Empty(t, reviewFindingsProblem(g, []Finding{{Criterion: CriterionCrossings, Severity: SeverityAdvisory, Source: SourceReview, Evidence: "visible crossing", NodeIDs: []string{"a"}, EdgeOrdinals: []int{0}}}))
}

func TestRun_RequestProvenanceAndImmutability(t *testing.T) {
	job := prReviewJob(t, 2)
	before := cloneValue(t, job.Manifest)
	r := newScriptedRenderer()
	rv := &scriptedReviewer{}
	res := newCoord(t, r, rv).Run(context.Background(), job, nil)
	require.Equal(t, VerdictPassed, res.Verdict)
	require.Len(t, r.requests, 1)
	req := r.requests[0]
	assert.Equal(t, job.Revision, req.Revision)
	assert.Equal(t, 1, req.Attempt)
	types := map[string]string{}
	for _, n := range job.Manifest.Nodes {
		types[n.ID] = string(n.Type)
	}
	assert.Equal(t, types, req.WorkflowNodeTypes)
	assert.Equal(t, before, job.Manifest)
	assert.Equal(t, res.Best.Geometry, req.Geometry)
	assert.Equal(t, DefaultOptions(), res.Best.Geometry.Options)
	assert.Equal(t, rv.seen[0].Rendering.Artifacts, res.Best.Review.Evidence.Artifacts)
	for _, which := range []string{"nil", "missing", "extra", "wrong"} {
		t.Run(which, func(t *testing.T) {
			bad := cloneValue(t, req)
			switch which {
			case "nil":
				bad.WorkflowNodeTypes = nil
			case "missing":
				delete(bad.WorkflowNodeTypes, bad.Geometry.Nodes[0].ID)
			case "extra":
				bad.WorkflowNodeTypes["extra"] = "script"
			case "wrong":
				bad.WorkflowNodeTypes[bad.Geometry.Nodes[0].ID] = "wrong_type"
			}
			_, err := newScriptedRenderer().Render(context.Background(), bad)
			assert.Error(t, err)
		})
	}
	// Vary the raw manifest type, not its presentation caption.
	job.Manifest.Nodes[0].Type = workflow.NodeTypeAgentCall
	r = newScriptedRenderer()
	res = newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), job, nil)
	require.NotNil(t, res.Best)
	require.NotEmpty(t, r.requests)
	assert.Equal(t, "agent_call", r.requests[0].WorkflowNodeTypes[job.Manifest.Nodes[0].ID])
}

func TestRun_StaleRenderingCannotPassEvenWithTrustingReviewer(t *testing.T) {
	for _, which := range []string{"revision", "attempt", "digest", "report", "time"} {
		t.Run(which, func(t *testing.T) {
			base := newScriptedRenderer()
			r := hookedRenderer{scriptedRenderer: base, render: func(ctx context.Context, req RenderRequest) (Rendering, error) {
				v, err := base.Render(ctx, req)
				switch which {
				case "revision":
					v.Revision = "old"
				case "attempt":
					v.Attempt++
				case "digest":
					v.GeometryDigest = "stale"
				case "report":
					v.Report = RenderReport{}
				case "time":
					v.RenderedAt = time.Time{}
				}
				return v, err
			}}
			trusting := reviewFunc(func(_ context.Context, req ReviewRequest) (Review, error) { return unitEvidence(req.Geometry), nil })
			res := newCoord(t, r, trusting).Run(context.Background(), prReviewJob(t, 3), nil)
			assert.NotEqual(t, VerdictPassed, res.Verdict)
			assert.True(t, res.Unreviewed)
			assert.Equal(t, 1, res.AttemptsUsed)
		})
	}
}

func TestRun_UnitReviewerRejectsMissingReport(t *testing.T) {
	g := validTwoNodeGeometry()
	rendering := Rendering{Revision: "r", Attempt: 1, GeometryDigest: GeometryDigest(g)}
	rv := &scriptedReviewer{}
	review, err := rv.Review(context.Background(), ReviewRequest{Revision: "r", Attempt: 1, Geometry: g, Rendering: rendering})
	require.NoError(t, err)
	assert.False(t, review.Rendered)
}

func TestRun_ReviewedOutranksSyntacticallyPresentMismatchedReview(t *testing.T) {
	rv := &scriptedReviewer{steps: map[int]reviewStep{1: {findings: []Finding{reviewFinding(CriterionNodeOverlap), reviewFinding(CriterionMergedLines)}}, 2: {wrongDigest: true}}}
	res := newCoord(t, newScriptedRenderer(), rv).Run(context.Background(), prReviewJob(t, 2), nil)
	require.NotNil(t, res.Best)
	assert.Equal(t, 1, res.Best.Attempt)
	assert.False(t, res.Unreviewed)
	assert.Equal(t, VerdictNeedsAttention, res.Verdict)
}

func TestRun_UnreviewedRankingUsesCompletedFindings(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{steps: map[int]reviewStep{
		1: {notRendered: true, findings: []Finding{reviewFinding(CriterionTextClipped)}},
		2: {notRendered: true, findings: []Finding{reviewFinding(CriterionTextClipped), reviewFinding(CriterionNodeOverlap), reviewFinding(CriterionMergedLines)}},
	}}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 2), nil)
	require.NotNil(t, res.Best)
	assert.Equal(t, 1, res.Best.Attempt, "do not discard the earlier best on an incomplete pre-review score")
	assert.True(t, res.Unreviewed)
	assert.Equal(t, 2, BlockingCount(res.Findings))
}

func TestRun_ParentCancellationReachesMeasureAndReview(t *testing.T) {
	for _, stage := range []string{"measure", "review"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := newScriptedRenderer()
			r := hookedRenderer{scriptedRenderer: base}
			rv := Reviewer(&scriptedReviewer{})
			reached := false
			wait := func(stageCtx context.Context) {
				cancel()
				<-stageCtx.Done()
				reached = true
				assert.ErrorIs(t, stageCtx.Err(), context.Canceled)
			}
			want := StageRendering
			if stage == "measure" {
				r.measure = func(ctx context.Context, _ MeasureRequest) (Measurements, error) {
					wait(ctx)
					return Measurements{}, ctx.Err()
				}
			} else {
				want = StageReviewing
				rv = reviewFunc(func(ctx context.Context, _ ReviewRequest) (Review, error) { wait(ctx); return Review{}, ctx.Err() })
			}
			res := newCoord(t, r, rv).Run(ctx, prReviewJob(t, 2), nil)
			assert.True(t, reached)
			assert.True(t, res.Cancelled)
			assert.Equal(t, VerdictFailed, res.Verdict)
			assert.Equal(t, 1, res.AttemptsUsed)
			require.NotNil(t, res.Failure)
			assert.Equal(t, want, res.Failure.Stage)
		})
	}
}

func TestRun_StageContextsAreCancelledPromptly(t *testing.T) {
	base := newScriptedRenderer()
	rv := &scriptedReviewer{}
	var contexts []context.Context
	r := hookedRenderer{scriptedRenderer: base,
		measure: func(ctx context.Context, req MeasureRequest) (Measurements, error) {
			contexts = append(contexts, ctx)
			return base.Measure(ctx, req)
		},
		render: func(ctx context.Context, req RenderRequest) (Rendering, error) {
			assert.ErrorIs(t, contexts[0].Err(), context.Canceled)
			contexts = append(contexts, ctx)
			return base.Render(ctx, req)
		},
	}
	reviewer := reviewFunc(func(ctx context.Context, req ReviewRequest) (Review, error) {
		assert.ErrorIs(t, contexts[1].Err(), context.Canceled)
		contexts = append(contexts, ctx)
		return rv.Review(ctx, req)
	})
	res := newCoord(t, r, reviewer).Run(context.Background(), prReviewJob(t, 1), nil)
	require.Equal(t, VerdictPassed, res.Verdict)
	require.Len(t, contexts, 3)
	for _, ctx := range contexts {
		assert.ErrorIs(t, ctx.Err(), context.Canceled)
	}
}

func TestRun_ConcurrentPerJobTimeoutCopies(t *testing.T) {
	// Separate collaborators aren't necessary: these doubles are stateless and race-safe.
	base := hookedRenderer{measure: func(ctx context.Context, req MeasureRequest) (Measurements, error) {
		return newFakeMeasurer().Measure(ctx, req)
	}, render: func(ctx context.Context, req RenderRequest) (Rendering, error) {
		<-ctx.Done()
		return Rendering{}, ctx.Err()
	}}
	co := newCoord(t, base, reviewFunc(func(context.Context, ReviewRequest) (Review, error) { return Review{}, errors.New("not reached") }))
	job := prReviewJob(t, 1)
	var wg sync.WaitGroup
	for _, timeout := range []time.Duration{10 * time.Millisecond, 25 * time.Millisecond} {
		wg.Add(1)
		go func(d time.Duration) {
			defer wg.Done()
			local := *co
			WithStageTimeout(d)(&local)
			res := local.Run(context.Background(), job, nil)
			assert.Equal(t, VerdictFailed, res.Verdict)
			require.NotNil(t, res.Failure)
			assert.Equal(t, StageRendering, res.Failure.Stage)
			assert.False(t, res.Cancelled)
		}(timeout)
	}
	wg.Wait()
	assert.Equal(t, 2*time.Minute, co.stageTimeout)
}

func TestRun_CappedPolicyStopsButOptionsOnlyPlateauDoesNot(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionAmbiguousCrossing)}}}
	res := newCoord(t, r, rv).Run(context.Background(), Job{Revision: "r", Manifest: &workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}}}, MaxAttempts: 4}, nil)
	assert.Equal(t, 4, res.AttemptsUsed)
	assert.Len(t, r.requests, 4)
	// A single node doesn't move as crossing-search and lane policy grow.
	for _, req := range r.requests[1:] {
		g := req.Geometry
		g.Options = r.requests[0].Geometry.Options
		assert.True(t, reflect.DeepEqual(r.requests[0].Geometry, g), "presentation plateau")
	}
	o := DefaultOptions()
	o.LaneSeparation = 60
	o.PortSpacing = 120
	o.CrossingSweeps = 64
	res = newCoord(t, newScriptedRenderer(), rv, WithOptions(o)).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, 1, res.AttemptsUsed)
	assert.Equal(t, "no_refinement", res.ExhaustedReason)
}

// Paired 8: the coordinator must not trust a Reviewer to cite the artifacts it
// was handed. Evidence for another rendering, or a capture failure returned as
// an artifact-free Rendering with a nil error, never passes.
func TestRun_ReviewEvidenceMustCiteItsOwnRenderingArtifacts(t *testing.T) {
	for _, which := range []string{"other-rendering", "empty-capture", "dropped-tile", "extra-tile"} {
		t.Run(which, func(t *testing.T) {
			base := newScriptedRenderer()
			r := hookedRenderer{scriptedRenderer: base, render: func(ctx context.Context, req RenderRequest) (Rendering, error) {
				v, err := base.Render(ctx, req)
				switch which {
				case "empty-capture":
					v.Artifacts = nil
				case "dropped-tile":
					tile := v.Artifacts[1]
					half := tile
					half.Region.H /= 2
					half.Path += "-top"
					rest := half
					rest.Region.Y += half.Region.H
					rest.Path = tile.Path + "-bottom"
					v.Artifacts = []Artifact{v.Artifacts[0], half, rest}
				}
				return v, err
			}}
			reviewer := reviewFunc(func(ctx context.Context, req ReviewRequest) (Review, error) {
				review := Review{Rendered: true, Evidence: Evidence{
					GeometryDigest: req.Rendering.GeometryDigest, RendererVersion: req.Rendering.Report.SurfaceVersion,
					Contract: req.Rendering.Report.Contract, FontsReady: true, Zoom: req.Rendering.Report.Zoom,
					Artifacts: append([]Artifact(nil), req.Rendering.Artifacts...), ReviewedAt: time.Unix(1, 0).UTC(),
				}}
				switch which {
				case "other-rendering", "empty-capture":
					review.Evidence.Artifacts = unitEvidence(req.Geometry).Evidence.Artifacts
					review.Evidence.Artifacts[0].Path = "/unit/attempt-0/overview.png"
				case "dropped-tile":
					full := unitEvidence(req.Geometry).Evidence.Artifacts[1]
					review.Evidence.Artifacts = []Artifact{req.Rendering.Artifacts[0], full}
				case "extra-tile":
					extra := review.Evidence.Artifacts[1]
					extra.Path = "/unit/attempt-0/tile.png"
					review.Evidence.Artifacts = append(review.Evidence.Artifacts, extra)
				}
				review.Evidence.Tiles = 0
				for _, a := range review.Evidence.Artifacts {
					if a.Kind == "tile" {
						review.Evidence.Tiles++
					}
				}
				require.True(t, HasRenderedEvidence(req.Geometry, &review), "the forged envelope is self-consistent")
				return review, nil
			})
			res := newCoord(t, r, reviewer).Run(context.Background(), prReviewJob(t, 1), nil)
			assert.NotEqual(t, VerdictPassed, res.Verdict)
			assert.True(t, res.Unreviewed)
			found := false
			for _, f := range res.Findings {
				found = found || (f.Criterion == CriterionRenderMismatch && f.Severity == SeverityBlocking)
			}
			assert.True(t, found, "the mismatch is reported as a blocking render_mismatch finding")
		})
	}
}
