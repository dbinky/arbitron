package renderreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Updates always use the built production surface, never a painted test scene.
func TestGenerateGroupReadabilityFixtures(t *testing.T) {
	if os.Getenv("ARBITRON_UPDATE_RENDER_FIXTURES") != "1" {
		t.Skip("explicit real-browser fixture update only")
	}
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	for i, name := range []string{"branches", "workflows", "nested", "loop"} {
		req := capturedGroupRequestWithLabel(t, b, i+1, "Überprüfe sämtliche Änderungen\n審查所有拉取請求\njede Abhängigkeit gründlich")
		rend, err := b.Render(context.Background(), req)
		require.NoError(t, err)
		review, err := NewReadabilityReviewer(nil).Review(context.Background(), layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: req.Geometry, WorkflowGroups: req.WorkflowGroups, Rendering: rend})
		require.NoError(t, err)
		require.Zero(t, layout.BlockingCount(review.Findings), "%+v", review.Findings)
		require.True(t, layout.HasRenderedEvidence(req.Geometry, &review))
		raw, err := os.ReadFile("../../dashboard/src/graph/arranged/groupGeometry.v2.json")
		require.NoError(t, err)
		var scenes []struct {
			Definition workflow.Workflow `json:"definition"`
		}
		require.NoError(t, json.Unmarshal(raw, &scenes))
		wf := scenes[i+1].Definition
		for j := range wf.Nodes {
			wf.Nodes[j].Label = req.Geometry.Nodes[j].Text
			if scope := req.WorkflowGroups.Groups[wf.Nodes[j].ID]; scope != nil {
				cfg := scope.Config
				wf.Nodes[j].Config.Group = &cfg
			}
		}
		dir := filepath.Join("testdata", "readability", "group-"+name)
		require.NoError(t, os.MkdirAll(dir, 0755))
		for j := range rend.Artifacts {
			a := &rend.Artifacts[j]
			raw, err := os.ReadFile(a.Path)
			require.NoError(t, err)
			a.Path = filepath.Base(a.Path)
			require.NoError(t, os.WriteFile(filepath.Join(dir, a.Path), raw, 0644))
		}
		review.Evidence.Artifacts = append([]layout.Artifact(nil), rend.Artifacts...)
		for name, v := range map[string]any{"request.json": req, "rendering.json": rend, "workflow.json": wf, "review.json": review} {
			raw, err := json.MarshalIndent(v, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), raw, 0644))
		}
	}
}

func newGroupReviewFixture(t *testing.T, name string) *reviewFixture {
	t.Helper()
	dir := filepath.Join("testdata", "readability", "group-"+name)
	var req layout.RenderRequest
	raw, err := os.ReadFile(filepath.Join(dir, "request.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &req))
	var rend layout.Rendering
	raw, err = os.ReadFile(filepath.Join(dir, "rendering.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &rend))
	require.Equal(t, layout.GeometryDigest(req.Geometry), rend.GeometryDigest)
	fx := &reviewFixture{geo: req.Geometry, dir: t.TempDir()}
	for i := range rend.Artifacts {
		a := &rend.Artifacts[i]
		require.Equal(t, filepath.Base(a.Path), a.Path)
		raw, err := os.ReadFile(filepath.Join(dir, a.Path))
		require.NoError(t, err)
		a.Path = filepath.Join(fx.dir, a.Path)
		require.NoError(t, os.WriteFile(a.Path, raw, 0600))
		if a.Kind == "tile" {
			fx.tiles = append(fx.tiles, *a)
		}
	}
	fx.req = layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: req.Geometry, WorkflowGroups: req.WorkflowGroups, Rendering: rend}
	return fx
}
