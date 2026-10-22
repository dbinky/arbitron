package renderreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image/color"
	"os"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

func TestReadabilityReviewer_GroupRasterMutationsBlock(t *testing.T) {
	for _, tc := range []struct {
		name, scene string
		region      func(*reviewFixture) []layout.Rect
		ink         color.Color
		criterion   layout.Criterion
	}{
		{"container border", "branches", func(f *reviewFixture) []layout.Rect {
			for _, e := range f.geo.GroupElements {
				if e.Role == "container" {
					return []layout.Rect{{X: e.Box.X - 1, Y: e.Box.Y + 15, W: 4, H: 35}}
				}
			}
			return nil
		}, color.RGBA{26, 26, 46, 255}, layout.CriterionRenderMismatch},
		{"lane border", "branches", func(f *reviewFixture) []layout.Rect {
			for _, e := range f.geo.GroupElements {
				if e.Role == "lane" {
					return []layout.Rect{{X: e.Box.X - 1, Y: e.Box.Y + 15, W: 4, H: 35}}
				}
			}
			return nil
		}, color.RGBA{26, 26, 46, 255}, layout.CriterionRenderMismatch},
		{"footer label", "branches", func(f *reviewFixture) []layout.Rect {
			for _, e := range f.req.Rendering.Report.GroupElements {
				for _, w := range f.geo.GroupElements {
					if w.ID == e.ID && w.Role == "footer" {
						return e.Texts[0].Text.Glyphs
					}
				}
			}
			return nil
		}, color.RGBA{30, 30, 56, 255}, layout.CriterionTextUnreadable},
		{"card background", "workflows", func(f *reviewFixture) []layout.Rect {
			for _, e := range f.geo.GroupElements {
				if e.Role == "task" {
					return []layout.Rect{{X: e.Box.X + 4, Y: e.Box.Bottom() - 20, W: e.Box.W - 8, H: 15}}
				}
			}
			return nil
		}, color.RGBA{26, 26, 46, 255}, layout.CriterionRenderMismatch},
		{"synthetic middle", "branches", func(f *reviewFixture) []layout.Rect {
			s := f.req.Rendering.Report.GroupConnectors[0].Segments[1]
			p := atDistance(s, segmentLength(s)/2)
			return []layout.Rect{{X: p.X - 12, Y: p.Y - 3, W: 24, H: 6}}
		}, color.RGBA{26, 26, 46, 255}, layout.CriterionMergedLines},
		{"synthetic marker", "branches", func(f *reviewFixture) []layout.Rect {
			p := f.req.Rendering.Report.GroupConnectors[0].End
			return []layout.Rect{{X: p.X - 7, Y: p.Y - 12, W: 14, H: 11}}
		}, color.RGBA{26, 26, 46, 255}, layout.CriterionArrowDirection},
		{"connector crossing gap", "loop", func(f *reviewFixture) []layout.Rect {
			require.NotEmpty(t, f.geo.GroupCrossings)
			p := f.geo.GroupCrossings[1].Point
			return []layout.Rect{{X: p.X - 1, Y: p.Y - 5, W: 2, H: 10}}
		}, color.RGBA{138, 138, 176, 255}, layout.CriterionAmbiguousCrossing},
		{"real-under mixed gap", "loop", func(f *reviewFixture) []layout.Rect {
			c := f.geo.GroupCrossings[0]
			require.Equal(t, layout.ManifestRouteKey(7), c.Under)
			return []layout.Rect{{X: c.Point.X - 1, Y: c.Point.Y - 5, W: 2, H: 10}}
		}, color.RGBA{124, 140, 245, 255}, layout.CriterionAmbiguousCrossing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newGroupReviewFixture(t, tc.scene)
			rs := tc.region(fx)
			require.NotEmpty(t, rs)
			repaintArtifacts(t, fx, rs, tc.ink)
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			fs := withCriterion(rv.Findings, tc.criterion)
			require.NotEmpty(t, fs, "%+v", rv.Findings)
			require.False(t, rv.Rendered)
			requireFindingsAcceptedByCoordinator(t, fx.geo, rv.Findings)
			for _, f := range fs {
				require.NotEmpty(t, f.NodeIDs)
				require.Empty(t, f.EdgeOrdinals, "synthetic defects never invent manifest ordinals")
			}
		})
	}
	for _, name := range []string{"missing", "unreadable", "hash mismatch"} {
		t.Run(name, func(t *testing.T) {
			fx := newGroupReviewFixture(t, "workflows")
			a := &fx.req.Rendering.Artifacts[1]
			switch name {
			case "missing":
				require.NoError(t, os.Remove(a.Path))
			case "unreadable":
				require.NoError(t, os.WriteFile(a.Path, []byte("not png"), 0600))
				sum := sha256.Sum256([]byte("not png"))
				a.SHA256 = hex.EncodeToString(sum[:])
			case "hash mismatch":
				a.SHA256 = "wrong"
			}
			_, err := testReviewer().Review(context.Background(), fx.req)
			require.ErrorIs(t, err, ErrEvidenceUnavailable)
			if name == "unreadable" {
				require.ErrorContains(t, err, "not a PNG")
			}
		})
	}
}
