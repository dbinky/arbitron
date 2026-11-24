package builtin_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/adapter/runner/builtin"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSelectors returns a provider keyed by binding model and records which
// selector method was used, so the runner must pick the right one per op/kind.
type fakeSelectors struct {
	via      []string
	bindings []provider.ResolvedBinding
	err      error // returned by the provider call (adapter error)
	last     *fakeProv
}

type fakeProv struct {
	label string
	err   error
	got   *engine.ChatRequest
	gotM  *engine.AgentMission
}

func (p *fakeProv) Chat(_ context.Context, r engine.ChatRequest) (*engine.ChatResponse, error) {
	p.got = &r
	if p.err != nil {
		return nil, p.err
	}
	return &engine.ChatResponse{Output: p.label + ":" + r.Prompt}, nil
}
func (p *fakeProv) StreamChat(_ context.Context, r engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	p.got = &r
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan engine.ChatChunk, 1)
	ch <- engine.ChatChunk{Token: p.label + ":" + r.Prompt, Done: true}
	close(ch)
	return ch, nil
}
func (p *fakeProv) RunAgent(_ context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
	p.gotM = &m
	if p.err != nil {
		return nil, p.err
	}
	return &engine.AgentResult{Summary: p.label + ":" + m.Prompt}, nil
}

func (s *fakeSelectors) ChatProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	s.via, s.bindings = append(s.via, "chats"), append(s.bindings, b)
	s.last = &fakeProv{label: "chat/" + b.Model, err: s.err}
	return s.last, nil
}
func (s *fakeSelectors) AgentProviderFor(b provider.ResolvedBinding) (engine.AgentProvider, error) {
	s.via, s.bindings = append(s.via, "agents"), append(s.bindings, b)
	s.last = &fakeProv{label: "agent/" + b.Model, err: s.err}
	return s.last, nil
}

type fakeStreams struct{ fakeSelectors }

func (s *fakeStreams) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	s.via, s.bindings = append(s.via, "streams"), append(s.bindings, b)
	s.last = &fakeProv{label: "istream/" + b.Model, err: s.err}
	return s.last, nil
}

func assignment() runner.Assignment {
	id := uuid.New()
	return runner.Assignment{
		Job:   runner.Job{ID: id, RunnerID: runner.BuiltInRunnerID},
		Fence: runner.Fence{JobID: id, RunnerID: runner.BuiltInRunnerID, Generation: 1},
	}
}

func newRunner(chats *fakeSelectors, streams *fakeStreams) *builtin.Runner {
	return builtin.New(builtin.Deps{Chats: chats, Agents: chats, Streams: streams, Process: process.NewLocalProcessRunner(), Confirmations: &fakeConfirmations{}})
}

func TestBuiltinRunner_ChatUsesInnerChatSelector(t *testing.T) {
	chats, streams := &fakeSelectors{}, &fakeStreams{}
	b := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Model: "m1", ContextWindow: provider.Context1M, Effort: provider.EffortHigh, AllowedTools: []string{"Read"}}
	request := engine.ChatRequest{Model: "m1", Prompt: "p", ToolScope: []string{"Read"}, RequireExecutedModel: true}
	out, err := newRunner(chats, streams).Run(context.Background(), assignment(), runner.StepJob{
		Op: runner.OpType("chat"), Binding: b, Chat: &request,
	})
	require.NoError(t, err)
	require.NotNil(t, out.Chat)
	assert.Equal(t, "chat/m1:p", out.Chat.Output)
	assert.Equal(t, []string{"chats"}, chats.via)
	assert.Equal(t, b, chats.bindings[0])
	require.NotNil(t, chats.last.got)
	assert.Equal(t, request, *chats.last.got)
	assert.Empty(t, streams.via)
}

