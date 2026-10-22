package renderreview

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

func TestReadabilityReviewer_GroupRuntimeProbes(t *testing.T) {
	fx := newGroupReviewFixture(t, "nested")
	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	require.Zero(t, layout.BlockingCount(rv.Findings))
	names := append(append([]string(nil), layout.RuntimeStates...), layout.GroupProbeNames(fx.req.WorkflowGroups)...)
	require.Len(t, fx.req.Rendering.Report.States, len(names))
	for i, name := range names {
		require.Equal(t, name, fx.req.Rendering.Report.States[i].State)
		t.Run("missing/"+name, func(t *testing.T) {
			f := newGroupReviewFixture(t, "nested")
			ss := f.req.Rendering.Report.States
			f.req.Rendering.Report.States = append(ss[:i], ss[i+1:]...)
			rv, err := testReviewer().Review(context.Background(), f.req)
			require.NoError(t, err)
			require.False(t, rv.Rendered)
			require.NotEmpty(t, withCriterion(rv.Findings, layout.CriterionCoverage))
		})
	}
	for _, name := range []string{"group:run:gathering", "group:run:resolving_conflicts", "group:run:done", "group:task:unfinished"} {
		for _, kind := range []string{"geometry", "readability", "issues"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				f := newGroupReviewFixture(t, "nested")
				for i := range f.req.Rendering.Report.States {
					s := &f.req.Rendering.Report.States[i]
					if s.State == name {
						switch kind {
						case "geometry":
							s.NodeBoxesStable = false
						case "readability":
							s.LabelsReadable = false
						case "issues":
							s.Issues = []string{"group live row clipped"}
						}
					}
				}
				rv, err := testReviewer().Review(context.Background(), f.req)
				require.NoError(t, err)
				require.Positive(t, layout.BlockingCount(rv.Findings))
				requireFindingsAcceptedByCoordinator(t, f.geo, rv.Findings)
			})
		}
	}
}

func TestReadabilityReviewer_GroupRuntimeProbeOwnership(t *testing.T) {
	for _, kind := range []string{"text", "connector"} {
		t.Run(kind, func(t *testing.T) {
			f := newGroupReviewFixture(t, "branches")
			id := f.geo.GroupElements[0].Texts[0].Key
			if kind == "connector" {
				id = f.geo.GroupConnectors[0].ID
			}
			s := &f.req.Rendering.Report.States[len(f.req.Rendering.Report.States)-1]
			s.LabelsReadable = false
			s.Issues = []string{"group " + kind + " " + id + " changed"}
			rv, err := testReviewer().Review(context.Background(), f.req)
			require.NoError(t, err)
			fs := withCriterion(rv.Findings, layout.CriterionTextUnreadable)
			require.NotEmpty(t, fs)
			require.Contains(t, fs[0].NodeIDs, "fan")
			require.Empty(t, fs[0].EdgeOrdinals)
			requireFindingsAcceptedByCoordinator(t, f.geo, rv.Findings)
		})
	}
}

func TestReadabilityReviewer_GroupRuntimeProbeFlagsRetainOwners(t *testing.T) {
	for _, name := range []string{"group:run:gathering", `["group-probe","winner","a","second"]`} {
		for _, flag := range []string{"stability", "readability"} {
			t.Run(flag+"/"+name, func(t *testing.T) {
				f := newGroupReviewFixture(t, "nested")
				found := false
				for i := range f.req.Rendering.Report.States {
					s := &f.req.Rendering.Report.States[i]
					if s.State == name {
						found = true
						s.Issues = nil
						if flag == "stability" {
							s.NodeBoxesStable = false
						} else {
							s.LabelsReadable = false
						}
					}
				}
				require.True(t, found)
				rv, err := testReviewer().Review(context.Background(), f.req)
				require.NoError(t, err)
				criterion := layout.CriterionRenderMismatch
				if flag == "readability" {
					criterion = layout.CriterionTextUnreadable
				}
				fs := withCriterion(rv.Findings, criterion)
				require.Len(t, fs, 1)
				require.Equal(t, layout.SeverityBlocking, fs[0].Severity)
				require.Contains(t, fs[0].Evidence, strconv.Quote(name))
				require.ElementsMatch(t, []string{"fan", "a"}, fs[0].NodeIDs, "broadcast probes exercise both real group owners, even without detailed issues")
				require.Empty(t, fs[0].EdgeOrdinals)
				requireFindingsAcceptedByCoordinator(t, f.geo, rv.Findings)
			})
		}
	}
}

