package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type validAgents struct{}

func (validAgents) AgentProviderFor(provider.ResolvedBinding) (engine.AgentProvider, error) {
	return nil, nil
}

type validChats struct{}

func (validChats) ChatProviderFor(provider.ResolvedBinding) (engine.ChatProvider, error) {
	return nil, nil
}
func (validChats) StreamProviderFor(provider.ResolvedBinding) (engine.ChatProvider, error) {
	return nil, nil
}

func sinkCtx(run uuid.UUID) (context.Context, *engine.StepCompletionSink) {
	ctx, _, _ := stamped(run, 1)
	sink := engine.NewStepCompletionSink()
	return engine.ContextWithStepCompletionSink(ctx, sink), sink
}

func TestAgentAffinity(t *testing.T) {
	assert.Equal(t, runner.AffinityWorkspace, runner.AgentAffinity(engine.AgentMission{WorkDir: "/ws/instances/r"}))
	assert.Equal(t, runner.AffinityBuiltIn, runner.AgentAffinity(engine.AgentMission{}))
}

// A routed agent mission with a working directory runs the workspace protocol
// (size filled before placement, attempt begun on the placed runner, receipt recorded).
func TestRoutedAgent_WorkspaceAffinity(t *testing.T) {
	r := &scriptedRunner{id: builtin, fallback: func(_ context.Context, _ runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
		return runner.StepOutput{Agent: &engine.AgentResult{Summary: "edited " + job.Agent.WorkDir}}, nil
	}}
	f := builtinOnly(t, anthropicSub(builtin), r)
	ws := &recWorkspace{bytes: 2048}
	f.d.SetWorkspace(ws)
	p, err := runner.NewRoutingAgentSelector(validAgents{}, f.d).AgentProviderFor(
		provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-sonnet-4-5", Backend: "claude-code"})
	require.NoError(t, err)

	run := uuid.New()
	ctx, sink := sinkCtx(run)
	dir := "/srv/ws/instances/" + run.String()
	res, err := p.RunAgent(ctx, engine.AgentMission{Model: "claude-sonnet-4-5", Prompt: "edit", WorkDir: dir})
	require.NoError(t, err)
	assert.Equal(t, "edited "+dir, res.Summary)
	begun, reqs := ws.attempts()
	require.Len(t, begun, 1)
	assert.Equal(t, dir, begun[0].WorkDir)
	assert.Equal(t, builtin, begun[0].Runner)
	assert.Equal(t, runner.AffinityWorkspace, reqs[0].Affinity)
	assert.Equal(t, int64(2048), reqs[0].WorkspaceBytes, "the size reached placement")
	comps := sink.Completions()
	require.Len(t, comps, 1)
	require.NotNil(t, comps[0].Workspace)

	// No working directory: Phase 1 behaviour, no workspace protocol.
	ctx2, _ := sinkCtx(uuid.New())
	_, err = p.RunAgent(ctx2, engine.AgentMission{Model: "claude-sonnet-4-5", Prompt: "think"})
	require.NoError(t, err)
	begun, _ = ws.attempts()
	assert.Len(t, begun, 1)
}

// Chat stays hermetic/built-in and interactive streams stay built-in until P4,
// even with a workspace service installed.
func TestRoutedChat_HermeticAffinityUnchanged(t *testing.T) {
	r := &scriptedRunner{id: builtin, fallback: func(ctx context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
		if job.Op == runner.OpStream {
			return streamOf(engine.ChatChunk{Token: "hi"}, engine.ChatChunk{Done: true})(ctx, a, job)
		}
		return succeed("ok")(ctx, a, job)
	}}
	f := builtinOnly(t, anthropicSub(builtin), r)
	ws := &recWorkspace{}
	f.d.SetWorkspace(ws)
	b := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Model: "claude-sonnet-4-5"}

	chat, err := runner.NewRoutingChatSelector(validChats{}, f.d, runner.BuiltInAffinity).ChatProviderFor(b)
	require.NoError(t, err)
	ctx, _ := sinkCtx(uuid.New())
	_, err = chat.Chat(ctx, engine.ChatRequest{Model: "claude-sonnet-4-5", Prompt: "summarise"})
	require.NoError(t, err)

	stream, err := runner.NewRoutingStreamSelector(validChats{}, f.d).StreamProviderFor(b)
	require.NoError(t, err)
	sctx, _, _ := stamped(uuid.New(), 1)
	ch, err := stream.StreamChat(sctx, engine.ChatRequest{Model: "claude-sonnet-4-5", Prompt: "hi"})
	require.NoError(t, err)
	for range ch {
	}
	begun, _ := ws.attempts()
	assert.Empty(t, begun, "chat and interactive turns never run the workspace protocol in Phase 3")
}

