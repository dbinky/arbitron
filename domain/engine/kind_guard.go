package engine

import (
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ErrBindingKindMismatch is returned at the Execute boundary when a node's
// declared Type disagrees with the resolved binding's capability Kind.
//
// SECURITY (PR #18, comments 3350533313 / 3350519165): a single executor is
// registered under both the chat and agent node types and routes purely on
// binding.Kind. The two postures are OPPOSITE — chat is tool-suppressed
// (--allowedTools ""), agent is YOLO (--dangerously-skip-permissions). A node
// declared as chat whose profile resolves to an agent binding (or vice versa)
// is a config-driven privilege escalation from chat to RCE-capable agent. The
// executors assert the invariant at the execution boundary and fail closed with
// this named, errors.Is-checkable error rather than trusting that workflow
// validation ran upstream (snapshots from older workflow versions, or
// programmatically built nodes, can bypass validation).
var ErrBindingKindMismatch = errors.New("engine: binding kind does not match node type")

// expectedKindForNodeType maps a node type to the ProfileKind its resolved
// binding must carry. ok is false for node types that do not resolve a
// model_profile (the caller should not be enforcing the invariant for them).
func expectedKindForNodeType(t workflow.NodeType) (provider.ProfileKind, bool) {
	switch t {
	case workflow.NodeTypeLLMCall, workflow.NodeTypeInteractiveLLM:
		return provider.ProfileKindChat, true
	case workflow.NodeTypeAgentCall, workflow.NodeTypeInteractiveAgent:
		return provider.ProfileKindAgent, true
	default:
		return "", false
	}
}

// assertBindingKind verifies that the resolved binding's Kind matches the kind
// required by the node's Type. It returns a wrapped ErrBindingKindMismatch on a
// mismatch so callers can attribute it to the node and match it with errors.Is.
func assertBindingKind(nodeID string, nodeType workflow.NodeType, bindingKind provider.ProfileKind) error {
	want, ok := expectedKindForNodeType(nodeType)
	if !ok {
		return nil
	}
	if bindingKind != want {
		return fmt.Errorf("%w: node %q is %s (requires a %s binding) but resolved binding is %s",
			ErrBindingKindMismatch, nodeID, nodeType, want, bindingKind)
	}
	return nil
}
