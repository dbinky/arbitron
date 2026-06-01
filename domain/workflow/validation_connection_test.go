package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockConnChecker is a test double for the ConnectionChecker interface: an
// in-memory map of alias -> ConnectionRef and a set of existing connection
// names. An alias absent from refs reports aliasKnown=false.
type mockConnChecker struct {
	refs  map[string]string // alias -> connectionRef
	known map[string]bool   // alias presence (aliasKnown)
	conns map[string]bool   // existing connection names
}

func (m *mockConnChecker) ProfileConnectionRef(alias string) (string, bool) {
	ref, ok := m.refs[alias]
	if !ok {
		// Allow modelling a known alias with an empty ref via the known set.
		if m.known != nil && m.known[alias] {
			return "", true
		}
		return "", false
	}
	return ref, true
}

func (m *mockConnChecker) ConnectionExists(name string) bool {
	return m.conns[name]
}

// ---------------------------------------------------------------------------
// Happy path: a profile referencing an existing connection validates clean.
// ---------------------------------------------------------------------------

func TestValidate_ConnectionRefExists_OK(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "chatp")
	profiles := &mockProfileChecker{kinds: map[string]string{"chatp": "chat"}}
	conns := &mockConnChecker{
		refs:  map[string]string{"chatp": "azure-prod"},
		conns: map[string]bool{"azure-prod": true},
	}

	errs := workflow.Validate(wf, nil, profiles, conns)
	assert.Empty(t, errs)
}

// ---------------------------------------------------------------------------
// Success: an empty ConnectionRef (claude-cli subscription path) validates
// with no existence check performed.
// ---------------------------------------------------------------------------

func TestValidate_EmptyConnectionRef_OK(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "chatp")
	profiles := &mockProfileChecker{kinds: map[string]string{"chatp": "chat"}}
	conns := &mockConnChecker{
		refs:  map[string]string{"chatp": ""}, // subscription path
		conns: map[string]bool{},
	}

	errs := workflow.Validate(wf, nil, profiles, conns)
	codes := extractCodes(errs)
	assert.NotContains(t, codes, "UNKNOWN_CONNECTION")
}

// ---------------------------------------------------------------------------
// Failure: a profile referencing a missing connection yields a clear error
// naming both the alias and the connection.
// ---------------------------------------------------------------------------

func TestValidate_ConnectionRefMissing_Error(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "chatp")
	profiles := &mockProfileChecker{kinds: map[string]string{"chatp": "chat"}}
	conns := &mockConnChecker{
		refs:  map[string]string{"chatp": "ghost-conn"},
		conns: map[string]bool{"other": true},
	}

	errs := workflow.Validate(wf, nil, profiles, conns)
	codes := extractCodes(errs)
	require.Equal(t, 1, countCode(codes, "UNKNOWN_CONNECTION"))

	var got workflow.ValidationError
	for _, e := range errs {
		if e.Code == "UNKNOWN_CONNECTION" {
			got = e
		}
	}
	assert.Equal(t, "entry", got.NodeID)
	assert.Contains(t, got.Message, "chatp")
	assert.Contains(t, got.Message, "ghost-conn")
}

// ---------------------------------------------------------------------------
// Error: nil ConnectionChecker skips the connection check without panicking
// while the Phase-1 ProfileChecker still runs kind-matching; an unknown alias
// is not double-reported by the connection check (Phase 1 owns that error).
// ---------------------------------------------------------------------------

func TestValidate_NilConnChecker_SkipsConnectionCheck(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "chatp")
	profiles := &mockProfileChecker{kinds: map[string]string{"chatp": "chat"}}

	errs := workflow.Validate(wf, nil, profiles, nil)
	codes := extractCodes(errs)
	assert.NotContains(t, codes, "UNKNOWN_CONNECTION")
	assert.Empty(t, errs)
}

func TestValidate_UnknownAlias_NotDoubleReportedByConnectionCheck(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeLLMCall, "ghost")
	profiles := &mockProfileChecker{kinds: map[string]string{"other": "chat"}}
	conns := &mockConnChecker{
		refs:  map[string]string{}, // ghost alias unknown
		conns: map[string]bool{},
	}

	errs := workflow.Validate(wf, nil, profiles, conns)
	codes := extractCodes(errs)
	// Phase 1 reports the unknown alias; the connection check must not add to it.
	assert.Equal(t, 1, countCode(codes, "UNKNOWN_PROFILE"))
	assert.NotContains(t, codes, "UNKNOWN_CONNECTION")
}

// ---------------------------------------------------------------------------
// Edge case: a workflow mixing a no-connection Claude profile and a
// real-connection profile validates the real one and skips the empty one;
// kind-matching from Phase 1 still fails an agent_call->chat profile.
// ---------------------------------------------------------------------------

