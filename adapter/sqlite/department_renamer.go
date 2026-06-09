package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
)

var _ identity.DepartmentRenamer = (*DepartmentRenamer)(nil)

// DepartmentRenamer implements identity.DepartmentRenamer over SQLite. It rewrites
// a department string across ALL FOUR tables that persist one: user_departments
// (membership snapshot), instances (run stamp), schedules (attribution), and
// published_workflows (department-visibility publication). Omitting any of the four
// would leave a stale/orphaned department; in particular omitting published_workflows
// would hide a department-visibility published workflow from its members (now under
// the friendly name) because the publication still carried the old verbatim value.
type DepartmentRenamer struct {
	db *sql.DB
}

// NewDepartmentRenamer constructs the renamer over the given DB handle.
func NewDepartmentRenamer(db *sql.DB) *DepartmentRenamer {
	return &DepartmentRenamer{db: db}
}

// RenameDepartment rewrites every occurrence of `from` to `to` across the four
// department-bearing tables in a SINGLE transaction, returning the total rows
// changed. Idempotent: once the stored values are `to`, the WHERE clauses match
// nothing and it returns 0.
//
// user_departments has a composite PK (user_id, department), so a member already
// in BOTH `from` and `to` would make a naive UPDATE violate the PK. The rename
// there is therefore two statements: UPDATE OR IGNORE migrates every NON-colliding
// verbatim row to the friendly value (a colliding row is skipped, NOT renamed, and
// NOT deleted, by SQLite's OR IGNORE conflict resolution); a follow-up DELETE then
// purges any verbatim rows that survived the UPDATE — i.e. exactly the colliding
// ones, whose friendly membership already exists, so no membership is lost. Without
// the DELETE the verbatim duplicate would orphan a department. Only the UPDATE's
// affected rows count toward the returned total (the DELETE is silent cleanup of a
// row the friendly membership already covers). The other three tables have no
// unique constraint on department, so a plain UPDATE is correct there.
func (r *DepartmentRenamer) RenameDepartment(ctx context.Context, from, to string) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("beginning rename-department transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	var total int64

	// user_departments: collision-safe rename, then purge surviving duplicates.
	res, err := tx.ExecContext(ctx,
		`UPDATE OR IGNORE user_departments SET department = ? WHERE department = ?`, to, from)
	if err != nil {
		return 0, fmt.Errorf("renaming user_departments %q -> %q: %w", from, to, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting user_departments rows for %q -> %q: %w", from, to, err)
	}
	total += n
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM user_departments WHERE department = ?`, from); err != nil {
		return 0, fmt.Errorf("purging collided user_departments %q: %w", from, err)
	}

	// The other three tables have no unique constraint on department.
	stmts := []string{
		`UPDATE instances SET department = ? WHERE department = ?`,
		`UPDATE schedules SET department = ? WHERE department = ?`,
		`UPDATE published_workflows SET department = ? WHERE department = ?`,
	}
	for _, stmt := range stmts {
		res, err := tx.ExecContext(ctx, stmt, to, from)
		if err != nil {
			return 0, fmt.Errorf("renaming department %q -> %q: %w", from, to, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("counting rows for %q -> %q: %w", from, to, err)
		}
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing rename-department transaction: %w", err)
	}
	return total, nil
}
