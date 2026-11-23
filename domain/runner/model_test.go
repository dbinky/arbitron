package runner_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
)

func TestMakeSourceID(t *testing.T) {
	cases := []struct {
		r    runner.RunnerID
		k    runner.CredentialKind
		ref  string
		want runner.SourceID
	}{
		{runner.BuiltInRunnerID, runner.CredAnthropicSubscription, "", "builtin/anthropic_subscription/-"},
		{runner.BuiltInRunnerID, runner.CredOpenAISubscription, "", "builtin/openai_subscription/-"},
		{runner.BuiltInRunnerID, runner.CredLunaRoute, "lr-prod", "builtin/lunaroute/lr-prod"},
		{runner.BuiltInRunnerID, runner.CredServerConnection, "azure-east", "builtin/server_connection/azure-east"},
		{runner.BuiltInRunnerID, runner.CredLocalHarness, "pi", "builtin/local_harness/pi"},
		{"mac-mini", runner.CredAnthropicSubscription, "  ", "mac-mini/anthropic_subscription/-"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, runner.MakeSourceID(c.r, c.k, c.ref))
	}
}

func TestMakeSourceID_TwoConnectionsNeverShareAnID(t *testing.T) {
	a := runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredServerConnection, "conn-a")
	b := runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredServerConnection, "conn-b")
	assert.NotEqual(t, a, b, "connection sources stay separate even when their families match")
	l1 := runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredLunaRoute, "lr-1")
	l2 := runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredLunaRoute, "lr-2")
	assert.NotEqual(t, l1, l2)
}

func TestStateVocabularyIsStable(t *testing.T) {
	assert.Equal(t, runner.RunnerID("builtin"), runner.BuiltInRunnerID)
	assert.Equal(t, []string{"online", "suspect", "lost", "offline"},
		[]string{string(runner.LivenessOnline), string(runner.LivenessSuspect), string(runner.LivenessLost), string(runner.LivenessOffline)})
	assert.Equal(t, []string{"compatible", "incompatible", "chat_only"},
		[]string{string(runner.CompatCompatible), string(runner.CompatIncompatible), string(runner.CompatChatOnly)})
	assert.Equal(t, []string{"unknown", "ok", "logged_out"},
		[]string{string(runner.AuthUnknown), string(runner.AuthOK), string(runner.AuthLoggedOut)})
	assert.Equal(t, []string{"available", "exhausted", "probation"},
		[]string{string(runner.CapacityAvailable), string(runner.CapacityExhausted), string(runner.CapacityProbation)})
	assert.Equal(t, []string{"chat", "stream", "agent", "process"},
		[]string{string(runner.OpChat), string(runner.OpStream), string(runner.OpAgent), string(runner.OpProcess)})
	assert.Equal(t, []string{"none", "builtin", "workspace", "session"},
		[]string{string(runner.AffinityNone), string(runner.AffinityBuiltIn), string(runner.AffinityWorkspace), string(runner.AffinitySession)})
}
