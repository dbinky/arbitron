package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInteractiveExecutor_FirstTurnRefusedWhileLeaseHeld(t *testing.T) {
	instRepo, nodeExecRepo, llm, files, bus, queue, instanceID := interactiveTestSetup()
	files.files["test-wf/orchestration.md"] = []byte("o")
	files.files["test-wf/template.md"] = []byte("t")
	llm.streamCallFn = cannedStream([]string{"hi"}, 1, 1)
	exec := buildInteractiveExecutor(instRepo, nodeExecRepo, llm, files, bus, queue)
	store := engine.NewMemoryTurnLeaseStore()
	exec.SetTurnLeases(store, time.Minute)

	held, err := store.Acquire(context.Background(), engine.RunLeaseKey(instanceID), time.Minute) // another process' first turn
	require.NoError(t, err)
	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err = exec.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")
	assert.ErrorIs(t, err, instance.ErrInteractiveTurnInProgress)
	assert.Empty(t, llm.getCapturedReqs(), "no second first-turn reaches the provider")

	require.NoError(t, store.Release(context.Background(), held))
	_, err = exec.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")
	require.NoError(t, err)
	_, err = store.Acquire(context.Background(), engine.RunLeaseKey(instanceID), time.Minute)
	assert.NoError(t, err, "the executor released its lease after the first turn")
}

func TestGroupResolutionBridge_StartResolutionRefusedWhileLeaseHeld(t *testing.T) {
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"x"}, "s")
	store := engine.NewMemoryTurnLeaseStore()
	f.bridge.SetTurnLeases(store, time.Minute)
	key := engine.ResolutionLeaseKey(engine.GroupResolutionIdentity{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1})
	held, err := store.Acquire(context.Background(), key, time.Minute)
	require.NoError(t, err)

	_, err = f.start(1)
	assert.ErrorIs(t, err, instance.ErrInteractiveTurnInProgress)
	assert.Empty(t, f.agent.getCapturedReqs())
	assert.Empty(t, f.sessionRows())

	require.NoError(t, store.Release(context.Background(), held))
	_, err = f.start(1)
	require.NoError(t, err)
	assert.Len(t, f.sessionRows(), 1)
}

func TestInteractiveExecutor_SetTurnLeasesRejectsNil(t *testing.T) {
	instRepo, nodeExecRepo, llm, files, bus, queue, _ := interactiveTestSetup()
	exec := buildInteractiveExecutor(instRepo, nodeExecRepo, llm, files, bus, queue)
	assert.Panics(t, func() { exec.SetTurnLeases(nil, time.Minute) })
}
