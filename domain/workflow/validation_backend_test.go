package workflow

import "testing"

// helper: build a minimal valid agent_call node with a backend value, plus the
// templateFile/model_profile so the only backend-related finding can be
// isolated. Backend validation runs in validateNodeConfigs, independent of the
// profile/connection checkers.
func agentNodeWithBackend(id, backend string) Node {
	return Node{
		ID:   id,
		Type: NodeTypeAgentCall,
		Config: NodeConfig{
			TemplateFile: "mission.txt",
			ModelProfile: "agent-default",
			Backend:      backend,
		},
	}
}

// countBackendErrors returns the INVALID_AGENT_BACKEND findings for nodeID.
func countBackendErrors(errs []ValidationError, nodeID string) int {
	n := 0
	for _, e := range errs {
		if e.Code == "INVALID_AGENT_BACKEND" && e.NodeID == nodeID {
			n++
		}
	}
	return n
}

func validateNodeOnly(node Node) []ValidationError {
	wf := &Workflow{Nodes: []Node{node}}
	return validateNodeConfigs(wf)
}

// happy — empty backend is valid (inherit).
func TestValidateBackend_EmptyIsValid(t *testing.T) {
	errs := validateNodeOnly(agentNodeWithBackend("a", ""))
	if got := countBackendErrors(errs, "a"); got != 0 {
		t.Fatalf("empty backend must be valid; got %d INVALID_AGENT_BACKEND errors", got)
	}
}

// happy — claude-code is valid.
func TestValidateBackend_ClaudeCodeValid(t *testing.T) {
	errs := validateNodeOnly(agentNodeWithBackend("a", BackendClaudeCode))
	if got := countBackendErrors(errs, "a"); got != 0 {
		t.Fatalf("claude-code must be valid; got %d errors", got)
	}
}

// happy — opencode is valid.
func TestValidateBackend_OpencodeValid(t *testing.T) {
	errs := validateNodeOnly(agentNodeWithBackend("a", BackendOpencode))
	if got := countBackendErrors(errs, "a"); got != 0 {
		t.Fatalf("opencode must be valid; got %d errors", got)
	}
}

// validation — an unknown backend is rejected with exactly one error naming the
// node and the bad value (fail-closed).
func TestValidateBackend_UnknownRejected(t *testing.T) {
	errs := validateNodeOnly(agentNodeWithBackend("a", "gpt-cli"))
	if got := countBackendErrors(errs, "a"); got != 1 {
		t.Fatalf("unknown backend must produce exactly one error; got %d", got)
	}
	var found ValidationError
	for _, e := range errs {
		if e.Code == "INVALID_AGENT_BACKEND" {
			found = e
		}
	}
	if found.NodeID != "a" {
		t.Fatalf("error must name the node; got NodeID %q", found.NodeID)
	}
	if !contains(found.Message, "a") || !contains(found.Message, "gpt-cli") {
		t.Fatalf("message must name node and bad value; got %q", found.Message)
	}
}

// boundary — case sensitivity: the allowed set is exact-match lowercase.
func TestValidateBackend_CaseSensitiveRejected(t *testing.T) {
	errs := validateNodeOnly(agentNodeWithBackend("a", "Claude-Code"))
	if got := countBackendErrors(errs, "a"); got != 1 {
		t.Fatalf("a near-miss case must be rejected; got %d errors", got)
	}
}

// boundary — backend on a non-agent node (llm_call) is ignored: a bogus value
// there produces NO backend error so chat authors are not blocked.
func TestValidateBackend_IgnoredOnLLMCall(t *testing.T) {
	node := Node{
		ID:   "chat1",
		Type: NodeTypeLLMCall,
		Config: NodeConfig{
			TemplateFile: "prompt.txt",
			Backend:      "opencode",
		},
	}
	errs := validateNodeOnly(node)
	if got := countBackendErrors(errs, "chat1"); got != 0 {
		t.Fatalf("backend must be ignored on llm_call; got %d errors", got)
	}
}

// boundary — interactive_agent (the other agent node type) DOES validate the
// backend.
func TestValidateBackend_InteractiveAgentValidates(t *testing.T) {
	node := Node{
		ID:   "ia",
		Type: NodeTypeInteractiveAgent,
		Config: NodeConfig{
			OrchestrationFile: "orch.md",
			TemplateFile:      "mission.txt",
			Backend:           "gpt-cli",
		},
	}
	errs := validateNodeOnly(node)
	if got := countBackendErrors(errs, "ia"); got != 1 {
		t.Fatalf("interactive_agent must validate backend; got %d errors", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
