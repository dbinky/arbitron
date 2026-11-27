package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
)

// P1 T16 alignment acceptance (routed group-resolution bridge and handler):
// through the REAL root, a real gather conflict opens a resolution session.
// The bridge's autonomous first turn and a human follow-up through the
// conversation handler are each placed through the runner pool: one completed
// runner_jobs row per turn on builtin, both keyed under the group node's OPEN
// durable invocation with distinct resolution operation ids. The session record
// persists both placements in its placement history, and no graph-node job
// exists for the group node.
func TestRunnerWiring_ResolutionSessionUsesPool(t *testing.T) {
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	ctx := adminCtxForLocalUser(t, ds)

	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	run := waitForOpenResolution(t, ds, parentID)
	require.NotNil(t, run.Gather.ActiveSession)
	opPrefix := "resolution/" + run.ID.String() + ":"

	groupJobs := func() []rwJob {
		var out []rwJob
		for _, j := range rwJobs(t, ds.db, parentID) {
			if j.NodeID == "group-1" {
				out = append(out, j)
			}
		}
		return out
	}
	allCompleted := func(jobs []rwJob, n int) bool {
		if len(jobs) != n {
			return false
		}
		for _, j := range jobs {
			if j.State != "completed" {
				return false
			}
		}
		return true
	}
	require.Eventually(t, func() bool { return allCompleted(groupJobs(), 1) },
		20*time.Second, 50*time.Millisecond, "the autonomous resolution turn completed one pool job")

	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, parentID, "please finish resolving"))
	require.Eventually(t, func() bool { return allCompleted(groupJobs(), 2) },
		20*time.Second, 50*time.Millisecond, "first turn + follow-up each completed one pool job")

	// The group node's visit is still open: the parent waits on the session.
	var open string
	require.NoError(t, ds.db.QueryRow(`SELECT id FROM node_invocations WHERE run_id = ? AND node_id = 'group-1' AND committed_at IS NULL`,
		parentID.String()).Scan(&open))
	var openCount int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM node_invocations WHERE run_id = ? AND node_id = 'group-1' AND committed_at IS NULL`,
		parentID.String()).Scan(&openCount))
	require.Equal(t, 1, openCount, "exactly one open group visit")

	jobs := groupJobs()
	for _, j := range jobs {
		assert.Equal(t, open, j.InvocationID, "resolution turns key under the OPEN group invocation")
		assert.Equal(t, "stream", j.Op)
		assert.Equal(t, string(runner.BuiltInRunnerID), j.RunnerID)
		assert.Equal(t, string(rwAnthropic), j.SourceID)
		assert.True(t, strings.HasPrefix(j.OperationID, opPrefix), "resolution operation id: %s", j.OperationID)
		assert.NotEqual(t, "node", j.OperationID, "no graph-node job for the group node")
	}
	require.Len(t, jobs, 2)
	assert.NotEqual(t, jobs[0].OperationID, jobs[1].OperationID, "each turn is its own operation")
	assert.Equal(t, 0, rwCountJobs(ds.db, "run_id = '"+parentID.String()+"' AND node_id = 'group-1' AND operation_id = 'node'"),
		"the parallel_group node itself is never placed")

	// Persisted placement history on the session record names both jobs.
	rows := groupSessionRows(t, ds, parentID)
	require.Len(t, rows, 1, "one session record for attempt 1")
	require.True(t, engine.IsOpenGroupResolutionSession(rows[0].LLMConversation))
	var runnerID sql.NullString
	var history sql.NullString
	require.NoError(t, ds.db.QueryRow(`SELECT runner_id, placement_history FROM node_executions WHERE id = ?`,
		rows[0].ID.String()).Scan(&runnerID, &history))
	require.True(t, runnerID.Valid)
	assert.Equal(t, string(runner.BuiltInRunnerID), runnerID.String)
	require.True(t, history.Valid, "placement history is persisted")
	var hist []instance.PlacementRecord
	require.NoError(t, json.Unmarshal([]byte(history.String), &hist))
	var jobIDs []string
	require.NoError(t, func() error {
		r, err := ds.db.Query(`SELECT id FROM runner_jobs WHERE run_id = ? AND node_id = 'group-1'`, parentID.String())
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var id string
			if err := r.Scan(&id); err != nil {
				return err
			}
			jobIDs = append(jobIDs, id)
		}
		return r.Err()
	}())
	var histJobs []string
	for _, h := range hist {
		assert.Equal(t, string(runner.BuiltInRunnerID), h.RunnerID)
		histJobs = append(histJobs, h.JobID)
	}
	assert.ElementsMatch(t, jobIDs, histJobs, "the session's placement history names each turn's job")

	inst, err := ds.instanceRepo.Get(context.Background(), parentID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, inst.State)
}
