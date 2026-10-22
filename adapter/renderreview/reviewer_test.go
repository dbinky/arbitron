package renderreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var reviewClock = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// reviewGeometry is a small candidate covering every edge kind the reviewer
// inspects: a labelled forward branch (0), a right-gutter loop return (1) and a
// forward edge (2) that crosses the loop return with a recorded crossing.
func reviewGeometry() layout.Geometry {
	return layout.Geometry{
		Format:   layout.FormatV2,
		Contract: layout.DefaultContract(),
		Options:  layout.DefaultOptions(),
		Nodes: []layout.NodeBox{
			{ID: "a", Box: layout.Rect{X: 40, Y: 40, W: 200, H: 60}, Text: "Start", TypeCaption: "agent call", TextLines: 1, Rank: 0},
			{ID: "b", Box: layout.Rect{X: 40, Y: 200, W: 200, H: 60}, Text: "Review", TypeCaption: "agent call", TextLines: 1, Rank: 1},
			{ID: "c", Box: layout.Rect{X: 300, Y: 200, W: 200, H: 60}, Text: "Ship", TypeCaption: "transform", TextLines: 1, Rank: 1},
		},
		Edges: []layout.EdgeRoute{
			{
				Ordinal: 0, Source: "a", Target: "b", Output: "yes", Kind: layout.EdgeForward,
				Points:       []layout.Point{{X: 140, Y: 100}, {X: 140, Y: 200}},
				SourceAttach: layout.Attachment{NodeID: "a", ElementID: layout.GroupElementID("a", "header", ""), Side: layout.SideBottom, Point: layout.Point{X: 140, Y: 100}},
				TargetAttach: layout.Attachment{NodeID: "b", ElementID: layout.GroupElementID("b", "header", ""), Side: layout.SideTop, Point: layout.Point{X: 140, Y: 200}},
				Text:         &layout.LabelBox{Text: "yes", Box: layout.Rect{X: 150, Y: 136, W: 34, H: 22}, Lines: 1},
			},
			{
				Ordinal: 1, Source: "b", Target: "a", Kind: layout.EdgeLoopReturn, Side: layout.SideRight, Lane: 1,
				Points:       []layout.Point{{X: 210, Y: 260}, {X: 210, Y: 285}, {X: 270, Y: 285}, {X: 270, Y: 20}, {X: 140, Y: 20}, {X: 140, Y: 40}},
				SourceAttach: layout.Attachment{NodeID: "b", ElementID: layout.GroupElementID("b", "header", ""), Side: layout.SideBottom, Point: layout.Point{X: 210, Y: 260}},
				TargetAttach: layout.Attachment{NodeID: "a", ElementID: layout.GroupElementID("a", "header", ""), Side: layout.SideTop, Point: layout.Point{X: 140, Y: 40}},
			},
			{
				Ordinal: 2, Source: "a", Target: "c", Kind: layout.EdgeForward,
				Points:       []layout.Point{{X: 200, Y: 100}, {X: 200, Y: 150}, {X: 400, Y: 150}, {X: 400, Y: 200}},
				SourceAttach: layout.Attachment{NodeID: "a", ElementID: layout.GroupElementID("a", "header", ""), Side: layout.SideBottom, Point: layout.Point{X: 200, Y: 100}},
				TargetAttach: layout.Attachment{NodeID: "c", ElementID: layout.GroupElementID("c", "header", ""), Side: layout.SideTop, Point: layout.Point{X: 400, Y: 200}},
			},
		},
		Crossings: []layout.Crossing{{Point: layout.Point{X: 270, Y: 150}, Over: 2, Under: 1}},
		Canvas:    layout.Rect{X: 0, Y: 0, W: 560, H: 320},
	}
}

func testReviewer() *ReadabilityReviewer {
	r := NewReadabilityReviewer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.now = func() time.Time { return reviewClock }
	return r
}

func withCriterion(fs []layout.Finding, c layout.Criterion) []layout.Finding {
	var out []layout.Finding
	for _, f := range fs {
		if f.Criterion == c {
			out = append(out, f)
		}
	}
	return out
}

func TestReadabilityReviewer_ImplementsThePort(t *testing.T) {
	var _ layout.Reviewer = (*ReadabilityReviewer)(nil)
	r := NewReadabilityReviewer(nil)
	require.NotNil(t, r.logger, "a nil logger defaults to slog.Default()")
	require.NotNil(t, r.now)
}

