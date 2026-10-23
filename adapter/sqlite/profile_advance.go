package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
)

// Compile-time assertion that ProfileRepo satisfies the advancement port.
var _ provider.AdvanceStore = (*ProfileRepo)(nil)

// CommitAdvance atomically moves a following profile's model to req.Model and
// writes the journal row. The update is conditional on EVERY field of
// req.Expected (selection, target, model, kind, connection, backend, tools,
// axes and revision), so an ABA edit that restores the visible fields still
// loses on the revision. A mismatch or a missing alias returns
// ErrAdvanceConflict with no row or journal change. A request ID that already
// has a journal row replays the original advancement without a new revision.
//
// Inside the same transaction it validates req.ExecutionRevision against the
// persisted policy and connection revisions: a revision another process bumped
// after the in-process gate recheck returns ErrExecutionRevisionStale, and an
// incomplete token returns ErrExecutionRevisionUnavailable.
func (r *ProfileRepo) CommitAdvance(ctx context.Context, req provider.AdvanceRequest) (provider.Advancement, error) {
	exp, err := provider.NormalizeSelection(req.Expected)
	if err != nil {
		return provider.Advancement{}, err
	}
	switch {
	case req.ID == "":
		return provider.Advancement{}, errors.New("advancing profile: empty request id")
	case req.Model == "":
		return provider.Advancement{}, errors.New("advancing profile: empty target model")
	case req.Model == exp.Model:
		return provider.Advancement{}, errors.New("advancing profile: target model equals expected model")
	case exp.SelectionMode != provider.SelectionFollowing:
		return provider.Advancement{}, fmt.Errorf("%w: profile %q is not following", provider.ErrAdvanceConflict, exp.Alias)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return provider.Advancement{}, fmt.Errorf("beginning advancement of %q: %w", exp.Alias, err)
	}
	defer func() { _ = tx.Rollback() }()

	if prior, found, err := journalByID(ctx, tx, req); err != nil {
		return provider.Advancement{}, err
	} else if found {
		return prior, nil
	}
	if err := checkExecutionRevisionTx(ctx, tx, req.ExecutionRevision); err != nil {
		return provider.Advancement{}, fmt.Errorf("advancing profile %q: %w", exp.Alias, err)
	}
	adv, err := commitAdvanceTx(ctx, tx, exp, req)
	if err != nil {
		return provider.Advancement{}, err
	}
	if err := tx.Commit(); err != nil {
		return provider.Advancement{}, fmt.Errorf("committing advancement of %q: %w", exp.Alias, err)
	}
	return adv, nil
}

