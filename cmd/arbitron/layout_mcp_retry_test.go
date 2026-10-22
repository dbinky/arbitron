package main

import (
	"encoding/json"
	"testing"
	"time"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayoutMCPE2E_RetryGenerationSinks(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	srv := newLayoutE2EMCPServer(t, ds)
	ctx := adminCtxForLocalUser(t, ds)
	const name = "generations"
	save := func(label string) string {
		wf := queryE2EWorkflow(name, label)
		if label == "A" {
			require.NoError(t, ds.workflowSvc.Create(ctx, name, wf))
		} else {
			require.NoError(t, ds.workflowSvc.Update(ctx, name, wf))
		}
		rev := manifestRevisionOnDisk(t, ds, name)
		row := layoutE2EWaitTerminal(t, ds, rev)
		require.Equal(t, layout.StateFailed, row.State, "missing real browser must remain visibly retryable")
		require.Equal(t, layout.StageRendering, row.FailureStage)
		return rev
	}
	a := save("A")
	id, _, err := ds.eng.Trigger(ctx, name, nil, "normal")
	require.NoError(t, err)
	require.Equal(t, a, snapshotRevision(t, ds, id))
	b := save("B")
	unrelatedID, _, err := ds.eng.Trigger(ctx, name, nil, "normal")
	require.NoError(t, err)
	require.Equal(t, b, snapshotRevision(t, ds, unrelatedID))
	c := save("C")
	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, id)
		return err == nil && inst.State == instance.StateCompleted
	}, 10*time.Second, 10*time.Millisecond)
	executionBefore, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	traceBefore, err := ds.nodeExecRepo.ListByInstance(ctx, id)
	require.NoError(t, err)
	variablesBefore, err := ds.snapshotRepo.ListByInstance(ctx, id)
	require.NoError(t, err)
	require.NotEmpty(t, traceBefore)
	require.NotEmpty(t, variablesBefore)
	before := map[string]layout.Record{}
	for _, rev := range []string{a, b, c} {
		before[rev], err = ds.layout.store.Get(ctx, rev)
		require.NoError(t, err)
	}

	// Nil wiring and empty tokens must fail the success path without generation changes.
	deps := ds.mcpDeps()
	deps.Layouts = nil
	unwired := adaptmcp.NewServerWithDeps("nil", "test", deps)
	adaptmcp.RegisterAllTools(unwired)
	for _, tc := range []struct {
		srv         *adaptmcp.Server
		token, want string
	}{
		{unwired, c, "layout service not configured"}, {srv, "", "expectedRevision is required"},
	} {
		res := callMCPToolE2E(t, tc.srv, ctx, "workflow_layout_retry", map[string]any{"name": name, "expectedRevision": tc.token})
		require.True(t, res.IsError)
		assert.Contains(t, mcpE2EText(t, res), tc.want)
	}
	stale := callMCPToolE2E(t, srv, ctx, "workflow_layout_retry", map[string]any{"name": name, "expectedRevision": a})
	require.True(t, stale.IsError)
	assert.Contains(t, mcpE2EText(t, stale), "revision conflict")
	for _, rev := range []string{a, b, c} {
		row, err := ds.layout.store.Get(ctx, rev)
		require.NoError(t, err)
		assert.Equal(t, before[rev], row, "rejected retry must not modify any revision")
	}
	nilSummary := callMCPToolE2E(t, unwired, ctx, "workflow_get", map[string]any{"name": name})
	require.False(t, nilSummary.IsError)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, nilSummary)), &manifest))
	summary := manifest["arrangement"].(map[string]any)
	assert.Equal(t, c, summary["savedRevision"])
	assert.Equal(t, false, summary["available"])
	assert.Equal(t, "layout service not configured", summary["availabilityError"])

	retry := func(tool, subject, rev string) {
		args := map[string]any{"expectedRevision": rev}
		if tool == "workflow_layout_retry" {
			args["name"] = subject
		} else {
			args["id"] = subject
		}
		res := callMCPToolE2E(t, srv, ctx, tool, args)
		require.False(t, res.IsError, mcpE2EText(t, res))
		var ack layout.RetryAck
		require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, res)), &ack))
		assert.Equal(t, rev, ack.SavedRevision)
		assert.Equal(t, layout.StatePending, ack.State)
		assert.Equal(t, layout.DisplayPending, ack.DisplayStatus)
		require.Equal(t, before[rev].JobGeneration+1, ack.JobGeneration)
		row, err := ds.layout.store.Get(ctx, rev)
		require.NoError(t, err)
		assert.Equal(t, ack.JobGeneration, row.JobGeneration, "MCP acknowledgement must match persisted SQLite")
		assert.Equal(t, before[rev].Manifest, row.Manifest, "retry must use immutable saved input")
		require.Eventually(t, func() bool {
			var v layout.View
			var err error
			if tool == "workflow_layout_retry" {
				v, err = ds.layout.query.WorkflowLayout(ctx, name)
			} else {
				v, err = ds.layout.query.InstanceLayout(ctx, id)
			}
			return err == nil && v.SavedRevision == rev && v.JobGeneration == ack.JobGeneration && v.State == layout.StateFailed && v.FailureStage == layout.StageRendering
		}, 10*time.Second, 10*time.Millisecond, "real worker/query must finish the acknowledged generation")
	}
	retry("workflow_layout_retry", name, c)
	afterC, err := ds.layout.store.Get(ctx, c)
	require.NoError(t, err)
	for _, rev := range []string{a, b} {
		row, err := ds.layout.store.Get(ctx, rev)
		require.NoError(t, err)
		assert.Equal(t, before[rev].JobGeneration, row.JobGeneration)
	}
	require.NoError(t, ds.workflowSvc.Delete(ctx, name))
	for _, tc := range []struct {
		srv         *adaptmcp.Server
		token, want string
	}{{unwired, a, "layout service not configured"}, {srv, "", "expectedRevision is required"}} {
		res := callMCPToolE2E(t, tc.srv, ctx, "instance_layout_retry", map[string]any{"id": id.String(), "expectedRevision": tc.token})
		require.True(t, res.IsError)
		assert.Contains(t, mcpE2EText(t, res), tc.want)
	}
	retry("instance_layout_retry", id.String(), a)
	executionAfter, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	traceAfter, err := ds.nodeExecRepo.ListByInstance(ctx, id)
	require.NoError(t, err)
	variablesAfter, err := ds.snapshotRepo.ListByInstance(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, executionBefore, executionAfter, "layout retry must not alter instance state or its snapshot")
	assert.Equal(t, traceBefore, traceAfter, "layout retry must not re-execute nodes")
	assert.Equal(t, variablesBefore, variablesAfter, "layout retry must not alter execution variables")
	for _, want := range []layout.Record{before[b], afterC} {
		row, err := ds.layout.store.Get(ctx, want.Revision)
		require.NoError(t, err)
		assert.Equal(t, want.JobGeneration, row.JobGeneration)
	}
	require.NoFileExists(t, e.privatePath(name), "historical retry must not recreate a deleted workflow")
}