func TestReadabilityReviewer_CleanRenderingPasses(t *testing.T) {
	fx := newReviewFixture(t)
	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)

	assert.True(t, rv.Rendered)
	assert.Zero(t, layout.BlockingCount(rv.Findings), "clean rendering must have no blocking findings: %+v", rv.Findings)
	require.Len(t, rv.Findings, 1, "only the advisory crossing count remains")
	assert.Equal(t, layout.CriterionCrossings, rv.Findings[0].Criterion)
	assert.Equal(t, layout.SeverityAdvisory, rv.Findings[0].Severity)
	assert.Equal(t, layout.SourceReview, rv.Findings[0].Source)
	assert.Equal(t, "1 crossing(s) remain in the rendered layout", rv.Findings[0].Evidence)

	ev := rv.Evidence
	assert.Equal(t, layout.GeometryDigest(fx.geo), ev.GeometryDigest)
	assert.Equal(t, layout.ContractVersion, ev.RendererVersion)
	assert.Equal(t, fx.geo.Contract, ev.Contract)
	assert.True(t, ev.FontsReady)
	assert.Equal(t, 0.9, ev.Zoom)
	assert.Equal(t, len(fx.tiles), ev.Tiles)
	assert.Equal(t, fx.req.Rendering.Artifacts, ev.Artifacts)
	assert.Equal(t, reviewClock, ev.ReviewedAt)
}

func TestReadabilityReviewer_EvidenceIsRequired(t *testing.T) {
	fx := newReviewFixture(t)
	req := layout.ReviewRequest{Revision: "rev-1", Attempt: 1, Geometry: fx.geo}
	rv, err := testReviewer().Review(context.Background(), req)
	require.NoError(t, err, "an empty rendering is reported in-band, never as a pass")
	assert.False(t, rv.Rendered)
	assert.NotZero(t, layout.BlockingCount(rv.Findings))
	assert.NotEmpty(t, withCriterion(rv.Findings, layout.CriterionRenderMismatch))
	assert.NotEmpty(t, withCriterion(rv.Findings, layout.CriterionCoverage))
	assert.Empty(t, rv.Evidence.GeometryDigest, "evidence describes what was examined: nothing")
}

func TestReadabilityReviewer_IntegrityMismatchesAreNotRendered(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(fx *reviewFixture)
		criterion layout.Criterion
		nodes     []string
		edges     []int
	}{
		{"other revision", func(fx *reviewFixture) { fx.req.Rendering.Revision = "rev-0" }, layout.CriterionRenderMismatch, nil, nil},
		{"other attempt", func(fx *reviewFixture) { fx.req.Rendering.Attempt = 2 }, layout.CriterionRenderMismatch, nil, nil},
		{"other geometry", func(fx *reviewFixture) { fx.req.Rendering.GeometryDigest = "deadbeef" }, layout.CriterionRenderMismatch, nil, nil},
		{"other surface version", func(fx *reviewFixture) { fx.req.Rendering.Report.SurfaceVersion = "arranged-renderer/0" }, layout.CriterionRenderMismatch, nil, nil},
		{"other contract", func(fx *reviewFixture) { fx.req.Rendering.Report.Contract.NodeLabelFontPx = 12 }, layout.CriterionRenderMismatch, nil, nil},
		{"other zoom", func(fx *reviewFixture) { fx.req.Rendering.Report.Zoom = 0.5 }, layout.CriterionRenderMismatch, nil, nil},
		{"other canvas", func(fx *reviewFixture) { fx.req.Rendering.Report.Canvas.H = 100 }, layout.CriterionRenderMismatch, nil, nil},
		{"missing node", func(fx *reviewFixture) {
			fx.req.Rendering.Report.Nodes = fx.req.Rendering.Report.Nodes[:2]
		}, layout.CriterionCoverage, []string{"c"}, nil},
		{"duplicated node", func(fx *reviewFixture) {
			r := &fx.req.Rendering.Report
			r.Nodes = append(r.Nodes, r.Nodes[0])
		}, layout.CriterionCoverage, []string{"a"}, nil},
		{"unknown node", func(fx *reviewFixture) { fx.req.Rendering.Report.Nodes[2].ID = "zz" }, layout.CriterionCoverage, []string{"zz"}, nil},
		{"missing edge", func(fx *reviewFixture) {
			fx.req.Rendering.Report.Edges = fx.req.Rendering.Report.Edges[:2]
		}, layout.CriterionCoverage, nil, nil},
		{"edge out of order", func(fx *reviewFixture) {
			e := fx.req.Rendering.Report.Edges
			e[0], e[1] = e[1], e[0]
		}, layout.CriterionCoverage, nil, []int{1}},
		{"missing crossing", func(fx *reviewFixture) { fx.req.Rendering.Report.Crossings = nil }, layout.CriterionCoverage, nil, nil},
		{"runtime state not exercised", func(fx *reviewFixture) {
			fx.req.Rendering.Report.States = fx.req.Rendering.Report.States[:3]
		}, layout.CriterionCoverage, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newReviewFixture(t)
			tc.mutate(fx)
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			assert.False(t, rv.Rendered, "a mismatched or incomplete rendering is not rendered evidence")
			got := withCriterion(rv.Findings, tc.criterion)
			require.NotEmpty(t, got, "want a %s finding, got %+v", tc.criterion, rv.Findings)
			assert.Equal(t, layout.SeverityBlocking, got[0].Severity)
			assert.Equal(t, layout.SourceReview, got[0].Source)
			assert.NotEmpty(t, got[0].Evidence)
			if tc.nodes != nil {
				assert.Equal(t, tc.nodes, got[0].NodeIDs)
			}
			if tc.edges != nil {
				assert.Equal(t, tc.edges, got[0].EdgeOrdinals)
			}
		})
	}
}

