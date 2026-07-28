# Phase 1 — Task 2: spawn node author-time validation

**Goal:** Reject an unauthorable `spawn_workflow` node at workflow load — missing child workflow name, unknown/empty mode, missing `success`/`failure` edges in wait-for-child mode, branch ports wired in fire-and-forget mode, a spawn node marked as entrypoint, and a `model_profile` that resolves to the wrong posture.

**Architecture:** Adds a `validateSpawnWorkflowNode` arm to the existing per-type switch in `validateNodeConfigs` (`domain/workflow/validation.go`). Port presence reuses the `(source, output)` edge-set technique from `validateConditionalEdges`; the entrypoint and edge-count checks mirror `validateWebhookWaitNode`. Errors are batched `ValidationError{Code, Message, NodeID}` values so an author sees every problem at once.

**Tech Stack:** Go 1.25.5, standard library `testing`, module `github.com/arbitron/arbitron`.

---

### Task 2: `validateSpawnWorkflowNode`

**Files:**
- Modify: `domain/workflow/validation.go:494-544` (the `validateNodeConfigs` switch), and append the new function after `validateWebhookWaitNode` (~line 625)
- Modify: `domain/workflow/validation.go:157-181` (`validateModelProfiles` — add the spawn arm)
- Test:   `domain/workflow/validation_spawn_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `domain/workflow/validation_spawn_test.go`:

```go
package workflow

import "testing"

// spawnWF builds a two-or-three node workflow whose middle node is the
// spawn node under test. entry -> spawn -> (targets...).
func spawnWF(spawn Node, edges []Edge) *Workflow {
	nodes := []Node{
		{ID: "entry", Type: NodeTypeTransform, Entrypoint: true,
			Config: NodeConfig{Operations: []Operation{{Variable: "x", Operation: "set", Value: 1}}}},
		spawn,
		{ID: "ok", Type: NodeTypeTransform,
			Config: NodeConfig{Operations: []Operation{{Variable: "y", Operation: "set", Value: 1}}}},
		{ID: "bad", Type: NodeTypeTransform,
			Config: NodeConfig{Operations: []Operation{{Variable: "z", Operation: "set", Value: 1}}}},
	}
	all := append([]Edge{{Source: "entry", Target: spawn.ID}}, edges...)
	return &Workflow{Name: "parent", Nodes: nodes, Edges: all}
}

// hasError reports whether errs contains a finding with the given code on the
// given node id. Asserting the SPECIFIC code and node — not merely "some error"
// — is what makes these tests useful to an author reading the response.
func hasError(errs []ValidationError, code string, nodeID string) bool {
	for _, e := range errs {
		if e.Code == code && e.NodeID == nodeID {
			return true
		}
	}
	return false
}

func codesFor(errs []ValidationError, nodeID string) []string {
	var out []string
	for _, e := range errs {
		if e.NodeID == nodeID {
			out = append(out, e.Code)
		}
	}
	return out
}

// ---------- happy path ----------

func TestValidateSpawn_WaitForChildWithBothPorts_Valid(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeWaitForChild,
		}},
		[]Edge{
			{Source: "spawn", Target: "ok", Output: SpawnPortSuccess},
			{Source: "spawn", Target: "bad", Output: SpawnPortFailure},
		},
	)
	errs := Validate(wf, nil, nil, nil)
	if got := codesFor(errs, "spawn"); len(got) != 0 {
		t.Fatalf("expected no findings on spawn node, got %v", got)
	}
}

func TestValidateSpawn_FireAndForgetWithOneUnnamedEdge_Valid(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeFireAndForget,
		}},
		[]Edge{{Source: "spawn", Target: "ok"}},
	)
	errs := Validate(wf, nil, nil, nil)
	if got := codesFor(errs, "spawn"); len(got) != 0 {
		t.Fatalf("expected no findings on spawn node, got %v", got)
	}
}

// ---------- failure ----------