func TestBuiltinRunner_AgentUsesInnerAgentSelector(t *testing.T) {
	chats := &fakeSelectors{}
	b := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Model: "m2", Backend: "codex"}
	mission := engine.AgentMission{Model: "m2", Prompt: "go", ToolScope: []string{"Bash"}, WorkDir: "/workspace", SessionID: "session"}
	out, err := newRunner(chats, &fakeStreams{}).Run(context.Background(), assignment(), runner.StepJob{
		Op: runner.OpType("agent"), Binding: b, Agent: &mission,
	})
	require.NoError(t, err)
	assert.Equal(t, "agent/m2:go", out.Agent.Summary)
	require.NotNil(t, chats.last.gotM)
	assert.Equal(t, mission, *chats.last.gotM)
	assert.Equal(t, "codex", chats.bindings[0].Backend, "binding forwarded verbatim")
}

func TestBuiltinRunner_StreamPicksSelectorByProfileKind(t *testing.T) {
	chats, streams := &fakeSelectors{}, &fakeStreams{}
	r := newRunner(chats, streams)

	out, err := r.Run(context.Background(), assignment(), runner.StepJob{
		Op: runner.OpType("stream"), Binding: provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Model: "a"},
		Chat: &engine.ChatRequest{Prompt: "x"},
	})
	require.NoError(t, err)
	assert.Equal(t, "istream/a:x", (<-out.Stream).Token)

	out, err = r.Run(context.Background(), assignment(), runner.StepJob{
		Op: runner.OpType("stream"), Binding: provider.ResolvedBinding{Kind: provider.ProfileKindChat, Model: "c"},
		Chat: &engine.ChatRequest{Prompt: "y"},
	})
	require.NoError(t, err)
	assert.Equal(t, "chat/c:y", (<-out.Stream).Token)
	assert.Equal(t, []string{"streams"}, streams.via)
	assert.Equal(t, []string{"chats"}, chats.via)
}

func TestBuiltinRunner_ProcessUsesLocalRunner(t *testing.T) {
	out, err := newRunner(&fakeSelectors{}, &fakeStreams{}).Run(context.Background(), assignment(), runner.StepJob{
		Op:      runner.OpType("process"),
		Process: &engine.ProcessSpec{Kind: engine.ProcessToolCall, Command: "/bin/sh", Args: []string{"-c", "printf '{\"a\":1}'; exit 4"}},
	})
	require.NoError(t, err, "non-zero exit is a result, not an execution fault")
	require.NotNil(t, out.Process)
	assert.Equal(t, `{"a":1}`, out.Process.Stdout)
	assert.Equal(t, 4, out.Process.ExitCode)
}

// Failure: adapter errors are returned as-is so the attached outcome survives.
func TestBuiltinRunner_AdapterErrorKeepsOutcome(t *testing.T) {
	chats := &fakeSelectors{err: runner.WithOutcome(errors.New("harness process failed"), runner.SourceOutcome{Class: runner.OutcomeLoggedOut})}
	_, err := newRunner(chats, &fakeStreams{}).Run(context.Background(), assignment(), runner.StepJob{
		Op: runner.OpType("chat"), Binding: provider.ResolvedBinding{Model: "m"}, Chat: &engine.ChatRequest{},
	})
	require.Error(t, err)
	assert.Equal(t, runner.OutcomeLoggedOut, runner.ClassifyError(err).Class)
}

// Error/edge: a missing payload or unknown op is refused without calling anything.
func TestBuiltinRunner_RejectsMalformedJobs(t *testing.T) {
	chats, streams := &fakeSelectors{}, &fakeStreams{}
	r := newRunner(chats, streams)
	for name, job := range map[string]runner.StepJob{
		"chat without request":   {Op: runner.OpType("chat")},
		"stream without request": {Op: runner.OpStream},
		"agent without mission":  {Op: runner.OpType("agent")},
		"process without spec":   {Op: runner.OpType("process")},
		"unknown op":             {Op: runner.OpType("teleport"), Chat: &engine.ChatRequest{}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := r.Run(context.Background(), assignment(), job)
			assert.ErrorIs(t, err, builtin.ErrMalformedJob)
		})
	}
	assert.Empty(t, chats.via)
	assert.Empty(t, streams.via)
}

