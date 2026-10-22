package layouttest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

// CapturedGroup loads immutable production-browser evidence. It is for JSON
// storage/read tests, not a replacement for running the installed reviewer.
func CapturedGroup(t testing.TB, name string) (*workflow.Workflow, layout.Geometry, *layout.Review) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("fixture source unavailable")
	}
	dir := filepath.Join(filepath.Dir(file), "../../../adapter/renderreview/testdata/readability/group-"+name)
	decode := func(name string, out any) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
	var wf workflow.Workflow
	decode("workflow.json", &wf)
	var req layout.RenderRequest
	decode("request.json", &req)
	var review layout.Review
	decode("review.json", &review)
	for i := range review.Evidence.Artifacts {
		review.Evidence.Artifacts[i].Path = filepath.Join(dir, review.Evidence.Artifacts[i].Path)
	}
	graph, err := layout.BuildGraph(&wf)
	if err != nil {
		t.Fatal(err)
	}
	if !layout.Compatible(req.Geometry, graph) || !layout.HasRenderedEvidence(req.Geometry, &review) || layout.BlockingCount(review.Findings) != 0 {
		t.Fatal("incompatible captured group fixture")
	}
	return &wf, req.Geometry, &review
}
