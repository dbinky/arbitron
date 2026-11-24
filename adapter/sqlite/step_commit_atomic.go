package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

var _ engine.StepCommitter = (*StepCommitRepo)(nil)

// suspensionOperationID keys a node's own suspension record in step_suspensions.
const suspensionOperationID = "node"

// CommitStep writes c in one transaction or not at all (RR-10, contracts §21):
// validate invocation/run/node and every exact job fence, resource claim and
// prepared candidate; promote candidates and home pointers; complete the jobs
// and release the claims; persist operations, the execution, variables,
// continuation and the step_commits (advance) or step_suspensions (suspend)
// row. An identical repeat returns nil and writes nothing.
func (r *StepCommitRepo) CommitStep(ctx context.Context, c engine.StepCommit) error {
	if err := c.Validate(); err != nil {
		return err
	}
	varsJSON := ""
	if c.Mode == engine.StepCommitAdvance {
		v, err := toJSONString(c.Variables)
		if err != nil {
			return fmt.Errorf("step commit: marshalling variables: %w", err)
		}
		varsJSON = v
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("step commit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	inv := uuidToString(c.InvocationID)
	// First statement takes the SQLite write lock before any read.
	if _, err := tx.ExecContext(ctx, `UPDATE node_invocations SET id = id WHERE id = ?`, inv); err != nil {
		return fmt.Errorf("step commit: lock: %w", err)
	}
	var runStr, nodeID string
	var committedAt sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT run_id, node_id, committed_at FROM node_invocations WHERE id = ?`, inv).Scan(&runStr, &nodeID, &committedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("step commit: %w: %s", engine.ErrInvocationNotFound, c.InvocationID)
	}
	if err != nil {
		return fmt.Errorf("step commit: reading invocation: %w", err)
	}
	if runStr != uuidToString(c.RunID) || nodeID != c.NodeID {
		return fmt.Errorf("%w: invocation %s is node %q of run %s, not node %q of run %s",
			engine.ErrInvalidStepCompletion, c.InvocationID, nodeID, runStr, c.NodeID, c.RunID)
	}
	replayed, err := sameCommitTx(ctx, tx, c, varsJSON)
	if err != nil {
		return err
	}
	if replayed {
		return nil // already durable; the deferred rollback discards the read-only transaction
	}
	if committedAt.Valid {
		return fmt.Errorf("%w: invocation %s is already committed", engine.ErrInvalidStepCompletion, c.InvocationID)
	}
	now := timeToString(r.now())
	for _, comp := range c.Completions {
		if err := consumeCompletionTx(ctx, tx, c, comp, now); err != nil {
			return err
		}
	}
	switch c.Mode {
	case engine.StepCommitAdvance:
		if err := insertNodeExecutionTx(ctx, tx, c.Execution); err != nil {
			return fmt.Errorf("step commit: %w", err)
		}
		res, err := tx.ExecContext(ctx, `UPDATE instances SET variables = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?`,
			varsJSON, uuidToString(c.RunID))
		if err != nil {
			return fmt.Errorf("step commit: updating variables: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("step commit: run %s not found", c.RunID)
		}
		if c.NextNodeID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE instances SET current_node_id = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?`,
				c.NextNodeID, uuidToString(c.RunID)); err != nil {
				return fmt.Errorf("step commit: advancing run: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE node_invocations SET committed_at = ? WHERE id = ? AND committed_at IS NULL`, now, inv); err != nil {
				return fmt.Errorf("step commit: committing invocation: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO step_commits (invocation_id, run_id, node_id, execution_id, next_node_id, variables, committed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, inv, uuidToString(c.RunID), c.NodeID, uuidToString(c.Execution.ID), c.NextNodeID, varsJSON, now); err != nil {
			return fmt.Errorf("step commit: recording commit: %w", err)
		}
	case engine.StepCommitSuspend:
		state, err := toJSONString(c.Suspension.State)
		if err != nil {
			return fmt.Errorf("step commit: marshalling suspension state: %w", err)
		}
		var child sql.NullString
		if c.Suspension.ChildLinkID != nil {
			child = sql.NullString{String: uuidToString(*c.Suspension.ChildLinkID), Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO step_suspensions (invocation_id, operation_id, kind, child_link_id, state, committed_at)
			VALUES (?, ?, ?, ?, ?, ?)`, inv, suspensionOperationID, c.Suspension.Kind, child, state, now); err != nil {
			return fmt.Errorf("step commit: recording suspension: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("step commit: commit: %w", err)
	}
	return nil
}

// sameCommitTx reports whether exactly c is already durable. A different
// commit already recorded for the invocation is a conflict.
func sameCommitTx(ctx context.Context, tx *sql.Tx, c engine.StepCommit, varsJSON string) (bool, error) {
	inv := uuidToString(c.InvocationID)
	conflict := fmt.Errorf("%w: invocation %s already committed a different %s", engine.ErrInvalidStepCompletion, c.InvocationID, c.Mode)
	switch c.Mode {
	case engine.StepCommitAdvance:
		var execStr, next, vars string
		err := tx.QueryRowContext(ctx, `SELECT execution_id, next_node_id, variables FROM step_commits WHERE invocation_id = ?`, inv).Scan(&execStr, &next, &vars)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("step commit: reading prior commit: %w", err)
		}
		if execStr != uuidToString(c.Execution.ID) || next != c.NextNodeID || vars != varsJSON {
			return false, conflict
		}
	case engine.StepCommitSuspend:
		var advanced int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM step_commits WHERE invocation_id = ?`, inv).Scan(&advanced); err != nil {
			return false, fmt.Errorf("step commit: reading prior commit: %w", err)
		}
		if advanced > 0 {
			return false, conflict
		}
		var kind, state string
		var child sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT kind, child_link_id, state FROM step_suspensions WHERE invocation_id = ? AND operation_id = ?`,
			inv, suspensionOperationID).Scan(&kind, &child, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("step commit: reading prior suspension: %w", err)
		}
		wantState, err := toJSONString(c.Suspension.State)
		if err != nil {
			return false, err
		}
		wantChild := ""
		if c.Suspension.ChildLinkID != nil {
			wantChild = uuidToString(*c.Suspension.ChildLinkID)
		}
		if kind != c.Suspension.Kind || child.String != wantChild || state != wantState {
			return false, conflict
		}
	}
	var consumed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM step_commit_operations WHERE invocation_id = ?`, inv).Scan(&consumed); err != nil {
		return false, fmt.Errorf("step commit: counting prior operations: %w", err)
	}
	if consumed != len(c.Completions) {
		return false, conflict
	}
	for _, comp := range c.Completions {
		prev, found, err := readOperationTx(ctx, tx, c.InvocationID, comp.JobFence.JobID)
		if err != nil {
			return false, err
		}
		if !found || !sameReceipt(prev, comp) {
			return false, conflict
		}
	}
	return true, nil
}

// consumeCompletionTx validates one receipt against the exact live job fence,
// resource claim and prepared candidate, then promotes, completes, releases
// and records it. A receipt this invocation already consumed (its suspension)
// is accepted once more without completing anything again.
func consumeCompletionTx(ctx context.Context, tx *sql.Tx, c engine.StepCommit, comp engine.StepCompletion, now string) error {
	prev, found, err := readOperationTx(ctx, tx, c.InvocationID, comp.JobFence.JobID)
	if err != nil {
		return err
	}
	if found {
		if sameReceipt(prev, comp) {
			return nil
		}
		return fmt.Errorf("%w: job %s was already committed with a different receipt", engine.ErrInvalidStepCompletion, comp.JobFence.JobID)
	}
	f := runner.Fence{JobID: comp.JobFence.JobID, RunnerID: runner.RunnerID(comp.JobFence.RunnerID), Generation: comp.JobFence.Generation}
	if err := fenceErr(ctx, tx, f); err != nil {
		return fmt.Errorf("step commit: %w", err)
	}
	job, err := getJob(ctx, tx, f.JobID)
	if err != nil {
		return err
	}
	if job.Key.RunID != c.RunID || job.Key.InvocationID != c.InvocationID {
		return fmt.Errorf("%w: job %s belongs to run %s invocation %s, not %s/%s",
			engine.ErrInvalidStepCompletion, f.JobID, job.Key.RunID, job.Key.InvocationID, c.RunID, c.InvocationID)
	}
	affinity, err := jobAffinityTx(ctx, tx, f.JobID)
	if err != nil {
		return err
	}
	w := comp.Workspace
	checkpointRef := ""
	switch {
	case w == nil && affinity == runner.AffinityWorkspace:
		return fmt.Errorf("%w: workspace operation job %s has no workspace receipt", engine.ErrInvalidStepCompletion, f.JobID)
	case w != nil:
		if err := checkResourceFenceTx(ctx, tx, w.Resource); err != nil {
			return fmt.Errorf("step commit: %w", err)
		}
		if err := requireJobHolderTx(ctx, tx, w.Resource, f); err != nil {
			return err
		}
		if w.NonPortable {
			if err := commitNonPortableTx(ctx, tx, c.RunID, f, now); err != nil {
				return err
			}
		} else {
			if err := promoteCandidateTx(ctx, tx, c.RunID, f, *w, now); err != nil {
				return err
			}
			checkpointRef = w.Ref
		}
	}
	if err := completeJobTx(ctx, tx, f, "step:"+uuidToString(c.InvocationID), checkpointRef, now); err != nil {
		return fmt.Errorf("step commit: %w", err)
	}
	var ref, key, owner, token sql.NullString
	var resGen sql.NullInt64
	if w != nil {
		if err := releaseResourceTx(ctx, tx, w.Resource); err != nil {
			return fmt.Errorf("step commit: %w", err)
		}
		ref = sql.NullString{String: checkpointRef, Valid: checkpointRef != ""}
		key = sql.NullString{String: w.Resource.Key, Valid: true}
		owner = sql.NullString{String: w.Resource.Owner, Valid: true}
		token = sql.NullString{String: w.Resource.Token.String(), Valid: true}
		resGen = sql.NullInt64{Int64: w.Resource.Generation, Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO step_commit_operations (invocation_id, job_id, job_runner_id, job_generation,
		checkpoint_ref, resource_key, resource_owner, resource_generation, resource_token) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuidToString(c.InvocationID), uuidToString(f.JobID), string(f.RunnerID), f.Generation, ref, key, owner, resGen, token); err != nil {
		return fmt.Errorf("step commit: recording operation: %w", err)
	}
	return nil
}

func jobAffinityTx(ctx context.Context, tx groupQueryer, jobID uuid.UUID) (runner.Affinity, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT requirements FROM runner_jobs WHERE id = ?`, uuidToString(jobID)).Scan(&raw); err != nil {
		return "", fmt.Errorf("step commit: reading job requirements: %w", err)
	}
	var req runner.ExecRequirements
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return "", fmt.Errorf("step commit: decoding job requirements: %w", err)
	}
	return req.Affinity, nil
}

// requireJobHolderTx checks that the exact claim generation is held by this
// exact job generation (P3 T3 workspace_lease_holders).
func requireJobHolderTx(ctx context.Context, tx groupQueryer, res engine.ResourceFenceRef, f runner.Fence) error {
	var jobID, runnerID sql.NullString
	var gen sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT job_id, job_runner_id, job_generation FROM workspace_lease_holders
		WHERE resource_key = ? AND generation = ? AND token = ?`, res.Key, res.Generation, res.Token.String()).Scan(&jobID, &runnerID, &gen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("step commit: reading claim holder: %w", err)
	}
	if err != nil || !jobID.Valid || jobID.String != uuidToString(f.JobID) || runnerID.String != string(f.RunnerID) || gen.Int64 != f.Generation {
		return fmt.Errorf("%w: claim %s generation %d is not held by job %s generation %d",
			engine.ErrInvalidStepCompletion, res.Key, res.Generation, f.JobID, f.Generation)
	}
	return nil
}

// committedRefTx is the run's committed recovery base ("" before the first).
func committedRefTx(ctx context.Context, tx groupQueryer, runID uuid.UUID) (string, error) {
	var version int64
	err := tx.QueryRowContext(ctx, `SELECT checkpoint_version FROM workspace_homes WHERE run_id = ?`, uuidToString(runID)).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && version == 0) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("step commit: reading home pointer: %w", err)
	}
	var hash string
	if err := tx.QueryRowContext(ctx, `SELECT manifest_hash FROM workspace_checkpoints WHERE run_id = ? AND version = ? AND state = 'committed'`,
		uuidToString(runID), version).Scan(&hash); err != nil {
		return "", fmt.Errorf("step commit: committed checkpoint %d of run %s: %w", version, runID, err)
	}
	return runner.CheckpointRef{RunID: runID, Version: version, ManifestHash: hash}.String(), nil
}

// promoteCandidateTx promotes exactly the receipt's prepared candidate: same
// run, same job generation, same resource claim, retained artifact, based on
// the current committed pointer. No newest-row query, no fallback.
func promoteCandidateTx(ctx context.Context, tx groupQueryer, runID uuid.UUID, f runner.Fence, w engine.WorkspaceCompletion, now string) error {
	ref, err := runner.ParseCheckpointRef(w.Ref)
	if err != nil || ref.RunID != runID {
		return fmt.Errorf("%w: checkpoint %q is not a candidate of run %s", engine.ErrInvalidStepCompletion, w.Ref, runID)
	}
	run := uuidToString(runID)
	var state, archive, base, resKey, resOwner, resToken string
	var jobID sql.NullString
	var jobGen sql.NullInt64
	var resGen int64
	err = tx.QueryRowContext(ctx, `SELECT state, archive_path, base_ref, job_id, job_generation, resource_key, resource_owner, resource_generation, resource_token
		FROM workspace_checkpoints WHERE run_id = ? AND version = ? AND manifest_hash = ?`, run, ref.Version, ref.ManifestHash).
		Scan(&state, &archive, &base, &jobID, &jobGen, &resKey, &resOwner, &resGen, &resToken)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: checkpoint %s was never prepared", engine.ErrInvalidStepCompletion, w.Ref)
	}
	if err != nil {
		return fmt.Errorf("step commit: reading candidate: %w", err)
	}
	switch {
	case state != "prepared":
		return fmt.Errorf("%w: checkpoint %s is %s, not a prepared candidate", engine.ErrInvalidStepCompletion, w.Ref, state)
	case archive == "":
		return fmt.Errorf("%w: checkpoint %s has no retained artifact", engine.ErrInvalidStepCompletion, w.Ref)
	case !jobID.Valid || jobID.String != uuidToString(f.JobID) || !jobGen.Valid || jobGen.Int64 != f.Generation:
		return fmt.Errorf("%w: checkpoint %s was prepared by another operation", engine.ErrInvalidStepCompletion, w.Ref)
	case resKey != w.Resource.Key || resOwner != w.Resource.Owner || resGen != w.Resource.Generation || resToken != w.Resource.Token.String():
		return fmt.Errorf("%w: checkpoint %s was prepared under another resource claim", engine.ErrInvalidStepCompletion, w.Ref)
	}
	cur, err := committedRefTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	if base != cur {
		return fmt.Errorf("%w: checkpoint %s is based on %q, but the committed checkpoint is %q", engine.ErrInvalidStepCompletion, w.Ref, base, cur)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_checkpoints SET state = 'committed' WHERE run_id = ? AND version = ? AND state = 'prepared'`,
		run, ref.Version); err != nil {
		return fmt.Errorf("step commit: promoting candidate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_homes (run_id, updated_at) VALUES (?, ?) ON CONFLICT(run_id) DO NOTHING`, run, now); err != nil {
		return fmt.Errorf("step commit: home row: %w", err)
	}
	// A portable candidate also clears the RR-08 non-portable marker.
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_homes SET checkpoint_version = ?, dirty = 0, non_portable_reason = '', updated_at = ? WHERE run_id = ?`,
		ref.Version, now, run); err != nil {
		return fmt.Errorf("step commit: advancing home pointer: %w", err)
	}
	return nil
}

func readOperationTx(ctx context.Context, q groupQueryer, invocationID, jobID uuid.UUID) (engine.StepCompletion, bool, error) {
	var runnerID string
	var gen int64
	var ref, key, owner, token sql.NullString
	var resGen sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT job_runner_id, job_generation, checkpoint_ref, resource_key, resource_owner, resource_generation, resource_token
		FROM step_commit_operations WHERE invocation_id = ? AND job_id = ?`, uuidToString(invocationID), uuidToString(jobID)).
		Scan(&runnerID, &gen, &ref, &key, &owner, &resGen, &token)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.StepCompletion{}, false, nil
	}
	if err != nil {
		return engine.StepCompletion{}, false, fmt.Errorf("step commit: reading operation: %w", err)
	}
	c, err := operationFromRow(jobID, runnerID, gen, ref, key, owner, resGen, token)
	return c, err == nil, err
}

