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

// Compile-time: satisfies the engine's durable invocation port.
var _ engine.InvocationStore = (*InvocationRepo)(nil)

// InvocationRepo persists node-visit invocations (migration 040). Hand-written
// SQL, modeled on AuditRepo.
type InvocationRepo struct {
	db  *sql.DB
	now func() time.Time
}

// NewInvocationRepo creates an InvocationRepo over db.
func NewInvocationRepo(db *sql.DB) *InvocationRepo {
	return &InvocationRepo{db: db, now: time.Now}
}

const invocationSelect = `SELECT id, run_id, node_id, visit, created_at, committed_at FROM node_invocations`

// Begin returns the open invocation for (runID,nodeID) or creates visit+1.
// Read and insert share one transaction; the partial unique index rejects a
// concurrent second open row.
func (r *InvocationRepo) Begin(ctx context.Context, runID uuid.UUID, nodeID string) (engine.Invocation, error) {
	if runID == uuid.Nil || strings.TrimSpace(nodeID) == "" {
		return engine.Invocation{}, errors.New("invocation repo: run and node are required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return engine.Invocation{}, fmt.Errorf("beginning invocation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	open, err := scanInvocation(tx.QueryRowContext(ctx,
		invocationSelect+` WHERE run_id = ? AND node_id = ? AND committed_at IS NULL`,
		uuidToString(runID), nodeID))
	switch {
	case err == nil:
		if err := tx.Commit(); err != nil {
			return engine.Invocation{}, fmt.Errorf("committing invocation read: %w", err)
		}
		return open, nil
	case !errors.Is(err, sql.ErrNoRows):
		return engine.Invocation{}, fmt.Errorf("reading open invocation: %w", err)
	}

	var maxVisit int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(visit), 0) FROM node_invocations WHERE run_id = ? AND node_id = ?`,
		uuidToString(runID), nodeID).Scan(&maxVisit); err != nil {
		return engine.Invocation{}, fmt.Errorf("reading last visit: %w", err)
	}
	inv := engine.Invocation{
		ID: uuid.New(), RunID: runID, NodeID: nodeID, Visit: maxVisit + 1,
		CreatedAt: r.now().UTC().Truncate(time.Second),
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO node_invocations (id, run_id, node_id, visit, created_at) VALUES (?, ?, ?, ?, ?)`,
		uuidToString(inv.ID), uuidToString(runID), nodeID, inv.Visit, timeToString(inv.CreatedAt)); err != nil {
		return engine.Invocation{}, fmt.Errorf("inserting invocation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return engine.Invocation{}, fmt.Errorf("committing invocation: %w", err)
	}
	return inv, nil
}

// Commit marks the invocation committed; committing twice is a no-op.
func (r *InvocationRepo) Commit(ctx context.Context, id uuid.UUID) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE node_invocations SET committed_at = ? WHERE id = ? AND committed_at IS NULL`,
		timeToString(r.now()), uuidToString(id))
	if err != nil {
		return fmt.Errorf("committing invocation: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		return nil
	}
	_, err = r.Get(ctx, id) // already committed => nil; unknown => ErrInvocationNotFound
	return err
}

// Get loads one invocation.
func (r *InvocationRepo) Get(ctx context.Context, id uuid.UUID) (engine.Invocation, error) {
	inv, err := scanInvocation(r.db.QueryRowContext(ctx, invocationSelect+` WHERE id = ?`, uuidToString(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return engine.Invocation{}, fmt.Errorf("%w: %s", engine.ErrInvocationNotFound, id)
	}
	if err != nil {
		return engine.Invocation{}, fmt.Errorf("reading invocation: %w", err)
	}
	return inv, nil
}

func scanInvocation(s rowScanner) (engine.Invocation, error) {
	var idStr, runStr, nodeID, createdStr string
	var visit int
	var committed sql.NullString
	if err := s.Scan(&idStr, &runStr, &nodeID, &visit, &createdStr, &committed); err != nil {
		return engine.Invocation{}, err
	}
	id, err := parseUUID(idStr)
	if err != nil {
		return engine.Invocation{}, err
	}
	run, err := parseUUID(runStr)
	if err != nil {
		return engine.Invocation{}, err
	}
	created, err := parseTime(createdStr)
	if err != nil {
		return engine.Invocation{}, err
	}
	committedAt, err := nullStringToTimePtr(committed)
	if err != nil {
		return engine.Invocation{}, err
	}
	return engine.Invocation{ID: id, RunID: run, NodeID: nodeID, Visit: visit, CreatedAt: created, CommittedAt: committedAt}, nil
}