func TestValidateSpawn_WaitForChildMissingFailureEdge_Rejected(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeWaitForChild,
		}},
		[]Edge{{Source: "spawn", Target: "ok", Output: SpawnPortSuccess}},
	)
	errs := Validate(wf, nil, nil, nil)
	if !hasError(errs, "MISSING_SPAWN_EDGE", "spawn") {
		t.Fatalf("expected MISSING_SPAWN_EDGE on spawn, got %v", codesFor(errs, "spawn"))
	}
}

func TestValidateSpawn_WaitForChildMissingSuccessEdge_Rejected(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeWaitForChild,
		}},
		[]Edge{{Source: "spawn", Target: "bad", Output: SpawnPortFailure}},
	)
	errs := Validate(wf, nil, nil, nil)
	if !hasError(errs, "MISSING_SPAWN_EDGE", "spawn") {
		t.Fatalf("expected MISSING_SPAWN_EDGE on spawn, got %v", codesFor(errs, "spawn"))
	}
}

func TestValidateSpawn_WaitForChildMissingBothEdges_ReportsBoth(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeWaitForChild,
		}},
		nil,
	)
	errs := Validate(wf, nil, nil, nil)
	n := 0
	for _, e := range errs {
		if e.Code == "MISSING_SPAWN_EDGE" && e.NodeID == "spawn" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("expected 2 MISSING_SPAWN_EDGE findings (success and failure), got %d: %v", n, codesFor(errs, "spawn"))
	}
}

func TestValidateSpawn_NoChildWorkflowName_Rejected(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			SpawnMode: SpawnModeFireAndForget,
		}},
		[]Edge{{Source: "spawn", Target: "ok"}},
	)
	errs := Validate(wf, nil, nil, nil)
	if !hasError(errs, "MISSING_REQUIRED_CONFIG", "spawn") {
		t.Fatalf("expected MISSING_REQUIRED_CONFIG on spawn, got %v", codesFor(errs, "spawn"))
	}
}

func TestValidateSpawn_FireAndForgetWithBranchPorts_Rejected(t *testing.T) {
	// An author who wires branch ports in fire-and-forget mode has
	// misunderstood the mode; silently ignoring the ports would strand half
	// the graph.
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeFireAndForget,
		}},
		[]Edge{
			{Source: "spawn", Target: "ok", Output: SpawnPortSuccess},
			{Source: "spawn", Target: "bad", Output: SpawnPortFailure},
		},
	)
	errs := Validate(wf, nil, nil, nil)
	if !hasError(errs, "INVALID_SPAWN_WORKFLOW", "spawn") {
		t.Fatalf("expected INVALID_SPAWN_WORKFLOW on spawn, got %v", codesFor(errs, "spawn"))
	}
}

func TestValidateSpawn_AsEntrypoint_Rejected(t *testing.T) {
	wf := &Workflow{
		Name: "parent",
		Nodes: []Node{
			{ID: "spawn", Type: NodeTypeSpawnWorkflow, Entrypoint: true, Config: NodeConfig{
				ChildWorkflow: "child", SpawnMode: SpawnModeFireAndForget,
			}},
			{ID: "ok", Type: NodeTypeTransform,
				Config: NodeConfig{Operations: []Operation{{Variable: "y", Operation: "set", Value: 1}}}},
		},
		Edges: []Edge{{Source: "spawn", Target: "ok"}},
	}
	errs := Validate(wf, nil, nil, nil)
	if !hasError(errs, "INVALID_SPAWN_WORKFLOW", "spawn") {
		t.Fatalf("expected INVALID_SPAWN_WORKFLOW on spawn, got %v", codesFor(errs, "spawn"))
	}
}

// ---------- edge cases ----------

func TestValidateSpawn_EmptyMode_RejectedNotInherited(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", // SpawnMode deliberately unset
		}},
		[]Edge{{Source: "spawn", Target: "ok"}},
	)
	errs := Validate(wf, nil, nil, nil)
	if !hasError(errs, "INVALID_SPAWN_MODE", "spawn") {
		t.Fatalf("expected INVALID_SPAWN_MODE for an empty mode, got %v", codesFor(errs, "spawn"))
	}
}

