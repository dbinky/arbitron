package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

var _ engine.TurnLeaseRecoveryStore = (*TurnLeaseRepo)(nil)

func invalidProof(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{engine.ErrTurnRecoveryProofInvalid}, args...)...)
}

// turnOperationMatches: equality or a sub-operation ("turn/1/stream"); never turn/1 vs turn/10.
func turnOperationMatches(jobOp, intentOp string) bool {
	return jobOp == intentOp || strings.HasPrefix(jobOp, intentOp+"/")
}

// RecoverTurnLease validates the proof against the durable intent, job and
// resource records and swaps the lease token in ONE transaction. A failed
// check changes nothing.
func (r *TurnLeaseRepo) RecoverTurnLease(ctx context.Context, old engine.TurnLease, p engine.TurnRecoveryProof, ttl time.Duration) (engine.TurnLease, error) {
	if ttl <= 0 {
		return engine.TurnLease{}, fmt.Errorf("turn lease %s: ttl must be positive", old.Key)
	}
	if err := p.Validate(); err != nil {
		return engine.TurnLease{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return engine.TurnLease{}, fmt.Errorf("recovering turn lease %s: %w", old.Key, err)
	}
	defer func() { _ = tx.Rollback() }()

	var tok sql.NullString
	var gen int64
	var heldBoot string
	err = tx.QueryRowContext(ctx, `SELECT token, generation, boot_id FROM turn_leases WHERE key = ?`, old.Key).Scan(&tok, &gen, &heldBoot)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.TurnLease{}, engine.ErrTurnLeaseLost
	}
	if err != nil {
		return engine.TurnLease{}, fmt.Errorf("recovering turn lease %s: %w", old.Key, err)
	}
	if !tok.Valid || tok.String != old.Token.String() || gen != old.Generation {
		return engine.TurnLease{}, engine.ErrTurnLeaseLost
	}

	var runID, invocationID, opID string
	var intentGen int64
	err = tx.QueryRowContext(ctx, `SELECT run_id, invocation_id, operation_id, session_generation FROM turn_intents
WHERE id = ? AND lease_key = ? AND completed_at IS NULL`, p.IntentID.String(), old.Key).Scan(&runID, &invocationID, &opID, &intentGen)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.TurnLease{}, invalidProof("intent %s is not pending on %s", p.IntentID, old.Key)
	}
	if err != nil {
		return engine.TurnLease{}, fmt.Errorf("recovering turn lease %s: intent: %w", old.Key, err)
	}
	if intentGen != old.Generation {
		return engine.TurnLease{}, invalidProof("intent %s was accepted at generation %d, lease is at %d", p.IntentID, intentGen, old.Generation)
	}

	if p.StaleHolder {
		if heldBoot == "" || r.bootID == "" || heldBoot == r.bootID {
			return engine.TurnLease{}, invalidProof("holder boot %q is not an earlier boot than %q", heldBoot, r.bootID)
		}
		rows, err := tx.QueryContext(ctx, `SELECT operation_id FROM runner_jobs WHERE run_id = ? AND invocation_id = ?`, runID, invocationID)
		if err != nil {
			return engine.TurnLease{}, fmt.Errorf("recovering turn lease %s: jobs: %w", old.Key, err)
		}
		dispatched := false
		for rows.Next() {
			var op string
			if err := rows.Scan(&op); err != nil {
				rows.Close()
				return engine.TurnLease{}, err
			}
			dispatched = dispatched || turnOperationMatches(op, opID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return engine.TurnLease{}, err
		}
		if err := rows.Close(); err != nil {
			return engine.TurnLease{}, err
		}
		if dispatched {
			return engine.TurnLease{}, invalidProof("operation %s was dispatched; use the job proof", opID)
		}
	}
	for _, j := range p.Jobs {
		var jRun, jInv, jOp, state string
		var jRunner sql.NullString
		var jGen int64
		err := tx.QueryRowContext(ctx, `SELECT run_id, invocation_id, operation_id, runner_id, generation, state FROM runner_jobs WHERE id = ?`,
			j.JobID.String()).Scan(&jRun, &jInv, &jOp, &jRunner, &jGen, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return engine.TurnLease{}, invalidProof("job %s does not exist", j.JobID)
		}
		if err != nil {
			return engine.TurnLease{}, fmt.Errorf("recovering turn lease %s: job %s: %w", old.Key, j.JobID, err)
		}
		if jRun != runID || jInv != invocationID || !turnOperationMatches(jOp, opID) {
			return engine.TurnLease{}, invalidProof("job %s belongs to another operation (%s)", j.JobID, jOp)
		}
		live := (state == "offered" || state == "running") && jRunner.String == j.RunnerID && jGen == j.Generation
		if p.Adopted && !live {
			return engine.TurnLease{}, invalidProof("job %s is not live at generation %d", j.JobID, j.Generation)
		}
		if !p.Adopted && live {
			return engine.TurnLease{}, invalidProof("job %s is still live; fence it before replay", j.JobID)
		}
	}
	if !p.Adopted {
		rows, err := tx.QueryContext(ctx, `SELECT operation_id FROM runner_jobs WHERE run_id = ? AND invocation_id = ? AND state IN ('offered', 'running')`,
			runID, invocationID)
		if err != nil {
			return engine.TurnLease{}, fmt.Errorf("recovering turn lease %s: live jobs: %w", old.Key, err)
		}
		var liveOp string
		for rows.Next() {
			var op string
			if err := rows.Scan(&op); err != nil {
				rows.Close()
				return engine.TurnLease{}, err
			}
			if turnOperationMatches(op, opID) {
				liveOp = op
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return engine.TurnLease{}, err
		}
		if err := rows.Close(); err != nil {
			return engine.TurnLease{}, err
		}
		if liveOp != "" {
			return engine.TurnLease{}, invalidProof("operation %s still has a live job", liveOp)
		}
	}
	if p.Resource != nil {
		var active int
		var rGen int64
		var rTok, owner string
		err := tx.QueryRowContext(ctx, `SELECT active, generation, token, owner FROM workspace_leases WHERE resource_key = ?`, p.Resource.Key).
			Scan(&active, &rGen, &rTok, &owner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return engine.TurnLease{}, fmt.Errorf("recovering turn lease %s: resource: %w", old.Key, err)
		}
		held := err == nil && active == 1 && rGen == p.Resource.Generation && rTok == p.Resource.Token.String() && owner == p.Resource.Owner
		if p.Adopted && !held {
			return engine.TurnLease{}, invalidProof("resource %s is no longer held by the adopted job", p.Resource.Key)
		}
		if !p.Adopted && held {
			return engine.TurnLease{}, invalidProof("resource %s is still owned; supersede it before replay", p.Resource.Key)
		}
	}

	next := gen
	if !p.Adopted {
		next = gen + 1
	}
	token := uuid.New()
	exp := r.now().UTC().Add(ttl)
	res, err := tx.ExecContext(ctx, `UPDATE turn_leases SET token = ?, generation = ?, expires_at = ?, holder = ?, boot_id = ?
WHERE key = ? AND token = ? AND generation = ?`, token.String(), next, exp.UnixMilli(), r.holder, r.bootID, old.Key, old.Token.String(), old.Generation)
	if err != nil {
		return engine.TurnLease{}, fmt.Errorf("recovering turn lease %s: %w", old.Key, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return engine.TurnLease{}, engine.ErrTurnLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return engine.TurnLease{}, fmt.Errorf("recovering turn lease %s: commit: %w", old.Key, err)
	}
	return engine.TurnLease{Key: old.Key, Token: token, Generation: next, ExpiresAt: time.UnixMilli(exp.UnixMilli()).UTC()}, nil
}