// Portable process routing must carry the workspace attempt through to its
// completion receipt, while unflagged scripts retain built-in-only execution.
func TestRoutedProcess_WorkspaceAffinity(t *testing.T) {
	r := &scriptedRunner{id: builtin, fallback: func(_ context.Context, _ runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
		require.Equal(t, runner.OpProcess, job.Op)
		require.NotNil(t, job.Process)
		return runner.StepOutput{Process: &engine.ProcessResult{Stdout: job.Process.Command + ":" + job.Process.WorkDir}}, nil
	}}
	f := builtinOnly(t, anthropicSub(builtin), r)
	ws := &recWorkspace{bytes: 2048}
	f.d.SetWorkspace(ws)
	pr := runner.NewRoutedProcessRunner(f.d)
	run := uuid.New()
	dir := "/srv/ws/instances/" + run.String()

	ctx, sink := sinkCtx(run)
	res, err := pr.RunProcess(ctx, engine.ProcessSpec{Kind: engine.ProcessToolCall, Command: "jq", Portable: true, WorkDir: dir})
	require.NoError(t, err)
	assert.Equal(t, "jq:"+dir, res.Stdout)
	begun, reqs := ws.attempts()
	require.Len(t, begun, 1)
	assert.Equal(t, dir, begun[0].WorkDir)
	assert.Equal(t, runner.AffinityWorkspace, reqs[0].Affinity)
	assert.Equal(t, run, reqs[0].RunID)
	assert.Equal(t, int64(2048), reqs[0].WorkspaceBytes)
	require.Len(t, sink.Completions(), 1)
	require.NotNil(t, sink.Completions()[0].Workspace)
	assert.Equal(t, begun[0].Resource, sink.Completions()[0].Workspace.Resource)
	assert.NotEmpty(t, sink.Completions()[0].Workspace.Ref)

	ctx2, _ := sinkCtx(run)
	res, err = pr.RunProcess(ctx2, engine.ProcessSpec{Kind: engine.ProcessScript, Command: "/bin/sh", Args: []string{"/s.sh"}, WorkDir: dir})
	require.NoError(t, err)
	assert.Equal(t, "/bin/sh:"+dir, res.Stdout)
	begun, _ = ws.attempts()
	assert.Len(t, begun, 1, "an unflagged script stays built-in with no workspace protocol")
}

// SetWorkspace installs the service and ends legacy mode; nil is refused.
func TestSetWorkspace_InstallsServiceAndEndsLegacyMode(t *testing.T) {
	r := &scriptedRunner{id: builtin, fallback: func(context.Context, runner.Assignment, runner.StepJob) (runner.StepOutput, error) {
		return runner.StepOutput{Agent: &engine.AgentResult{Summary: "ok"}}, nil
	}}
	f := builtinOnly(t, anthropicSub(builtin), r)
	assert.Panics(t, func() { f.d.SetWorkspace(nil) })
	ws := &recWorkspace{}
	f.d.SetWorkspace(ws)
	p, err := runner.NewRoutingAgentSelector(validAgents{}, f.d).AgentProviderFor(
		provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-sonnet-4-5", Backend: "claude-code"})
	require.NoError(t, err)
	ctx, _ := sinkCtx(uuid.New())
	_, err = p.RunAgent(ctx, engine.AgentMission{Model: "claude-sonnet-4-5", Prompt: "edit", WorkDir: "/srv/ws/instances/x"})
	require.NoError(t, err)
	begun, _ := ws.attempts()
	assert.Len(t, begun, 1)
}
