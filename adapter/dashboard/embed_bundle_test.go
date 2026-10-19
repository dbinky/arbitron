package dashboard_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/dashboard"
)

// The SPA reaches users only through adapter/dashboard/dist (//go:embed
// dist/*). A stale dist compiles and serves perfectly well, so only a content
// probe catches a release that forgot `make ui`. Each marker is a string
// literal the minifier keeps, owned by one Phase 4 dashboard task.
func TestEmbeddedBundle_ContainsParallelGroupVisibility(t *testing.T) {
	sub := dashboard.AssetsForTest()
	entries, err := fs.ReadDir(sub, "assets")
	if err != nil {
		t.Fatalf("reading assets dir: %v", err)
	}
	var js strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		b, err := fs.ReadFile(sub, "assets/"+e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		js.Write(b)
	}
	if js.Len() == 0 {
		t.Fatal("no JavaScript bundle is embedded")
	}
	bundle := js.String()
	for _, marker := range []string{
		"/graph-state",              // P4-T6 api.instances.graphState
		"/group-layout",             // P4-T6 api.workflows.groupLayout (WorkflowReview, P4-T9)
		"subtree=1",                 // P4-T6 subtree WebSocket (P4-T5 server mode)
		"activeDescendantAncestors", // P4-T10 queue ancestor discovery (P4-T3 filter)
		"run-live-pane",             // P4-T10 queue live-run pane
		"Load more child runs",      // P4-T10 hierarchy pagination
	} {
		if !strings.Contains(bundle, marker) {
			t.Errorf("embedded dashboard bundle is stale: %q not found (run `make ui`)", marker)
		}
	}
}
