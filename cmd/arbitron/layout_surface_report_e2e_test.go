package main

import (
	"encoding/json"
	"image/png"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Overlay only the page shell. The root still constructs the production browser,
// collector and raster reviewer using the embedded JS/CSS/fonts and saved input.
type reportSurfaceAssets struct {
	fs.FS
	html []byte
}

func (a reportSurfaceAssets) Open(name string) (fs.File, error) {
	if name == "render.html" {
		return fstest.MapFS{name: {Data: a.html}}.Open(name)
	}
	return a.FS.Open(name)
}

func reportCrossingWorkflow() *workflow.Workflow {
	wf := &workflow.Workflow{Name: "surface-report"}
	// K3,3 between the middle ranks forces actual crossing gaps; no geometry,
	// measurements or report fields are supplied by the test.
	for _, id := range []string{"start", "a", "b", "c", "d", "e", "f", "end"} {
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: id, Type: workflow.NodeTypeTransform, Label: id, Entrypoint: id == "start", Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: id, Operation: "set", Value: true}}}})
	}
	for _, a := range []string{"a", "b", "c"} {
		wf.Edges = append(wf.Edges, workflow.Edge{Source: "start", Target: a})
		for _, b := range []string{"d", "e", "f"} {
			wf.Edges = append(wf.Edges, workflow.Edge{Source: a, Target: b})
		}
	}
	for _, b := range []string{"d", "e", "f"} {
		wf.Edges = append(wf.Edges, workflow.Edge{Source: b, Target: "end"})
	}
	return wf
}

