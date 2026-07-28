# Phase 1 — Task 1: `spawn_workflow` node type and mode vocabulary

**Goal:** Add the `spawn_workflow` node type, its two-value mode vocabulary with a fail-closed validity predicate, and the two new `NodeConfig` fields the executor will read.

**Architecture:** Extends the existing node-type constant block in `domain/workflow/workflow.go` — the single source of truth `AGENTS.md` names for the node vocabulary shared by validation, dashboard behavior, and tests. The mode predicate mirrors `IsValidAgentBackend` (exact-match lowercase, no near-misses) but rejects the empty value, because the two modes have opposite control-flow consequences and there is no sensible inherited default. Guidance prose and the preparation model reuse the existing `TemplateFile` / `ModelProfile` fields rather than duplicating them.

**Tech Stack:** Go 1.25.5, standard library `testing`, module `github.com/arbitron/arbitron`.

---

### Task 1: spawn node type + mode vocabulary

**Files:**
- Modify: `domain/workflow/workflow.go:9-20` (node type constants), `:50-57` (beside `IsValidAgentBackend`), `:100-127` (`NodeConfig`)
- Test:   `domain/workflow/workflow_spawn_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `domain/workflow/workflow_spawn_test.go`:

```go
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
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/workflow/ -run 'TestNodeTypeSpawnWorkflowConstant|TestSpawnMode|TestSpawnPort|TestIsValidSpawnMode|TestNodeConfigSpawnFields'
```

Expected output (compile failure — the symbols do not exist yet):

```
# github.com/arbitron/arbitron/domain/workflow [github.com/arbitron/arbitron/domain/workflow.test]
domain/workflow/workflow_spawn_test.go:8:16: undefined: NodeTypeSpawnWorkflow
domain/workflow/workflow_spawn_test.go:16:5: undefined: SpawnModeFireAndForget
...
FAIL	github.com/arbitron/arbitron/domain/workflow [build failed]
```

- [ ] **Step 3: Minimal implementation**

In `domain/workflow/workflow.go`, add the constant to the node-type block (after `NodeTypeAgentCall`, line 19):

```go
	NodeTypeAgentCall        NodeType = "agent_call"
	// NodeTypeSpawnWorkflow starts a CHILD workflow instance from within a
	// parent workflow. It is ONE node type with two modes (see SpawnMode*);
	// there is deliberately no separate node type per mode.
	NodeTypeSpawnWorkflow NodeType = "spawn_workflow"
)
```

Then, immediately after `IsValidAgentBackend` (line 57), add:

```go
// Spawn mode axis. A spawn_workflow node either starts the child and continues
// (fire-and-forget) or suspends the parent until the child finishes
// (wait-for-child). These constants are the single source of truth for the
// allowed vocabulary, shared by validation (this package), the spawn executor,
// and the persisted link on the child instance. The allowed set is exact-match
// lowercase.
const (
	// SpawnModeFireAndForget starts the child and continues the parent along a
	// single unnamed outgoing edge. Child outcomes never affect the parent.
	SpawnModeFireAndForget = "fire_and_forget"
	// SpawnModeWaitForChild suspends the parent until the child reaches a
	// terminal state, then resumes it through the success or failure port.
	SpawnModeWaitForChild = "wait_for_child"
)

// Spawn output ports. Both are REQUIRED on a wait-for-child node and both are
// REJECTED on a fire-and-forget node (see validateSpawnWorkflowNode).
const (
	SpawnPortSuccess = "success"
	SpawnPortFailure = "failure"
)

// IsValidSpawnMode reports whether m is a recognized spawn mode.
//
// Unlike IsValidAgentBackend, the EMPTY value is INVALID. There is no sensible
// inherited default: the two modes have opposite control-flow consequences (the
// parent continues vs. the parent suspends and requires two connected ports),
// so an unset mode is an authoring error, not an inheritance request. An
// unrecognized value must fail closed at workflow load rather than mis-route or
// silently fall back at run time.
func IsValidSpawnMode(m string) bool {
	switch m {
	case SpawnModeFireAndForget, SpawnModeWaitForChild:
		return true
	default:
		return false
	}
}
```

Finally, add the two fields to `NodeConfig` (after `ToolScope`, line 107):

```go
	ToolScope          []string          `json:"toolScope,omitempty"`     // agent_call: allowed-tools list (default: full agent scope)
	ChildWorkflow      string            `json:"childWorkflow,omitempty"` // spawn_workflow: the child workflow's name
	SpawnMode          string            `json:"spawnMode,omitempty"`     // spawn_workflow: "fire_and_forget" | "wait_for_child"
```

> Note for the implementer: `spawn_workflow` reuses the EXISTING `TemplateFile`
> field for the optional input-preparation guidance prose, and the EXISTING
> `ModelProfile` field for the chat profile of the preparation call. Do not add
> spawn-specific duplicates of either.

- [ ] **Step 4: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/workflow/workflow.go domain/workflow/workflow_spawn_test.go
go test ./domain/workflow/ -run 'TestNodeTypeSpawnWorkflowConstant|TestSpawnMode|TestSpawnPort|TestIsValidSpawnMode|TestNodeConfigSpawnFields' -v
```

Expected output:

```
=== RUN   TestNodeTypeSpawnWorkflowConstant
--- PASS: TestNodeTypeSpawnWorkflowConstant (0.00s)
=== RUN   TestSpawnModeConstants
--- PASS: TestSpawnModeConstants (0.00s)
=== RUN   TestSpawnPortConstants
--- PASS: TestSpawnPortConstants (0.00s)
=== RUN   TestIsValidSpawnMode
--- PASS: TestIsValidSpawnMode (0.00s)
=== RUN   TestSpawnModeVocabularyTotality
--- PASS: TestSpawnModeVocabularyTotality (0.00s)
=== RUN   TestNodeConfigSpawnFieldsRoundTrip
--- PASS: TestNodeConfigSpawnFieldsRoundTrip (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/workflow	0.2s
```

- [ ] **Step 5: Confirm nothing else regressed**

```bash
go test ./domain/... ./adapter/... ./cmd/...
```

Expected: `ok` for every package (no `FAIL` lines).

- [ ] **Step 6: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/workflow/workflow.go domain/workflow/workflow_spawn_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(workflow): add spawn_workflow node type and spawn mode vocabulary"
```
