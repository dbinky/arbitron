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

// The generic model/model_profile conflict check (validateModelProfiles) is
// shared across every capability-checked node type, including spawn — this
// pins that the spawn arm actually reaches it rather than assuming the shared
// code path is exercised elsewhere.
func TestValidateSpawn_ModelAndModelProfileConflict_Rejected(t *testing.T) {
	wf := spawnWF(
		Node{ID: "spawn", Type: NodeTypeSpawnWorkflow, Config: NodeConfig{
			ChildWorkflow: "child", SpawnMode: SpawnModeFireAndForget,
			ModelProfile: "chatty", Model: "claude-opus-4",
		}},
		[]Edge{{Source: "spawn", Target: "ok"}},
	)
	errs := Validate(wf, nil, fixedProfileChecker{"chatty": "chat"}, nil)
	if !hasError(errs, "MODEL_PROFILE_CONFLICT", "spawn") {
		t.Fatalf("expected MODEL_PROFILE_CONFLICT on spawn, got %v", codesFor(errs, "spawn"))
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
