package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This closes the findings/score production provenance gate. The separate
// all-samples-passed rendered acceptance gate remains owned by P3T7.
func TestLayoutPreflight_RealBrowserSavedFindingsAndScoreReachView(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, name := range []string{"feature-pipeline", "pr-review", "ralph-loop", "harness-update"} {
		t.Run(name, func(t *testing.T) {
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5", realSample: name})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
			ds, _ := env.boot(t)
			ctx := context.Background()
			saved := layoutE2EManifest(t, env.sharedPath(name))
			before, err := os.ReadFile(env.sharedPath(name))
			require.NoError(t, err)
			rec := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), 3*time.Minute)
			require.Contains(t, []layout.State{layout.StatePassed, layout.StateNeedsAttention}, rec.State, "%s: %+v", rec.FailureReason, rec.Findings)
			require.Positive(t, rec.ResultGeneration)
			result, err := ds.layout.store.Result(ctx, rec.Revision, rec.ResultGeneration)
			require.NoError(t, err)
			graph, err := layout.BuildGraph(saved)
			require.NoError(t, err)
			require.NoError(t, result.Geometry.Validate(graph))
			metrics, err := ds.layout.renderer.Measure(ctx, layout.MeasureRequestFor(graph, result.Geometry.Contract))
			require.NoError(t, err)
			require.True(t, metrics.FontsReady)
			assert.Equal(t, layout.Preflight(graph, result.Geometry, metrics), result.Preflight,
				"stored preflight must belong to the returned geometry and real browser metrics")

			// An independent, nonempty assertion is essential: comparing two empty
			// outputs of a disconnected/no-op Preflight would prove nothing.
			assert.Zero(t, layout.BlockingCount(result.Preflight), "real measured sample geometry must pass geometric preflight")
			crossingAdvisories := 0
			for _, f := range result.Preflight {
				assert.Equal(t, layout.SourcePreflight, f.Source)
				assert.NotEmpty(t, f.Evidence)
				if f.Criterion == layout.CriterionCrossings {
					crossingAdvisories++
					assert.Equal(t, layout.SeverityAdvisory, f.Severity)
					assert.Contains(t, f.Evidence, fmt.Sprint(len(result.Geometry.Crossings)))
				}
			}
			if len(saved.Edges) > 0 {
				require.NotEmpty(t, result.Geometry.Crossings, "branching samples must exercise nonempty preflight data")
				assert.Equal(t, 1, crossingAdvisories)
			} else {
				assert.Empty(t, result.Preflight)
			}

			require.NotNil(t, result.Review, "even preflight failures must receive rendered review")
			assert.Equal(t, layout.GeometryDigest(result.Geometry), result.Review.Evidence.GeometryDigest)
			assertEvidenceOnDisk(t, env, result.Review.Evidence)
			findings := append(append([]layout.Finding{}, result.Preflight...), result.Review.Findings...)
			wantScore := layout.Score{Attempt: result.Score.Attempt, Crossings: len(result.Geometry.Crossings)}
			for _, f := range findings {
				switch f.Severity {
				case layout.SeverityBlocking:
					wantScore.Blocking++
				case layout.SeverityAdvisory:
					if f.Criterion != layout.CriterionCrossings {
						wantScore.Advisory++
					}
				default:
					t.Fatalf("invalid persisted severity: %+v", f)
				}
			}
			assert.Equal(t, wantScore, result.Score, "crossing advisories must not be double-counted")
			require.GreaterOrEqual(t, result.Score.Attempt, 1)
			require.LessOrEqual(t, result.Score.Attempt, rec.AttemptsUsed)
			// Bind the score's attempt to its actual immutable browser capture,
			// not merely to another value computed by ScoreOf.
			captureRoot := filepath.Join(env.root, "layout-artifacts", rec.Revision, fmt.Sprint(result.Score.Attempt))
			for _, artifact := range result.Review.Evidence.Artifacts {
				rel, err := filepath.Rel(captureRoot, artifact.Path)
				require.NoError(t, err)
				assert.True(t, filepath.IsLocal(rel), "evidence must belong to score attempt %d: %s", result.Score.Attempt, artifact.Path)
			}
			assert.Equal(t, findings, rec.Findings)
			if wantScore.Blocking > 0 || !layout.HasRenderedEvidence(result.Geometry, result.Review) {
				assert.Equal(t, layout.StateNeedsAttention, rec.State, "clean geometry alone cannot establish a pass")
			} else {
				assert.Equal(t, layout.StatePassed, rec.State)
			}

			view := ds.layout.reader.View(ctx, saved)
			require.True(t, view.Available, "%s", view.AvailabilityError)
			assert.Equal(t, rec.Revision, view.SavedRevision)
			assert.Equal(t, rec.ResultGeneration, view.ResultGeneration)
			assert.Equal(t, rec.State, view.State)
			assert.Equal(t, findings, view.Findings, "both preflight and review findings must reach the display sink")
			assert.Equal(t, result.IsUnreviewed(), view.Unreviewed)
			require.NotNil(t, view.Geometry)
			assert.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*view.Geometry))
			after, err := os.ReadFile(env.sharedPath(name))
			require.NoError(t, err)
			assert.Equal(t, before, after, "preflight/review must not modify saved workflow bytes")
			t.Logf("%s: preflight=%+v score=%+v state=%s unreviewed=%t", name, result.Preflight, result.Score, rec.State, view.Unreviewed)
		})
	}
}
