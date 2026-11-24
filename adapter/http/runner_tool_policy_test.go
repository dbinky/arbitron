package http

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestRunnerTranslate_ToolPolicySurvivesHelloAndHeartbeat(t *testing.T) {
	fp := strings.Repeat("a", 64)
	src := runnerproto.WireSource{Kind: "anthropic_subscription", Families: []string{"anthropic"},
		Capabilities: []runnerproto.ExecutionCapability{
			{Op: "agent", Backend: "claude-code", Provider: "claude-cli", EndpointFingerprint: fp, Families: []string{"anthropic"}, Models: []string{"claude-opus-5-5"}, Tools: []string{"Read"}, ToolPolicy: "allowlist", ExecutedModel: true},
			{Op: "chat", Provider: "claude-cli", EndpointFingerprint: fp, Families: []string{"anthropic"}, Models: []string{"claude-opus-5-5"}},
		}}
	hello, err := helloInfo(runnerproto.Hello{Sources: []runnerproto.WireSource{src}})
	require.NoError(t, err)
	require.Len(t, hello.Sources, 1)
	require.Len(t, hello.Sources[0].Capabilities, 2)
	assert.Equal(t, runner.ToolPolicyAllowlist, hello.Sources[0].Capabilities[0].ToolPolicy)
	assert.Equal(t, runner.ToolPolicy(""), hello.Sources[0].Capabilities[1].ToolPolicy, "chat evidence carries no tool posture")
	hb, err := heartbeatInfo(runnerproto.Heartbeat{Sources: []runnerproto.WireSource{src}})
	require.NoError(t, err)
	assert.Equal(t, hello.Sources, hb.Sources)
}