func TestValidateSpawn_WrongCaseMode_Rejected(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: "Wait_For_Child",
		}},
		[]Edge{
			{Source: "spawn", Target: "ok", Output: SpawnPortSuccess},
			{Source: "spawn", Target: "bad", Output: SpawnPortFailure},
		},
	)
	errs := Validate(wf, nil, nil, nil)
	if !hasError(errs, "INVALID_SPAWN_MODE", "spawn") {
		t.Fatalf("expected INVALID_SPAWN_MODE for wrong-case mode, got %v", codesFor(errs, "spawn"))
	}
}

func TestValidateSpawn_UnknownModeSkipsPortChecks(t *testing.T) {
	// An unknown mode reports exactly one finding (the mode), not a cascade of
	// port findings whose applicability is undefined.
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: "async",
		}},
		nil,
	)
	errs := Validate(wf, nil, nil, nil)
	got := codesFor(errs, "spawn")
	if len(got) != 1 || got[0] != "INVALID_SPAWN_MODE" {
		t.Fatalf("expected exactly [INVALID_SPAWN_MODE], got %v", got)
	}
}

func TestValidateSpawn_FireAndForgetWithTwoUnnamedEdges_Rejected(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeFireAndForget,
		}},
		[]Edge{
			{Source: "spawn", Target: "ok"},
			{Source: "spawn", Target: "bad"},
		},
	)
	errs := Validate(wf, nil, nil, nil)
	if !hasError(errs, "INVALID_SPAWN_WORKFLOW", "spawn") {
		t.Fatalf("expected INVALID_SPAWN_WORKFLOW for two outgoing edges, got %v", codesFor(errs, "spawn"))
	}
}

func TestValidateSpawn_WaitForChildExtraUnnamedEdge_Rejected(t *testing.T) {
	// A wait-for-child node routes ONLY by port. An extra unnamed edge would be
	// followed by findNextNode for an empty port and silently bypass the branch.
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeWaitForChild,
		}},
		[]Edge{
			{Source: "spawn", Target: "ok", Output: SpawnPortSuccess},
			{Source: "spawn", Target: "bad", Output: SpawnPortFailure},
			{Source: "spawn", Target: "ok"},
		},
	)
	errs := Validate(wf, nil, nil, nil)
	if !hasError(errs, "INVALID_SPAWN_WORKFLOW", "spawn") {
		t.Fatalf("expected INVALID_SPAWN_WORKFLOW for an unnamed edge on a wait node, got %v", codesFor(errs, "spawn"))
	}
}

// ---------- model_profile posture, at AUTHOR time ----------

// fixedProfileChecker resolves exactly the aliases it is given. It HONOURS the
// alias argument: a checker that answered "chat" for everything would make the
// mismatch test unfailable.
type fixedProfileChecker map[string]string

func (c fixedProfileChecker) ProfileKind(alias string) (string, bool) {
	k, ok := c[alias]
	return k, ok
}

// A spawn node's input-preparation call is tool-SUPPRESSED, so an agent-kind
// profile is a config-driven escalation from a reasoning call to an RCE-capable
// one. The engine's kind guard fails closed at run time; this is its author-time
// counterpart, which every other LLM-calling node type already has.
func TestValidateSpawn_AgentProfile_RejectedAtAuthorTime(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeFireAndForget, ModelProfile: "agenty",
		}},
		[]Edge{{Source: "spawn", Target: "ok"}},
	)
	errs := Validate(wf, nil, fixedProfileChecker{"agenty": "agent"}, nil)
	if !hasError(errs, "PROFILE_KIND_MISMATCH", "spawn") {
		t.Fatalf("expected PROFILE_KIND_MISMATCH on spawn, got %v", codesFor(errs, "spawn"))
	}
}

