package renderreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"testing"
)

func repaintArtifacts(t *testing.T, fx *reviewFixture, regions []layout.Rect, ink color.Color) {
	t.Helper()
	for i := range fx.req.Rendering.Artifacts {
		a := &fx.req.Rendering.Artifacts[i]
		raw, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		src, err := png.Decode(bytes.NewReader(raw))
		require.NoError(t, err)
		img := image.NewRGBA(src.Bounds())
		draw.Draw(img, img.Bounds(), src, src.Bounds().Min, draw.Src)
		for _, r := range regions {
			rect := image.Rect(int(math.Floor((r.X-a.Region.X)*a.Scale)), int(math.Floor((r.Y-a.Region.Y)*a.Scale)), int(math.Ceil((r.Right()-a.Region.X)*a.Scale)), int(math.Ceil((r.Bottom()-a.Region.Y)*a.Scale)))
			draw.Draw(img, rect, &image.Uniform{C: ink}, image.Point{}, draw.Src)
		}
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		require.NoError(t, os.WriteFile(a.Path, buf.Bytes(), 0600))
		sum := sha256.Sum256(buf.Bytes())
		a.SHA256 = hex.EncodeToString(sum[:])
	}
}
func TestReviewCapture_RequiresVisibleFeatures(t *testing.T) {
	cases := []struct {
		name      string
		regions   []layout.Rect
		ink       color.Color
		criterion layout.Criterion
	}{
		{"route middle", []layout.Rect{{X: 320, Y: 147, W: 30, H: 6}}, color.RGBA{26, 26, 46, 255}, layout.CriterionMergedLines},
		{"arrow", []layout.Rect{{X: 393, Y: 187, W: 14, H: 11}}, color.RGBA{26, 26, 46, 255}, layout.CriterionArrowDirection},
		{"label ink", []layout.Rect{{X: 53, Y: 68, W: 34, H: 20}}, color.RGBA{26, 26, 46, 255}, layout.CriterionTextUnreadable},
		{"gap filled", []layout.Rect{{X: 269, Y: 145, W: 2, H: 10}}, color.RGBA{124, 140, 245, 255}, layout.CriterionAmbiguousCrossing},
		{"rectangles only", []layout.Rect{{X: 0, Y: 0, W: 560, H: 320}}, color.RGBA{26, 26, 46, 255}, layout.CriterionTextUnreadable},
		{"bright rectangle instead of glyphs", []layout.Rect{{X: 54, Y: 69.5, W: 30.56, H: 16}}, color.RGBA{224, 224, 224, 255}, layout.CriterionTextUnreadable},
		{"all white", []layout.Rect{{X: 0, Y: 0, W: 560, H: 320}}, color.White, layout.CriterionCoverage},
		{"arrow wings only", []layout.Rect{{X: 394, Y: 189, W: 4, H: 6}, {X: 402, Y: 189, W: 4, H: 6}}, color.RGBA{26, 26, 46, 255}, layout.CriterionArrowDirection},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newReviewFixture(t)
			if tc.name == "rectangles only" {
				repaintArtifacts(t, fx, tc.regions, tc.ink)
				for _, n := range fx.geo.Nodes {
					repaintArtifacts(t, fx, []layout.Rect{n.Box}, color.RGBA{50, 50, 70, 255})
				}
			} else {
				repaintArtifacts(t, fx, tc.regions, tc.ink)
			}
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			require.NotEmpty(t, withCriterion(rv.Findings, tc.criterion), "%+v", rv.Findings)
			require.False(t, rv.Rendered)
		})
	}
	t.Run("parallel lane removed", func(t *testing.T) {
		fx := newReviewFixtureFor(t, fixtureScenes()[2])
		repaintArtifacts(t, fx, []layout.Rect{{X: 107, Y: 120, W: 6, H: 60}}, color.RGBA{26, 26, 46, 255})
		rv, err := testReviewer().Review(context.Background(), fx.req)
		require.NoError(t, err)
		fs := withCriterion(rv.Findings, layout.CriterionMergedLines)
		require.NotEmpty(t, fs)
		require.Contains(t, fs[len(fs)-1].EdgeOrdinals, 3)
	})
}
func TestReviewCapture_LowContrastInkCannotPass(t *testing.T) {
	fx := newReviewFixture(t)
	repaintArtifacts(t, fx, []layout.Rect{{X: 54, Y: 69.5, W: 30.56, H: 16}}, color.RGBA{80, 80, 80, 255})
	for x := 54.0; x < 84; x += 10 {
		repaintArtifacts(t, fx, []layout.Rect{{X: x, Y: 69.5, W: 5, H: 16}}, color.RGBA{120, 120, 120, 255})
	}
	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	require.NotEmpty(t, withCriterion(rv.Findings, layout.CriterionTextUnreadable), "%+v", rv.Findings)
}

type cancelDuringReview struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *cancelDuringReview) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestReviewCapture_CancellationDuringPixelChecks(t *testing.T) {
	fx := newReviewFixture(t)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelDuringReview{Context: base, cancel: cancel, remaining: 20}
	_, err := testReviewer().Review(ctx, fx.req)
	require.ErrorIs(t, err, context.Canceled)
}

func TestReviewCapture_Integrity(t *testing.T) {
	for _, name := range []string{"wrong scale", "wrong size", "missing tile", "abutting", "nonfinite"} {
		t.Run(name, func(t *testing.T) {
			fx := newReviewFixture(t)
			a := &fx.req.Rendering.Artifacts[1]
			switch name {
			case "wrong scale":
				a.Scale = .5
			case "wrong size":
				a.Region.W -= 20
			case "missing tile":
				fx.req.Rendering.Artifacts = fx.req.Rendering.Artifacts[:2]
			case "abutting":
				for i := 1; i < len(fx.req.Rendering.Artifacts); i++ {
					fx.req.Rendering.Artifacts[i].Region.X = float64(i-1) * 400
				}
			case "nonfinite":
				a.Scale = math.NaN()
			}
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			require.False(t, rv.Rendered)
			require.NotEmpty(t, withCriterion(rv.Findings, layout.CriterionCoverage))
		})
	}
}
