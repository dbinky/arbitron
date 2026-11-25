package sqlite

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
)

var _ runner.ResourceLeaseStore = (*ResourceLeaseRepo)(nil)

// Supersede hands a claim to a new server-side holder only on durable proof
// that the old writer can no longer write, all inside ONE transaction:
//  1. old is the exact active claim (else ErrResourceFenced);
//  2. a job-held claim names proof.OldJob exactly and the ledger no longer
//     authorizes that job fence (the caller fenced it first); a server-side
//     claim requires proof.OldJob == nil;
//  3. the kind's evidence holds now (lost runner, revoked key, confirmed stop,
//     a reconnect-absence report, or a server-side claim from an earlier boot);
//  4. a supersession receipt is written and the claim moves to generation+1
//     with a fresh token for holder/owner, recorded under this boot.
//
// Suspect/offline liveness never authorizes it; there is no TTL path. Any
// failure changes nothing.
func (r *ResourceLeaseRepo) Supersede(ctx context.Context, old engine.ResourceFenceRef, proof runner.SupersedeProof, holder string, owner runner.RunnerID) (engine.ResourceFenceRef, error) {
	if holder == "" || owner == "" || r.bootID == "" {
		return engine.ResourceFenceRef{}, errors.New("resource lease: supersede needs holder, owner and a boot ID")
	}
	if err := old.Validate(); err != nil {
		return engine.ResourceFenceRef{}, fmt.Errorf("%w: %v", runner.ErrResourceFenced, err)
	}
	tx, err := r.lockLease(ctx, old.Key)
	if err != nil {
		return engine.ResourceFenceRef{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkResourceFenceTx(ctx, tx, old); err != nil {
		return engine.ResourceFenceRef{}, err
	}
	rec, found, err := readHolder(ctx, tx, old)
	if err != nil {
		return engine.ResourceFenceRef{}, err
	}
	if !found {
		return engine.ResourceFenceRef{}, fmt.Errorf("%w: claim %s generation %d has no holder record", runner.ErrSupersedeUnproven, old.Key, old.Generation)
	}
	if err := r.proveOldJobStopped(ctx, tx, rec, proof); err != nil {
		return engine.ResourceFenceRef{}, err
	}
	if err := r.proveKind(ctx, tx, rec, proof.Kind); err != nil {
		return engine.ResourceFenceRef{}, err
	}
	var jobID, jobGen any
	if rec.Job != nil {
		jobID, jobGen = uuidToString(rec.Job.JobID), rec.Job.Generation
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_lease_supersessions(resource_key, old_generation, old_token,
		new_generation, kind, job_id, job_generation, superseded_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		old.Key, old.Generation, old.Token.String(), old.Generation+1, string(proof.Kind), jobID, jobGen, timeToString(r.now())); err != nil {
		return engine.ResourceFenceRef{}, fmt.Errorf("resource lease: supersession receipt: %w", err)
	}
	ref, err := r.grant(ctx, tx, old.Key, holder, string(owner), old.Generation, nil, r.bootID)
	if err != nil {
		return engine.ResourceFenceRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.ResourceFenceRef{}, fmt.Errorf("resource lease: commit: %w", err)
	}
	return ref, nil
}

func (r *ResourceLeaseRepo) proveOldJobStopped(ctx context.Context, tx groupQueryer, rec runner.LeaseRecord, proof runner.SupersedeProof) error {
	if rec.Job == nil {
		if proof.OldJob != nil {
			return fmt.Errorf("%w: claim %s has no job but the proof names one", runner.ErrSupersedeUnproven, rec.Fence.Key)
		}
		return nil
	}
	if proof.OldJob == nil || *proof.OldJob != *rec.Job {
		return fmt.Errorf("%w: proof does not name the claim's job %s generation %d", runner.ErrSupersedeUnproven, rec.Job.JobID, rec.Job.Generation)
	}
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_jobs WHERE id = ? AND generation = ? AND runner_id = ? AND state IN ('offered', 'running')`,
		uuidToString(rec.Job.JobID), rec.Job.Generation, string(rec.Job.RunnerID)).Scan(&live); err != nil {
		return fmt.Errorf("resource lease: old job fence: %w", err)
	}
	if live != 0 {
		return fmt.Errorf("%w: job %s generation %d is still authorized; fence it first", runner.ErrSupersedeUnproven, rec.Job.JobID, rec.Job.Generation)
	}
	return nil
}

func (r *ResourceLeaseRepo) proveKind(ctx context.Context, tx groupQueryer, rec runner.LeaseRecord, kind runner.SupersedeKind) error {
	owner := rec.Fence.Owner
	remote := owner != string(runner.BuiltInRunnerID)
	var ok int
	var err error
	switch {
	case kind == runner.SupersedeRunnerLost && remote:
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runners WHERE id = ? AND liveness = 'lost'`, owner).Scan(&ok)
	case kind == runner.SupersedeRevoked && remote:
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_keys WHERE runner_id = ? AND revoked_at IS NOT NULL`, owner).Scan(&ok)
	case kind == runner.SupersedeStopped && rec.Job != nil && !remote:
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM builtin_processes WHERE job_id = ? AND generation = ? AND state = 'stopped'`,
			uuidToString(rec.Job.JobID), rec.Job.Generation).Scan(&ok)
	case kind == runner.SupersedeStopped && rec.Job != nil && remote:
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_inbox_cursors WHERE job_id = ? AND generation = ? AND result_received = 1`,
			uuidToString(rec.Job.JobID), rec.Job.Generation).Scan(&ok)
	case kind == runner.SupersedeReconnectAbsent && rec.Job != nil && remote:
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_job_absences WHERE job_id = ? AND generation = ? AND runner_id = ?`,
			uuidToString(rec.Job.JobID), rec.Job.Generation, owner).Scan(&ok)
	case kind == runner.SupersedeServerRestart && rec.Job == nil:
		if rec.BootID != r.bootID {
			ok = 1
		}
	}
	if err != nil {
		return fmt.Errorf("resource lease: %s evidence: %w", kind, err)
	}
	if ok == 0 {
		return fmt.Errorf("%w: no durable %q evidence for claim %s generation %d (owner %q)", runner.ErrSupersedeUnproven, kind, rec.Fence.Key, rec.Fence.Generation, owner)
	}
	return nil
}
