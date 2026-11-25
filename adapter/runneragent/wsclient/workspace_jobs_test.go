// adapter/runneragent/wsclient/workspace_jobs_test.go
package wsclient_test

import (
	"testing"

	"github.com/arbitron/arbitron/adapter/runneragent/wsclient"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceJobsFromWire_CopiesEveryField(t *testing.T) {
	d := runnerproto.Dispatch{Op: "agent",
		Process: &runnerproto.WireProcess{Kind: "script", Command: "/bin/sh", Args: []string{"./run.sh"}, Dir: "/d", Stdin: []byte("{}"),
			NodeEnv: map[string]string{"A": "1"}, Passthrough: []string{"P"}, TimeoutSecs: 9, WorkDir: "/w", BundleHash: "h", BundleEntrypoint: "run.sh", BundleToken: "bt", Requires: []string{"jq"}},
		Agent: &runnerproto.WireAgent{Model: "m", Prompt: "p", WorkDir: "/w", ToolScope: []string{"Edit"}, SessionID: "s", TimeoutSecs: 5,
			CredentialKind: "anthropic_subscription", Backend: "claude-code", Effort: "high", ContextWindow: "1m", RequireExecutedModel: true, Provider: "claude-cli"},
		Workspace: &runnerproto.WireWorkspace{RunID: "r", WorkDir: "/w", HomeGeneration: 2, CheckpointVersion: 3, ManifestHash: "mh",
			BaseRef: "r@3:mh", FenceKey: "run:r", FenceOwner: "mac-1", FenceGeneration: 4, FenceToken: "ft", PullToken: "pt", PushToken: "ut", PartBytes: 7}}
	p, a, w := wsclient.WorkspaceJobsFromWire(d)
	require.NotNil(t, p)
	require.NotNil(t, a)
	require.NotNil(t, w)
	assert.Equal(t, runneragent.ProcessJob{Kind: "script", Command: "/bin/sh", Args: []string{"./run.sh"}, Dir: "/d", Stdin: []byte("{}"),
		NodeEnv: map[string]string{"A": "1"}, Passthrough: []string{"P"}, TimeoutSecs: 9, WorkDir: "/w", BundleHash: "h", BundleEntrypoint: "run.sh", BundleToken: "bt", Requires: []string{"jq"}}, *p)
	assert.Equal(t, "1m", a.ContextWindow)
	assert.Equal(t, "claude-cli", a.Provider)
	assert.True(t, a.RequireExecutedModel)
	assert.Equal(t, runneragent.WorkspaceJob{RunID: "r", WorkDir: "/w", HomeGeneration: 2, CheckpointVersion: 3, ManifestHash: "mh",
		BaseRef: "r@3:mh", FenceKey: "run:r", FenceOwner: "mac-1", FenceGeneration: 4, FenceToken: "ft", PullToken: "pt", PushToken: "ut", PartBytes: 7}, *w,
		"the pinned base and claim survive translation (unstick 2026-11-24)")

	pp, aa, ww := wsclient.WorkspaceJobsFromWire(runnerproto.Dispatch{Op: "chat"})
	assert.Nil(t, pp)
	assert.Nil(t, aa)
	assert.Nil(t, ww)
}

func TestWorkspaceResultsToWire(t *testing.T) {
	pr, ar := wsclient.WorkspaceResultsToWire(runneragent.Result{OK: true,
		Process: &runneragent.ProcessResult{Stdout: "o", Stderr: "e", ExitCode: 2},
		Agent: &runneragent.AgentResult{Summary: "s", Steps: []runneragent.AgentStep{{Kind: "edit", Summary: "x"}}, Model: "m",
			ModelEvidence: []runneragent.ModelEvidence{{Model: "m", Provider: "anthropic", Source: "result", Kind: "reported_execution"}},
			DroppedTools:  []string{"T"}, TokensIn: 1, TokensOut: 2, SessionID: "sid"}})
	assert.Equal(t, runnerproto.WireProcessResult{Stdout: "o", Stderr: "e", ExitCode: 2}, *pr)
	assert.Equal(t, []runnerproto.WireAgentStep{{Kind: "edit", Summary: "x"}}, ar.Steps)
	assert.Equal(t, "reported_execution", ar.ModelEvidence[0].Kind)
	assert.Equal(t, "sid", ar.SessionID)
}
