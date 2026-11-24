package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// ResourceLeaseRepo implements runner.ResourceLeaseStore over 044
// workspace_leases (current claim) and workspace_lease_holders (durable
// operation association per generation). Every check and write runs in one
// transaction on the shared single-connection DB.
type ResourceLeaseRepo struct {
	db     *sql.DB
	bootID string
	now    func() time.Time
}

// NewResourceLeaseRepo builds the repo. bootID identifies this server process
// lifetime (P3 T13 passes one uuid.NewString() created at startup); server-side
// claims record it so a restart can prove their holder is gone.
func NewResourceLeaseRepo(db *sql.DB, bootID string) *ResourceLeaseRepo {
	return &ResourceLeaseRepo{db: db, bootID: bootID, now: func() time.Time { return time.Now().UTC() }}
}

type leaseRow struct {
	ref    engine.ResourceFenceRef
	holder string
	active bool
}

func readLease(ctx context.Context, q groupQueryer, key string) (leaseRow, bool, error) {
	var lr leaseRow
	var token string
	var active int
	err := q.QueryRowContext(ctx, `SELECT holder, owner, generation, token, active FROM workspace_leases WHERE resource_key = ?`, key).
		Scan(&lr.holder, &lr.ref.Owner, &lr.ref.Generation, &token, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return leaseRow{}, false, nil
	}
	if err != nil {
		return leaseRow{}, false, fmt.Errorf("resource lease: read %s: %w", key, err)
	}
	if lr.ref.Token, err = parseUUID(token); err != nil {
		return leaseRow{}, false, err
	}
	lr.ref.Key, lr.active = key, active == 1
	return lr, true, nil
}

// readHolder loads the durable association of one claimed generation.
func readHolder(ctx context.Context, q groupQueryer, f engine.ResourceFenceRef) (runner.LeaseRecord, bool, error) {
	rec := runner.LeaseRecord{Fence: f}
	var jobID, jobRunner sql.NullString
	var jobGen sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT holder, job_id, job_runner_id, job_generation, boot_id FROM workspace_lease_holders
		WHERE resource_key = ? AND generation = ? AND token = ?`, f.Key, f.Generation, f.Token.String()).
		Scan(&rec.Holder, &jobID, &jobRunner, &jobGen, &rec.BootID)
	if errors.Is(err, sql.ErrNoRows) {
		return runner.LeaseRecord{}, false, nil
	}
	if err != nil {
		return runner.LeaseRecord{}, false, fmt.Errorf("resource lease: read holder: %w", err)
	}
	if jobID.Valid {
		id, err := parseUUID(jobID.String)
		if err != nil {
			return runner.LeaseRecord{}, false, err
		}
		rec.Job = &runner.Fence{JobID: id, RunnerID: runner.RunnerID(jobRunner.String), Generation: jobGen.Int64}
	}
	return rec, true, nil
}

// checkResourceFenceTx is THE resource fence: f must be the exact active claim
// (key, owner, generation, token). Committers (P3 T2/T5/T7/T13, P4 T6) call it
// with their own *sql.Tx, never the *sql.DB, so a single-connection DB cannot
// deadlock.
func checkResourceFenceTx(ctx context.Context, q groupQueryer, f engine.ResourceFenceRef) error {
	if err := f.Validate(); err != nil {
		return fmt.Errorf("%w: %v", runner.ErrResourceFenced, err)
	}
	lr, ok, err := readLease(ctx, q, f.Key)
	if err != nil {
		return err
	}
	if !ok || !lr.active || lr.ref != f {
		return fmt.Errorf("%w: %s generation %d is not the active claim", runner.ErrResourceFenced, f.Key, f.Generation)
	}
	return nil
}

// releaseResourceTx releases exactly f inside the caller's transaction; a stale
// or already released ref changes nothing and returns ErrResourceFenced.
func releaseResourceTx(ctx context.Context, q groupQueryer, f engine.ResourceFenceRef) error {
	if err := f.Validate(); err != nil {
		return fmt.Errorf("%w: %v", runner.ErrResourceFenced, err)
	}
	res, err := q.ExecContext(ctx, `UPDATE workspace_leases SET active = 0
		WHERE resource_key = ? AND owner = ? AND generation = ? AND token = ? AND active = 1`,
		f.Key, f.Owner, f.Generation, f.Token.String())
	if err != nil {
		return fmt.Errorf("resource lease: release: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%w: %s generation %d is not the active claim", runner.ErrResourceFenced, f.Key, f.Generation)
	}
	return nil
}

// grant writes a new active claim (generation+1, fresh token) and its holder
// row. The caller has verified the resource is free.
func (r *ResourceLeaseRepo) grant(ctx context.Context, tx *sql.Tx, key, holder, owner string, prevGen int64, job *runner.Fence, bootID string) (engine.ResourceFenceRef, error) {
	ref := engine.ResourceFenceRef{Key: key, Owner: owner, Generation: prevGen + 1, Token: uuid.New()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_leases(resource_key, holder, owner, generation, token, active) VALUES (?, ?, ?, ?, ?, 1)
		ON CONFLICT(resource_key) DO UPDATE SET holder = excluded.holder, owner = excluded.owner,
		generation = excluded.generation, token = excluded.token, active = 1`,
		key, holder, owner, ref.Generation, ref.Token.String()); err != nil {
		return engine.ResourceFenceRef{}, fmt.Errorf("resource lease: claim %s: %w", key, err)
	}
	var jobID, jobRunner any
	var jobGen any
	if job != nil {
		jobID, jobRunner, jobGen = uuidToString(job.JobID), string(job.RunnerID), job.Generation
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_lease_holders(resource_key, generation, token, holder, owner,
		job_id, job_runner_id, job_generation, boot_id, claimed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key, ref.Generation, ref.Token.String(), holder, owner, jobID, jobRunner, jobGen, bootID, timeToString(r.now())); err != nil {
		return engine.ResourceFenceRef{}, fmt.Errorf("resource lease: record holder of %s: %w", key, err)
	}
	return ref, nil
}

