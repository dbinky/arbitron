package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/arbitron/arbitron/domain/provider"
)

var _ provider.ProfileSeedStore = (*ProfileRepo)(nil)

// PendingProfileSeed records the original seed before its first profile write.
// The marker remains after completion so deleting profiles cannot re-seed them.
// No transaction or DB connection is held during service proof/discovery.
func (r *ProfileRepo) PendingProfileSeed(ctx context.Context, candidates []provider.Profile) ([]provider.Profile, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Also protect rows created after migration but before the first seed call.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO profile_seed_state (id)
		SELECT 1 WHERE EXISTS (SELECT 1 FROM profiles)`); err != nil {
		return nil, err
	}
	var initialized int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile_seed_state`).Scan(&initialized); err != nil {
		return nil, err
	}
	if initialized == 0 && len(candidates) > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO profile_seed_state (id) VALUES (1)`); err != nil {
			return nil, err
		}
		for i, p := range candidates {
			body, err := json.Marshal(p)
			if err != nil {
				return nil, err
			}
			// As with the original create loop, the first duplicate alias wins.
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO profile_seed_pending
				(alias, position, profile_json) VALUES (?, ?, ?)`, p.Alias, i, string(body)); err != nil {
				return nil, err
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT alias, profile_json FROM profile_seed_pending ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []provider.Profile
	for rows.Next() {
		var alias, body string
		if err := rows.Scan(&alias, &body); err != nil {
			return nil, err
		}
		var p provider.Profile
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			return nil, fmt.Errorf("reading pending profile seed %q: %w", alias, err)
		}
		if p.Alias != alias {
			return nil, fmt.Errorf("pending profile seed alias mismatch for %q", alias)
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pending, nil
}

func (r *ProfileRepo) CompleteProfileSeed(ctx context.Context, alias string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM profile_seed_pending WHERE alias = ?`, alias)
	return err
}

// ReplaceProfileSeed rewrites an unfinished entry's original snapshot. A missing
// (completed or never-seeded) alias is left alone.
func (r *ProfileRepo) ReplaceProfileSeed(ctx context.Context, p provider.Profile) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE profile_seed_pending SET profile_json = ? WHERE alias = ?`, string(body), p.Alias)
	return err
}
