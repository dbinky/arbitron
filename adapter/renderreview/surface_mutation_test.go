package renderreview

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only the HTML is overlaid. All renderer code, CSS, fonts and collectors are
// the production embedded build; mutations act on its actual DOM before probes.
type surfaceHTML struct {
	fs.FS
	html []byte
}

func (f surfaceHTML) Open(name string) (fs.File, error) {
	if name == surfacePage {
		return fstest.MapFS{surfacePage: {Data: f.html}}.Open(name)
	}
	return f.FS.Open(name)
}

func TestBrowserRenderer_RealSurfaceDOMMutationsReachReport(t *testing.T) {
	cases := []struct {
		name, style, script string
		check               func(*testing.T, layout.Rendering, layout.Geometry)
	}{
		{name: "ancestor opacity", style: `#render-root {opacity:0!important}`, check: func(t *testing.T, r layout.Rendering, _ layout.Geometry) {
			for _, n := range r.Report.Nodes {
				assert.False(t, n.Label.Visible)
				assert.False(t, n.Type.Visible)
			}
			for _, e := range r.Report.Edges {
				assert.False(t, e.Visible)
				assert.False(t, e.HasArrow)
			}
		}},
		{name: "path opacity", style: `path[data-edge-path] {opacity:0!important}`, check: func(t *testing.T, r layout.Rendering, _ layout.Geometry) {
			for _, e := range r.Report.Edges {
				assert.False(t, e.Visible)
				assert.False(t, e.HasArrow)
			}
		}},
		{name: "hidden marker", style: `marker {opacity:0!important}`, check: func(t *testing.T, r layout.Rendering, _ layout.Geometry) {
			for _, e := range r.Report.Edges {
				assert.False(t, e.HasArrow)
			}
		}},
		{name: "actual content", script: `for(const e of document.querySelectorAll('[data-node-text="label"]'))if(e.textContent!=='Changed')e.textContent='Changed'`, check: func(t *testing.T, r layout.Rendering, _ layout.Geometry) {
			for _, n := range r.Report.Nodes {
				assert.Equal(t, "Changed", n.Label.Content)
			}
		}},
		{name: "runtime label displacement", style: `[data-arranged-canvas]:has([data-state="running"]) [data-edge-label] {margin-left:5px!important}`, check: func(t *testing.T, r layout.Rendering, _ layout.Geometry) {
			found := false
			for _, s := range r.Report.States {
				if s.State == "running" {
					found = true
					assert.False(t, s.NodeBoxesStable)
					assert.NotEmpty(t, s.Issues)
				}
			}
			assert.True(t, found)
		}},
		{name: "runtime middle path", script: `if(document.querySelector('[data-state="running"]'))for(const e of document.querySelectorAll('[data-edge-path="0"], [data-edge-overlay="0"]')){const d=e.getAttribute('d');if(!e.dataset.original)e.dataset.original=d;const ns=e.dataset.original.match(/-?\d+(?:\.\d+)?/g).map(Number);const x=ns[0],y=ns[1],ex=ns.at(-2),ey=ns.at(-1);const changed='M '+x+','+y+' L '+(x+5)+','+y+' L '+(x+5)+','+ey+' L '+ex+','+ey;if(d!==changed)e.setAttribute('d',changed)}`, check: func(t *testing.T, r layout.Rendering, _ layout.Geometry) {
			for _, s := range r.Report.States {
				if s.State == "running" {
					assert.False(t, s.NodeBoxesStable)
					assert.NotEmpty(t, s.Issues)
				}
			}
		}},
		{name: "reversed marker", script: `for(const e of document.querySelectorAll('marker'))if(e.getAttribute('orient')!=='180')e.setAttribute('orient','180')`, check: func(t *testing.T, r layout.Rendering, _ layout.Geometry) {
			for _, e := range r.Report.Edges {
				assert.False(t, e.HasArrow)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assets := embeddedSurface(t)
			html, err := fs.ReadFile(assets, surfacePage)
			require.NoError(t, err)
			injection := "<style>" + tc.style + "</style>"
			if tc.script != "" {
				injection += `<script>new MutationObserver(()=>{` + tc.script + `}).observe(document.documentElement,{subtree:true,childList:true,attributes:true,attributeFilter:['data-state','d','orient']})</script>`
			}
			assets = surfaceHTML{assets, []byte(strings.Replace(string(html), "</head>", injection+"</head>", 1))}
			b := newTestRenderer(t, testBrowserConfig(t, assets))
			wf := loadSampleWorkflow(t, "pr-review")
			graph, err := layout.BuildGraph(wf)
			require.NoError(t, err)
			metrics, err := b.Measure(context.Background(), layout.MeasureRequestFor(graph, layout.DefaultContract()))
			require.NoError(t, err)
			geo, err := layout.Arrange(context.Background(), graph, metrics, layout.DefaultContract(), layout.DefaultOptions())
			require.NoError(t, err)
			types := map[string]string{}
			for _, n := range wf.Nodes {
				types[n.ID] = string(n.Type)
			}
			r, err := b.Render(context.Background(), layout.NewRenderRequest("dom-mutation", 1, geo, types))
			require.NoError(t, err)
			require.NotEmpty(t, r.Report.Nodes)
			require.NotEmpty(t, r.Report.Edges)
			tc.check(t, r, geo)
		})
	}
}
