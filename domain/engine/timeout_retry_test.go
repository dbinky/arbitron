package engine_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// timeoutRetryWorkflow builds A -> B -> C where B has the given type, node
// timeout, retry scale and retry count.
func timeoutRetryWorkflow(nodeType workflow.NodeType, timeoutSeconds, scale, retries int) *workflow.Workflow {
	wf := errorWorkflow(workflow.ErrorBehavior{Type: "retry", Retries: retries})
	wf.Nodes[1].Type = nodeType
	wf.Nodes[1].Config.TimeoutSeconds = timeoutSeconds
	wf.Nodes[1].Config.TimeoutRetryScale = scale
	return wf
}

// recordTimeouts installs a handler for B that records the TimeoutSeconds each
// attempt saw and returns errs[i] for the i-th attempt (the last entry repeats).
func recordTimeouts(f *testFixtures, errs ...error) func() []int {
	var mu sync.Mutex
	var seen []int
	f.executor.onNode("B", func(_ context.Context, node workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, node.Config.TimeoutSeconds)
		i := len(seen) - 1
		if i >= len(errs) {
			i = len(errs) - 1
		}
		if errs[i] == nil {
			return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
		}
		return nil, errs[i]
	})
	return func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), seen...)
	}
}

// scriptTimeoutErr mirrors the script executor's error for an expired node
// deadline: the runner returns execCtx.Err() wrapped with the node id.
func scriptTimeoutErr() error {
	return fmt.Errorf("script %q: %w", "B", context.DeadlineExceeded)
}

func nodeBErrors(t *testing.T, execs []instance.NodeExecution) []map[string]any {
	t.Helper()
	var bs []instance.NodeExecution
	for _, e := range execs {
		if e.NodeID == "B" {
			bs = append(bs, e)
		}
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i].Attempt < bs[j].Attempt })
	out := make([]map[string]any, len(bs))
	for i, e := range bs {
		out[i] = e.Error
	}
	return out
}

func TestTimeoutRetry_ScalesTimeoutByDefaultFactorAndCapsAt4x(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(timeoutRetryWorkflow(workflow.NodeTypeScript, 300, 0, 3))
	seen := recordTimeouts(f, scriptTimeoutErr())

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	assert.Equal(t, []int{300, 600, 1200, 1200}, seen(), "x2 per timeout, capped at 4x configured")

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	errs := nodeBErrors(t, execs)
	require.Len(t, errs, 4)
	assert.Contains(t, errs[0]["error"], `script "B": timed out after 300s (attempt 1/4); next attempt 600s`)
	assert.Contains(t, errs[1]["error"], `timed out after 600s (attempt 2/4); next attempt 1200s`)
	assert.Contains(t, errs[2]["error"], `timed out after 1200s (attempt 3/4); next attempt 1200s`)
	assert.Contains(t, errs[3]["error"], `timed out after 1200s (attempt 4/4); no attempts left`)
	assert.Equal(t, true, errs[0]["timedOut"])
	assert.Equal(t, 300, errs[0]["timeoutSeconds"])
}

func TestTimeoutRetry_SucceedsWithScaledTimeout(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(timeoutRetryWorkflow(workflow.NodeTypeToolCall, 100, 0, 2))
	seen := recordTimeouts(f, scriptTimeoutErr(), nil)

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, []int{100, 200}, seen())
}

func TestTimeoutRetry_CustomScaleIsCappedAt4x(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(timeoutRetryWorkflow(workflow.NodeTypeScript, 100, 3, 2))
	seen := recordTimeouts(f, scriptTimeoutErr())

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, []int{100, 300, 400}, seen())
}

func TestTimeoutRetry_ScaleOneKeepsTimeoutButStillExplains(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(timeoutRetryWorkflow(workflow.NodeTypeScript, 300, 1, 2))
	seen := recordTimeouts(f, scriptTimeoutErr())

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, []int{300, 300, 300}, seen())

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	errs := nodeBErrors(t, execs)
	require.Len(t, errs, 3)
	assert.Contains(t, errs[0]["error"], `timed out after 300s (attempt 1/3); next attempt 300s`)
}

func TestTimeoutRetry_NonTimeoutErrorsAreUnchanged(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(timeoutRetryWorkflow(workflow.NodeTypeScript, 300, 0, 2))
	seen := recordTimeouts(f, errors.New("node B exploded"))

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, []int{300, 300, 300}, seen())

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	for _, e := range nodeBErrors(t, execs) {
		assert.Equal(t, map[string]any{"error": "node B exploded"}, e)
	}
}

func TestTimeoutRetry_TimeoutThenOtherFailureKeepsScaledTimeout(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(timeoutRetryWorkflow(workflow.NodeTypeScript, 100, 0, 2))
	seen := recordTimeouts(f, scriptTimeoutErr(), errors.New("boom"), errors.New("boom"))

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, []int{100, 200, 200}, seen(), "a non-timeout failure neither grows nor shrinks the timeout")
}

// Cancellation and pause (a done parent context, or context.Canceled) are
// covered by TestIsNodeTimeout in timeout_retry_internal_test.go: the engine
// routes a cancelled node to the cancellation path, not to failure.

func TestTimeoutRetry_NoNodeTimeoutMeansNoScaling(t *testing.T) {
	// A DeadlineExceeded with no node timeout came from somewhere else (a
	// provider default or an inner call), so the engine must not claim it.
	f := newTestFixtures()
	f.workflowRepo.add(timeoutRetryWorkflow(workflow.NodeTypeScript, 0, 0, 1))
	seen := recordTimeouts(f, scriptTimeoutErr())

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, []int{0, 0}, seen())

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	for _, e := range nodeBErrors(t, execs) {
		assert.NotContains(t, e["error"], "timed out after")
	}
}

func TestTimeoutRetry_NodeTypesThatIgnoreTimeoutSecondsAreUnchanged(t *testing.T) {
	// llm_call does not honour timeoutSeconds, so a DeadlineExceeded there is a
	// provider-side timeout, not the node's.
	f := newTestFixtures()
	f.workflowRepo.add(timeoutRetryWorkflow(workflow.NodeTypeLLMCall, 300, 0, 1))
	seen := recordTimeouts(f, fmt.Errorf("llm: %w", context.DeadlineExceeded))

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, []int{300, 300}, seen())
}
