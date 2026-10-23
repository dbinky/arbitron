package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/modelidentity"
)

// ModelIdentityPolicyRepo persists the D2 identity policy as one JSON document
// guarded by a revision, so a replacement is a compare-and-swap.
type ModelIdentityPolicyRepo struct{ db *sql.DB }

var _ modelidentity.PolicyStore = (*ModelIdentityPolicyRepo)(nil)

func NewModelIdentityPolicyRepo(db *sql.DB) *ModelIdentityPolicyRepo {
	return &ModelIdentityPolicyRepo{db: db}
}

type policyQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readPolicy(ctx context.Context, q policyQuerier) (modelidentity.IdentityPolicy, error) {
	var (
		revision int64
		raw      string
	)
	err := q.QueryRowContext(ctx, `SELECT revision, rules FROM model_identity_policy WHERE id = 1`).Scan(&revision, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return modelidentity.IdentityPolicy{}, errors.New("identity policy row is missing")
	}
	if err != nil {
		return modelidentity.IdentityPolicy{}, fmt.Errorf("reading identity policy: %w", err)
	}
	var rules []modelidentity.PatternRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return modelidentity.IdentityPolicy{}, fmt.Errorf("decoding identity policy rules: %w", err)
	}
	return modelidentity.IdentityPolicy{Revision: revision, Rules: rules}, nil
}

func (r *ModelIdentityPolicyRepo) Snapshot(ctx context.Context) (modelidentity.IdentityPolicy, error) {
	return readPolicy(ctx, r.db)
}

func (r *ModelIdentityPolicyRepo) Replace(ctx context.Context, expected int64, rules []modelidentity.PatternRule) (modelidentity.IdentityPolicy, error) {
	raw, err := json.Marshal(rules)
	if err != nil {
		return modelidentity.IdentityPolicy{}, fmt.Errorf("encoding identity policy rules: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return modelidentity.IdentityPolicy{}, fmt.Errorf("beginning identity policy update: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE model_identity_policy SET revision = revision + 1, rules = ? WHERE id = 1 AND revision = ?`,
		string(raw), expected)
	if err != nil {
		return modelidentity.IdentityPolicy{}, fmt.Errorf("updating identity policy: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return modelidentity.IdentityPolicy{}, fmt.Errorf("updating identity policy: %w", err)
	}
	if n == 0 {
		if _, err := readPolicy(ctx, tx); err != nil {
			return modelidentity.IdentityPolicy{}, err
		}
		return modelidentity.IdentityPolicy{}, modelidentity.ErrConflict
	}
	stored, err := readPolicy(ctx, tx)
	if err != nil {
		return modelidentity.IdentityPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return modelidentity.IdentityPolicy{}, fmt.Errorf("committing identity policy update: %w", err)
	}
	return stored, nil
}
