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
// Edge case: a profile checker returning an unrecognized kind (not "chat" or
// "agent") still produces a clear PROFILE_KIND_MISMATCH — the checker may be
// backed by a future profile registry that has expanded kinds. The mismatch
// message must name both the required kind and the actual kind.
// ---------------------------------------------------------------------------

func TestValidate_LLMCallWithUnrecognizedKind_KindMismatch(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "myp")
	// Checker returns a kind string that is not "chat" or "agent" — simulates a
	// misconfigured profile or a future extended kind unsupported by this node.
	checker := &mockProfileChecker{kinds: map[string]string{"myp": "multimodal"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	codes := extractCodes(errs)
	require.Equal(t, 1, countCode(codes, "PROFILE_KIND_MISMATCH"))

	var got workflow.ValidationError
	for _, e := range errs {
		if e.Code == "PROFILE_KIND_MISMATCH" {
			got = e
		}
	}
	// Message must name the required kind ("chat") and the actual kind ("multimodal").
	assert.Contains(t, got.Message, "chat",
		"mismatch message must name the required kind")
	assert.Contains(t, got.Message, "multimodal",
		"mismatch message must name the actual kind returned by the checker")
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

// ---------------------------------------------------------------------------
// Tests: interactive node kind-checking (interactive_llm requires chat;
// interactive_agent requires agent).
// ---------------------------------------------------------------------------

// interactiveCallNodeWorkflow builds a minimal workflow whose entry node is of
// the given interactive type with both required config fields (orchestrationFile
// + templateFile) set, so only the profile-kind validation is under test.
func interactiveCallNodeWorkflow(nodeType workflow.NodeType, alias string) *workflow.Workflow {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = nodeType
	wf.Nodes[0].Config.ModelProfile = alias
	wf.Nodes[0].Config.TemplateFile = "prompt.hbs"
	wf.Nodes[0].Config.OrchestrationFile = "orch.yaml"
	return wf
}

func TestValidate_InteractiveLLM_WithChatProfile_OK(t *testing.T) {
	wf := interactiveCallNodeWorkflow(workflow.NodeTypeInteractiveLLM, "my-chat")
	checker := &mockProfileChecker{kinds: map[string]string{"my-chat": "chat"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	assert.Empty(t, errs)
}

func TestValidate_InteractiveAgent_WithAgentProfile_OK(t *testing.T) {
	wf := interactiveCallNodeWorkflow(workflow.NodeTypeInteractiveAgent, "my-agent")
	checker := &mockProfileChecker{kinds: map[string]string{"my-agent": "agent"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	assert.Empty(t, errs)
}

func TestValidate_InteractiveLLM_WithAgentProfile_KindMismatch(t *testing.T) {
	wf := interactiveCallNodeWorkflow(workflow.NodeTypeInteractiveLLM, "agentp")
	checker := &mockProfileChecker{kinds: map[string]string{"agentp": "agent"}}

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
	assert.Contains(t, mismatch.Message, "chat")
	assert.Contains(t, mismatch.Message, "agent")
}

func TestValidate_InteractiveAgent_WithChatProfile_KindMismatch(t *testing.T) {
	wf := interactiveCallNodeWorkflow(workflow.NodeTypeInteractiveAgent, "chatp")
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

// ---------------------------------------------------------------------------
// SF2 (PR #18, comment 3350531429): node.Config.Model is dead for any node that
// resolves via model_profile (the model always comes from the resolved
// binding). A workflow that sets BOTH model and model_profile is a maintenance
// trap — the model knob is silently ignored. Validation must reject it with an
// explicit, conflict-naming error rather than a silent no-op.
// ---------------------------------------------------------------------------

func TestValidate_ModelAndModelProfileConflict_Rejected(t *testing.T) {
	for _, nodeType := range []workflow.NodeType{
		workflow.NodeTypeLLMCall,
		workflow.NodeTypeAgentCall,
		workflow.NodeTypeInteractiveLLM,
		workflow.NodeTypeInteractiveAgent,
	} {
		t.Run(string(nodeType), func(t *testing.T) {
			wantKind := "chat"
			if nodeType == workflow.NodeTypeAgentCall || nodeType == workflow.NodeTypeInteractiveAgent {
				wantKind = "agent"
			}
			wf := callNodeWorkflow(nodeType, "prof")
			wf.Nodes[0].Config.Model = "claude-opus-4" // the dead, conflicting field

			checker := &mockProfileChecker{kinds: map[string]string{"prof": wantKind}}
			errs := workflow.Validate(wf, nil, checker, nil)

			codes := make([]string, len(errs))
			var conflictMsg string
			for i, e := range errs {
				codes[i] = e.Code
				if e.Code == "MODEL_PROFILE_CONFLICT" {
					conflictMsg = e.Message
				}
			}
			require.Equal(t, 1, countCode(codes, "MODEL_PROFILE_CONFLICT"),
				"setting model alongside model_profile must produce exactly one conflict error")
			// The message must name the conflict, not be a bare "invalid config".
			assert.Contains(t, conflictMsg, "model")
			assert.Contains(t, conflictMsg, "model_profile")
		})
	}
}

func TestValidate_ModelProfileWithoutModel_OK(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "prof")
	wf.Nodes[0].Config.Model = "" // no conflict
	checker := &mockProfileChecker{kinds: map[string]string{"prof": "chat"}}

	errs := workflow.Validate(wf, nil, checker, nil)
	codes := make([]string, len(errs))
	for i, e := range errs {
		codes[i] = e.Code
	}
	assert.NotContains(t, codes, "MODEL_PROFILE_CONFLICT")
}
