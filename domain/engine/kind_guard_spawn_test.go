package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// TestExpectedKindForSpawnWorkflow: the spawn node performs a tool-SUPPRESSED
// reasoning call for input preparation, so its resolved binding must be chat.
func TestExpectedKindForSpawnWorkflow(t *testing.T) {
	kind, ok := expectedKindForNodeType(workflow.NodeTypeSpawnWorkflow)
	if !ok {
		t.Fatal("expectedKindForNodeType(spawn_workflow) returned ok=false; " +
			"an unmapped node type makes assertBindingKind a silent no-op")
	}
	if kind != provider.ProfileKindChat {
		t.Fatalf("expectedKindForNodeType(spawn_workflow) = %q, want %q", kind, provider.ProfileKindChat)
	}
}

// TestAssertBindingKind_SpawnWithAgentBinding_FailsClosed: a spawn node whose
// profile resolves to an AGENT binding is a config-driven escalation from a
// tool-suppressed call to an RCE-capable one. It must fail closed.
func TestAssertBindingKind_SpawnWithAgentBinding_FailsClosed(t *testing.T) {
	err := assertBindingKind("spawn-1", workflow.NodeTypeSpawnWorkflow, provider.ProfileKindAgent)
	if err == nil {
		t.Fatal("expected an error for an agent binding on a spawn_workflow node")
	}
	if !errors.Is(err, ErrBindingKindMismatch) {
		t.Fatalf("error is not ErrBindingKindMismatch: %v", err)
	}
	if !strings.Contains(err.Error(), "spawn-1") {
		t.Fatalf("error must name the offending node id, got: %v", err)
	}
}

// TestAssertBindingKind_SpawnWithChatBinding_OK
func TestAssertBindingKind_SpawnWithChatBinding_OK(t *testing.T) {
	if err := assertBindingKind("spawn-1", workflow.NodeTypeSpawnWorkflow, provider.ProfileKindChat); err != nil {
		t.Fatalf("expected no error for a chat binding, got %v", err)
	}
}

// TestAssertBindingKind_UnmappedNodeType_NoOp exercises the no-op claim
// through assertBindingKind itself (not just expectedKindForNodeType): a node
// type with no chat/agent mapping must never fail closed, no matter what
// binding.Kind is passed, because the guard is not meant to enforce anything
// for it. If a future change to expectedKindForNodeType accidentally mapped
// an unrelated node type, this is the test that would catch a stray failure
// closed for it.
func TestAssertBindingKind_UnmappedNodeType_NoOp(t *testing.T) {
	for _, kind := range []provider.ProfileKind{provider.ProfileKindChat, provider.ProfileKindAgent, provider.ProfileKind("bogus")} {
		if err := assertBindingKind("script-1", workflow.NodeTypeScript, kind); err != nil {
			t.Fatalf("assertBindingKind(unmapped type, %q) = %v, want nil (no-op)", kind, err)
		}
	}
}

// TestExpectedKindForNodeType_OtherMappingsUnchanged pins that this change is
// targeted: no other node type's mapping moved.
func TestExpectedKindForNodeType_OtherMappingsUnchanged(t *testing.T) {
	cases := []struct {
		t    workflow.NodeType
		kind provider.ProfileKind
		ok   bool
	}{
		{workflow.NodeTypeLLMCall, provider.ProfileKindChat, true},
		{workflow.NodeTypeInteractiveLLM, provider.ProfileKindChat, true},
		{workflow.NodeTypeAgentCall, provider.ProfileKindAgent, true},
		{workflow.NodeTypeInteractiveAgent, provider.ProfileKindAgent, true},
		{workflow.NodeTypeScript, "", false},
		{workflow.NodeTypeToolCall, "", false},
		{workflow.NodeTypeConditional, "", false},
		{workflow.NodeTypeTransform, "", false},
		{workflow.NodeTypeTimerDelay, "", false},
		{workflow.NodeTypeWebhookWait, "", false},
	}
	for _, c := range cases {
		kind, ok := expectedKindForNodeType(c.t)
		if ok != c.ok || kind != c.kind {
			t.Errorf("expectedKindForNodeType(%s) = (%q, %v), want (%q, %v)", c.t, kind, ok, c.kind, c.ok)
		}
	}
}
