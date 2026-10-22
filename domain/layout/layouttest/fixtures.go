package layouttest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

func SampleWorkflow() *workflow.Workflow {
	return &workflow.Workflow{Name: "sample", Nodes: []workflow.Node{
		{ID: "start", Type: workflow.NodeTypeScript, Label: "Start"},
		{ID: "check", Type: workflow.NodeTypeConditional, Label: "Check"},
		{ID: "fix", Type: workflow.NodeTypeAgentCall, Label: "Fix"},
		{ID: "ship", Type: workflow.NodeTypeScript, Label: "Ship"},
	}, Edges: []workflow.Edge{{Source: "start", Target: "check"}, {Source: "check", Target: "fix", Output: "retry"}, {Source: "fix", Target: "check"}, {Source: "check", Target: "ship", Output: "pass"}}}
}
func Arranged(t testing.TB, wf *workflow.Workflow) layout.Geometry {
	t.Helper()
	g, err := layout.BuildGraph(wf)
	if err != nil {
		t.Fatal(err)
	}
	c := layout.DefaultContract()
	geo, err := layout.Arrange(context.Background(), g, Measurements(g, c), c, layout.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err = geo.Validate(g); err != nil {
		t.Fatal(err)
	}
	return geo
}
func ManifestJSON(t testing.TB, wf *workflow.Workflow) []byte {
	t.Helper()
	b, err := json.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Artifacts supplies synthetic unit envelopes, not screenshot files or raster evidence.
func Artifacts(geo layout.Geometry) []layout.Artifact {
	out := make([]layout.Artifact, 0, 2)
	for _, kind := range []string{"overview", "tile"} {
		id := sha256.Sum256([]byte(kind + layout.GeometryDigest(geo)))
		out = append(out, layout.Artifact{Kind: kind, Path: "unit-only/" + hex.EncodeToString(id[:]) + ".png", SHA256: hex.EncodeToString(id[:]), Region: geo.Canvas, Scale: geo.Contract.PracticalZoom})
	}
	return out
}