func TestValidateSpawn_ChatProfile_Accepted(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeFireAndForget, ModelProfile: "chatty",
		}},
		[]Edge{{Source: "spawn", Target: "ok"}},
	)
	errs := Validate(wf, nil, fixedProfileChecker{"chatty": "chat"}, nil)
	if got := codesFor(errs, "spawn"); len(got) != 0 {
		t.Fatalf("expected no findings on spawn node, got %v", got)
	}
}

func TestValidateSpawn_UnknownProfile_Rejected(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeFireAndForget, ModelProfile: "ghost",
		}},
		[]Edge{{Source: "spawn", Target: "ok"}},
	)
	errs := Validate(wf, nil, fixedProfileChecker{}, nil)
	if !hasError(errs, "UNKNOWN_PROFILE", "spawn") {
		t.Fatalf("expected UNKNOWN_PROFILE on spawn, got %v", codesFor(errs, "spawn"))
	}
}

// An ABSENT model_profile is NOT an author error on a spawn node, unlike
// llm_call/agent_call. A child workflow that declares zero inputs needs no
// preparation call at all, and validation cannot load the child to find out —
// so requiring the alias here would reject a legitimate workflow. The empty
// alias fails closed at run time only if a preparation call is actually made.
func TestValidateSpawn_NoModelProfile_IsNotAnAuthorError(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeFireAndForget,
		}},
		[]Edge{{Source: "spawn", Target: "ok"}},
	)
	errs := Validate(wf, nil, fixedProfileChecker{"chatty": "chat"}, nil)
	for _, e := range errs {
		if e.NodeID == "spawn" && (e.Code == "MISSING_REQUIRED_CONFIG" || e.Code == "UNKNOWN_PROFILE") {
			t.Fatalf("an absent model_profile must not be reported on a spawn node: %+v", e)
		}
	}
}

// ---------- non-spawn nodes are untouched ----------

