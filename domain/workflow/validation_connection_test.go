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
