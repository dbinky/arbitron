package wsclient_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/arbitron/arbitron/adapter/runneragent/wsclient"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
)

var agentRequestFacts = []string{"Backend", "ContextWindow", "CredentialKind", "Effort", "Model", "Prompt", "Provider",
	"RequireExecutedModel", "SessionID", "TimeoutSecs", "ToolScope", "WorkDir"}

// mentions reports whether t is, or points/slices/maps to, target.
func mentions(t, target reflect.Type) bool {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		if t.Kind() == reflect.Map && mentions(t.Key(), target) {
			return true
		}
		t = t.Elem()
	}
	return t == target
}

func TestAgentMirrors_CarryRequestFactsOnly(t *testing.T) {
	capType := reflect.TypeOf(provider.BackendCapability{})
	rulesType := reflect.TypeOf(provider.CapabilityRules{})
	for _, v := range []any{runner.AgentDispatch{}, runnerproto.WireAgent{}, runneragent.AgentJob{}} {
		typ := reflect.TypeOf(v)
		var names []string
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			names = append(names, f.Name)
			assert.NotEqual(t, "Capability", f.Name, "%s must not carry a server capability", typ)
			assert.False(t, mentions(f.Type, capType) || mentions(f.Type, rulesType), "%s.%s carries server capability rules", typ, f.Name)
		}
		sort.Strings(names)
		assert.Equal(t, agentRequestFacts, names, "%s carries exactly the accepted request facts", typ)
	}

	// Assert the actual wire-to-executor seam, not only the mirror shapes.
	_, got, _ := wsclient.WorkspaceJobsFromWire(runnerproto.Dispatch{Op: "agent", Agent: &runnerproto.WireAgent{
		Backend: "claude-code", ContextWindow: "1m", CredentialKind: "anthropic_subscription",
		Effort: "high", Model: "claude-opus-4-1", Prompt: "edit the workspace", Provider: "claude-cli",
		RequireExecutedModel: true, SessionID: "session-1", TimeoutSecs: 19,
		ToolScope: []string{"Edit"}, WorkDir: "/workspace/run-1",
	}})
	assert.Equal(t, &runneragent.AgentJob{
		Backend: "claude-code", ContextWindow: "1m", CredentialKind: "anthropic_subscription",
		Effort: "high", Model: "claude-opus-4-1", Prompt: "edit the workspace", Provider: "claude-cli",
		RequireExecutedModel: true, SessionID: "session-1", TimeoutSecs: 19,
		ToolScope: []string{"Edit"}, WorkDir: "/workspace/run-1",
	}, got, "every accepted request fact reaches the executor mirror")
}
