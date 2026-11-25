package http

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerConnectJobs_ToWire(t *testing.T) {
	runID := uuid.New()
	p := toWireProcess(&runner.ProcessDispatch{Kind: engine.ProcessScript, Command: "/bin/sh", Args: []string{"./r.sh"},
		NodeEnv: map[string]string{"A": "1"}, Passthrough: []string{"P"}, TimeoutSecs: 5, WorkDir: "/w",
		BundleHash: strings.Repeat("d", 64), BundleEntrypoint: "r.sh", BundleToken: "bt", Requires: []string{"jq"}})
	assert.Equal(t, &runnerproto.WireProcess{Kind: "script", Command: "/bin/sh", Args: []string{"./r.sh"}, NodeEnv: map[string]string{"A": "1"},
		Passthrough: []string{"P"}, TimeoutSecs: 5, WorkDir: "/w", BundleHash: strings.Repeat("d", 64), BundleEntrypoint: "r.sh", BundleToken: "bt", Requires: []string{"jq"}}, p)

	res := engine.ResourceFenceRef{Key: "run:" + runID.String(), Owner: "mac-1", Generation: 4, Token: uuid.New()}
	base := runner.CheckpointRef{RunID: runID, Version: 3, ManifestHash: strings.Repeat("m", 64)}
	w := toWireWorkspace(&runner.WorkspaceDispatch{RunID: runID, WorkDir: "/w", HomeGeneration: 2, CheckpointVersion: 3,
		ManifestHash: base.ManifestHash, BaseRef: base.String(), Resource: res, PullToken: "pt", PushToken: "ps", PartBytes: 10})
	assert.Equal(t, runID.String(), w.RunID)
	assert.Equal(t, int64(3), w.CheckpointVersion)
	assert.Equal(t, base.String(), w.BaseRef, "the pinned base reaches the runner (unstick 2026-11-24)")
	assert.Equal(t, res.Key, w.FenceKey)
	assert.Equal(t, "mac-1", w.FenceOwner)
	assert.Equal(t, int64(4), w.FenceGeneration)
	assert.Equal(t, res.Token.String(), w.FenceToken)
	assert.NoError(t, w.Validate(), "a dispatch built from a complete plan validates on the runner")

	a := toWireAgent(&runner.AgentDispatch{Model: "m", Prompt: "p", WorkDir: "/w", CredentialKind: runner.CredOpenAISubscription, Backend: "codex", Provider: "claude-cli"})
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"credentialKind":"openai_subscription"`)
	assert.Nil(t, toWireProcess(nil))
	assert.Nil(t, toWireAgent(nil))
	assert.Nil(t, toWireWorkspace(nil))
}

func TestRunnerConnectJobs_ChannelTranslation(t *testing.T) {
	id := uuid.New()
	res := engine.ResourceFenceRef{Key: "run:" + id.String(), Owner: "mac-1", Generation: 4, Token: uuid.New()}
	base := runner.CheckpointRef{RunID: id, Version: 3, ManifestHash: strings.Repeat("a", 64)}
	workspace := &runner.WorkspaceDispatch{RunID: id, WorkDir: "/w", HomeGeneration: 2, CheckpointVersion: 3,
		ManifestHash: base.ManifestHash, BaseRef: base.String(), Resource: res, PullToken: "pull", PushToken: "push", PartBytes: 10}
	for _, op := range []runner.OpType{runner.OpProcess, runner.OpAgent} {
		t.Run(string(op), func(t *testing.T) {
			d := &runner.DispatchPayload{Op: op, Workspace: workspace}
			result := &runnerproto.Result{OK: true}
			if op == runner.OpProcess {
				d.Process = &runner.ProcessDispatch{Kind: engine.ProcessToolCall, Command: "./tool", Dir: "/w", WorkDir: "/w", Requires: []string{"jq"}}
				result.Process = &runnerproto.WireProcessResult{Stdout: "{\"answer\":42}", ExitCode: 0}
			} else {
				d.Agent = &runner.AgentDispatch{Provider: "claude-cli", Model: "claude-sonnet-4-5", Prompt: "edit", WorkDir: "/w", Backend: "claude-code", CredentialKind: runner.CredAnthropicSubscription}
				result.Agent = &runnerproto.WireAgentResult{Summary: "edited", Model: "claude-sonnet-4-5"}
			}
			env, err := fromOutbound(runner.Outbound{Kind: runner.MsgDispatch, JobID: id, Generation: 2, Dispatch: d}, "mac-1", 5)
			require.NoError(t, err)
			raw, err := runnerproto.Encode(env)
			require.NoError(t, err)
			env, err = runnerproto.Decode(raw)
			require.NoError(t, err)
			var wire runnerproto.Dispatch
			require.NoError(t, runnerproto.DecodePayload(env, &wire))
			require.NotNil(t, wire.Workspace)
			assert.Equal(t, base.String(), wire.Workspace.BaseRef)
			assert.Equal(t, res.Token.String(), wire.Workspace.FenceToken)
			if op == runner.OpProcess {
				require.NotNil(t, wire.Process)
				assert.Equal(t, []string{"jq"}, wire.Process.Requires)
			} else {
				require.NotNil(t, wire.Agent)
				assert.Equal(t, "edit", wire.Agent.Prompt)
			}
			env, err = runnerproto.NewJobEnvelope(runnerproto.KindResult, id.String(), 2, 0, result)
			require.NoError(t, err)
			in, err := toInbound(env)
			require.NoError(t, err)
			out, err := runner.WorkspaceStepOutput(op, in.Result)
			require.NoError(t, err)
			if op == runner.OpProcess {
				assert.Equal(t, result.Process.Stdout, out.Process.Stdout)
			} else {
				assert.Equal(t, result.Agent.Summary, out.Agent.Summary)
			}
		})
	}
}

func TestRunnerConnectJobs_FromWireResults(t *testing.T) {
	pr := fromWireProcessResult(&runnerproto.WireProcessResult{Stdout: "{}", Stderr: "e", ExitCode: 3})
	assert.Equal(t, &engine.ProcessResult{Stdout: "{}", Stderr: "e", ExitCode: 3}, pr)
	ar := fromWireAgentResult(&runnerproto.WireAgentResult{Summary: "s", Model: "m", SessionID: "sid", TokensIn: 1, TokensOut: 2,
		Steps:         []runnerproto.WireAgentStep{{Kind: "edit", Summary: "x"}},
		ModelEvidence: []runnerproto.WireModelEvidence{{Model: "m", Source: "result", Kind: "reported_execution"}}})
	assert.Equal(t, "s", ar.Summary)
	assert.Equal(t, []engine.AgentStep{{Kind: "edit", Summary: "x"}}, ar.Steps)
	require.Len(t, ar.ModelEvidence, 1)
	assert.True(t, ar.ModelEvidence[0].ReportsExecution())
	assert.Nil(t, fromWireProcessResult(nil))
	assert.Nil(t, fromWireAgentResult(nil))
}