// P3T17's capture-only long-label case includes a hanging Range beyond its
// reported text box. Capture readiness alone must not override review's existing
// containment tolerance; passing fixtures use fully contained multiline labels.
// G1: pre-wrap labels hang the space at each soft wrap past their measured
// box; that space is not ink. Real ink outside the box must still block.
func TestReadabilityReviewer_GroupActualWrapContainment(t *testing.T) {
	assets := embeddedSurface(t)
	req := capturedGroupRequest(t, newTestRenderer(t, testBrowserConfig(t, assets)), 1)
	review := func(t *testing.T, b *BrowserRenderer) (layout.Rendering, []layout.Finding) {
		rend, err := b.Render(context.Background(), req)
		require.NoError(t, err)
		rv, err := testReviewer().Review(context.Background(), layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: req.Geometry, WorkflowGroups: req.WorkflowGroups, Rendering: rend})
		require.NoError(t, err)
		return rend, withCriterion(rv.Findings, layout.CriterionTextClipped)
	}
	t.Run("soft wrap", func(t *testing.T) {
		rend, clipped := review(t, newTestRenderer(t, testBrowserConfig(t, assets)))
		require.Empty(t, clipped)
		for _, n := range rend.Report.Nodes {
			require.Greater(t, len(n.Label.Glyphs), 1, "%s must wrap", n.ID)
			for _, g := range n.Label.Glyphs {
				require.True(t, n.Label.Box.Inflate(boxTolerancePx).Contains(g), "%s glyph %+v outside %+v", n.ID, g, n.Label.Box)
			}
		}
	})
	t.Run("unwrapped overflow", func(t *testing.T) {
		html, err := fs.ReadFile(assets, surfacePage)
		require.NoError(t, err)
		page := strings.Replace(string(html), "</head>", `<style>[data-node-text="label"]{white-space:nowrap!important}</style></head>`, 1)
		_, clipped := review(t, newTestRenderer(t, testBrowserConfig(t, surfaceHTML{assets, []byte(page)})))
		require.NotEmpty(t, clipped)
		evidence := ""
		for _, f := range clipped {
			evidence += f.Evidence + ";"
		}
		require.Contains(t, evidence, "glyph bounds escape its box")
	})
}

func TestReadabilityReviewer_GroupRuntimeProbesActualBrowser(t *testing.T) {
	assets := embeddedSurface(t)
	producer := newTestRenderer(t, testBrowserConfig(t, assets))
	req := capturedGroupRequestWithLabel(t, producer, 2, "Captured group 審查")
	for _, tc := range []struct{ name, style, script, probe, issue string }{
		{name: "live row only", style: `[data-overlay-row="frozen"] > [data-group-text] {height:1px!important;overflow:hidden!important}`, probe: "group:task:unfinished", issue: "clipped"},
		{name: "winner only", style: `[data-group-text*="overlay:winner:"] {color:#1e1e38!important}`, probe: `["group-probe","winner","fan","second"]`, issue: "contrast"},
		{name: "redacted identity", script: `for(const e of document.querySelectorAll('[data-group-text]'))if(e.textContent==='hidden dependency')e.textContent='private workflow identity'`, probe: "group:redacted", issue: "content changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			html, err := fs.ReadFile(assets, surfacePage)
			require.NoError(t, err)
			injection := "<style>" + tc.style + "</style>"
			if tc.script != "" {
				injection += `<script>new MutationObserver(()=>{` + tc.script + `}).observe(document.documentElement,{subtree:true,childList:true,characterData:true})</script>`
			}
			b := newTestRenderer(t, testBrowserConfig(t, surfaceHTML{assets, []byte(strings.Replace(string(html), "</head>", injection+"</head>", 1))}))
			rend, err := b.Render(context.Background(), req)
			require.NoError(t, err)
			found := false
			for _, s := range rend.Report.States {
				if s.State == tc.probe {
					found = true
					require.False(t, s.NodeBoxesStable && s.LabelsReadable)
					require.Contains(t, strings.Join(s.Issues, ";"), tc.issue)
				}
			}
			require.True(t, found)
			rv, err := testReviewer().Review(context.Background(), layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: req.Geometry, WorkflowGroups: req.WorkflowGroups, Rendering: rend})
			require.NoError(t, err)
			require.Positive(t, layout.BlockingCount(rv.Findings))
			for _, f := range rv.Findings {
				if strings.Contains(f.Evidence, strconv.Quote(tc.probe)) {
					require.Contains(t, f.NodeIDs, "fan", "runtime group defects must retain their actual owner")
					require.Empty(t, f.EdgeOrdinals)
				}
			}
			var evidence string
			for _, f := range rv.Findings {
				evidence += f.Evidence + "\n"
			}
			require.Contains(t, evidence, strconv.Quote(tc.probe))
			require.Contains(t, evidence, tc.issue)
		})
	}
}

