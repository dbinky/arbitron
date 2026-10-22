package filesystem

import "testing"

func TestReservedWorkflowFileFoldsCase(t *testing.T) {
	for _, tc := range []struct {
		path     string
		reserved bool
	}{
		{"workflow.json", true},
		{"Workflow.json", true},
		{"WORKFLOW.JSON", true},
		{"layout.json", true},
		{"LAYOUT.JSON", true},
		{"Layout.Json", true},
		{".arbitron-locks/x", true},
		{".Arbitron-Locks/x", true},
		{"sub/.ARBITRON-LAYOUT-JOURNAL/y", true},
		{".Arbitron-Tree-Commits", true},
		{"prompts/workflow.json.md", false},
		{"sub/layout.json", false},
		{"README.md", false},
	} {
		if got := reservedWorkflowFile(tc.path); got != tc.reserved {
			t.Errorf("reservedWorkflowFile(%q) = %v, want %v", tc.path, got, tc.reserved)
		}
	}
}
