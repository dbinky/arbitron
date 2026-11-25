package runner_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsjPuller honours its arguments: it returns a plan derived from the run it
// was asked about (with that run's exact claim and committed ref) and records
// every assignment it saw.
type wsjPuller struct {
	seen    []runner.Assignment
	runs    []uuid.UUID
	err     error
	noClaim bool
	noBase  bool
}

func (p *wsjPuller) PreparePull(_ context.Context, a runner.Assignment, runID uuid.UUID) (runner.PullPlan, error) {
	p.seen = append(p.seen, a)
	p.runs = append(p.runs, runID)
	if p.err != nil {
		return runner.PullPlan{}, p.err
	}
	plan := runner.PullPlan{
		RunID: runID,
		Ref:   runner.CheckpointRef{RunID: runID, Version: 7, ManifestHash: strings.Repeat("e", 64)},
		Home:  runner.Home{RunID: runID, RunnerID: a.Fence.RunnerID, Generation: 4},
		Resource: engine.ResourceFenceRef{Key: "run:" + runID.String(), Owner: string(a.Fence.RunnerID),
			Generation: 5, Token: uuid.MustParse("5a5a5a5a-5a5a-4a5a-8a5a-5a5a5a5a5a5a")},
		PullToken: runner.TransferToken{Token: "pull-" + a.Job.ID.String()},
		PushToken: runner.TransferToken{Token: "push-" + a.Job.ID.String()},
	}
	if p.noClaim {
		plan.Resource = engine.ResourceFenceRef{}
	}
	if p.noBase {
		plan.Ref = runner.CheckpointRef{}
	}
	return plan, nil
}

// wsjTokens records every scope it was asked to issue (the P3 T7 TransferScope API).
type wsjTokens struct{ scopes []runner.TransferScope }

func (t *wsjTokens) Issue(_ context.Context, s runner.TransferScope, ttl time.Duration) (runner.TransferToken, error) {
	t.scopes = append(t.scopes, s)
	return runner.TransferToken{Token: s.Direction + "-" + s.Fence.JobID.String(), JobID: s.Fence.JobID, Generation: s.Fence.Generation,
		RunnerID: s.Fence.RunnerID, RunID: s.RunID, Direction: s.Direction, BaseRef: s.BaseRef, BundleHash: s.BundleHash,
		Resource: s.Resource, ExpiresAt: time.Now().Add(ttl)}, nil
}

func wsjAssignment(op runner.OpType) runner.Assignment {
	runID := uuid.New()
	job := runner.Job{ID: uuid.New(), Key: runner.JobKey{RunID: runID, InvocationID: uuid.New(), OperationID: "node", Attempt: 1},
		NodeID: "n", Op: op, RunnerID: "mac-1", Generation: 2,
		Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"}}
	return runner.Assignment{Job: job, Fence: runner.Fence{JobID: job.ID, RunnerID: "mac-1", Generation: 2}}
}

func wsjDeps(p *wsjPuller, tk *wsjTokens) *runner.WorkspaceJobDeps {
	caps := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"claude-code": {Backend: "claude-code", EffortLevels: []provider.Effort{"low", "high"}},
	})
	return &runner.WorkspaceJobDeps{Sync: p, Tokens: tk, Capabilities: caps, PartBytes: 64 << 20, BundleTokenTTL: time.Minute}
}

func bundledSpec(ws string) *engine.ProcessSpec {
	return &engine.ProcessSpec{Kind: engine.ProcessScript, Command: "/bin/sh", Args: []string{"./run.sh", "x"},
		Dir: "/srv/workflows/wf", Stdin: []byte("{}"), NodeEnv: map[string]string{"A": "1"}, Passthrough: []string{"TOKEN"},
		TimeoutSecs: 9, WorkDir: ws, Portable: true, Requires: []string{"jq"},
		Bundle: &engine.ScriptBundleRef{Hash: strings.Repeat("c", 64), Entrypoint: "run.sh"}}
}