func TestValidateSpawn_OtherNodeTypesUnaffected(t *testing.T) {
	wf := &Workflow{
		Name: "plain",
		Nodes: []Node{
			{ID: "entry", Type: NodeTypeTransform, Entrypoint: true,
				Config: NodeConfig{Operations: []Operation{{Variable: "x", Operation: "set", Value: 1}}}},
		},
	}
	errs := Validate(wf, nil, nil, nil)
	for _, e := range errs {
		if e.Code == "INVALID_SPAWN_MODE" || e.Code == "MISSING_SPAWN_EDGE" || e.Code == "INVALID_SPAWN_WORKFLOW" {
			t.Fatalf("spawn validation leaked onto a transform node: %+v", e)
		}
	}
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/workflow/ -run TestValidateSpawn
```

Expected output (the arm does not exist, so every finding assertion fails):

```
--- FAIL: TestValidateSpawn_WaitForChildMissingFailureEdge_Rejected (0.00s)
    validation_spawn_test.go:...: expected MISSING_SPAWN_EDGE on spawn, got []
--- FAIL: TestValidateSpawn_NoChildWorkflowName_Rejected (0.00s)
    validation_spawn_test.go:...: expected MISSING_REQUIRED_CONFIG on spawn, got []
...
FAIL	github.com/arbitron/arbitron/domain/workflow	0.2s
```

- [ ] **Step 3: Minimal implementation**

In `domain/workflow/validation.go`, add the arm to the `validateNodeConfigs` switch, right after the `NodeTypeWebhookWait` case (~line 539):

```go
		case NodeTypeWebhookWait:
			errs = append(errs, validateWebhookWaitNode(n, wf.Edges)...)
		case NodeTypeSpawnWorkflow:
			errs = append(errs, validateSpawnWorkflowNode(n, wf.Edges)...)
```

Then append the function after `validateWebhookWaitNode` (~line 626):

```go
// validateSpawnWorkflowNode checks spawn_workflow-specific validation rules.
//
// The mode is checked FIRST and, when unrecognized, short-circuits: the port
// rules are mode-specific, so reporting them against an undefined mode would
// bury the one finding the author can act on. An empty mode is an authoring
// error, NOT an inheritance request (see IsValidSpawnMode).
func validateSpawnWorkflowNode(node Node, edges []Edge) []ValidationError {
	var errs []ValidationError

	if node.Entrypoint {
		errs = append(errs, ValidationError{
			Code:    "INVALID_SPAWN_WORKFLOW",
			Message: "spawn_workflow node " + node.ID + " cannot be an entrypoint",
			NodeID:  node.ID,
		})
	}

	if node.Config.ChildWorkflow == "" {
		errs = append(errs, ValidationError{
			Code:    "MISSING_REQUIRED_CONFIG",
			Message: "spawn_workflow node " + node.ID + " must have a childWorkflow",
			NodeID:  node.ID,
		})
	}

	if !IsValidSpawnMode(node.Config.SpawnMode) {
		errs = append(errs, ValidationError{
			Code: "INVALID_SPAWN_MODE",
			Message: "spawn_workflow node " + node.ID + " has an unrecognized spawnMode " +
				strconv.Quote(node.Config.SpawnMode) + " (must be exactly " +
				strconv.Quote(SpawnModeFireAndForget) + " or " + strconv.Quote(SpawnModeWaitForChild) + ")",
			NodeID: node.ID,
		})
		return errs
	}

	// Collect this node's outgoing ports. Mirrors validateConditionalEdges'
	// (source, output) set technique.
	ports := map[string]int{}
	for _, e := range edges {
		if e.Source == node.ID {
			ports[e.Output]++
		}
	}

	switch node.Config.SpawnMode {
	case SpawnModeWaitForChild:
		for _, want := range []string{SpawnPortSuccess, SpawnPortFailure} {
			if ports[want] == 0 {
				errs = append(errs, ValidationError{
					Code:    "MISSING_SPAWN_EDGE",
					Message: "spawn_workflow node " + node.ID + " (wait_for_child) missing edge for output: " + want,
					NodeID:  node.ID,
				})
			}
		}
		if ports[""] > 0 {
			errs = append(errs, ValidationError{
				Code: "INVALID_SPAWN_WORKFLOW",
				Message: "spawn_workflow node " + node.ID +
					" (wait_for_child) must not have an unnamed outgoing edge; route through the success and failure ports",
				NodeID: node.ID,
			})
		}
	case SpawnModeFireAndForget:
		if ports[SpawnPortSuccess] > 0 || ports[SpawnPortFailure] > 0 {
			errs = append(errs, ValidationError{
				Code: "INVALID_SPAWN_WORKFLOW",
				Message: "spawn_workflow node " + node.ID +
					" (fire_and_forget) must not wire the success or failure ports; those exist only in wait_for_child mode",
				NodeID: node.ID,
			})
		}
		if ports[""] != 1 {
			errs = append(errs, ValidationError{
				Code: "INVALID_SPAWN_WORKFLOW",
				Message: "spawn_workflow node " + node.ID +
					" (fire_and_forget) must have exactly one unnamed outgoing edge",
				NodeID: node.ID,
			})
		}
	}

	return errs
}
```

Add `strconv` to the import block at the top of `domain/workflow/validation.go`:

```go
import (
	"strconv"
	"strings"
)
```

- [ ] **Step 3b: Give the spawn node an author-time model_profile posture check**

`validateModelProfiles` (`domain/workflow/validation.go:157`) is the author-time
counterpart of `domain/engine`'s binding-kind guard. Every other LLM-calling node
type is in its switch; `spawn_workflow` performs a tool-suppressed preparation
chat call and must be too, or an agent-kind alias is only caught at run time.

Its one asymmetry: an **absent** alias is not an author error on a spawn node.
A child workflow declaring zero inputs needs no preparation call at all, and
validation cannot load the child to find out — so requiring the alias would
reject a legitimate workflow. Only a *set* alias is checked.

Change the switch head (line 160-172) and the empty-alias arm (line 174-181):

```go
	for _, n := range wf.Nodes {
		var wantKind string
		switch n.Type {
		case NodeTypeLLMCall:
			wantKind = "chat"
		case NodeTypeAgentCall:
			wantKind = "agent"
		case NodeTypeInteractiveLLM:
			wantKind = "chat"
		case NodeTypeInteractiveAgent:
			wantKind = "agent"
		case NodeTypeSpawnWorkflow:
			// The input-preparation call is tool-SUPPRESSED, so an agent-kind
			// binding here is a config-driven escalation from a reasoning call to
			// an RCE-capable one. This is the author-time half of the guard
			// domain/engine's expectedKindForNodeType enforces at run time.
			wantKind = "chat"
		default:
			continue
		}

		if n.Config.ModelProfile == "" {
			// A spawn node's alias is OPTIONAL: a child that declares zero inputs
			// never makes a preparation call, and validation cannot load the child
			// to know. An empty alias fails closed at run time only if a call is
			// actually made. Every other type here REQUIRES it.
			if n.Type == NodeTypeSpawnWorkflow {
				continue
			}
			errs = append(errs, ValidationError{
				Code:    "MISSING_REQUIRED_CONFIG",
				Message: string(n.Type) + " node " + n.ID + " must have a model_profile",
				NodeID:  n.ID,
			})
			continue
		}
```

Everything below that (the `Model` conflict check, the unknown-alias check, and
the kind comparison) is unchanged and now covers the spawn node too.

- [ ] **Step 4: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/workflow/validation.go domain/workflow/validation_spawn_test.go
go test ./domain/workflow/ -run TestValidateSpawn -v
```

Expected output:

```
--- PASS: TestValidateSpawn_WaitForChildWithBothPorts_Valid (0.00s)
--- PASS: TestValidateSpawn_FireAndForgetWithOneUnnamedEdge_Valid (0.00s)
--- PASS: TestValidateSpawn_WaitForChildMissingFailureEdge_Rejected (0.00s)
--- PASS: TestValidateSpawn_WaitForChildMissingSuccessEdge_Rejected (0.00s)
--- PASS: TestValidateSpawn_WaitForChildMissingBothEdges_ReportsBoth (0.00s)
--- PASS: TestValidateSpawn_NoChildWorkflowName_Rejected (0.00s)
--- PASS: TestValidateSpawn_FireAndForgetWithBranchPorts_Rejected (0.00s)
--- PASS: TestValidateSpawn_AsEntrypoint_Rejected (0.00s)
--- PASS: TestValidateSpawn_EmptyMode_RejectedNotInherited (0.00s)
--- PASS: TestValidateSpawn_WrongCaseMode_Rejected (0.00s)
--- PASS: TestValidateSpawn_UnknownModeSkipsPortChecks (0.00s)
--- PASS: TestValidateSpawn_FireAndForgetWithTwoUnnamedEdges_Rejected (0.00s)
--- PASS: TestValidateSpawn_WaitForChildExtraUnnamedEdge_Rejected (0.00s)
--- PASS: TestValidateSpawn_AgentProfile_RejectedAtAuthorTime (0.00s)
--- PASS: TestValidateSpawn_ChatProfile_Accepted (0.00s)
--- PASS: TestValidateSpawn_UnknownProfile_Rejected (0.00s)
--- PASS: TestValidateSpawn_NoModelProfile_IsNotAnAuthorError (0.00s)
--- PASS: TestValidateSpawn_OtherNodeTypesUnaffected (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/workflow	0.2s
```

- [ ] **Step 5: Confirm the whole workflow package still passes**

```bash
go test ./domain/workflow/
```

Expected: `ok  	github.com/arbitron/arbitron/domain/workflow`

- [ ] **Step 6: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/workflow/validation.go domain/workflow/validation_spawn_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(workflow): validate spawn_workflow node contract"
```
