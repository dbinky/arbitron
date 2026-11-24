package remote

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

// recordingHub records exactly what crosses into the hub and answers from its fields.
type recordingHub struct {
	jobs []runner.StepJob
	out  runner.StepOutput
	err  error
}

func (h *recordingHub) Run(_ context.Context, _ runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	h.jobs = append(h.jobs, job)
	return h.out, h.err
}

func remoteAssignment(id runner.RunnerID) runner.Assignment {
	jobID := uuid.New()
	return runner.Assignment{Job: runner.Job{ID: jobID, RunnerID: id}, Fence: runner.Fence{JobID: jobID, RunnerID: id, Generation: 1}}
}

func TestRun_ConfirmNeverShipsTheServerScratch(t *testing.T) {
	job := runner.StepJob{Op: runner.OpConfirm, Binding: provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-opus-5-5"}, Confirmation: &runner.ConfirmationRequest{WorkDir: "/srv/arbitron/workspaces/.runner-confirm"}}
	exact := &runner.ConfirmationResult{RequestedModel: "claude-opus-5-5", ExecutedModel: "claude-opus-5-5", SessionID: "rs"}
	h := &recordingHub{out: runner.StepOutput{Confirmation: exact}}
	out, err := (&Runner{hub: h}).Run(context.Background(), remoteAssignment("mac-a"), job)
	require.NoError(t, err)
	assert.Equal(t, exact, out.Confirmation)
	require.Len(t, h.jobs, 1)
	require.NotNil(t, h.jobs[0].Confirmation)
	assert.Empty(t, h.jobs[0].Confirmation.WorkDir, "the runner uses its own scratch root")
	assert.Equal(t, "/srv/arbitron/workspaces/.runner-confirm", job.Confirmation.WorkDir, "the caller's job is untouched")
}

func TestRun_HandsTheHubAHermeticCopy(t *testing.T) {
	revealed := false
	job := runner.StepJob{Op: runner.OpChat,
		Binding: provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: provider.CanonicalLunaRoute, Model: "glm-5.3",
			ConnectionRef: "lunaroute", BaseURL: "https://lunaroute.internal.example/v1", Extras: map[string]string{"api_key": "lr_extra"},
			AllowedTools: []string{"Read"}, Pinned: true,
			RevealKey: func(context.Context) (string, error) { revealed = true; return "lr_secret", nil }},
		Chat: &engine.ChatRequest{Model: "glm-5.3", Prompt: "classify", WorkDir: "/srv/arbitron/workspaces/run-1"}}
	h := &recordingHub{out: runner.StepOutput{Chat: &engine.ChatResponse{Output: "ok"}}}

	out, err := (&Runner{hub: h}).Run(context.Background(), remoteAssignment("mac-a"), job)
	require.NoError(t, err)
	assert.Equal(t, "ok", out.Chat.Output)
	require.Len(t, h.jobs, 1)
	sent := h.jobs[0]
	assert.Nil(t, sent.Binding.RevealKey, "the key closure never reaches the remote path")
	assert.Empty(t, sent.Binding.BaseURL)
	assert.Nil(t, sent.Binding.Extras)
	assert.Empty(t, sent.Chat.WorkDir, "remote work runs in the runner's own scratch dir")
	assert.Equal(t, "glm-5.3", sent.Binding.Model)
	assert.Equal(t, provider.CanonicalLunaRoute, sent.Binding.Provider)
	assert.Equal(t, "lunaroute", sent.Binding.ConnectionRef)
	assert.True(t, sent.Binding.Pinned)
	assert.Equal(t, []string{"Read"}, sent.Binding.AllowedTools)
	assert.Equal(t, "classify", sent.Chat.Prompt)
	assert.False(t, revealed)

	assert.NotNil(t, job.Binding.RevealKey, "the caller's job is untouched")
	assert.Equal(t, "https://lunaroute.internal.example/v1", job.Binding.BaseURL)
	assert.Equal(t, "/srv/arbitron/workspaces/run-1", job.Chat.WorkDir, "a re-placed built-in attempt still needs the server workspace")
}

func TestRun_RefusesBuiltInAndUnassignedRunners(t *testing.T) {
	for _, id := range []runner.RunnerID{"", runner.BuiltInRunnerID} {
		h := &recordingHub{}
		_, err := (&Runner{hub: h}).Run(context.Background(), remoteAssignment(id), runner.StepJob{Op: runner.OpChat, Chat: &engine.ChatRequest{}})
		require.Error(t, err, id)
		assert.Equal(t, runner.OutcomeOther, runner.ClassifyError(err).Class, id)
		assert.Empty(t, h.jobs, "%q never reaches the hub", id)
	}
}

func TestRun_PassesHubOutcomesThrough(t *testing.T) {
	lost := runner.WithOutcome(errors.New("mac-a went quiet"), runner.SourceOutcome{Class: runner.OutcomeRunnerLost})
	h := &recordingHub{err: lost}
	_, err := (&Runner{hub: h}).Run(context.Background(), remoteAssignment("mac-a"), runner.StepJob{Op: runner.OpAgent, Agent: &engine.AgentMission{}})
	assert.ErrorIs(t, err, lost)
	assert.Equal(t, runner.OutcomeRunnerLost, runner.ClassifyError(err).Class)
	require.Len(t, h.jobs, 1, "non-chat work is passed to the hub, which refuses it with a typed error")
	assert.Nil(t, h.jobs[0].Chat)
}

func TestNew_RequiresHub(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}
