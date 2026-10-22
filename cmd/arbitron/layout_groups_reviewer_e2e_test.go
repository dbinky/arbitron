package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Erase only an empty-interior group outline, not ordinary text/routes. Hashes
// describe the altered PNGs; the clean actual DOM report is deliberately kept.
func eraseCapturedGroupOutline(rend *layout.Rendering) error {
	b := rend.Report.GroupElements[0].Box
	return eraseCapturedGroupRegion(rend, layout.Rect{X: b.X - 1, Y: b.Y + 20, W: 4, H: 35})
}

func eraseCapturedGroupRegion(rend *layout.Rendering, region layout.Rect) error {
	for i := range rend.Artifacts {
		a := &rend.Artifacts[i]
		raw, err := os.ReadFile(a.Path)
		if err != nil {
			return err
		}
		src, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		dst := image.NewRGBA(src.Bounds())
		draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
		r := image.Rect(int(math.Floor((region.X-a.Region.X)*a.Scale)), int(math.Floor((region.Y-a.Region.Y)*a.Scale)), int(math.Ceil((region.Right()-a.Region.X)*a.Scale)), int(math.Ceil((region.Bottom()-a.Region.Y)*a.Scale)))
		draw.Draw(dst, r, &image.Uniform{C: color.RGBA{26, 26, 46, 255}}, image.Point{}, draw.Src)
		var encoded bytes.Buffer
		if err = png.Encode(&encoded, dst); err != nil {
			return err
		}
		if err = os.WriteFile(a.Path, encoded.Bytes(), 0600); err != nil {
			return err
		}
		sum := sha256.Sum256(encoded.Bytes())
		a.SHA256 = hex.EncodeToString(sum[:])
	}
	return nil
}

func TestLayoutGroups_ReviewerAndStoreTruthControls(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, kind := range []string{"missing report", "missing ink", "missing lane ink", "missing corridor"} {
		t.Run(kind, func(t *testing.T) {
			previous := layoutRenderReviewerFactory
			t.Cleanup(func() { layoutRenderReviewerFactory = previous })
			captured := make(chan layout.Rendering, 8)
			var repaired atomic.Bool
			layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
				renderer, reviewer, closeFn, err := newLayoutRenderReviewer(cfg, logger)
				return &reviewerEvidenceRenderer{Renderer: renderer, captured: captured, mutate: func(r *layout.Rendering) error {
					if repaired.Load() || len(r.Report.GroupElements) == 0 {
						return nil
					}
					if kind == "missing report" {
						r.Report.GroupElements = nil
						return nil
					}
					if kind == "missing corridor" {
						c := r.Report.GroupConnectors[0]
						for _, segment := range c.Segments {
							a, b := segment.Start, segment.End
							if math.Hypot(b.X-a.X, b.Y-a.Y) > 20 {
								return eraseCapturedGroupRegion(r, layout.Rect{X: (a.X+b.X)/2 - 8, Y: (a.Y+b.Y)/2 - 8, W: 16, H: 16})
							}
						}
						return fmt.Errorf("fixture has no synthetic corridor")
					}
					if kind == "missing lane ink" {
						for _, e := range r.Report.GroupElements {
							if e.ID == layout.GroupElementID("t2", "lane", "second") {
								return eraseCapturedGroupRegion(r, layout.Rect{X: e.Box.X - 1, Y: e.Box.Y + 20, W: 4, H: 35})
							}
						}
						return fmt.Errorf("fixture has no lane")
					}
					return eraseCapturedGroupOutline(r)
				}}, reviewer, closeFn, err
			}
			budget := "1"
			if kind == "missing corridor" {
				budget = "5"
			}
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: budget})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
			ds, _ := env.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			wf := layoutOwnershipWorkflow(workflow.GroupFormBranches, false)
			require.NoError(t, ds.workflowSvc.Create(ctx, wf.Name, wf))
			before, err := os.ReadFile(env.privatePath(wf.Name))
			require.NoError(t, err)
			saved := layoutE2EManifest(t, env.privatePath(wf.Name))
			row := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), 5*time.Minute)
			require.Equal(t, layout.StateNeedsAttention, row.State, "%s: %+v", row.FailureReason, row.Findings)
			if kind == "missing corridor" {
				require.Equal(t, 5, row.AttemptsUsed)
				require.Equal(t, "budget", row.FailureReason)
			} else {
				require.Equal(t, 1, row.AttemptsUsed)
			}
			best, err := ds.layout.store.Result(ctx, row.Revision, row.ResultGeneration)
			require.NoError(t, err)
			require.NotEmpty(t, best.Geometry.GroupElements)
			require.NotNil(t, best.Review)
			require.False(t, best.Review.Rendered)
			var ev string
			for _, f := range best.Review.Findings {
				ev += f.Evidence + "\n"
			}
			if kind == "missing report" {
				require.Contains(t, ev, "group element/connector/crossing coverage")
			} else if kind == "missing corridor" {
				require.Contains(t, ev, "corridor ink is missing")
			} else {
				require.Contains(t, ev, "outline ink is missing")
				require.NotContains(t, ev, "node text")
			}
			require.NotEmpty(t, captured)
			actual := <-captured
			require.NotEmpty(t, actual.Report.GroupElements)
			require.NotEmpty(t, actual.Report.GroupConnectors)
			view, err := ds.layout.query.WorkflowLayout(ctx, wf.Name)
			require.NoError(t, err)
			require.True(t, view.Unreviewed)
			require.True(t, view.RetryEligible)
			require.Nil(t, view.Evidence)
			require.Equal(t, best.Geometry, *view.Geometry)
			id := surfaceSnapshot(t, ds, saved)
			snap, err := ds.layout.query.InstanceLayout(ctx, id)
			require.NoError(t, err)
			require.Equal(t, view, snap)
			// Retry keeps the real unreviewed best until fresh, fully painted
			// evidence replaces it. A later save cannot move this snapshot.
			repaired.Store(true)
			ack, err := ds.layout.query.RetryInstance(ctx, id, saved.SavedRevision())
			require.NoError(t, err)
			require.Equal(t, row.JobGeneration+1, ack.JobGeneration)
			pending, err := ds.layout.query.InstanceLayout(ctx, id)
			require.NoError(t, err)
			require.True(t, pending.Unreviewed)
			require.Equal(t, best.Geometry, *pending.Geometry, "Retry retains the actual best while fresh evidence is pending")
			retry := layoutRenderWaitTerminal(t, ds, row.Revision, time.Minute)
			requireGroupReviewCheckpoint(t, retry)
			require.Equal(t, ack.JobGeneration, retry.ResultGeneration)
			require.ErrorIs(t, ds.layout.store.Complete(ctx, layout.Fence{Revision: row.Revision, Generation: row.JobGeneration}, layout.Completion{State: layout.StatePassed}, time.Now()), layout.ErrStaleGeneration)
			reviewed, err := ds.layout.query.InstanceLayout(ctx, id)
			require.NoError(t, err)
			require.False(t, reviewed.Unreviewed)
			require.False(t, reviewed.RetryEligible)
			require.Equal(t, layout.GeometryDigest(best.Geometry), layout.GeometryDigest(*reviewed.Geometry))
			after, err := os.ReadFile(env.privatePath(wf.Name))
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
