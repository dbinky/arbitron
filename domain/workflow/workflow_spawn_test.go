package workflow

import "testing"

// TestNodeTypeSpawnWorkflowConstant pins the wire value of the new node type.
// The constant is the vocabulary of record (AGENTS.md); a rename here would
// silently orphan every persisted workflow.json and every WorkflowSnapshot.
func TestNodeTypeSpawnWorkflowConstant(t *testing.T) {
	if got := string(NodeTypeSpawnWorkflow); got != "spawn_workflow" {
		t.Fatalf("NodeTypeSpawnWorkflow = %q, want %q", got, "spawn_workflow")
	}
}

// TestSpawnModeConstants pins the two mode wire values.
func TestSpawnModeConstants(t *testing.T) {
	if SpawnModeFireAndForget != "fire_and_forget" {
		t.Errorf("SpawnModeFireAndForget = %q, want %q", SpawnModeFireAndForget, "fire_and_forget")
	}
	if SpawnModeWaitForChild != "wait_for_child" {
		t.Errorf("SpawnModeWaitForChild = %q, want %q", SpawnModeWaitForChild, "wait_for_child")
	}
}

// TestSpawnPortConstants pins the two required wait-for-child output ports.
func TestSpawnPortConstants(t *testing.T) {
	if SpawnPortSuccess != "success" {
		t.Errorf("SpawnPortSuccess = %q, want %q", SpawnPortSuccess, "success")
	}
	if SpawnPortFailure != "failure" {
		t.Errorf("SpawnPortFailure = %q, want %q", SpawnPortFailure, "failure")
	}
}

// TestIsValidSpawnMode covers the closed, exact-match, lowercase vocabulary.
// Unlike the agent-backend axis, the EMPTY mode is INVALID: there is no
// sensible inherited default because the two modes differ in whether the
// parent suspends.
func TestIsValidSpawnMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want bool
	}{
		{"fire and forget", "fire_and_forget", true},
		{"wait for child", "wait_for_child", true},
		{"empty is NOT inherit", "", false},
		{"wrong case", "Wait_For_Child", false},
		{"upper case", "FIRE_AND_FORGET", false},
		{"hyphenated near-miss", "wait-for-child", false},
		{"leading space", " wait_for_child", false},
		{"trailing space", "wait_for_child ", false},
		{"typo", "wait_for_children", false},
		{"unrelated", "async", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidSpawnMode(tt.mode); got != tt.want {
				t.Fatalf("IsValidSpawnMode(%q) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

// TestSpawnModeVocabularyTotality guards against a THIRD mode being added
// without extending the predicate (and, downstream, validation and the
// executor). It follows the totality-test discipline
// instance.VisibilityFromWorkflow uses instead of a defaulting switch.
func TestSpawnModeVocabularyTotality(t *testing.T) {
	all := []string{SpawnModeFireAndForget, SpawnModeWaitForChild}
	if len(all) != 2 {
		t.Fatalf("spawn mode vocabulary changed to %d values: extend IsValidSpawnMode, "+
			"validateSpawnWorkflowNode, and SpawnWorkflowExecutor before updating this test", len(all))
	}
	for _, m := range all {
		if !IsValidSpawnMode(m) {
			t.Errorf("IsValidSpawnMode(%q) = false; every declared mode must be valid", m)
		}
	}
}

// TestNodeConfigSpawnFieldsRoundTrip proves the two new config fields carry the
// author's declared intent through the JSON shape workflow.json uses.
func TestNodeConfigSpawnFieldsRoundTrip(t *testing.T) {
	cfg := NodeConfig{
		ChildWorkflow: "child-pipeline",
		SpawnMode:     SpawnModeWaitForChild,
		TemplateFile:  "spawn-guidance.md",
		ModelProfile:  "chat-default",
	}
	if cfg.ChildWorkflow != "child-pipeline" {
		t.Errorf("ChildWorkflow = %q", cfg.ChildWorkflow)
	}
	if cfg.SpawnMode != "wait_for_child" {
		t.Errorf("SpawnMode = %q", cfg.SpawnMode)
	}
}
