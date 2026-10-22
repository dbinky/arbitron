package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// LayoutRepo is the durable authority for immutable layout inputs and results.
// Generation and destination checks execute in SQL, including across processes.
type LayoutRepo struct{ db *sql.DB }

func NewLayoutRepo(db *sql.DB) *LayoutRepo { return &LayoutRepo{db: db} }

var _ layout.Store = (*LayoutRepo)(nil)
var _ workflow.DisplayedLayoutReader = (*LayoutRepo)(nil)

// DisplayedLayout supplies retained positions from the referenced best, never
// from a newer but inferior attempt or a mutable filesystem/cache source.
func (r *LayoutRepo) DisplayedLayout(ctx context.Context, revision string) (workflow.RetainedLayout, bool, error) {
	row, err := r.Get(ctx, revision)
	if errors.Is(err, layout.ErrRecordNotFound) {
		return workflow.RetainedLayout{}, false, nil
	}
	if err != nil {
		return workflow.RetainedLayout{}, false, err
	}
	if row.ResultGeneration == 0 {
		return workflow.RetainedLayout{}, false, nil
	}
	result, err := r.Result(ctx, revision, row.ResultGeneration)
	if err != nil {
		return workflow.RetainedLayout{}, false, layoutError(err)
	}
	var wf workflow.Workflow
	if err = json.Unmarshal(row.Manifest, &wf); err != nil {
		return workflow.RetainedLayout{}, false, layoutError(err)
	}
	graph, err := layout.BuildGraph(&wf)
	if err != nil {
		return workflow.RetainedLayout{}, false, layoutError(err)
	}
	if !layout.Compatible(result.Geometry, graph) {
		return workflow.RetainedLayout{}, false, layoutError(fmt.Errorf("stored geometry is incompatible with captured manifest"))
	}
	retained := workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: revision, FromGeneration: row.ResultGeneration, Positions: make(map[string]workflow.NodePosition, len(result.Geometry.Nodes))}
	for _, node := range result.Geometry.Nodes {
		retained.Positions[node.ID] = workflow.NodePosition{X: node.Box.X, Y: node.Box.Y}
	}
	return retained, true, nil
}

const layoutColumns = `revision,workflow_id,scope_kind,owner_id,workflow_name,reason,input_manifest,retained,state,stage,job_generation,attempts_used,attempt_limit,verdict,findings,unreviewed,failure_stage,failure_reason,result_generation,cache_generation,lease_owner,lease_expires_at,requested_at,updated_at,started_at,finished_at`

