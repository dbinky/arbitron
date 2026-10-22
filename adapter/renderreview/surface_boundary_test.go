package renderreview

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type withoutLabelSubset struct{ fs.FS }

func (f withoutLabelSubset) Open(name string) (fs.File, error) {
	if strings.Contains(name, "inter-latin-600-normal-") && (strings.HasSuffix(name, ".woff2") || strings.HasSuffix(name, ".woff")) { // the face and its CSS .woff fallback
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return f.FS.Open(name)
}

func TestBrowserRenderer_RealSurfaceRejectsMissingFontAndUnsupportedGlyph(t *testing.T) {
	for _, name := range []string{"missing font", "unsupported glyph"} {
		t.Run(name, func(t *testing.T) {
			var assets fs.FS = embeddedSurface(t)
			req := measureReq("actual-label")
			if name == "missing font" {
				assets = withoutLabelSubset{assets}
			} else {
				req.Items[0].Text = "\U0010ffff"
			}
			b := newTestRenderer(t, testBrowserConfig(t, assets))
			_, err := b.Measure(context.Background(), req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "render surface failed:")
			assert.NotErrorIs(t, err, ErrBrowserUnavailable)
		})
	}
}

func TestBrowserRenderer_RealSurfaceRejectsMissingRenderSubset(t *testing.T) {
	assets := embeddedSurface(t)
	b := newTestRenderer(t, testBrowserConfig(t, assets))
	wf := loadSampleWorkflow(t, "pr-review")
	g, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	m, err := b.Measure(context.Background(), layout.MeasureRequestFor(g, layout.DefaultContract()))
	require.NoError(t, err)
	geo, err := layout.Arrange(context.Background(), g, m, layout.DefaultContract(), layout.DefaultOptions())
	require.NoError(t, err)
	types := map[string]string{}
	for _, n := range wf.Nodes {
		types[n.ID] = string(n.Type)
	}
	missing := newTestRenderer(t, testBrowserConfig(t, withoutLabelSubset{assets}))
	_, err = missing.Render(context.Background(), layout.NewRenderRequest("missing-subset", 1, geo, types))
	require.ErrorContains(t, err, "font asset unavailable")
}

func TestBrowserRenderer_RealSurfaceNilMeasurementsCannotArrange(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	wf := loadSampleWorkflow(t, "pr-review")
	graph, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	req := layout.MeasureRequestFor(graph, layout.DefaultContract())
	measured, err := b.Measure(context.Background(), req)
	require.NoError(t, err)
	measured.Items = nil
	_, err = layout.Arrange(context.Background(), graph, measured, layout.DefaultContract(), layout.DefaultOptions())
	assert.Error(t, err, "dropping production measurements must fail at the geometry sink")
}
