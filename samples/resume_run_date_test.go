package samples_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// A resumed run must glob docs under the date they were written, not the date
// the resume started.
//
// Run b3fd1fb8 (2026-10-01) resumed a feature whose plans were all named
// 2026-09-29-latest-model-profiles-implementation-phase-*-task-*.md. Every
// template globs {{run_date}}-{{slug}}-..., and run_date was still the resume's
// own start date, so impl-assess found no plans, reported "0 plan task(s)
// remain", and the run opened a BLOCKED draft PR with 14 of 18 tasks unbuilt.
// detect-docs now emits safe_run_date; both promote-docs nodes must carry it
// into run_date.
func promoteDocs(t *testing.T, wf *workflow.Workflow, nodeID, detectID string, detected map[string]any) *variable.VariableStore {
	t.Helper()
	var node workflow.Node
	for _, n := range wf.Nodes {
		if n.ID == nodeID {
			node = n
		}
	}
	if node.ID == "" {
		t.Fatalf("feature-pipeline has no %s node", nodeID)
	}
	vars := variable.NewStore()
	if err := vars.SetInput("run_date", "2026-10-01"); err != nil {
		t.Fatal(err)
	}
	vars.SetNodeOutput(detectID, detected)
	if _, err := engine.NewTransformExecutor().Execute(context.Background(), node, vars, wf.Name); err != nil {
		t.Fatal(err)
	}
	return vars
}

func TestResumePromotesTheDocsRunDate(t *testing.T) {
	wf := loadSample(t, "feature-pipeline")
	for _, tc := range []struct{ node, detect string }{
		{"promote-docs", "detect-docs"},
		{"promote-docs-final", "detect-docs-final"},
	} {
		t.Run(tc.node, func(t *testing.T) {
			vars := promoteDocs(t, wf, tc.node, tc.detect, map[string]any{
				"safe_slug":     "latest-model-profiles",
				"safe_run_date": "2026-09-29",
				"spec_path":     "docs/specs/2026-09-29-latest-model-profiles-spec.md",
				"resume_stage":  "implementation",
			})
			got, ok := vars.Get("run_date")
			if !ok || got != "2026-09-29" {
				t.Errorf("run_date = %v, want 2026-09-29 — the plans' date, not the resume's start date", got)
			}
		})
	}
}