// operationFromRow rebuilds a persisted receipt. A NULL checkpoint_ref under a
// resource claim is an RR-08 non-portable receipt; a "local:" ref is RR-17 B's
// (P3 T16 Cycle 3 also sets LocalRef in that branch).
func operationFromRow(jobID uuid.UUID, runnerID string, gen int64, ref, key, owner sql.NullString, resGen sql.NullInt64, token sql.NullString) (engine.StepCompletion, error) {
	c := engine.StepCompletion{JobFence: engine.JobFenceRef{JobID: jobID, RunnerID: runnerID, Generation: gen}}
	if !key.Valid {
		return c, nil
	}
	tok, err := parseUUID(token.String)
	if err != nil {
		return engine.StepCompletion{}, err
	}
	w := &engine.WorkspaceCompletion{Resource: engine.ResourceFenceRef{Key: key.String, Owner: owner.String, Generation: resGen.Int64, Token: tok}}
	switch {
	case !ref.Valid:
		w.NonPortable = true
	case strings.HasPrefix(ref.String, "local:"):
		w.NonPortable = true
	default:
		w.Ref = ref.String
	}
	c.Workspace = w
	return c, nil
}

func sameReceipt(a, b engine.StepCompletion) bool {
	if a.JobFence != b.JobFence || (a.Workspace == nil) != (b.Workspace == nil) {
		return false
	}
	return a.Workspace == nil || *a.Workspace == *b.Workspace
}