func TestLayoutRender_RealSurfaceReportDefectsReachSavedResults(t *testing.T) {
	browser := requireBrowserE2E(t)
	cases := []struct {
		name, style, script string
		criterion           layout.Criterion
		crossings           bool
	}{
		{name: "baseline"},
		{name: "actual text", script: `for(const e of document.querySelectorAll('[data-node-text="label"]'))if(e.textContent!=='Changed')e.textContent='Changed'`, criterion: layout.CriterionRenderMismatch},
		{name: "ancestor opacity", style: `#render-root {opacity:0!important}`, criterion: layout.CriterionTextUnreadable},
		{name: "hidden marker", style: `marker {opacity:0!important}`, criterion: layout.CriterionArrowDirection},
		{name: "rotated marker", script: `for(const e of document.querySelectorAll('marker'))if(e.getAttribute('orient')!=='180')e.setAttribute('orient','180')`, criterion: layout.CriterionArrowDirection},
		{name: "middle route", script: `for(const e of document.querySelectorAll('[data-edge-path="0"], [data-edge-overlay="0"]')){if(!e.dataset.original)e.dataset.original=e.getAttribute('d');const ns=e.dataset.original.match(/-?\d+(?:\.\d+)?/g).map(Number),x=ns[0],y=ns[1],ex=ns.at(-2),ey=ns.at(-1);const d='M '+x+','+y+' L '+(x+5)+','+y+' L '+(x+5)+','+ey+' L '+ex+','+ey;if(e.getAttribute('d')!==d)e.setAttribute('d',d)}`, criterion: layout.CriterionRenderMismatch},
		{name: "runtime branch label", style: `[data-arranged-canvas]:has([data-state="running"]) [data-edge-label] {margin-left:5px!important}`, criterion: layout.CriterionRenderMismatch},
		{name: "runtime crossing gaps", crossings: true, script: `if(document.querySelector('[data-state="running"]'))for(const e of document.querySelectorAll('[data-edge-path], [data-edge-overlay]')){const d=e.getAttribute('d');if(!e.dataset.original)e.dataset.original=d;const changed=e.dataset.original.replaceAll(' M ', ' L ');if(d!==changed)e.setAttribute('d',changed)}`, criterion: layout.CriterionRenderMismatch},
		{name: "all white capture", style: `html,body {background:white!important} #render-root {opacity:0!important}`, criterion: layout.CriterionTextUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originalAssets := layoutRenderAssets
			assets := originalAssets()
			html, err := fs.ReadFile(assets, "render.html")
			require.NoError(t, err)
			injection := "<style>" + tc.style + "</style>"
			if tc.script != "" {
				injection += `<script>new MutationObserver(()=>{` + tc.script + `}).observe(document.documentElement,{subtree:true,childList:true,attributes:true,attributeFilter:['data-state','d','orient']})</script>`
			}
			layoutRenderAssets = func() fs.FS {
				return reportSurfaceAssets{assets, []byte(strings.Replace(string(html), "</head>", injection+"</head>", 1))}
			}
			t.Cleanup(func() { layoutRenderAssets = originalAssets })
			probe := &renderPortsProbe{}
			installRenderPortsProbe(t, probe)
			e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(e.root, "layout-artifacts"))
			ds, _ := e.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			wf := layoutE2EWorkflow("surface-report", "Café 測試 ✅")
			wf.Edges[0].Label = "Freigabe 審查"
			if tc.crossings {
				wf = reportCrossingWorkflow()
			}
			require.NoError(t, ds.workflowSvc.Create(ctx, wf.Name, wf))
			saved := layoutE2EManifest(t, e.privatePath(wf.Name))
			before, err := os.ReadFile(e.privatePath(wf.Name))
			require.NoError(t, err)
			row := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), time.Minute)
			want := layout.StateNeedsAttention
			if tc.criterion == "" {
				want = layout.StatePassed
			}
			require.Equal(t, want, row.State, "%s: %+v", row.FailureReason, row.Findings)
			require.Equal(t, 1, row.AttemptsUsed)
			result, err := ds.layout.store.Result(ctx, row.Revision, row.ResultGeneration)
			require.NoError(t, err)
			require.NotNil(t, result.Review)
			attempts := probe.observed()
			require.Len(t, attempts, 1)
			a := attempts[0]
			require.Equal(t, row.Revision, a.request.Revision)
			require.Equal(t, layout.GeometryDigest(result.Geometry), a.request.Rendering.GeometryDigest)
			captured, err := json.Marshal(saved)
			require.NoError(t, err)
			require.JSONEq(t, string(captured), string(row.Manifest))
			assertEvidenceOnDisk(t, e, a.review.Evidence)
			report := a.request.Rendering.Report
			require.Len(t, report.Nodes, len(saved.Nodes))
			require.Len(t, report.Edges, len(saved.Edges))
			view, err := ds.layout.query.WorkflowLayout(ctx, wf.Name)
			require.NoError(t, err)
			require.Equal(t, row.Revision, view.SavedRevision)
			if tc.criterion == "" {
				require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
				require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
				for i, n := range report.Nodes {
					require.Equal(t, saved.Nodes[i].Label, n.Label.Content)
					require.True(t, n.Label.Visible)
					require.NotEmpty(t, n.Label.Glyphs)
				}
			} else {
				require.Equal(t, layout.DisplayNeedsAttention, view.DisplayStatus)
				for label, findings := range map[string][]layout.Finding{"review": result.Review.Findings, "row": row.Findings, "query": view.Findings} {
					var matching []layout.Finding
					for _, f := range findings {
						if f.Criterion == tc.criterion {
							matching = append(matching, f)
						}
					}
					require.Positive(t, layout.BlockingCount(matching), "%s must retain the actual DOM defect: %+v", label, findings)
				}
			}
			switch tc.name {
			case "actual text":
				require.Equal(t, "Changed", report.Nodes[0].Label.Content)
			case "ancestor opacity", "all white capture":
				require.False(t, report.Nodes[0].Label.Visible)
			case "hidden marker", "rotated marker":
				require.False(t, report.Edges[0].HasArrow)
			case "middle route":
				points := result.Geometry.Edges[0].Points
				const tolerance = 0.01 // CSS/SVG screen-to-canvas floating-point round trip.
				require.InDelta(t, points[0].X, report.Edges[0].Start.X, tolerance)
				require.InDelta(t, points[0].Y, report.Edges[0].Start.Y, tolerance)
				require.InDelta(t, points[len(points)-1].X, report.Edges[0].End.X, tolerance)
				require.InDelta(t, points[len(points)-1].Y, report.Edges[0].End.Y, tolerance)
				require.Len(t, report.Edges[0].Segments, 3)
				require.InDelta(t, points[0].X+5, report.Edges[0].Segments[0].End.X, tolerance)
				require.Greater(t, math.Abs(points[1].X-report.Edges[0].Segments[0].End.X), tolerance)
			case "runtime branch label", "runtime crossing gaps":
				if tc.crossings {
					require.NotEmpty(t, result.Geometry.Crossings, "the saved graph must feed real gap probes")
					require.Len(t, report.Crossings, len(result.Geometry.Crossings))
				}
				var running *layout.StateProbe
				for i := range report.States {
					if report.States[i].State == "running" {
						running = &report.States[i]
					}
				}
				require.NotNil(t, running)
				require.False(t, running.NodeBoxesStable)
				if tc.crossings {
					require.Contains(t, running.Issues, "crossing separation changed")
				} else {
					require.Contains(t, running.Issues, "edge 0 label changed")
				}
			}
			if tc.name == "all white capture" {
				require.False(t, result.Review.Rendered)
				var blankOverview bool
				for _, finding := range result.Review.Findings {
					if finding.Criterion == layout.CriterionCoverage && finding.Evidence == "overview contains no visible graph" {
						blankOverview = true
					}
				}
				require.True(t, blankOverview, "the real PNG pixel check, independently of DOM visibility, must reject a blank capture")
				for _, artifact := range a.request.Rendering.Artifacts {
					f, err := os.Open(artifact.Path)
					require.NoError(t, err)
					img, err := png.Decode(f)
					require.NoError(t, f.Close())
					require.NoError(t, err)
					for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
						for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
							r, g, b, _ := img.At(x, y).RGBA()
							if r != 0xffff || g != 0xffff || b != 0xffff {
								t.Fatalf("%s is not a real all-white capture at %d,%d", artifact.Kind, x, y)
							}
						}
					}
				}
			}
			after, err := os.ReadFile(e.privatePath(wf.Name))
			require.NoError(t, err)
			require.Equal(t, before, after, "render probes must never change saved authoring bytes")
		})
	}
}
