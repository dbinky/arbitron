package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// ReferenceBackfiller implements workflow.ReferenceBackfiller over SQLite. It
// owns the SQL that stamps the stable workflow_id onto the three reference tables
// (instances, schedules, published_workflows) that key a workflow by name.
//
// NOTE on sqlc: this repo is hand-written with direct database/sql calls, matching
// schedule_repo.go and notification_repo.go. The backfill is a one-time startup
// reconciliation over a tiny, fixed set of statements; hand-written SQL avoids a
// blanket `sqlc generate` (which corrupts the hand-curated generated/ tree). A
// later sqlc regeneration is a drop-in.
//
// published_workflows keys the workflow by its `name` column (its PRIMARY KEY); the
// other two tables use `workflow_name`. All three gained a nullable workflow_id in
// migration 017.
type ReferenceBackfiller struct {
	db *sql.DB
}

// Compile-time assertion that ReferenceBackfiller satisfies the domain port.
var _ workflow.ReferenceBackfiller = (*ReferenceBackfiller)(nil)

// NewReferenceBackfiller creates a ReferenceBackfiller over the given database.
func NewReferenceBackfiller(db *sql.DB) *ReferenceBackfiller {
	return &ReferenceBackfiller{db: db}
}

// UnresolvedReferenceNames returns the distinct workflow names that appear in any
// reference table with a NULL workflow_id. Rows already stamped with an id are
// excluded so the backfill is idempotent.
func (r *ReferenceBackfiller) UnresolvedReferenceNames(ctx context.Context) ([]string, error) {
	const q = `
SELECT workflow_name FROM instances           WHERE workflow_id IS NULL
UNION
SELECT workflow_name FROM schedules           WHERE workflow_id IS NULL
UNION
SELECT name          FROM published_workflows WHERE workflow_id IS NULL`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("listing unresolved reference names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning reference name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating reference names: %w", err)
	}
	return names, nil
}

// SetReferenceID stamps workflow_id = id on every reference row whose name matches
// AND whose workflow_id is currently NULL, across all three tables, in a single
// transaction. The NULL guard means a row already carrying an id is left
// untouched. Returns the total rows updated.
func (r *ReferenceBackfiller) SetReferenceID(ctx context.Context, name string, id uuid.UUID) (int64, error) {
	idStr := id.String()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("beginning backfill transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	stmts := []struct {
		table string
		query string
	}{
		{"instances", `UPDATE instances SET workflow_id = ? WHERE workflow_name = ? AND workflow_id IS NULL`},
		{"schedules", `UPDATE schedules SET workflow_id = ? WHERE workflow_name = ? AND workflow_id IS NULL`},
		{"published_workflows", `UPDATE published_workflows SET workflow_id = ? WHERE name = ? AND workflow_id IS NULL`},
	}

	var total int64
	for _, s := range stmts {
		res, err := tx.ExecContext(ctx, s.query, idStr, name)
		if err != nil {
			return 0, fmt.Errorf("stamping workflow_id on %s for %q: %w", s.table, name, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("counting %s rows for %q: %w", s.table, name, err)
		}
		total += n
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing backfill for %q: %w", name, err)
	}
	return total, nil
}
