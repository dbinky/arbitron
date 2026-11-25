package runnerproto_test

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func validProcessDispatch() runnerproto.Dispatch {
	w := scopedWorkspace()
	return runnerproto.Dispatch{Op: "process", LeaseUntil: time.Now().Add(30 * time.Second), Workspace: &w,
		Process: &runnerproto.WireProcess{Kind: "tool_call", Command: "./tool", Dir: w.WorkDir, WorkDir: w.WorkDir, Passthrough: []string{"GITHUB_TOKEN"}, NodeEnv: map[string]string{"MODE": "fast"}, Requires: []string{"jq"}}}
}
func validScriptDispatch() runnerproto.Dispatch {
	d := validProcessDispatch()
	d.Process = &runnerproto.WireProcess{Kind: "script", Command: "/bin/sh", Args: []string{"./run.sh"}, WorkDir: d.Workspace.WorkDir, BundleHash: strings.Repeat("d", 64), BundleEntrypoint: "run.sh", BundleToken: "tok-b"}
	return d
}
func validAgentDispatch() runnerproto.Dispatch {
	w := scopedWorkspace()
	return runnerproto.Dispatch{Op: "agent", LeaseUntil: time.Now().Add(30 * time.Second), Workspace: &w, Agent: &runnerproto.WireAgent{Provider: "claude-cli", Model: "claude-sonnet-4-5", Prompt: "edit", WorkDir: w.WorkDir, ToolScope: []string{"Edit"}, CredentialKind: "anthropic_subscription", Backend: "claude-code"}}
}
func TestPhase3Dispatch_ValidProcessAndAgentAreAccepted(t *testing.T) {
	for name, d := range map[string]runnerproto.Dispatch{"tool_call": validProcessDispatch(), "script": validScriptDispatch(), "agent": validAgentDispatch()} {
		assert.NoError(t, d.Validate(), name)
	}
}
func TestPhase3Dispatch_IncompleteOrMixedPayloadsAreRefused(t *testing.T) {
	cases := map[string]func() runnerproto.Dispatch{
		"process without workspace": func() runnerproto.Dispatch { d := validProcessDispatch(); d.Workspace = nil; return d },
		"agent without workspace":   func() runnerproto.Dispatch { d := validAgentDispatch(); d.Workspace = nil; return d },
		"incomplete workspace":      func() runnerproto.Dispatch { d := validProcessDispatch(); d.Workspace.FenceToken = ""; return d },
		"process without payload":   func() runnerproto.Dispatch { d := validProcessDispatch(); d.Process = nil; return d },
		"agent without payload":     func() runnerproto.Dispatch { d := validAgentDispatch(); d.Agent = nil; return d },
		"process plus agent": func() runnerproto.Dispatch {
			d := validProcessDispatch()
			d.Agent = validAgentDispatch().Agent
			return d
		},
		"agent plus chat": func() runnerproto.Dispatch {
			d := validAgentDispatch()
			d.Chat = &runnerproto.WireChat{Model: "m", Prompt: "p", CredentialKind: "anthropic_subscription"}
			return d
		},
		"process workDir differs": func() runnerproto.Dispatch { d := validProcessDispatch(); d.Process.WorkDir = "/elsewhere"; return d },
		"agent workDir differs":   func() runnerproto.Dispatch { d := validAgentDispatch(); d.Agent.WorkDir = "/elsewhere"; return d },
		"unknown process kind":    func() runnerproto.Dispatch { d := validProcessDispatch(); d.Process.Kind = "shell"; return d },
		"no command":              func() runnerproto.Dispatch { d := validProcessDispatch(); d.Process.Command = ""; return d },
		"relative tool dir":       func() runnerproto.Dispatch { d := validProcessDispatch(); d.Process.Dir = "w"; return d },
		"script without bundle":   func() runnerproto.Dispatch { d := validScriptDispatch(); d.Process.BundleHash = ""; return d },
		"script bad bundle hash":  func() runnerproto.Dispatch { d := validScriptDispatch(); d.Process.BundleHash = "ZZ"; return d },
		"script escaping entry": func() runnerproto.Dispatch {
			d := validScriptDispatch()
			d.Process.BundleEntrypoint = "../run.sh"
			return d
		},
		"script without token": func() runnerproto.Dispatch { d := validScriptDispatch(); d.Process.BundleToken = ""; return d },
		"requires with slash": func() runnerproto.Dispatch {
			d := validProcessDispatch()
			d.Process.Requires = []string{"/usr/bin/jq"}
			return d
		},
		"requires duplicate": func() runnerproto.Dispatch {
			d := validProcessDispatch()
			d.Process.Requires = []string{"jq", "jq"}
			return d
		},
		"passthrough with =": func() runnerproto.Dispatch {
			d := validProcessDispatch()
			d.Process.Passthrough = []string{"A=b"}
			return d
		},
		"negative timeout": func() runnerproto.Dispatch { d := validProcessDispatch(); d.Process.TimeoutSecs = -1; return d },
		"agent no model":   func() runnerproto.Dispatch { d := validAgentDispatch(); d.Agent.Model = ""; return d },
		"agent no prompt":  func() runnerproto.Dispatch { d := validAgentDispatch(); d.Agent.Prompt = ""; return d },
		"agent server credential": func() runnerproto.Dispatch {
			d := validAgentDispatch()
			d.Agent.CredentialKind = "server_connection"
			return d
		},
		"agent no backend": func() runnerproto.Dispatch { d := validAgentDispatch(); d.Agent.Backend = ""; return d },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) { d := build(); assert.Error(t, d.Validate(), "never accepted, never defaulted") })
	}
}
func TestPhase3Result_ProcessAndAgentResultsValidate(t *testing.T) {
	require.NoError(t, (&runnerproto.Result{OK: true, Process: &runnerproto.WireProcessResult{Stdout: "{}", ExitCode: 3}}).Validate(), "a non-zero exit is a completed process; the server executor decides failure")
	require.NoError(t, (&runnerproto.Result{OK: true, Agent: &runnerproto.WireAgentResult{Summary: "s", Model: "m", ModelEvidence: []runnerproto.WireModelEvidence{{Model: "m", Source: "result", Kind: "reported_execution"}}}}).Validate())
	assert.Error(t, (&runnerproto.Result{OK: true, Process: &runnerproto.WireProcessResult{}, Agent: &runnerproto.WireAgentResult{}}).Validate(), "exactly one payload")
	assert.Error(t, (&runnerproto.Result{OK: true, Agent: &runnerproto.WireAgentResult{ModelEvidence: []runnerproto.WireModelEvidence{{Model: "", Source: "result"}}}}).Validate(), "evidence names a model")
	assert.Error(t, (&runnerproto.Result{OK: true, Agent: &runnerproto.WireAgentResult{DroppedTools: make([]string, 257)}}).Validate())
}
