package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProfileChecker is a test double for the ProfileChecker interface: an
// in-memory map of alias -> kind. An alias absent from the map reports ok=false
// (mirroring an unknown profile alias).
type mockProfileChecker struct {
	kinds map[string]string
}

func (m *mockProfileChecker) ProfileKind(alias string) (string, bool) {
	k, ok := m.kinds[alias]
	return k, ok
}

// callNodeWorkflow returns a minimal valid workflow whose single entry node is
// of the given type with the given model_profile alias and a template file set,
// so only the capability-validation behavior is under test.
func callNodeWorkflow(nodeType workflow.NodeType, alias string) *workflow.Workflow {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = nodeType
	wf.Nodes[0].Config.ModelProfile = alias
	wf.Nodes[0].Config.TemplateFile = "prompt.hbs"
	return wf
}

// ---------------------------------------------------------------------------
// Happy path: correct kinds produce no capability errors.
// ---------------------------------------------------------------------------

func TestValidate_AgentCallWithAgentProfile_OK(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeAgentCall, "my-agent")
	checker := &mockProfileChecker{kinds: map[string]string{"my-agent": "agent"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	assert.Empty(t, errs)
}

func TestValidate_LLMCallWithChatProfile_OK(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "my-chat")
	checker := &mockProfileChecker{kinds: map[string]string{"my-chat": "chat"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	assert.Empty(t, errs)
}

// ---------------------------------------------------------------------------
// Success: a workflow mixing both kinds validates, and the new agent_call
// templateFile structural requirement passes when a template is present.
// ---------------------------------------------------------------------------

func TestValidate_MixedKindsWorkflow_OK(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "mixed",
		Nodes: []workflow.Node{
			{
				ID:         "llm",
				Type:       workflow.NodeTypeLLMCall,
				Entrypoint: true,
				Config: workflow.NodeConfig{
					ModelProfile: "chatp",
					TemplateFile: "llm.hbs",
				},
			},
			{
				ID:   "agent",
				Type: workflow.NodeTypeAgentCall,
				Config: workflow.NodeConfig{
					ModelProfile: "agentp",
					TemplateFile: "agent.hbs",
				},
			},
		},
		Edges: []workflow.Edge{
			{Source: "llm", Target: "agent"},
		},
	}
	checker := &mockProfileChecker{kinds: map[string]string{
		"chatp":  "chat",
		"agentp": "agent",
	}}

	errs := workflow.Validate(wf, nil, checker, nil)
	assert.Empty(t, errs)
}

func TestValidate_AgentCallTemplateFilePresent_NoStructuralError(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeAgentCall, "my-agent")
	checker := &mockProfileChecker{kinds: map[string]string{"my-agent": "agent"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	codes := extractCodes(errs)
	assert.NotContains(t, codes, "MISSING_REQUIRED_CONFIG")
}

// ---------------------------------------------------------------------------
// Failure: kind mismatch yields exactly one PROFILE_KIND_MISMATCH per node.
// ---------------------------------------------------------------------------

func TestValidate_AgentCallWithChatProfile_KindMismatch(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeAgentCall, "chatp")
	checker := &mockProfileChecker{kinds: map[string]string{"chatp": "chat"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	codes := extractCodes(errs)
	require.Equal(t, 1, countCode(codes, "PROFILE_KIND_MISMATCH"))

	var mismatch workflow.ValidationError
	for _, e := range errs {
		if e.Code == "PROFILE_KIND_MISMATCH" {
			mismatch = e
		}
	}
	assert.Equal(t, "entry", mismatch.NodeID)
	assert.Contains(t, mismatch.Message, "agent")
	assert.Contains(t, mismatch.Message, "chat")
}

func TestValidate_LLMCallWithAgentProfile_KindMismatch(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "agentp")
	checker := &mockProfileChecker{kinds: map[string]string{"agentp": "agent"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	codes := extractCodes(errs)
	require.Equal(t, 1, countCode(codes, "PROFILE_KIND_MISMATCH"))
}

// ---------------------------------------------------------------------------
// Error: missing alias -> MISSING_REQUIRED_CONFIG; unknown alias ->
// UNKNOWN_PROFILE.
// ---------------------------------------------------------------------------

func TestValidate_MissingModelProfile_Error(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "")
	checker := &mockProfileChecker{kinds: map[string]string{}}

	errs := workflow.Validate(wf, nil, checker, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "MISSING_REQUIRED_CONFIG")
	// No spurious kind/unknown errors when the alias is absent.
	assert.NotContains(t, codes, "PROFILE_KIND_MISMATCH")
	assert.NotContains(t, codes, "UNKNOWN_PROFILE")
}

func TestValidate_AgentCallMissingModelProfile_Error(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeAgentCall, "")
	checker := &mockProfileChecker{kinds: map[string]string{}}

	errs := workflow.Validate(wf, nil, checker, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "MISSING_REQUIRED_CONFIG")
}

func TestValidate_UnknownModelProfile_Error(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "ghost")
	checker := &mockProfileChecker{kinds: map[string]string{"other": "chat"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	codes := extractCodes(errs)
	require.Equal(t, 1, countCode(codes, "UNKNOWN_PROFILE"))

	var unknown workflow.ValidationError
	for _, e := range errs {
		if e.Code == "UNKNOWN_PROFILE" {
			unknown = e
		}
	}
	assert.Equal(t, "entry", unknown.NodeID)
	assert.Contains(t, unknown.Message, "ghost")
}

// ---------------------------------------------------------------------------
// Edge cases: nil profile checker skips capability validation entirely; a
// workflow with no llm_call/agent_call nodes produces no profile errors even
// with a non-nil checker.
// ---------------------------------------------------------------------------

func TestValidate_NilProfileChecker_SkipsCapabilityValidation(t *testing.T) {
	// An agent_call bound to a chat profile would normally mismatch, but with a
	// nil profile checker the capability pass must be skipped (backward-compat).
	wf := callNodeWorkflow(workflow.NodeTypeAgentCall, "chatp")

	errs := workflow.Validate(wf, nil, nil, nil)
	codes := extractCodes(errs)
	assert.NotContains(t, codes, "PROFILE_KIND_MISMATCH")
	assert.NotContains(t, codes, "UNKNOWN_PROFILE")
}

func TestValidate_NoCallNodes_NoProfileErrors(t *testing.T) {
	wf := minimalValidWorkflow() // single transform node
	checker := &mockProfileChecker{kinds: map[string]string{}}

	errs := workflow.Validate(wf, nil, checker, nil)
	assert.Empty(t, errs)
}

// ---------------------------------------------------------------------------
// Regression: capability validation runs as part of Validate (alongside
// existing structural checks), not only in isolation.
// ---------------------------------------------------------------------------

func TestValidate_CapabilityRunsWithinValidate(t *testing.T) {
	// agent_call with a chat profile AND a missing template: both the structural
	// MISSING_REQUIRED_CONFIG (template) and the capability PROFILE_KIND_MISMATCH
	// must surface from a single Validate call.
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = workflow.NodeTypeAgentCall
	wf.Nodes[0].Config.ModelProfile = "chatp"
	// no TemplateFile set -> structural error
	checker := &mockProfileChecker{kinds: map[string]string{"chatp": "chat"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "MISSING_REQUIRED_CONFIG", "structural template check still fires")
	assert.Contains(t, codes, "PROFILE_KIND_MISMATCH", "capability check runs within Validate")
}