// checkExecutionRevisionTx validates the shared persisted revisions of an
// inventory token inside tx: the identity-policy revision and every listed
// connection's config_revision. A cycle validates them once at transaction
// entry; a deleted or bumped connection is stale, never skipped.
func checkExecutionRevisionTx(ctx context.Context, tx *sql.Tx, rev provider.ExecutionRevision) error {
	if rev.ID == "" || rev.PolicyRevision < 1 {
		return fmt.Errorf("%w: incomplete execution revision token", provider.ErrExecutionRevisionUnavailable)
	}
	var policy int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM model_identity_policy WHERE id = 1`).Scan(&policy); err != nil {
		return fmt.Errorf("%w: reading identity policy revision: %w", provider.ErrExecutionRevisionUnavailable, err)
	}
	if policy != rev.PolicyRevision {
		return fmt.Errorf("%w: identity policy revision %d, token %d", provider.ErrExecutionRevisionStale, policy, rev.PolicyRevision)
	}
	for name, want := range rev.Connections {
		var got int64
		err := tx.QueryRowContext(ctx, `SELECT config_revision FROM connections WHERE name = ?`, name).Scan(&got)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: connection %q removed", provider.ErrExecutionRevisionStale, name)
		}
		if err != nil {
			return fmt.Errorf("%w: reading connection %q revision: %w", provider.ErrExecutionRevisionUnavailable, name, err)
		}
		if got != want {
			return fmt.Errorf("%w: connection %q revision %d, token %d", provider.ErrExecutionRevisionStale, name, got, want)
		}
	}
	return nil
}

// commitAdvanceTx applies one proposal inside tx: the conditional update on
// every expected field and its journal row. The token must carry the revision
// of the connection the expected profile routes over, so a proposal can never
// commit against a connection its evidence did not cover; that is checked
// after the CAS, so a changed profile still reads as ErrAdvanceConflict, and
// the caller's rollback discards the update. Callers have already normalized
// exp, checked replay and validated the shared revisions.
func commitAdvanceTx(ctx context.Context, tx *sql.Tx, exp provider.Profile, req provider.AdvanceRequest) (provider.Advancement, error) {
	toolsJSON, err := toolsToJSON(exp.AllowedTools)
	if err != nil {
		return provider.Advancement{}, fmt.Errorf("marshalling allowed_tools for profile %q: %w", exp.Alias, err)
	}
	followProvider, followTier := followColumns(exp)
	at := req.At.UTC()
	if req.At.IsZero() {
		at = time.Now().UTC()
	}
	stamp := at.Format(time.RFC3339Nano)

	res, err := tx.ExecContext(ctx, `UPDATE profiles
SET model = ?, revision = revision + 1, updated_at = ?
WHERE alias = ? AND selection_mode = ? AND follow_provider = ? AND follow_tier = ?
  AND model = ? AND kind = ? AND connection_ref = ? AND backend = ? AND allowed_tools = ?
  AND context_window = ? AND effort = ? AND revision = ?`,
		req.Model, stamp,
		exp.Alias, string(exp.SelectionMode), followProvider, followTier,
		exp.Model, string(exp.Kind), exp.ConnectionRef, exp.Backend, toolsJSON,
		axisString(string(exp.ContextWindow)), axisString(string(exp.Effort)), exp.Revision)
	if err != nil {
		return provider.Advancement{}, fmt.Errorf("advancing profile %q: %w", exp.Alias, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return provider.Advancement{}, fmt.Errorf("advancing profile %q: %w", exp.Alias, err)
	}
	if n == 0 {
		return provider.Advancement{}, fmt.Errorf("profile %q: %w", exp.Alias, provider.ErrAdvanceConflict)
	}
	if exp.ConnectionRef != "" {
		if _, ok := req.ExecutionRevision.Connections[exp.ConnectionRef]; !ok {
			return provider.Advancement{}, fmt.Errorf("advancing profile %q: %w: token omits connection %q",
				exp.Alias, provider.ErrExecutionRevisionUnavailable, exp.ConnectionRef)
		}
	}
	adv := provider.Advancement{
		ID: req.ID, RefreshID: req.RefreshID, Alias: exp.Alias,
		FromModel: exp.Model, ToModel: req.Model, CanonicalID: req.CanonicalID,
		Revision: exp.Revision + 1, At: at,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO profile_advancement_journal
(id, refresh_id, alias, from_model, to_model, canonical_id, revision, at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		adv.ID, adv.RefreshID, adv.Alias, adv.FromModel, adv.ToModel, adv.CanonicalID, adv.Revision, stamp); err != nil {
		return provider.Advancement{}, fmt.Errorf("journaling advancement of %q: %w", exp.Alias, err)
	}
	return adv, nil
}

// ReplayAdvance never writes or revalidates evidence: the original transaction
// already did that. Bind the ID to its durable intent before returning it.
func (r *ProfileRepo) ReplayAdvance(ctx context.Context, req provider.AdvanceRequest) (provider.Advancement, bool, error) {
	return journalByID(ctx, r.db, req)
}

type advanceJournalReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func journalByID(ctx context.Context, q advanceJournalReader, req provider.AdvanceRequest) (provider.Advancement, bool, error) {
	var (
		adv provider.Advancement
		at  string
	)
	err := q.QueryRowContext(ctx, `SELECT id, refresh_id, alias, from_model, to_model, canonical_id, revision, at
FROM profile_advancement_journal WHERE id = ?`, req.ID).
		Scan(&adv.ID, &adv.RefreshID, &adv.Alias, &adv.FromModel, &adv.ToModel, &adv.CanonicalID, &adv.Revision, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return provider.Advancement{}, false, nil
	}
	if err != nil {
		return provider.Advancement{}, false, fmt.Errorf("reading advancement journal %q: %w", req.ID, err)
	}
	// A later cycle can retry the same alias/revision/model under a new refresh
	// ID and timestamp. Those observation fields are not advancement identity.
	if adv.Alias != req.Expected.Alias || adv.FromModel != req.Expected.Model ||
		adv.ToModel != req.Model || adv.CanonicalID != req.CanonicalID ||
		adv.Revision-1 != req.Expected.Revision {
		return provider.Advancement{}, false, fmt.Errorf("advancement request %q: %w", req.ID, provider.ErrAdvanceConflict)
	}
	if adv.At, err = time.Parse(time.RFC3339, at); err != nil {
		return provider.Advancement{}, false, fmt.Errorf("parsing advancement journal time %q: %w", req.ID, err)
	}
	return adv, true, nil
}
