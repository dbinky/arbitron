package renderreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func capturedGroupRequest(t *testing.T, b *BrowserRenderer, index int) layout.RenderRequest {
	t.Helper()
	return capturedGroupRequestWithLabel(t, b, index, "Überprüfe sämtliche Änderungen — 審查所有拉取請求 — und bestätige jede Abhängigkeit gründlich")
}

func capturedGroupRequestWithLabel(t *testing.T, b *BrowserRenderer, index int, label string) layout.RenderRequest {
	t.Helper()
	raw, err := os.ReadFile("../../dashboard/src/graph/arranged/groupGeometry.v2.json")
	require.NoError(t, err)
	var fixtures []struct {
		Definition workflow.Workflow `json:"definition"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	wf := fixtures[index].Definition
	for i := range wf.Nodes {
		wf.Nodes[i].Label = label
		if cfg := wf.Nodes[i].Config.Group; cfg != nil && cfg.Form == workflow.GroupFormWorkflows {
			for j := range cfg.Tasks {
				cfg.Tasks[j].Workflow = "Überprüfe sämtliche Abhängigkeiten 🧪 審查"
			}
		}
	}
	g, err := layout.BuildGraph(&wf)
	require.NoError(t, err)
	c := layout.DefaultContract()
	m, err := b.Measure(context.Background(), layout.MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := layout.Arrange(context.Background(), g, m, c, layout.DefaultOptions())
	require.NoError(t, err)
	types := map[string]string{}
	for _, n := range wf.Nodes {
		types[n.ID] = string(n.Type)
	}
	req := layout.NewRenderRequest("group-capture", index+1, geo, types)
	req.WorkflowGroups = g.Groups
	return req
}

func TestBrowserRenderer_GroupGeometryReachesActualCapture(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	for _, index := range []int{1, 2, 3} {
		t.Run([]string{"", "branches", "workflows", "nested"}[index], func(t *testing.T) {
			req := capturedGroupRequest(t, b, index)
			r, err := b.Render(context.Background(), req)
			require.NoError(t, err)
			rep := r.Report
			require.Len(t, rep.GroupElements, len(req.Geometry.GroupElements))
			require.NotEmpty(t, rep.GroupElements)
			require.Len(t, rep.GroupConnectors, len(req.Geometry.GroupConnectors))
			require.NotEmpty(t, rep.GroupConnectors)
			require.Len(t, rep.GroupCrossings, len(req.Geometry.GroupCrossings))
			for i, e := range rep.GroupElements {
				want := req.Geometry.GroupElements[i]
				assert.Equal(t, want.ID, e.ID)
				assert.InDelta(t, want.Box.X, e.Box.X, 1)
				assert.InDelta(t, want.Box.Y, e.Box.Y, 1)
				assert.InDelta(t, want.Box.W, e.Box.W, 1)
				assert.InDelta(t, want.Box.H, e.Box.H, 1)
				assert.True(t, e.Visible)
				assert.Equal(t, req.Geometry.Contract.NodeBorderPx, e.BorderPx)
				assert.NotEmpty(t, e.BorderColor)
				assert.NotContains(t, e.BackgroundColor, "0)")
				require.Len(t, e.Texts, len(want.Texts))
				for j, text := range e.Texts {
					assert.Equal(t, want.Texts[j].Key, text.Key)
					assert.Equal(t, want.Texts[j].Text, text.Text.Content)
					assert.True(t, text.Text.Visible, text.Key)
					assert.False(t, text.Text.Clipped, text.Key)
					assert.False(t, text.Text.Occluded, text.Key)
					assert.NotEmpty(t, text.Text.Glyphs, text.Key)
				}
			}
			for _, c := range rep.GroupConnectors {
				assert.True(t, c.Visible, c.ID)
				assert.True(t, c.HasArrow, c.ID)
				assert.Len(t, c.ArrowPoints, 3)
				assert.NotEmpty(t, c.Segments)
				assert.InDelta(t, req.Geometry.Contract.StrokePx, c.StrokePx, 0.001)
			}
			for _, c := range rep.GroupCrossings {
				assert.True(t, c.UnderGapVisible)
				assert.True(t, c.OverContinuous)
			}
			names := append(append([]string(nil), layout.RuntimeStates...), layout.GroupProbeNames(req.WorkflowGroups)...)
			var actual []string
			for _, s := range rep.States {
				actual = append(actual, s.State)
			}
			require.Equal(t, names, actual, "every captured runtime case must reach actual DOM")
			for _, s := range rep.States {
				assert.True(t, s.NodeBoxesStable, "%s: %v", s.State, s.Issues)
				assert.True(t, s.LabelsReadable, "%s: %v", s.State, s.Issues)
			}
			require.NotEmpty(t, r.Artifacts)
			assert.Equal(t, req.Geometry.Canvas, r.Artifacts[0].Region)
			for _, a := range r.Artifacts {
				raw, err := os.ReadFile(a.Path)
				require.NoError(t, err)
				sum := sha256.Sum256(raw)
				assert.Equal(t, hex.EncodeToString(sum[:]), a.SHA256)
				img, err := png.DecodeConfig(bytes.NewReader(raw))
				require.NoError(t, err)
				assert.InDelta(t, a.Region.W*a.Scale, float64(img.Width), 2)
				assert.InDelta(t, a.Region.H*a.Scale, float64(img.Height), 2)
			}
		})
	}
}

func TestBrowserRenderer_GroupDefectsReachActualCapture(t *testing.T) {
	assets := embeddedSurface(t)
	producer := newTestRenderer(t, testBrowserConfig(t, assets))
	req := capturedGroupRequest(t, producer, 2)
	for _, tc := range []struct{ name, style, script, wantError, probe, issue string }{
		{name: "missing ownership", wantError: "ownership"},
		{name: "missing text", script: `for(const e of document.querySelectorAll('[data-group-text]'))e.remove()`, wantError: "DOM coverage"},
		{name: "missing connector", script: `for(const e of document.querySelectorAll('path[data-group-connector]'))e.remove()`, wantError: "DOM coverage"},
		{name: "missing marker", style: `marker {opacity:0!important}`, probe: "group:badges", issue: "missing path or arrow"},
		{name: "runtime row displacement", style: `[data-overlay-row="frozen"] > [data-group-text] {margin-top:3px!important}`, probe: "group:task:unfinished", issue: "outside reserved row"},
		{name: "runtime clipping", style: `[data-overlay-row="frozen"] > [data-group-text] {height:1px!important;overflow:hidden!important}`, probe: "group:task:unfinished", issue: "clipped"},
		{name: "runtime text", script: `for(const e of document.querySelectorAll('[data-overlay-row="status"] > [data-group-text]'))if(e.textContent!=='Corrupted')e.textContent='Corrupted'`, probe: "group:task:failed", issue: "content changed"},
		{name: "runtime middle connector", script: `if(document.querySelector('[data-overlay-row] [data-group-text]'))for(const e of document.querySelectorAll('path[data-group-connector]')){if(!e.dataset.original)e.dataset.original=e.getAttribute('d');const ns=e.dataset.original.match(/-?\d+(?:\.\d+)?/g).map(Number),x=ns[0],y=ns[1],ex=ns.at(-2),ey=ns.at(-1);const changed='M '+x+','+y+' L '+(x+5)+','+y+' L '+(x+5)+','+ey+' L '+ex+','+ey;if(e.getAttribute('d')!==changed)e.setAttribute('d',changed)}`, probe: "group:badges", issue: "route changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := req
			if tc.name == "missing ownership" {
				input.WorkflowGroups = nil
			}
			html, err := fs.ReadFile(assets, surfacePage)
			require.NoError(t, err)
			injection := "<style>" + tc.style + "</style>"
			if tc.script != "" {
				injection += `<script>new MutationObserver(()=>{` + tc.script + `}).observe(document.documentElement,{subtree:true,childList:true,attributes:true,attributeFilter:['d']})</script>`
			}
			b := newTestRenderer(t, testBrowserConfig(t, surfaceHTML{assets, []byte(strings.Replace(string(html), "</head>", injection+"</head>", 1))}))
			r, err := b.Render(context.Background(), input)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Empty(t, r.Artifacts)
				return
			}
			require.NoError(t, err)
			for _, s := range r.Report.States {
				if s.State == tc.probe {
					assert.False(t, s.NodeBoxesStable && s.LabelsReadable)
					assert.Contains(t, strings.Join(s.Issues, "; "), tc.issue)
					return
				}
			}
			t.Fatalf("required probe %s missing", tc.probe)
		})
	}
}

type withoutGroupTitleFont struct{ fs.FS }

func (f withoutGroupTitleFont) Open(name string) (fs.File, error) {
	if strings.Contains(name, "noto-emoji") && (strings.HasSuffix(name, ".woff2") || strings.HasSuffix(name, ".woff")) { // the face and its CSS .woff fallback
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return f.FS.Open(name)
}

func TestBrowserRenderer_GroupTitleFontAndCancellation(t *testing.T) {
	assets := embeddedSurface(t)
	b := newTestRenderer(t, testBrowserConfig(t, assets))
	req := capturedGroupRequest(t, b, 2)
	missing := newTestRenderer(t, testBrowserConfig(t, withoutGroupTitleFont{assets}))
	_, err := missing.Render(context.Background(), req)
	require.ErrorContains(t, err, "font asset unavailable")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = b.Render(ctx, req)
	require.ErrorIs(t, err, context.Canceled)
}

func TestBrowserRenderer_GroupConcurrentCaptureAndOrdinaryReport(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	req := capturedGroupRequest(t, b, 1)
	var wg sync.WaitGroup
	results := make([]layout.Rendering, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			results[i], err = b.Render(context.Background(), req)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	require.NotEmpty(t, results[0].Artifacts)
	require.NotEmpty(t, results[1].Artifacts)
	assert.NotEqual(t, results[0].Artifacts[0].Path, results[1].Artifacts[0].Path)
	for _, r := range results {
		assert.Len(t, r.Report.GroupElements, len(req.Geometry.GroupElements))
	}
	ordinary := capturedGroupRequest(t, b, 0)
	r, err := b.Render(context.Background(), ordinary)
	require.NoError(t, err)
	assert.Empty(t, r.Report.GroupElements)
	assert.Empty(t, r.Report.GroupConnectors)
	assert.Empty(t, r.Report.GroupCrossings)
	raw, err := json.Marshal(r.Report)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"groupElements"`)
}