func (r *StepCommitRepo) operations(ctx context.Context, invocationID uuid.UUID) ([]engine.StepCompletion, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT job_id, job_runner_id, job_generation, checkpoint_ref, resource_key, resource_owner, resource_generation, resource_token
		FROM step_commit_operations WHERE invocation_id = ? ORDER BY rowid`, uuidToString(invocationID))
	if err != nil {
		return nil, fmt.Errorf("reading step operations: %w", err)
	}
	defer rows.Close()
	var out []engine.StepCompletion
	for rows.Next() {
		var jobStr, runnerID string
		var gen int64
		var ref, key, owner, token sql.NullString
		var resGen sql.NullInt64
		if err := rows.Scan(&jobStr, &runnerID, &gen, &ref, &key, &owner, &resGen, &token); err != nil {
			return nil, err
		}
		jobID, err := parseUUID(jobStr)
		if err != nil {
			return nil, err
		}
		c, err := operationFromRow(jobID, runnerID, gen, ref, key, owner, resGen, token)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CommittedResult returns the durable commit of invocationID with full replay
// data (execution, variables, continuation, receipts), a suspension record when
// the invocation only suspended, or false.
func (r *StepCommitRepo) CommittedResult(ctx context.Context, invocationID uuid.UUID) (*engine.StepCommit, bool, error) {
	inv := uuidToString(invocationID)
	var runStr, nodeID, execStr, next, varsJSON string
	err := r.db.QueryRowContext(ctx, `SELECT run_id, node_id, execution_id, next_node_id, variables FROM step_commits WHERE invocation_id = ?`, inv).
		Scan(&runStr, &nodeID, &execStr, &next, &varsJSON)
	switch {
	case err == nil:
		runID, err := parseUUID(runStr)
		if err != nil {
			return nil, false, err
		}
		execID, err := parseUUID(execStr)
		if err != nil {
			return nil, false, err
		}
		vars, err := fromJSONString(varsJSON)
		if err != nil {
			return nil, false, fmt.Errorf("step commit variables: %w", err)
		}
		exec, err := loadNodeExecution(ctx, r.db, runID, execID)
		if err != nil {
			return nil, false, err
		}
		comps, err := r.operations(ctx, invocationID)
		if err != nil {
			return nil, false, err
		}
		return &engine.StepCommit{Mode: engine.StepCommitAdvance, RunID: runID, InvocationID: invocationID, NodeID: nodeID,
			Execution: exec, Variables: vars, NextNodeID: next, Completions: comps}, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, false, fmt.Errorf("reading step commit: %w", err)
	}
	var kind, stateJSON string
	var child sql.NullString
	err = r.db.QueryRowContext(ctx, `SELECT s.kind, s.child_link_id, s.state, i.run_id, i.node_id
		FROM step_suspensions s JOIN node_invocations i ON i.id = s.invocation_id
		WHERE s.invocation_id = ? AND s.operation_id = ?`, inv, suspensionOperationID).Scan(&kind, &child, &stateJSON, &runStr, &nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading step suspension: %w", err)
	}
	runID, err := parseUUID(runStr)
	if err != nil {
		return nil, false, err
	}
	s := &engine.StepSuspension{Kind: kind}
	if s.State, err = fromJSONString(stateJSON); err != nil {
		return nil, false, fmt.Errorf("step suspension state: %w", err)
	}
	if child.Valid {
		id, err := parseUUID(child.String)
		if err != nil {
			return nil, false, err
		}
		s.ChildLinkID = &id
	}
	comps, err := r.operations(ctx, invocationID)
	if err != nil {
		return nil, false, err
	}
	return &engine.StepCommit{Mode: engine.StepCommitSuspend, RunID: runID, InvocationID: invocationID, NodeID: nodeID,
		Suspension: s, Completions: comps}, true, nil
}

// commitNonPortableTx is the RR-08 branch: the home carries the non-portable
// marker and lives on the built-in server home, and the job ran on the
// built-in runner. No candidate is promoted and checkpoint_version is
// unchanged; dirty = 1 records that the working copy is ahead of the committed
// base. P3 T16 Cycle 3 replaces dirty = 1 with the RR-17 B local-checkpoint
// promotion (local_checkpoint = LocalRef, dirty = 0).
func commitNonPortableTx(ctx context.Context, tx groupQueryer, runID uuid.UUID, f runner.Fence, now string) error {
	var reason, owner string
	err := tx.QueryRowContext(ctx, `SELECT non_portable_reason, runner_id FROM workspace_homes WHERE run_id = ?`, uuidToString(runID)).Scan(&reason, &owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && reason == "") {
		return fmt.Errorf("%w: run %s has a portable home; a non-portable receipt is not valid", engine.ErrInvalidStepCompletion, runID)
	}
	if err != nil {
		return fmt.Errorf("step commit: reading home: %w", err)
	}
	if (owner != string(runner.ServerHome) && owner != string(runner.BuiltInRunnerID)) || f.RunnerID != runner.BuiltInRunnerID {
		return fmt.Errorf("%w: non-portable home of run %s is on %q and job %s ran on %q; both must be the built-in server home",
			engine.ErrInvalidStepCompletion, runID, owner, f.JobID, f.RunnerID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_homes SET dirty = 1, updated_at = ? WHERE run_id = ?`, now, uuidToString(runID)); err != nil {
		return fmt.Errorf("step commit: marking non-portable home dirty: %w", err)
	}
	return nil
}