type layoutScanner interface{ Scan(...any) error }
type layoutQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func layoutError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, layout.ErrStoreCorrupt) {
		return err
	}
	return fmt.Errorf("%w: %v", layout.ErrStoreUnavailable, err)
}
func layoutTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(0, v.Int64).UTC()
	return &t
}
func layoutLocator(d layout.Destination) (string, string, string) {
	owner := ""
	if d.Scope.Kind != workflow.ScopeShared {
		owner = d.Scope.OwnerID.String()
	}
	return workflow.ScopeLabel(d.Scope), owner, d.Name
}
func scanLayout(row layoutScanner) (layout.Record, error) {
	var r layout.Record
	var wid sql.NullString
	var scope, owner string
	var retained, findings []byte
	var requested, updated int64
	var lease, started, finished sql.NullInt64
	err := row.Scan(&r.Revision, &wid, &scope, &owner, &r.Destination.Name, &r.Reason, &r.Manifest, &retained, &r.State, &r.Stage, &r.JobGeneration, &r.AttemptsUsed, &r.AttemptLimit, &r.Verdict, &findings, &r.Unreviewed, &r.FailureStage, &r.FailureReason, &r.ResultGeneration, &r.CacheGeneration, &r.LeaseOwner, &lease, &requested, &updated, &started, &finished)
	if err != nil {
		return r, err
	}
	r.Destination.Scope = workflow.Shared()
	if scope == workflow.ScopeLabelPrivate {
		id, err := uuid.Parse(owner)
		if err != nil {
			return r, fmt.Errorf("%w: owner_id: %v", layout.ErrStoreCorrupt, err)
		}
		r.Destination.Scope = workflow.Private(id)
	}
	if wid.Valid {
		id, err := uuid.Parse(wid.String)
		if err != nil {
			return r, fmt.Errorf("%w: workflow_id: %v", layout.ErrStoreCorrupt, err)
		}
		r.Destination.WorkflowID = id
	}
	if err = json.Unmarshal(retained, &r.Retained); err != nil {
		return r, fmt.Errorf("%w: retained: %v", layout.ErrStoreCorrupt, err)
	}
	if err = json.Unmarshal(findings, &r.Findings); err != nil {
		return r, fmt.Errorf("%w: findings: %v", layout.ErrStoreCorrupt, err)
	}
	r.RequestedAt = time.Unix(0, requested).UTC()
	r.UpdatedAt = time.Unix(0, updated).UTC()
	r.LeaseExpiresAt = layoutTime(lease)
	r.StartedAt = layoutTime(started)
	r.FinishedAt = layoutTime(finished)
	return r, nil
}
func layoutMiss(ctx context.Context, q layoutQuerier, rev string, otherwise error) error {
	var exists int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM layout_revisions WHERE revision=?`, rev).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return layout.ErrRecordNotFound
	}
	if err != nil {
		return layoutError(err)
	}
	return otherwise
}
func (r *LayoutRepo) Request(ctx context.Context, req layout.NewRequest) (layout.Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return layout.Record{}, false, err
	}
	if req.Revision == "" || len(req.Manifest) == 0 || req.AttemptLimit < 1 {
		return layout.Record{}, false, fmt.Errorf("layout: invalid stored request")
	}
	retained, err := json.Marshal(req.Retained)
	if err != nil {
		return layout.Record{}, false, layoutError(err)
	}
	scope, owner, name := layoutLocator(req.Destination)
	row, err := scanLayout(r.db.QueryRowContext(ctx, `INSERT INTO layout_revisions (
        revision,workflow_id,scope_kind,owner_id,workflow_name,reason,
        input_manifest,retained,attempt_limit,requested_at,updated_at
    ) VALUES(?,?,?,?,?,?,?,?,?,?,?)
    ON CONFLICT(revision) DO NOTHING RETURNING `+layoutColumns, req.Revision, ownerToNullString(req.Destination.WorkflowID), scope, owner, name, req.Reason, req.Manifest, string(retained), req.AttemptLimit, req.RequestedAt.UnixNano(), req.RequestedAt.UnixNano()))
	if errors.Is(err, sql.ErrNoRows) {
		row, err = r.Get(ctx, req.Revision)
		return row, false, err
	}
	return row, err == nil, layoutError(err)
}
func (r *LayoutRepo) Get(ctx context.Context, rev string) (layout.Record, error) {
	row, err := scanLayout(r.db.QueryRowContext(ctx, `SELECT `+layoutColumns+` FROM layout_revisions WHERE revision=?`, rev))
	if errors.Is(err, sql.ErrNoRows) {
		return row, layout.ErrRecordNotFound
	}
	return row, layoutError(err)
}
func (r *LayoutRepo) list(ctx context.Context, where string, args ...any) ([]layout.Record, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+layoutColumns+` FROM layout_revisions `+where, args...)
	if err != nil {
		return nil, layoutError(err)
	}
	defer rows.Close()
	out := []layout.Record{}
	for rows.Next() {
		row, err := scanLayout(rows)
		if err != nil {
			return nil, layoutError(err)
		}
		out = append(out, row)
	}
	return out, layoutError(rows.Err())
}
func (r *LayoutRepo) ListActive(ctx context.Context, d layout.Destination) ([]layout.Record, error) {
	scope, owner, name := layoutLocator(d)
	return r.list(ctx, `WHERE scope_kind=? AND owner_id=? AND workflow_name=? AND state IN ('pending','in_progress') ORDER BY requested_at,revision`, scope, owner, name)
}
func layoutLimit(n int) int {
	if n <= 0 {
		return -1
	}
	return n
}
func (r *LayoutRepo) Claimable(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT revision FROM layout_revisions WHERE state='pending' OR (state='in_progress' AND lease_expires_at < ?) ORDER BY requested_at,revision LIMIT ?`, now.UnixNano(), layoutLimit(limit))
	if err != nil {
		return nil, layoutError(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var rev string
		if err = rows.Scan(&rev); err != nil {
			return nil, layoutError(err)
		}
		out = append(out, rev)
	}
	return out, layoutError(rows.Err())
}
func (r *LayoutRepo) Claim(ctx context.Context, rev, owner string, now, until time.Time) (layout.Record, error) {
	row, err := scanLayout(r.db.QueryRowContext(ctx, `UPDATE layout_revisions SET
        job_generation=job_generation+CASE WHEN state='in_progress' THEN 1 ELSE 0 END,
        attempts_used=CASE WHEN state='in_progress' THEN 0 ELSE attempts_used END,
        state='in_progress',stage='arranging',lease_owner=?,lease_expires_at=?,started_at=?,updated_at=?
    WHERE revision=? AND (state='pending' OR (state='in_progress' AND lease_expires_at < ?))
    RETURNING `+layoutColumns, owner, until.UnixNano(), now.UnixNano(), now.UnixNano(), rev, now.UnixNano()))
	if errors.Is(err, sql.ErrNoRows) {
		return row, layoutMiss(ctx, r.db, rev, layout.ErrNotClaimable)
	}
	return row, layoutError(err)
}
func layoutAffected(ctx context.Context, q layoutQuerier, rev string, res sql.Result, err error, miss error) error {
	if err != nil {
		return layoutError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return layoutError(err)
	}
	if n == 0 {
		return layoutMiss(ctx, q, rev, miss)
	}
	return nil
}
func (r *LayoutRepo) Progress(ctx context.Context, f layout.Fence, stage layout.Stage, attempt int, now, until time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE layout_revisions SET stage=?,attempts_used=?,updated_at=?,lease_expires_at=? WHERE revision=? AND job_generation=? AND state='in_progress'`, stage, attempt, now.UnixNano(), until.UnixNano(), f.Revision, f.Generation)
	return layoutAffected(ctx, r.db, f.Revision, res, err, layout.ErrStaleGeneration)
}

const layoutResultColumns = `revision,generation,geometry,review,preflight,score,verdict,created_at`

func scanLayoutResult(row layoutScanner) (layout.StoredResult, error) {
	var r layout.StoredResult
	var geo, review, preflight, score []byte
	var created int64
	err := row.Scan(&r.Revision, &r.Generation, &geo, &review, &preflight, &score, &r.Verdict, &created)
	if err != nil {
		return r, err
	}
	for _, v := range []struct {
		name   string
		data   []byte
		target any
	}{{"geometry", geo, &r.Geometry}, {"review", review, &r.Review}, {"preflight", preflight, &r.Preflight}, {"score", score, &r.Score}} {
		if err = json.Unmarshal(v.data, v.target); err != nil {
			return r, fmt.Errorf("%w: %s: %v", layout.ErrStoreCorrupt, v.name, err)
		}
	}
	r.CreatedAt = time.Unix(0, created).UTC()
	return r, nil
}
func (r *LayoutRepo) Result(ctx context.Context, rev string, gen int64) (layout.StoredResult, error) {
	result, err := scanLayoutResult(r.db.QueryRowContext(ctx, `SELECT `+layoutResultColumns+` FROM layout_results WHERE revision=? AND generation=?`, rev, gen))
	if errors.Is(err, sql.ErrNoRows) {
		return result, layout.ErrRecordNotFound
	}
	return result, layoutError(err)
}
func insertLayoutResult(ctx context.Context, tx *sql.Tx, f layout.Fence, result layout.StoredResult) error {
	if result.Revision != f.Revision || result.Generation != f.Generation {
		return fmt.Errorf("layout: result does not match fence")
	}
	var data [4]string // geometry, review, preflight, score, in SQL column order
	for i, v := range []any{result.Geometry, result.Review, result.Preflight, result.Score} {
		b, err := json.Marshal(v)
		if err != nil {
			return layoutError(err)
		}
		data[i] = string(b)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO layout_results(`+layoutResultColumns+`) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(revision,generation) DO NOTHING`, f.Revision, f.Generation, data[0], data[1], data[2], data[3], result.Verdict, result.CreatedAt.UnixNano())
	if err != nil {
		return layoutError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return layoutError(err)
	}
	if n > 0 {
		return nil
	}
	var existing [4]string
	var verdict layout.Verdict
	var created int64
	err = tx.QueryRowContext(ctx, `SELECT geometry,review,preflight,score,verdict,created_at FROM layout_results WHERE revision=? AND generation=?`, f.Revision, f.Generation).Scan(&existing[0], &existing[1], &existing[2], &existing[3], &verdict, &created)
	if err != nil {
		return layoutError(err)
	}
	if existing != data || verdict != result.Verdict || created != result.CreatedAt.UnixNano() {
		return fmt.Errorf("layout: conflicting immutable result")
	}
	return nil
}

