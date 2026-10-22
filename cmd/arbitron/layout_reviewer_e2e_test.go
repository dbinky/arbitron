package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/renderreview"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

// Inject faults only after the real browser has produced its report and PNGs.
// Measurement, saved input, coordinator, reviewer and persistence stay real.
type reviewerEvidenceRenderer struct {
	layout.Renderer
	mutate   func(*layout.Rendering) error
	captured chan layout.Rendering
}

func (r *reviewerEvidenceRenderer) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	rend, err := r.Renderer.Render(ctx, req)
	if err != nil {
		return rend, err
	}
	original := rend
	original.Artifacts = append([]layout.Artifact(nil), rend.Artifacts...)
	r.captured <- original
	if r.mutate != nil {
		err = r.mutate(&rend)
	}
	return rend, err
}

func blankReviewerTiles(rend *layout.Rendering) error {
	for i := range rend.Artifacts {
		a := &rend.Artifacts[i]
		if a.Kind != "tile" {
			continue
		}
		raw, err := os.ReadFile(a.Path)
		if err != nil {
			return err
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		blank := image.NewRGBA(img.Bounds())
		draw.Draw(blank, blank.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, blank); err != nil {
			return err
		}
		if err := os.WriteFile(a.Path, encoded.Bytes(), 0600); err != nil {
			return err
		}
		sum := sha256.Sum256(encoded.Bytes())
		a.SHA256 = hex.EncodeToString(sum[:])
	}
	return nil
}

func TestLayoutRender_ReadabilityReviewerSavedEvidenceReachesSinks(t *testing.T) {
	browser := requireBrowserE2E(t)
	cases := []struct {
		name      string
		mutate    func(*layout.Rendering) error
		state     layout.State
		criterion layout.Criterion
		reason    string
		evidence  string
	}{
		{name: "complete", state: layout.StatePassed},
		{name: "stale revision", state: layout.StateNeedsAttention, criterion: layout.CriterionRenderMismatch, evidence: "rendering belongs to revision", mutate: func(r *layout.Rendering) error {
			r.Revision = "another-saved-revision"
			return nil
		}},
		{name: "missing tile", state: layout.StateFailed, mutate: func(r *layout.Rendering) error {
			return os.Remove(r.Artifacts[1].Path)
		}},
		{name: "hash mismatch", state: layout.StateFailed, mutate: func(r *layout.Rendering) error {
			original := append([]layout.Artifact(nil), r.Artifacts...)
			err := blankReviewerTiles(r)
			r.Artifacts = original // Valid PNGs, but not the pixels these hashes identify.
			return err
		}},
		{name: "blank tiles with valid hashes", state: layout.StateNeedsAttention, criterion: layout.CriterionTextUnreadable, evidence: "captured tiles", mutate: blankReviewerTiles},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originalFactory := layoutRenderReviewerFactory
			t.Cleanup(func() { layoutRenderReviewerFactory = originalFactory })
			captured := make(chan layout.Rendering, 1)
			layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
				renderer, reviewer, closeFn, err := newLayoutRenderReviewer(cfg, logger)
				return &reviewerEvidenceRenderer{renderer, tc.mutate, captured}, reviewer, closeFn, err
			}
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
			ds, _ := env.boot(t)
			require.IsType(t, &renderreview.ReadabilityReviewer{}, ds.layout.reviewer)
			ctx := adminCtxForLocalUser(t, ds)
			const name = "reviewer-evidence"
			wf := layoutE2EWorkflow(name, "Café 審查 ✅")
			wf.Edges[0].Label = "Freigabe 審查"
			require.NoError(t, ds.workflowSvc.Create(ctx, name, wf))
			saved := layoutE2EManifest(t, env.privatePath(name))
			before, err := os.ReadFile(env.privatePath(name))
			require.NoError(t, err)
			row := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), time.Minute)
			require.Equal(t, tc.state, row.State, "%s: %+v", row.FailureReason, row.Findings)
			require.Equal(t, 1, row.AttemptsUsed)
			manifest, err := json.Marshal(saved)
			require.NoError(t, err)
			require.JSONEq(t, string(manifest), string(row.Manifest))
			require.Equal(t, 1, layoutE2ECountRows(t, ds.db, name))
			var actual layout.Rendering
			select {
			case actual = <-captured:
			default:
				t.Fatal("saved input did not reach the actual browser")
			}
			require.Equal(t, saved.SavedRevision(), actual.Revision)
			require.Len(t, actual.Report.Nodes, len(saved.Nodes))
			for i, n := range actual.Report.Nodes {
				require.Equal(t, saved.Nodes[i].Label, n.Label.Content)
				require.True(t, n.Label.Visible)
				require.NotEmpty(t, n.Label.Glyphs)
			}
			require.Len(t, actual.Report.Edges, len(saved.Edges))
			require.NotNil(t, actual.Report.Edges[0].Label)
			require.Equal(t, saved.Edges[0].Label, actual.Report.Edges[0].Label.Content)
			view, err := ds.layout.query.WorkflowLayout(ctx, name)
			require.NoError(t, err)
			require.Equal(t, row.Revision, view.SavedRevision)
			if tc.state == layout.StateFailed {
				require.Equal(t, layout.StageReviewing, row.FailureStage)
				// Fixed category only: the evidence error and its artifact path are
				// logged, never served to viewers.
				require.Equal(t, "rendered review failed", row.FailureReason)
				require.NotContains(t, row.FailureReason, actual.Artifacts[1].Path)
				if tc.reason != "" {
					require.Contains(t, row.FailureReason, tc.reason)
				}
				result, err := ds.layout.store.Result(ctx, row.Revision, row.ResultGeneration)
				require.NoError(t, err)
				require.Nil(t, result.Review, "an unreadable capture must retain only the unreviewed candidate")
				require.True(t, row.Unreviewed)
				require.Equal(t, layout.DisplayFailed, view.DisplayStatus)
				require.Nil(t, view.Evidence)
				require.Equal(t, layoutE2EPositions, view.Retained.Positions)
			} else {
				result, err := ds.layout.store.Result(ctx, row.Revision, row.ResultGeneration)
				require.NoError(t, err)
				require.NotNil(t, result.Review)
				require.Equal(t, actual.GeometryDigest, layout.GeometryDigest(result.Geometry))
				assertEvidenceOnDisk(t, env, result.Review.Evidence)
				if tc.state == layout.StatePassed {
					require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
					require.Zero(t, layout.BlockingCount(result.Review.Findings))
					require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
					require.NotNil(t, view.Evidence)
					require.Equal(t, result.Review.Evidence.ForViewer(), *view.Evidence)
					require.NotNil(t, view.Geometry)
					require.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*view.Geometry))
				} else {
					require.False(t, result.Review.Rendered)
					require.True(t, row.Unreviewed)
					require.Equal(t, layout.DisplayNeedsAttention, view.DisplayStatus)
					require.Nil(t, view.Evidence)
					for label, findings := range map[string][]layout.Finding{"review": result.Review.Findings, "row": row.Findings, "query": view.Findings} {
						var matching []layout.Finding
						for _, f := range findings {
							if f.Criterion == tc.criterion && f.Source == layout.SourceReview && strings.Contains(f.Evidence, tc.evidence) {
								matching = append(matching, f)
							}
						}
						require.Positive(t, layout.BlockingCount(matching), "%s lost reviewer findings: %+v", label, findings)
					}
				}
			}
			after, err := os.ReadFile(env.privatePath(name))
			require.NoError(t, err)
			require.Equal(t, before, after, "review never rewrites saved authoring bytes")
		})
	}
}