func TestBuildWorkspaceJob_ScriptCarriesBundleAndWorkspacePlan(t *testing.T) {
	p, tk := &wsjPuller{}, &wsjTokens{}
	a := wsjAssignment(runner.OpProcess)
	ws := "/srv/ws/instances/" + a.Job.Key.RunID.String()
	spec := bundledSpec(ws)
	proc, agent, wsd, err := runner.BuildWorkspaceJob(context.Background(), wsjDeps(p, tk), a, runner.StepJob{Op: runner.OpProcess, Process: spec})
	require.NoError(t, err)
	require.Nil(t, agent)
	require.NotNil(t, proc)
	assert.Equal(t, "", proc.Dir, "a script never sends the server's script folder; it runs in the unpacked bundle")
	assert.Equal(t, spec.Bundle.Hash, proc.BundleHash)
	assert.Equal(t, "run.sh", proc.BundleEntrypoint)
	assert.Equal(t, "bundle-"+a.Job.ID.String(), proc.BundleToken)
	assert.Equal(t, []string{"TOKEN"}, proc.Passthrough)
	assert.Equal(t, map[string]string{"A": "1"}, proc.NodeEnv)
	assert.Equal(t, []string{"jq"}, proc.Requires, "RR-09: requires travel to the runner's accept check")
	spec.Requires[0] = "mutated"
	assert.Equal(t, []string{"jq"}, proc.Requires, "requires is a defensive copy")
	require.NotNil(t, wsd)
	assert.Equal(t, a.Job.Key.RunID, wsd.RunID)
	assert.Equal(t, ws, wsd.WorkDir)
	assert.Equal(t, int64(4), wsd.HomeGeneration)
	assert.Equal(t, int64(7), wsd.CheckpointVersion)
	assert.Equal(t, strings.Repeat("e", 64), wsd.ManifestHash)
	assert.Equal(t, runner.CheckpointRef{RunID: a.Job.Key.RunID, Version: 7, ManifestHash: strings.Repeat("e", 64)}.String(), wsd.BaseRef,
		"the dispatch pins the exact committed base the tokens were issued for")
	assert.Equal(t, p.seen[0].Fence.RunnerID, runner.RunnerID(wsd.Resource.Owner))
	assert.Equal(t, int64(5), wsd.Resource.Generation)
	assert.Equal(t, "run:"+a.Job.Key.RunID.String(), wsd.Resource.Key)
	assert.Equal(t, "pull-"+a.Job.ID.String(), wsd.PullToken)
	assert.Equal(t, "push-"+a.Job.ID.String(), wsd.PushToken)
	assert.Equal(t, []uuid.UUID{a.Job.Key.RunID}, p.runs, "the pull plan is for THIS run")
	assert.Equal(t, a.Fence, p.seen[0].Fence)
}

// The bundle token is scoped to exactly this script's hash and the run's live
// claim; it carries no base ref, so it can never read another bundle or the
// workspace (the HTTP enforcement is P3 T7's TestRunnerTransfer_BundleTokenRejectsOtherHash).
func TestScriptBundle_TokenCannotReadDifferentBundle(t *testing.T) {
	p, tk := &wsjPuller{}, &wsjTokens{}
	a := wsjAssignment(runner.OpProcess)
	_, _, wsd, err := runner.BuildWorkspaceJob(context.Background(), wsjDeps(p, tk), a,
		runner.StepJob{Op: runner.OpProcess, Process: bundledSpec("/w")})
	require.NoError(t, err)
	require.Len(t, tk.scopes, 1)
	s := tk.scopes[0]
	assert.Equal(t, runner.TransferBundle, s.Direction)
	assert.Equal(t, strings.Repeat("c", 64), s.BundleHash)
	assert.Empty(t, s.BaseRef)
	assert.Equal(t, a.Fence, s.Fence)
	assert.Equal(t, a.Job.Key.RunID, s.RunID)
	assert.Equal(t, wsd.Resource, s.Resource, "the bundle token is bound to the same live claim as the workspace plan")
}

func TestBuildWorkspaceJob_MissingClaimOrBaseFailsClosed(t *testing.T) {
	for name, p := range map[string]*wsjPuller{"no claim": {noClaim: true}, "no base": {noBase: true}} {
		t.Run(name, func(t *testing.T) {
			tk := &wsjTokens{}
			a := wsjAssignment(runner.OpProcess)
			_, _, _, err := runner.BuildWorkspaceJob(context.Background(), wsjDeps(p, tk), a,
				runner.StepJob{Op: runner.OpProcess, Process: bundledSpec("/w")})
			require.ErrorIs(t, err, runner.ErrResourceFenced, "incomplete evidence is refused, never defaulted")
			assert.Empty(t, tk.scopes, "no bundle token is issued without a complete plan")
		})
	}
}

func TestBuildWorkspaceJob_ScriptWithoutBundleIsRefused(t *testing.T) {
	a := wsjAssignment(runner.OpProcess)
	spec := &engine.ProcessSpec{Kind: engine.ProcessScript, Command: "/bin/sh", Args: []string{"/srv/wf/run.sh"}, Dir: "/srv/wf", WorkDir: "/w"}
	_, _, _, err := runner.BuildWorkspaceJob(context.Background(), wsjDeps(&wsjPuller{}, &wsjTokens{}), a, runner.StepJob{Op: runner.OpProcess, Process: spec})
	require.ErrorIs(t, err, runner.ErrBundleRequired)
}