func TestValidate_MixedConnectionRefs_ValidatesRealSkipsEmpty(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "mixed",
		Nodes: []workflow.Node{
			{
				ID:         "sub",
				Type:       workflow.NodeTypeLLMCall,
				Entrypoint: true,
				Config: workflow.NodeConfig{
					ModelProfile: "claudep",
					TemplateFile: "sub.hbs",
				},
			},
			{
				ID:   "real",
				Type: workflow.NodeTypeLLMCall,
				Config: workflow.NodeConfig{
					ModelProfile: "azurep",
					TemplateFile: "real.hbs",
				},
			},
		},
		Edges: []workflow.Edge{{Source: "sub", Target: "real"}},
	}
	profiles := &mockProfileChecker{kinds: map[string]string{
		"claudep": "chat",
		"azurep":  "chat",
	}}
	conns := &mockConnChecker{
		refs: map[string]string{
			"claudep": "",           // subscription path: skipped
			"azurep":  "azure-prod", // real connection: must exist
		},
		conns: map[string]bool{"azure-prod": true},
	}

	errs := workflow.Validate(wf, nil, profiles, conns)
	assert.Empty(t, errs)
}

func TestValidate_KindMismatchStillEnforced_Regression(t *testing.T) {
	// agent_call bound to a chat profile must still fail kind-matching even with
	// a connection checker present; this task must not weaken Phase 1.
	wf := callNodeWorkflow(workflow.NodeTypeAgentCall, "chatp")
	profiles := &mockProfileChecker{kinds: map[string]string{"chatp": "chat"}}
	conns := &mockConnChecker{
		refs:  map[string]string{"chatp": "azure-prod"},
		conns: map[string]bool{"azure-prod": true},
	}

	errs := workflow.Validate(wf, nil, profiles, conns)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "PROFILE_KIND_MISMATCH")
}

// ---------------------------------------------------------------------------
// Edge case: multiple call nodes with missing connections each generate their
// own UNKNOWN_CONNECTION error — one per node, none silently dropped.
// ---------------------------------------------------------------------------

func TestValidate_MultipleNodesMissingConnections_EachErrored(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "multi-miss",
		Nodes: []workflow.Node{
			{
				ID:         "llm1",
				Type:       workflow.NodeTypeLLMCall,
				Entrypoint: true,
				Config: workflow.NodeConfig{
					ModelProfile: "chatp",
					TemplateFile: "llm.hbs",
				},
			},
			{
				ID:   "agent1",
				Type: workflow.NodeTypeAgentCall,
				Config: workflow.NodeConfig{
					ModelProfile: "agentp",
					TemplateFile: "agent.hbs",
				},
			},
		},
		Edges: []workflow.Edge{{Source: "llm1", Target: "agent1"}},
	}
	profiles := &mockProfileChecker{kinds: map[string]string{
		"chatp":  "chat",
		"agentp": "agent",
	}}
	conns := &mockConnChecker{
		refs: map[string]string{
			"chatp":  "missing-conn-1",
			"agentp": "missing-conn-2",
		},
		conns: map[string]bool{}, // neither connection exists
	}

	errs := workflow.Validate(wf, nil, profiles, conns)
	codes := extractCodes(errs)

	require.Equal(t, 2, countCode(codes, "UNKNOWN_CONNECTION"),
		"each node must produce its own UNKNOWN_CONNECTION error; errors must not be silently dropped")

	// Each error names its own node.
	nodeIDs := map[string]bool{}
	for _, e := range errs {
		if e.Code == "UNKNOWN_CONNECTION" {
			nodeIDs[e.NodeID] = true
		}
	}
	assert.True(t, nodeIDs["llm1"], "llm1 must have its own UNKNOWN_CONNECTION error")
	assert.True(t, nodeIDs["agent1"], "agent1 must have its own UNKNOWN_CONNECTION error")
}

// ---------------------------------------------------------------------------
// agent_call parity: validateConnectionReferences handles both llm_call and
// agent_call in its switch; these tests cover the agent_call branches.
// ---------------------------------------------------------------------------

// Happy path: an agent_call whose profile references an existing connection
// validates clean — the agent_call branch is symmetric with llm_call.
func TestValidate_AgentCallConnectionRefExists_OK(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeAgentCall, "agentp")
	profiles := &mockProfileChecker{kinds: map[string]string{"agentp": "agent"}}
	conns := &mockConnChecker{
		refs:  map[string]string{"agentp": "opencode-conn"},
		conns: map[string]bool{"opencode-conn": true},
	}

	errs := workflow.Validate(wf, nil, profiles, conns)
	assert.Empty(t, errs)
}

// Failure: an agent_call whose profile references a missing connection yields
// UNKNOWN_CONNECTION naming both the alias and the missing connection.
func TestValidate_AgentCallConnectionRefMissing_Error(t *testing.T) {
	wf := callNodeWorkflow(workflow.NodeTypeAgentCall, "agentp")
	profiles := &mockProfileChecker{kinds: map[string]string{"agentp": "agent"}}
	conns := &mockConnChecker{
		refs:  map[string]string{"agentp": "ghost-conn"},
		conns: map[string]bool{"other": true},
	}

	errs := workflow.Validate(wf, nil, profiles, conns)
	codes := extractCodes(errs)
	require.Equal(t, 1, countCode(codes, "UNKNOWN_CONNECTION"))

	var got workflow.ValidationError
	for _, e := range errs {
		if e.Code == "UNKNOWN_CONNECTION" {
			got = e
		}
	}
	assert.Equal(t, "entry", got.NodeID)
	assert.Contains(t, got.Message, "agentp")
	assert.Contains(t, got.Message, "ghost-conn")
}
