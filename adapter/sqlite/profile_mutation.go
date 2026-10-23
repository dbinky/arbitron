package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/provider"
)

var _ provider.ProfileMutationStore = (*ProfileRepo)(nil)

const expectedProfileWhere = `WHERE alias = ? AND selection_mode = ? AND follow_provider = ? AND follow_tier = ?
 AND model = ? AND kind = ? AND connection_ref = ? AND backend = ? AND allowed_tools = ?
 AND context_window = ? AND effort = ? AND revision = ?`

func expectedProfileArgs(p provider.Profile) ([]any, error) {
	p, err := provider.NormalizeSelection(p)
	if err != nil {
		return nil, err
	}
	tools, err := toolsToJSON(p.AllowedTools)
	if err != nil {
		return nil, err
	}
	fp, ft := followColumns(p)
	return []any{p.Alias, string(p.SelectionMode), fp, ft, p.Model, string(p.Kind), p.ConnectionRef, p.Backend, tools, axisString(string(p.ContextWindow)), axisString(string(p.Effort)), p.Revision}, nil
}

func checkProfileMutation(result sql.Result, err error, alias string) error {
	if err != nil {
		return fmt.Errorf("mutating profile %q: %w", alias, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mutating profile %q: %w", alias, err)
	}
	if n == 0 {
		return fmt.Errorf("profile %q: %w", alias, provider.ErrAdvanceConflict)
	}
	return nil
}

// UpdateExpected conditionally replaces a complete row and returns the stored
// revision from the same transaction. A concurrent edit or removal is a conflict.
func (r *ProfileRepo) UpdateExpected(ctx context.Context, expected, replacement provider.Profile) (provider.Profile, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return provider.Profile{}, fmt.Errorf("beginning update of profile %q: %w", replacement.Alias, err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := updateExpectedTx(ctx, tx, expected, replacement)
	if err != nil {
		return provider.Profile{}, err
	}
	if err := tx.Commit(); err != nil {
		return provider.Profile{}, fmt.Errorf("committing update of profile %q: %w", replacement.Alias, err)
	}
	return stored, nil
}

// updateExpectedTx shares the complete expected-row CAS with validated selection
// writes, whose proof checks must occur inside this same transaction.
func updateExpectedTx(ctx context.Context, tx *sql.Tx, expected, replacement provider.Profile) (provider.Profile, error) {
	if replacement.Alias != expected.Alias {
		return provider.Profile{}, fmt.Errorf("%w: profile alias cannot change", provider.ErrAdvanceConflict)
	}
	args, err := expectedProfileArgs(expected)
	if err != nil {
		return provider.Profile{}, err
	}
	p, err := provider.NormalizeSelection(replacement)
	if err != nil {
		return provider.Profile{}, err
	}
	tools, err := toolsToJSON(p.AllowedTools)
	if err != nil {
		return provider.Profile{}, err
	}
	fp, ft := followColumns(p)
	values := []any{string(p.Kind), p.ConnectionRef, p.Model, p.Backend, tools, axisString(string(p.ContextWindow)), axisString(string(p.Effort)), string(p.SelectionMode), fp, ft, time.Now().UTC().Format(time.RFC3339)}
	result, err := tx.ExecContext(ctx, `UPDATE profiles SET kind = ?, connection_ref = ?, model = ?, backend = ?, allowed_tools = ?,
 context_window = ?, effort = ?, selection_mode = ?, follow_provider = ?, follow_tier = ?, updated_at = ?, revision = revision + 1
 `+expectedProfileWhere, append(values, args...)...)
	if err = checkProfileMutation(result, err, p.Alias); err != nil {
		return provider.Profile{}, err
	}
	return getProfile(ctx, generated.New(tx), p.Alias)
}

// DeleteExpected never removes a row changed since the caller read it.
func (r *ProfileRepo) DeleteExpected(ctx context.Context, expected provider.Profile) error {
	args, err := expectedProfileArgs(expected)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM profiles `+expectedProfileWhere, args...)
	return checkProfileMutation(result, err, expected.Alias)
}