// lockLease starts a transaction whose first statement takes the SQLite write
// lock, so the following reads cannot race another writer.
func (r *ResourceLeaseRepo) lockLease(ctx context.Context, key string) (*sql.Tx, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("resource lease: begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_leases SET active = active WHERE resource_key = ?`, key); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("resource lease: lock: %w", err)
	}
	return tx, nil
}

func (r *ResourceLeaseRepo) Claim(ctx context.Context, key, holder string, owner runner.RunnerID) (engine.ResourceFenceRef, error) {
	if key == "" || holder == "" || owner == "" || r.bootID == "" {
		return engine.ResourceFenceRef{}, errors.New("resource lease: claim needs key, holder, owner and a boot ID")
	}
	tx, err := r.lockLease(ctx, key)
	if err != nil {
		return engine.ResourceFenceRef{}, err
	}
	defer func() { _ = tx.Rollback() }()
	cur, ok, err := readLease(ctx, tx, key)
	if err != nil {
		return engine.ResourceFenceRef{}, err
	}
	if ok && cur.active {
		return engine.ResourceFenceRef{}, fmt.Errorf("%w: %s is held by %q", runner.ErrResourceBusy, key, cur.holder)
	}
	ref, err := r.grant(ctx, tx, key, holder, string(owner), cur.ref.Generation, nil, r.bootID)
	if err != nil {
		return engine.ResourceFenceRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.ResourceFenceRef{}, fmt.Errorf("resource lease: commit: %w", err)
	}
	return ref, nil
}

func (r *ResourceLeaseRepo) ClaimJob(ctx context.Context, key, holder string, job runner.Fence) (engine.ResourceFenceRef, error) {
	if key == "" || holder == "" || job.JobID == uuid.Nil || job.RunnerID == "" || job.Generation < 1 {
		return engine.ResourceFenceRef{}, fmt.Errorf("%w: claim needs key, holder and an exact job fence", runner.ErrFenced)
	}
	tx, err := r.lockLease(ctx, key)
	if err != nil {
		return engine.ResourceFenceRef{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fenceErr(ctx, tx, job); err != nil {
		if errors.Is(err, runner.ErrJobNotFound) {
			return engine.ResourceFenceRef{}, fmt.Errorf("%w: %w", runner.ErrFenced, err)
		}
		return engine.ResourceFenceRef{}, err
	}
	cur, ok, err := readLease(ctx, tx, key)
	if err != nil {
		return engine.ResourceFenceRef{}, err
	}
	if ok && cur.active {
		rec, found, err := readHolder(ctx, tx, cur.ref)
		if err != nil {
			return engine.ResourceFenceRef{}, err
		}
		if found && rec.Job != nil && *rec.Job == job && rec.Holder == holder {
			return cur.ref, tx.Commit() // the same exact job re-claiming: adopt its claim
		}
		return engine.ResourceFenceRef{}, fmt.Errorf("%w: %s is held by %q", runner.ErrResourceBusy, key, cur.holder)
	}
	ref, err := r.grant(ctx, tx, key, holder, string(job.RunnerID), cur.ref.Generation, &job, "")
	if err != nil {
		return engine.ResourceFenceRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.ResourceFenceRef{}, fmt.Errorf("resource lease: commit: %w", err)
	}
	return ref, nil
}

func (r *ResourceLeaseRepo) Check(ctx context.Context, f engine.ResourceFenceRef) error {
	return checkResourceFenceTx(ctx, r.db, f)
}

func (r *ResourceLeaseRepo) Active(ctx context.Context, key string) (runner.LeaseRecord, bool, error) {
	cur, ok, err := readLease(ctx, r.db, key)
	if err != nil || !ok || !cur.active {
		return runner.LeaseRecord{}, false, err
	}
	rec, found, err := readHolder(ctx, r.db, cur.ref)
	if err != nil {
		return runner.LeaseRecord{}, false, err
	}
	if !found {
		return runner.LeaseRecord{}, false, fmt.Errorf("resource lease: active claim %s generation %d has no holder record", key, cur.ref.Generation)
	}
	return rec, true, nil
}

func (r *ResourceLeaseRepo) Release(ctx context.Context, f engine.ResourceFenceRef) error {
	tx, err := r.lockLease(ctx, f.Key)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := releaseResourceTx(ctx, tx, f); err != nil {
		return err
	}
	return tx.Commit()
}
