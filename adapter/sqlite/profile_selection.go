package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/provider"
)

var _ provider.SelectionMutationStore = (*ProfileRepo)(nil)

// CreateValidated checks persistent proof revisions and inserts the prepared
// profile atomically. Discovery and external fingerprint validation are the
// caller's responsibility, before entering this transaction.
func (r *ProfileRepo) CreateValidated(ctx context.Context, selection provider.PreparedSelection) (provider.Profile, error) {
	return r.writeSelection(ctx, selection, func(tx *sql.Tx, p provider.Profile) (provider.Profile, error) {
		queries := generated.New(tx)
		if err := createProfile(ctx, queries, p); err != nil {
			return provider.Profile{}, err
		}
		return getProfile(ctx, queries, p.Alias)
	})
}

// UpdateValidated shares UpdateExpected's full-row CAS, while checking the
// prospective selection's persistent revisions inside the same transaction.
func (r *ProfileRepo) UpdateValidated(ctx context.Context, expected provider.Profile, selection provider.PreparedSelection) (provider.Profile, error) {
	return r.writeSelection(ctx, selection, func(tx *sql.Tx, p provider.Profile) (provider.Profile, error) {
		return updateExpectedTx(ctx, tx, expected, p)
	})
}

func (r *ProfileRepo) writeSelection(ctx context.Context, selection provider.PreparedSelection, write func(*sql.Tx, provider.Profile) (provider.Profile, error)) (provider.Profile, error) {
	p, err := provider.NormalizeSelection(selection.Profile)
	if err != nil {
		return provider.Profile{}, err
	}
	if p.Model == "" {
		return provider.Profile{}, fmt.Errorf("%w: prepared selection has no model", provider.ErrInvalidSelection)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return provider.Profile{}, fmt.Errorf("beginning selection of profile %q: %w", p.Alias, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkSelectionTx(ctx, tx, p, selection.Candidate); err != nil {
		return provider.Profile{}, err
	}
	stored, err := write(tx, p)
	if err != nil {
		return provider.Profile{}, err
	}
	if err := tx.Commit(); err != nil {
		return provider.Profile{}, fmt.Errorf("committing selection of profile %q: %w", p.Alias, err)
	}
	return stored, nil
}

func checkSelectionTx(ctx context.Context, tx *sql.Tx, p provider.Profile, candidate *provider.EligibleCandidate) error {
	// Pinned versions are validated against actual routes by the caller, without
	// a dependency on follow-tier policy or catalog availability.
	if p.SelectionMode != provider.SelectionFollowing {
		return nil
	}
	if candidate == nil || candidate.Model == "" || candidate.CanonicalID == "" || candidate.PathRevision == "" {
		return fmt.Errorf("%w: incomplete following selection proof", provider.ErrExecutionRevisionUnavailable)
	}
	if candidate.Model != p.Model {
		return fmt.Errorf("%w: candidate does not match prepared model", provider.ErrInvalidSelection)
	}
	rev := candidate.ExecutionRevision
	if rev.ID != candidate.PathRevision {
		return fmt.Errorf("%w: candidate token does not match prospective path revision", provider.ErrExecutionRevisionUnavailable)
	}
	if p.ConnectionRef != "" {
		if _, ok := rev.Connections[p.ConnectionRef]; !ok {
			return fmt.Errorf("%w: selection proof omits profile connection", provider.ErrExecutionRevisionUnavailable)
		}
	}
	return checkExecutionRevisionTx(ctx, tx, rev)
}