// persist holds the write transaction from the conditional fence through result
// insertion and best-pointer settlement. Any error rolls back all three writes.
func (r *LayoutRepo) persist(ctx context.Context, f layout.Fence, result *layout.StoredResult, replace bool, c *layout.Completion, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return layoutError(err)
	}
	defer tx.Rollback()
	states := `state='in_progress'`
	if c != nil {
		states = `state IN ('in_progress','superseded')`
	}
	_, err = scanLayout(tx.QueryRowContext(ctx, `UPDATE layout_revisions SET updated_at=? WHERE revision=? AND job_generation=? AND `+states+` RETURNING `+layoutColumns, now.UnixNano(), f.Revision, f.Generation))
	if errors.Is(err, sql.ErrNoRows) {
		return layoutMiss(ctx, tx, f.Revision, layout.ErrStaleGeneration)
	}
	if err != nil {
		return layoutError(err)
	}
	if c != nil && !c.State.Terminal() {
		return fmt.Errorf("layout: completion requires a terminal state")
	}
	if result != nil {
		if err = insertLayoutResult(ctx, tx, f, *result); err != nil {
			return err
		}
		if replace {
			findings, err := json.Marshal(result.UnresolvedFindings())
			if err != nil {
				return layoutError(err)
			}
			_, err = tx.ExecContext(ctx, `UPDATE layout_revisions SET result_generation=?,verdict=?,findings=?,unreviewed=?,cache_generation=0 WHERE revision=?`, result.Generation, result.Verdict, string(findings), result.IsUnreviewed(), f.Revision)
			if err != nil {
				return layoutError(err)
			}
		}
	}
	if c != nil {
		_, err = tx.ExecContext(ctx, `UPDATE layout_revisions SET
            state=CASE WHEN state='superseded' THEN state ELSE ? END,
            stage='done',attempts_used=?,failure_stage=?,failure_reason=?,
            lease_owner='',lease_expires_at=NULL,finished_at=?
        WHERE revision=?`, c.State, c.AttemptsUsed, c.FailureStage, c.FailureReason, now.UnixNano(), f.Revision)
		if err != nil {
			return layoutError(err)
		}
	}
	return layoutError(tx.Commit())
}
func (r *LayoutRepo) Complete(ctx context.Context, f layout.Fence, c layout.Completion, now time.Time) error {
	return r.persist(ctx, f, c.Result, c.ReplaceBest, &c, now)
}
func (r *LayoutRepo) Checkpoint(ctx context.Context, f layout.Fence, result layout.StoredResult, replace bool, now time.Time) error {
	return r.persist(ctx, f, &result, replace, nil, now)
}
func (r *LayoutRepo) Supersede(ctx context.Context, rev string, now time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE layout_revisions SET state='superseded',updated_at=? WHERE revision=? AND state IN ('pending','in_progress')`, now.UnixNano(), rev)
	return layoutAffected(ctx, r.db, rev, res, err, nil)
}
func (r *LayoutRepo) Retry(ctx context.Context, rev string, limit int, now time.Time) (layout.Record, error) {
	row, err := scanLayout(r.db.QueryRowContext(ctx, `UPDATE layout_revisions SET
        job_generation=job_generation+1,state='pending',stage='queued',attempts_used=0,
        attempt_limit=?,failure_stage='',failure_reason='',lease_owner='',lease_expires_at=NULL,
        started_at=NULL,finished_at=NULL,cache_generation=0,updated_at=?
    WHERE revision=? AND (state IN ('needs_attention','failed') OR (state='superseded' AND verdict!='passed'))
    RETURNING `+layoutColumns, limit, now.UnixNano(), rev))
	if errors.Is(err, sql.ErrNoRows) {
		return row, layoutMiss(ctx, r.db, rev, layout.ErrRetryConflict)
	}
	return row, layoutError(err)
}
func (r *LayoutRepo) Relocate(ctx context.Context, from, to layout.Destination, now time.Time) (int64, error) {
	fs, fo, fn := layoutLocator(from)
	ts, toOwner, tn := layoutLocator(to)
	wid := ownerToNullString(to.WorkflowID)
	res, err := r.db.ExecContext(ctx, `UPDATE layout_revisions SET
        scope_kind=?,owner_id=?,workflow_name=?,workflow_id=COALESCE(?,workflow_id),
        cache_generation=0,updated_at=?
    WHERE scope_kind=? AND owner_id=? AND workflow_name=?
        AND (scope_kind!=? OR owner_id!=? OR workflow_name!=? OR (? IS NOT NULL AND workflow_id IS NOT ?))`, ts, toOwner, tn, wid, now.UnixNano(), fs, fo, fn, ts, toOwner, tn, wid, wid)
	if err != nil {
		return 0, layoutError(err)
	}
	n, err := res.RowsAffected()
	return n, layoutError(err)
}
func (r *LayoutRepo) PendingCache(ctx context.Context, limit int) ([]layout.Record, error) {
	return r.list(ctx, `WHERE result_generation>0 AND cache_generation!=result_generation ORDER BY updated_at,revision LIMIT ?`, layoutLimit(limit))
}
func (r *LayoutRepo) MarkCached(ctx context.Context, f layout.Fence, gen int64, dest layout.Destination) error {
	scope, owner, name := layoutLocator(dest)
	res, err := r.db.ExecContext(ctx, `UPDATE layout_revisions SET cache_generation=?
    WHERE revision=? AND job_generation=? AND result_generation=? AND ?>0
        AND scope_kind=? AND owner_id=? AND workflow_name=? AND workflow_id IS ?`, gen, f.Revision, f.Generation, gen, gen, scope, owner, name, ownerToNullString(dest.WorkflowID))
	return layoutAffected(ctx, r.db, f.Revision, res, err, layout.ErrStaleGeneration)
}
func (r *LayoutRepo) InvalidateResult(ctx context.Context, f layout.Fence, gen int64, reason string, now time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE layout_revisions SET state='needs_attention',failure_reason=?,updated_at=?
    WHERE revision=? AND job_generation=? AND result_generation=?
        AND (state='passed' OR (state='superseded' AND verdict='passed'))`, reason, now.UnixNano(), f.Revision, f.Generation, gen)
	return layoutAffected(ctx, r.db, f.Revision, res, err, layout.ErrStaleGeneration)
}
