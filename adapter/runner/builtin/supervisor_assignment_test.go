package builtin_test

import (
	"context"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/adapter/runner/builtin"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// harnessAgents is an agent harness that launches its subprocess through a
// supervised CommandRunner, as every real built-in harness does once P3 T13
// wires it.
type harnessAgents struct {
	run  claudecli.CommandRunner
	pid  string
	seen []runner.Assignment
}

func (h *harnessAgents) AgentProviderFor(provider.ResolvedBinding) (engine.AgentProvider, error) {
	return h, nil
}

func (h *harnessAgents) RunAgent(ctx context.Context, _ engine.AgentMission) (*engine.AgentResult, error) {
	if a, ok := runner.AssignmentFromContext(ctx); ok {
		h.seen = append(h.seen, a)
	}
	lines, wait, err := h.run.Run(ctx, claudecli.RunSpec{Bin: "/bin/sh", Args: []string{"-c", `echo $$ > "$0"; echo agent-done`, h.pid}})
	if err != nil {
		return nil, err
	}
	var out []string
	for l := range lines {
		out = append(out, l)
	}
	if err := wait(); err != nil {
		return nil, err
	}
	return &engine.AgentResult{Summary: strings.Join(out, "\n")}, nil
}

// Named by the alignment prose ("Supervisor identity producer").
func TestBuiltInSupervisor_AssignmentReachesHarnessAndProcessLaunch(t *testing.T) {
	f := newSupFixture(t)
	_, agentJob := f.assign(t)
	_, procJob := f.assign(t)
	sup := builtin.NewProcessSupervisor(f.store, builtin.SupervisorOptions{})
	agents := &harnessAgents{run: claudecli.NewSupervisedExecRunner(sup), pid: f.file("agent.pid")}
	r := builtin.New(builtin.Deps{Chats: &fakeSelectors{}, Agents: agents, Streams: &fakeStreams{},
		Process: process.NewSupervisedLocalProcessRunner(sup), Confirmations: &fakeConfirmations{}})

	// The caller's ctx carries no stamp: Run must stamp its own assignment.
	out, err := r.Run(context.Background(), agentJob, runner.StepJob{Op: runner.OpAgent, Agent: &engine.AgentMission{Prompt: "go"}})
	require.NoError(t, err)
	assert.Equal(t, "agent-done", out.Agent.Summary)
	require.Equal(t, []runner.Assignment{agentJob}, agents.seen, "Run stamps the exact assignment before the harness runs")
	pid, marker, state := f.row(t, agentJob.Fence)
	assert.Equal(t, readPID(t, f.file("agent.pid")), pid, "the durable guardian row names the harness subprocess")
	assert.NotEmpty(t, marker)
	assert.Equal(t, "stopped", state)

	procPID := f.file("proc.pid")
	out, err = r.Run(context.Background(), procJob, runner.StepJob{Op: runner.OpProcess,
		Process: &engine.ProcessSpec{Kind: engine.ProcessToolCall, Command: "/bin/sh", Args: []string{"-c", `echo $$ > "$0"`, procPID}}})
	require.NoError(t, err)
	require.NotNil(t, out.Process)
	pid, _, state = f.row(t, procJob.Fence)
	assert.Equal(t, readPID(t, procPID), pid, "the process path records against its own job fence")
	assert.Equal(t, "stopped", state)
}