func TestBuiltinRunner_NewPanicsOnNilDeps(t *testing.T) {
	ok := builtin.Deps{Chats: &fakeSelectors{}, Agents: &fakeSelectors{}, Streams: &fakeStreams{}, Process: process.NewLocalProcessRunner(), Confirmations: &fakeConfirmations{}}
	for name, mutate := range map[string]func(*builtin.Deps){
		"chats":                   func(d *builtin.Deps) { d.Chats = nil },
		"agents":                  func(d *builtin.Deps) { d.Agents = nil },
		"streams":                 func(d *builtin.Deps) { d.Streams = nil },
		"process":                 func(d *builtin.Deps) { d.Process = nil },
		"confirmations":           func(d *builtin.Deps) { d.Confirmations = nil },
		"typed-nil confirmations": func(d *builtin.Deps) { d.Confirmations = (*fakeConfirmations)(nil) },
	} {
		d := ok
		mutate(&d)
		assert.Panics(t, func() { builtin.New(d) }, name)
	}
}

// Compile-time: the built-in runner satisfies the port the dispatcher consumes.
var _ runner.StepRunner = (*builtin.Runner)(nil)

type fakeConfirmations struct {
	got  []provider.ResolvedBinding
	dirs []string
	seen []bool // the per-job dir existed during the call
	res  runner.ConfirmationResult
	err  error
}

func (f *fakeConfirmations) Confirm(_ context.Context, b provider.ResolvedBinding, dir string) (runner.ConfirmationResult, error) {
	_, statErr := os.Stat(dir)
	f.got, f.dirs, f.seen = append(f.got, b), append(f.dirs, dir), append(f.seen, statErr == nil)
	return f.res, f.err
}

func TestBuiltinRunner_ConfirmUsesNativeVerifierInPerJobScratch(t *testing.T) {
	scratch := t.TempDir()
	conf := &fakeConfirmations{res: runner.ConfirmationResult{RequestedModel: "claude-opus-4-1", ExecutedModel: "claude-opus-4-1", SessionID: "s"}}
	chats, streams := &fakeSelectors{}, &fakeStreams{}
	r := builtin.New(builtin.Deps{Chats: chats, Agents: chats, Streams: streams, Process: process.NewLocalProcessRunner(), Confirmations: conf})
	b := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Backend: "claude-code", Model: "claude-opus-4-1"}
	job := runner.StepJob{Op: runner.OpConfirm, Binding: b, Confirmation: &runner.ConfirmationRequest{WorkDir: scratch}}

	out, err := r.Run(context.Background(), assignment(), job)
	require.NoError(t, err)
	require.NotNil(t, out.Confirmation)
	assert.Equal(t, conf.res, *out.Confirmation)
	require.Len(t, conf.got, 1)
	assert.Equal(t, b, conf.got[0])
	assert.Equal(t, scratch, filepath.Dir(conf.dirs[0]), "a per-job directory under the configured scratch")
	assert.True(t, conf.seen[0])
	_, statErr := os.Stat(conf.dirs[0])
	assert.True(t, os.IsNotExist(statErr), "the per-job directory is removed")
	assert.Empty(t, chats.via, "confirmation never builds an unrestricted chat/agent run")

	_, err = r.Run(context.Background(), assignment(), runner.StepJob{Op: runner.OpConfirm, Binding: b})
	assert.ErrorIs(t, err, builtin.ErrMalformedJob)
	assert.Len(t, conf.got, 1, "a malformed descriptor makes zero native launches")

	conf.err = runner.WithOutcome(errors.New("limit"), runner.SourceOutcome{Class: runner.OutcomeExhausted})
	_, err = r.Run(context.Background(), assignment(), job)
	assert.Equal(t, runner.OutcomeExhausted, runner.ClassifyError(err).Class)
}
