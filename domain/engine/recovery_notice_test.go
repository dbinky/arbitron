package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeNoticeSource struct {
	byRun map[uuid.UUID][]instance.RecoveryNotice
	err   error
	calls []uuid.UUID
}

func (f *fakeNoticeSource) RecoveryNotices(_ context.Context, runID uuid.UUID) ([]instance.RecoveryNotice, error) {
	f.calls = append(f.calls, runID)
	if f.err != nil {
		return nil, f.err
	}
	return f.byRun[runID], nil
}

func seedRun(t *testing.T, f *testFixtures) uuid.UUID {
	t.Helper()
	id := uuid.New()
	require.NoError(t, f.instanceRepo.Create(context.Background(), &instance.Instance{
		ID: id, WorkflowName: "linear-wf", State: instance.StateRunning, Variables: map[string]any{},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	for _, node := range []string{"A", "B"} {
		require.NoError(t, f.nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
			ID: uuid.New(), InstanceID: id, NodeID: node, Attempt: 1, Status: instance.NodeStatusSucceeded, StartedAt: time.Now(),
		}))
	}
	return id
}

func TestRecoveryNotices_OnInstanceAndTraceReads(t *testing.T) {
	f := newTestFixtures()
	id := seedRun(t, f)
	notice := instance.RecoveryNotice{NodeID: "B", JobID: uuid.New(), RunnerID: "builtin", Reason: "runner_lost", At: time.Now(), Text: instance.RecoveryNoticeText}
	src := &fakeNoticeSource{byRun: map[uuid.UUID][]instance.RecoveryNotice{id: {notice}}}
	f.engine.SetRecoveryNoticeSource(src)

	inst, err := f.engine.GetInstance(bgAdmin(), id)
	require.NoError(t, err)
	require.Len(t, inst.Recovery, 1)
	assert.Equal(t, instance.RecoveryNoticeText, inst.Recovery[0].Text)
	assert.Equal(t, []uuid.UUID{id}, src.calls, "the source is asked for THIS run")

	trace, err := f.engine.GetTrace(bgAdmin(), id)
	require.NoError(t, err)
	for _, ex := range trace {
		if ex.NodeID == "B" {
			require.Len(t, ex.Recovery, 1, "the interrupted node carries the notice")
			assert.Equal(t, notice.JobID, ex.Recovery[0].JobID)
		} else {
			assert.Empty(t, ex.Recovery, "other nodes do not")
		}
	}

	node, err := f.engine.GetNodeTrace(bgAdmin(), id, "B")
	require.NoError(t, err)
	require.Len(t, node.Recovery, 1)
}

func TestRecoveryNotices_AbsentWithoutSourceAndErrorsPropagate(t *testing.T) {
	f := newTestFixtures()
	id := seedRun(t, f)
	inst, err := f.engine.GetInstance(bgAdmin(), id)
	require.NoError(t, err)
	assert.Empty(t, inst.Recovery, "no source wired: Phase 1/2 behaviour")

	f.engine.SetRecoveryNoticeSource(&fakeNoticeSource{err: errors.New("ledger down")})
	_, err = f.engine.GetInstance(bgAdmin(), id)
	require.Error(t, err, "a notice that cannot be read is never silently dropped")
	assert.Contains(t, err.Error(), "ledger down")
}