// A settled-only collect in RenderSurface.animationPhases must miss these
// defects: the live text returns to its reserved row at both endpoints, and
// the final static report/PNGs contain no trace of the transient overflow.
func TestReadabilityReviewer_GroupRuntimeAnimationPhases(t *testing.T) {
	assets := embeddedSurface(t)
	producer := newTestRenderer(t, testBrowserConfig(t, assets))
	req := capturedGroupRequestWithLabel(t, producer, 2, "Captured group\n審查")
	html, err := fs.ReadFile(assets, surfacePage)
	require.NoError(t, err)
	const probe = "group:task:unfinished"
	var baseline layout.RenderReport
	for _, tc := range []struct {
		name, keyframes, delay string
		blocked                bool
	}{
		{"contained endpoints", "0%,100% { margin-top:0px }", "0s", false},
		{"midpoint overflow", "0%,100% { margin-top:0px } 50% { margin-top:3px }", "0s", true},
		// No quarter-cycle sample sees this narrow pulse. Its declared
		// computedOffset must be sampled, with the animation delay included.
		{"declared keyframe overflow", "0%,36%,38%,100% { margin-top:0px } 37% { margin-top:3px }", "0.2s", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			style := `<style>@keyframes review-row-pulse {` + tc.keyframes + `}
[data-overlay-row="frozen"] > [data-group-text] {
  animation: review-row-pulse 1s linear ` + tc.delay + ` both;
}</style>`
			b := newTestRenderer(t, testBrowserConfig(t, surfaceHTML{assets, []byte(strings.Replace(string(html), "</head>", style+"</head>", 1))}))
			rend, err := b.Render(context.Background(), req)
			require.NoError(t, err)
			var observed *layout.StateProbe
			for i := range rend.Report.States {
				if rend.Report.States[i].State == probe {
					observed = &rend.Report.States[i]
				}
			}
			require.NotNil(t, observed)
			require.True(t, observed.NodeBoxesStable, "only the live text moves")
			require.Equal(t, !tc.blocked, observed.LabelsReadable, "%s: %v", probe, observed.Issues)
			rv, err := testReviewer().Review(context.Background(), layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: req.Geometry, WorkflowGroups: req.WorkflowGroups, Rendering: rend})
			require.NoError(t, err)
			require.True(t, rv.Rendered, "restored static pixels must remain complete")
			if !tc.blocked {
				require.Empty(t, observed.Issues)
				require.Zero(t, layout.BlockingCount(rv.Findings), "%+v", rv.Findings)
				baseline = rend.Report
				return
			}
			require.Contains(t, strings.Join(observed.Issues, ";"), "outside reserved row")
			require.NotEmpty(t, baseline.GroupElements)
			require.Equal(t, baseline.GroupElements, rend.Report.GroupElements, "capture restores the static graph after transient overflow")
			var blocking []layout.Finding
			for _, f := range withCriterion(rv.Findings, layout.CriterionTextUnreadable) {
				if strings.Contains(f.Evidence, strconv.Quote(probe)) {
					blocking = append(blocking, f)
				}
			}
			require.Len(t, blocking, 1, "transient issues must reach the review sink")
			require.Equal(t, layout.SeverityBlocking, blocking[0].Severity)
			require.Contains(t, blocking[0].Evidence, "outside reserved row")
			require.Equal(t, []string{"fan"}, blocking[0].NodeIDs)
			require.Empty(t, blocking[0].EdgeOrdinals)
			requireFindingsAcceptedByCoordinator(t, req.Geometry, rv.Findings)
		})
	}
}