func TestReadabilityReviewer_EvidenceDescribesTheExaminedRendering(t *testing.T) {
	fx := newReviewFixture(t)
	fx.req.Rendering.GeometryDigest = "deadbeef"
	fx.req.Rendering.Report.FontFamilyResolved = false
	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", rv.Evidence.GeometryDigest, "evidence never claims the candidate's digest for another rendering")
	assert.False(t, rv.Evidence.FontsReady, "fonts count as ready only when the contract family resolved")
}

func TestReadabilityReviewer_UnreadableArtifactsAreAnError(t *testing.T) {
	cases := map[string]func(fx *reviewFixture){
		"missing file": func(fx *reviewFixture) {
			require.NoError(t, os.Remove(fx.req.Rendering.Artifacts[2].Path))
		},
		"hash mismatch": func(fx *reviewFixture) { fx.req.Rendering.Artifacts[1].SHA256 = "00" },
		"not a png": func(fx *reviewFixture) {
			a := &fx.req.Rendering.Artifacts[1]
			require.NoError(t, os.WriteFile(a.Path, []byte("not a png"), 0o600))
			sum := sha256.Sum256([]byte("not a png"))
			a.SHA256 = hex.EncodeToString(sum[:])
		},
		"relative path": func(fx *reviewFixture) { fx.req.Rendering.Artifacts[1].Path = "tile-000.png" },
		"no artifacts":  func(fx *reviewFixture) { fx.req.Rendering.Artifacts = nil },
		"no overview":   func(fx *reviewFixture) { fx.req.Rendering.Artifacts = fx.req.Rendering.Artifacts[1:] },
		"no tiles":      func(fx *reviewFixture) { fx.req.Rendering.Artifacts = fx.req.Rendering.Artifacts[:1] },
		"two overviews": func(fx *reviewFixture) {
			a := fx.req.Rendering.Artifacts
			fx.req.Rendering.Artifacts = append(a, a[0])
		},
		"unknown kind": func(fx *reviewFixture) { fx.req.Rendering.Artifacts[1].Kind = "thumbnail" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newReviewFixture(t)
			mutate(fx)
			_, err := testReviewer().Review(context.Background(), fx.req)
			require.ErrorIs(t, err, ErrEvidenceUnavailable, "unreadable screenshots are a retryable review failure, never a pass")
		})
	}
}

func TestReadabilityReviewer_Cancelled(t *testing.T) {
	fx := newReviewFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testReviewer().Review(ctx, fx.req)
	require.ErrorIs(t, err, context.Canceled)
}
