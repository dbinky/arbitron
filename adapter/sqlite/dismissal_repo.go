package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// DismissalRepo implements instance.DismissalRepository backed by SQLite,
// following the hand-written-SQL conventions of NotificationRepo.
type DismissalRepo struct {
	db *sql.DB
}

// NewDismissalRepo creates a DismissalRepo using the given database connection.
func NewDismissalRepo(db *sql.DB) *DismissalRepo {
	return &DismissalRepo{db: db}
}

// Dismiss is idempotent: ON CONFLICT DO NOTHING means dismissing an
// already-dismissed instance succeeds without changing dismissed_at.
func (r *DismissalRepo) Dismiss(ctx context.Context, userID, instanceID uuid.UUID) error {
	query := `INSERT INTO instance_dismissals (user_id, instance_id)
VALUES (?, ?)
ON CONFLICT(user_id, instance_id) DO NOTHING`
	_, err := r.db.ExecContext(ctx, query, uuidToString(userID), uuidToString(instanceID))
	if err != nil {
		return fmt.Errorf("dismissing instance: %w", err)
	}
	return nil
}

// DismissMany records all instanceIDs as dismissed by userID in one
// transaction: either every row is written or none are, so there is no
// partial-commit state. Each row is idempotent (ON CONFLICT DO NOTHING). An
// empty slice is a no-op success.
//
// SECURITY: instanceIDs must be derived server-side by the caller (never from
// client input), or this becomes a write-IDOR.
func (r *DismissalRepo) DismissMany(ctx context.Context, userID uuid.UUID, instanceIDs []uuid.UUID) error {
	if len(instanceIDs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning dismiss-many transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO instance_dismissals (user_id, instance_id)
VALUES (?, ?)
ON CONFLICT(user_id, instance_id) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("preparing dismiss-many statement: %w", err)
	}
	defer stmt.Close()

	uid := uuidToString(userID)
	for _, id := range instanceIDs {
		if _, err := stmt.ExecContext(ctx, uid, uuidToString(id)); err != nil {
			return fmt.Errorf("dismissing instance %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing dismiss-many transaction: %w", err)
	}
	return nil
}

// Restore removes the dismissal; deleting zero rows (not dismissed) is a no-op
// success, matching the idempotent semantics in the design.
func (r *DismissalRepo) Restore(ctx context.Context, userID, instanceID uuid.UUID) error {
	query := `DELETE FROM instance_dismissals WHERE user_id = ? AND instance_id = ?`
	_, err := r.db.ExecContext(ctx, query, uuidToString(userID), uuidToString(instanceID))
	if err != nil {
		return fmt.Errorf("restoring instance: %w", err)
	}
	return nil
}

// DismissedIDs returns the set of instance IDs userID has dismissed.
func (r *DismissalRepo) DismissedIDs(ctx context.Context, userID uuid.UUID) (map[uuid.UUID]bool, error) {
	query := `SELECT instance_id FROM instance_dismissals WHERE user_id = ?`
	rows, err := r.db.QueryContext(ctx, query, uuidToString(userID))
	if err != nil {
		return nil, fmt.Errorf("listing dismissed instance ids: %w", err)
	}
	defer rows.Close()

	ids := make(map[uuid.UUID]bool)
	for rows.Next() {
		var idStr string
		if err := rows.Scan(&idStr); err != nil {
			return nil, fmt.Errorf("scanning dismissed instance id: %w", err)
		}
		id, err := parseUUID(idStr)
		if err != nil {
			return nil, fmt.Errorf("converting dismissed instance id: %w", err)
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating dismissed instance ids: %w", err)
	}
	return ids, nil
}

// ClearForInstance removes the dismissal of instanceID for every user.
func (r *DismissalRepo) ClearForInstance(ctx context.Context, instanceID uuid.UUID) error {
	query := `DELETE FROM instance_dismissals WHERE instance_id = ?`
	_, err := r.db.ExecContext(ctx, query, uuidToString(instanceID))
	if err != nil {
		return fmt.Errorf("clearing dismissals for instance: %w", err)
	}
	return nil
}
