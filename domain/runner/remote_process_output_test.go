package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// badOutputRunner plays a remote runner that really edits and pushes its copy
// (the shared wsStepRunner path) and then reports executor output the node
// executor must reject.
type badOutputRunner struct {
	inner *wsStepRunner
	res   engine.ProcessResult
}

func (r *badOutputRunner) Run(ctx context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	out, err := r.inner.Run(ctx, a, job)
	if err != nil {
		return out, err
	}
	res := r.res
	out.Process = &res
	return out, nil
}

// Edge-case (P3 T8 alignment): a remote process whose candidate was pushed and
// prepared, but whose executor output is invalid (non-JSON stdout, or a
// nonzero exit), never commits the workspace. The real ToolCallExecutor runs
// through the real routed process runner; the engine contract
// (engine.releaseAttempt -> StepCompletionReleaser) hands the attempt's
// receipts back, and the prepared candidate is discarded.
func TestRemoteProcess_InvalidExecutorOutputDoesNotCommitWorkspace(t *testing.T) {
	cases := map[string]engine.ProcessResult{
		"stdout is not JSON": {Stdout: "Traceback: not json", ExitCode: 0},
		"nonzero exit":       {Stdout: `{"ok":true}`, Stderr: "boom", ExitCode: 3},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWSFixture(t)
			f.addRemote(t, "mac-1", 2)
			f.offline(t, builtin)
			base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
			inner := &wsStepRunner{f: f, id: "mac-1"}
			inner.set(map[string]string{"a.txt": "remote edit"}, nil)
			d := runner.NewStepDispatcher(runner.DispatcherDeps{
				Registry: f.pool.Registry, Ledger: f.pool.Ledger,
				Runners: runner.StaticRunnerSet{"mac-1": &badOutputRunner{inner: inner, res: res}},
				Events:  f.pool.Events, Status: f.status, Health: f.pool.Health,
				Telemetry: runner.EmptyTelemetry{}, Clock: f.pool.Clock.Now, TransientMaxAttempts: 2,
				BackoffMin: time.Millisecond, BackoffMax: time.Millisecond,
				Sleep: func(context.Context, time.Duration) error { return nil }, Workspace: f.sync,
			})
			t.Cleanup(d.Close)
			wd := &wdFixture{wsFixture: f, d: d}
			ctx, sink, inv := wd.stepCtx(t, context.Background(), true)

			vars := variable.NewStore()
			vars.SetReserved(engine.ReservedVarWorkspace, f.dir())
			node := workflow.Node{ID: "edit", Type: workflow.NodeTypeToolCall,
				Config: workflow.NodeConfig{Command: "/bin/sh", Args: []string{"edit.sh"}, Portable: true}}
			exec := process.NewToolCallExecutor(nil, runner.NewRoutedProcessRunner(d))

			_, execErr := exec.Execute(ctx, node, vars, "wf")
			require.Error(t, execErr, "invalid executor output fails the node")

			// Transport succeeded: the remote pushed a prepared candidate and one receipt exists.
			got := sink.Completions()
			require.Len(t, got, 1)
			require.NotNil(t, got[0].Workspace)
			cand, err := runner.ParseCheckpointRef(got[0].Workspace.Ref)
			require.NoError(t, err)
			assert.Equal(t, base.Version+1, cand.Version)
			assert.Equal(t, "mac-1", got[0].JobFence.RunnerID)

			// The engine never calls CommitStep for a failed execution; it releases.
			require.NoError(t, d.ReleaseCompletions(ctx, got, execErr))

			assert.Equal(t, base, f.latest(t), "no checkpoint version is published")
			// P3 T2 contract: catalog rows are never deleted (Prune keeps prepared
			// rows); the released candidate stays PREPARED and can never commit.
			state, _, _, _, found := f.preparedRow(t, cand.Version)
			require.True(t, found)
			assert.Equal(t, "prepared", state, "the candidate is never promoted")
			late := dispatched{inv: inv, sink: sink}
			require.Error(t, wd.commit(context.Background(), late, f.run), "a released receipt can never be committed later")
			assert.Equal(t, base, f.latest(t), "a late commit attempt moves nothing")
			job := wd.jobOf(t, dispatched{inv: inv})
			assert.Equal(t, runner.JobFailed, job.State)
			_, active := f.active(t)
			assert.False(t, active, "the exact resource claim is released")
			assert.Equal(t, "v1", readIn(t, f.restoreTo(t, f.latest(t)), "a.txt"))
		})
	}
}