func TestBuildWorkspaceJob_ToolCallRunsInWorkspace(t *testing.T) {
	tk := &wsjTokens{}
	a := wsjAssignment(runner.OpProcess)
	spec := &engine.ProcessSpec{Kind: engine.ProcessToolCall, Command: "./tool.sh", Dir: "/w", WorkDir: "/w", Portable: true}
	proc, _, wsd, err := runner.BuildWorkspaceJob(context.Background(), wsjDeps(&wsjPuller{}, tk), a, runner.StepJob{Op: runner.OpProcess, Process: spec})
	require.NoError(t, err)
	assert.Equal(t, "/w", proc.Dir)
	assert.Empty(t, proc.BundleToken)
	assert.Empty(t, tk.scopes, "a tool_call needs no bundle token")
	require.NotNil(t, wsd)
	assert.NotEmpty(t, wsd.BaseRef)
}

func TestBuildWorkspaceJob_AgentDescriptorHasNoSecrets(t *testing.T) {
	a := wsjAssignment(runner.OpAgent)
	b := provider.ResolvedBinding{Provider: "claude-cli", Model: "claude-sonnet-4-5", Backend: "claude-code", Effort: "high", Pinned: true,
		RevealKey: func(context.Context) (string, error) { return "sk-live", nil }}
	m := &engine.AgentMission{Model: "claude-sonnet-4-5", Prompt: "do it", WorkDir: "/w", ToolScope: []string{"Read"}, Timeout: 90 * time.Second}
	_, agent, wsd, err := runner.BuildWorkspaceJob(context.Background(), wsjDeps(&wsjPuller{}, &wsjTokens{}), a, runner.StepJob{Op: runner.OpAgent, Binding: b, Agent: m})
	require.NoError(t, err)
	require.NotNil(t, agent)
	require.NotNil(t, wsd)
	assert.Equal(t, runner.CredAnthropicSubscription, agent.CredentialKind)
	assert.Equal(t, "claude-code", agent.Backend)
	assert.Equal(t, "high", agent.Effort)
	assert.Equal(t, 90, agent.TimeoutSecs)
	assert.Equal(t, "claude-cli", agent.Provider)
	assert.Equal(t, []string{"Read"}, agent.ToolScope)
	assert.True(t, agent.RequireExecutedModel, "strict execution evidence survives serialization")
	assert.NotEmpty(t, wsd.BaseRef)
	assert.NoError(t, wsd.Resource.Validate())
}

func TestBuildWorkspaceJob_FailsClosedWithoutDeps(t *testing.T) {
	a := wsjAssignment(runner.OpAgent)
	_, _, _, err := runner.BuildWorkspaceJob(context.Background(), nil, a, runner.StepJob{Op: runner.OpAgent, Agent: &engine.AgentMission{WorkDir: "/w"}})
	require.ErrorIs(t, err, runner.ErrWorkspaceJobsUnavailable)

	boom := errors.New("checkpoint store down")
	_, _, _, err = runner.BuildWorkspaceJob(context.Background(), wsjDeps(&wsjPuller{err: boom}, &wsjTokens{}), a,
		runner.StepJob{Op: runner.OpAgent, Agent: &engine.AgentMission{WorkDir: "/w"}})
	require.ErrorIs(t, err, boom)
}

func TestWorkspaceStepOutput_MapsResults(t *testing.T) {
	out, err := runner.WorkspaceStepOutput(runner.OpProcess, &runner.RemoteResult{OK: true, Process: &engine.ProcessResult{Stdout: "{}", ExitCode: 0}})
	require.NoError(t, err)
	assert.Equal(t, "{}", out.Process.Stdout)
	out, err = runner.WorkspaceStepOutput(runner.OpAgent, &runner.RemoteResult{OK: true, Agent: &engine.AgentResult{Summary: "done"}})
	require.NoError(t, err)
	assert.Equal(t, "done", out.Agent.Summary)
	_, err = runner.WorkspaceStepOutput(runner.OpAgent, &runner.RemoteResult{OK: true})
	require.Error(t, err, "a success without the op's payload is a protocol error")
}

// The adapter mapping carries only the typed result. It never synthesizes a
// workspace receipt; P3 T6's dispatcher attaches the exact prepared candidate
// receipt afterwards, so a mapped output has no Completion of its own.
func TestWorkspaceStepOutput_CompletionSurvivesAdapterMapping(t *testing.T) {
	out, err := runner.WorkspaceStepOutput(runner.OpProcess, &runner.RemoteResult{OK: true, Process: &engine.ProcessResult{ExitCode: 0}})
	require.NoError(t, err)
	assert.Nil(t, out.Completion, "the mapping never invents a receipt from Latest or the result")
	receipt := &engine.StepCompletion{}
	out.Completion = receipt
	assert.Same(t, receipt, out.Completion, "a receipt attached by the dispatcher is carried as-is")
}
