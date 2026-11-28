package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatch_ProbeIdentityCannotBypassNativeCompletionOwner(t *testing.T) {
	f := builtinOnly(t, anthropicSub(builtin), &scriptedRunner{id: builtin})
	f.d.SetWorkspace(&recWorkspace{})
	run := uuid.New()
	ctx := engine.ContextWithStepIdentity(context.Background(), engine.StepIdentity{
		RunID: run, NodeID: "probe", InvocationID: uuid.New(), OperationID: "probe/confirm", Kind: engine.OperationProbe, Attempt: 1,
	})
	_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.ErrorIs(t, err, runner.ErrNoCompletionSink, "a chat cannot masquerade as a native quota confirmation")
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State)
	assert.Empty(t, f.health.outcomes(), "no recovery from an uncommitted ordinary operation")
}
