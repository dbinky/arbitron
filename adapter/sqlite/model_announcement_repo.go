package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

// ModelAnnouncementRepo shares the catalog database and never rewrites the
// original event payload. A complete part plan is durable before outbound I/O.
type ModelAnnouncementRepo struct{ db *sql.DB }

var _ modelcatalog.AnnouncementStore = (*ModelAnnouncementRepo)(nil)

func NewModelAnnouncementRepo(db *sql.DB) *ModelAnnouncementRepo {
	return &ModelAnnouncementRepo{db: db}
}

// Fixed-width UTC timestamps preserve nanosecond ordering in SQLite comparisons.
const (
	announcementTimeLayout     = "2006-01-02T15:04:05.000000000Z"
	announcementInitialBackoff = 4 * time.Hour
	announcementMaximumBackoff = 24 * time.Hour
)
const announcementColumns = `id,payload,state,claim_id,lease_until,attempts,next_attempt_at,last_code,parts,part_count,confirmed_parts`

func announcementTime(at time.Time) string { return at.UTC().Format(announcementTimeLayout) }
func announcementBackoff(attempts int) time.Duration {
	delay := announcementInitialBackoff
	for i := 1; i < attempts && delay < announcementMaximumBackoff; i++ {
		delay = min(delay*2, announcementMaximumBackoff)
	}
	return delay
}

func (r *ModelAnnouncementRepo) ClassifyAwaiting(ctx context.Context, configured bool) error {
	state := modelcatalog.EventSuppressed
	if configured {
		state = modelcatalog.EventPending
	}
	_, err := r.db.ExecContext(ctx, `UPDATE model_announcement_events SET state=? WHERE state='awaiting_setting'`, state)
	return err
}

func scanAnnouncement(row catalogReportScanner) (modelcatalog.Event, string, error) {
	var e modelcatalog.Event
	var id, raw, lease, next, parts, confirmed, attempts, partCount string
	if err := row.Scan(&id, &raw, &e.State, &e.ClaimID, &lease, &attempts, &next, &e.LastCode, &parts, &partCount, &confirmed); err != nil {
		return e, id, err
	}
	// SQLite's integer affinity also permits text and fractional values. Parse
	// counters after scanning so corrupt fields take the durable deferral path,
	// while query, cancellation and driver errors remain storage failures.
	for _, field := range []struct {
		text   string
		target *int
	}{{attempts, &e.Attempts}, {partCount, &e.PartCount}} {
		n, err := strconv.Atoi(field.text)
		if err != nil || n < 0 {
			return e, id, modelcatalog.ErrAnnouncementState
		}
		*field.target = n
	}
	if json.Unmarshal([]byte(raw), &e.Payload) != nil || json.Unmarshal([]byte(parts), &e.Parts) != nil || json.Unmarshal([]byte(confirmed), &e.ConfirmedParts) != nil {
		return e, id, modelcatalog.ErrAnnouncementState
	}
	for _, field := range []struct {
		text   string
		target *time.Time
	}{{lease, &e.LeaseUntil}, {next, &e.NextAttemptAt}} {
		if field.text != "" {
			at, err := time.Parse(time.RFC3339Nano, field.text)
			if err != nil {
				return e, id, modelcatalog.ErrAnnouncementState
			}
			*field.target = at
		}
	}
	return e, id, nil
}

func validAnnouncementPlan(e modelcatalog.Event) bool {
	if e.PartCount < 1 || len(e.Parts) != e.PartCount {
		return false
	}
	var paths []modelcatalog.UsabilityEvidence
	var changes []provider.Advancement
	for i, p := range e.Parts {
		if p.EventID != e.Payload.EventID || p.Index != i+1 || p.Count != e.PartCount || p.Payload.EventID != e.Payload.EventID || p.Payload.CanonicalID != e.Payload.CanonicalID || p.Payload.Provider != e.Payload.Provider || p.Payload.Tier != e.Payload.Tier || !p.Payload.FirstConfirmedAt.Equal(e.Payload.FirstConfirmedAt) {
			return false
		}
		paths = append(paths, p.Payload.Paths...)
		changes = append(changes, p.Payload.Changes...)
	}
	if !slices.Equal(paths, e.Payload.Paths) || !slices.Equal(changes, e.Payload.Changes) {
		return false
	}
	for i, index := range e.ConfirmedParts {
		if index < 1 || index > e.PartCount || (i > 0 && index <= e.ConfirmedParts[i-1]) {
			return false
		}
	}
	return true
}

func (r *ModelAnnouncementRepo) ClaimPending(ctx context.Context, at time.Time, lease time.Duration) (modelcatalog.Event, error) {
	if at.IsZero() || lease <= 0 {
		return modelcatalog.Event{}, modelcatalog.ErrAnnouncementState
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return modelcatalog.Event{}, err
	}
	defer tx.Rollback()
	// Acquire SQLite's writer before reading. This avoids read-to-write upgrade
	// races across independent processes/connections while the plan is prepared.
	due := `SELECT id FROM model_announcement_events WHERE state IN ('pending','delivery_unknown') AND next_attempt_at<=? ORDER BY next_attempt_at,rowid LIMIT 1`
	if _, err = tx.ExecContext(ctx, `UPDATE model_announcement_events SET attempts=attempts WHERE id=(`+due+`)`, announcementTime(at)); err != nil {
		return modelcatalog.Event{}, err
	}
	e, id, err := scanAnnouncement(tx.QueryRowContext(ctx, `SELECT `+announcementColumns+` FROM model_announcement_events WHERE id=(`+due+`)`, announcementTime(at)))
	if errors.Is(err, sql.ErrNoRows) {
		return e, modelcatalog.ErrNoPendingAnnouncements
	}
	if errors.Is(err, modelcatalog.ErrAnnouncementState) {
		return deferInvalidAnnouncement(ctx, tx, id, e.Attempts+1, at)
	}
	if err != nil {
		return e, err
	}
	e.Attempts++
	e.NextAttemptAt = at.Add(announcementBackoff(e.Attempts))
	if e.PartCount == 0 && len(e.Parts) == 0 && len(e.ConfirmedParts) == 0 {
		if e.Payload.EventID != id {
			err = modelcatalog.ErrAnnouncementPayload
		} else {
			e.Parts, err = notification.PartitionAnnouncement(e.Payload, modelcatalog.MaxAnnouncementBytes)
		}
		if err != nil {
			return deferInvalidAnnouncement(ctx, tx, id, e.Attempts, at)
		}
		e.PartCount = len(e.Parts)
	}
	if e.Payload.EventID != id || !validAnnouncementPlan(e) {
		return deferInvalidAnnouncement(ctx, tx, id, e.Attempts, at)
	}
	e.State = modelcatalog.EventInFlight
	e.ClaimID = uuid.NewString()
	e.LeaseUntil = at.Add(lease)
	parts, err := json.Marshal(e.Parts)
	if err != nil {
		return modelcatalog.Event{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE model_announcement_events SET state=?,claim_id=?,lease_until=?,attempts=?,next_attempt_at=?,parts=?,part_count=? WHERE id=?`, e.State, e.ClaimID, announcementTime(e.LeaseUntil), e.Attempts, announcementTime(e.NextAttemptAt), string(parts), e.PartCount, id)
	if err != nil {
		return modelcatalog.Event{}, err
	}
	if err = tx.Commit(); err != nil {
		return modelcatalog.Event{}, err
	}
	return e, nil
}

// Only a committed deferral permits Drain to continue past a corrupt row.
// Keep the frozen payload, plan and acknowledgements available for repair.
func deferInvalidAnnouncement(ctx context.Context, tx *sql.Tx, id string, attempts int, at time.Time) (modelcatalog.Event, error) {
	res, err := tx.ExecContext(ctx, `UPDATE model_announcement_events SET state='pending',claim_id='',lease_until='',attempts=?,next_attempt_at=?,last_code='payload_invalid' WHERE id=?`, attempts, announcementTime(at.Add(announcementBackoff(attempts))), id)
	if err != nil {
		return modelcatalog.Event{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return modelcatalog.Event{}, err
	}
	if n != 1 {
		return modelcatalog.Event{}, modelcatalog.ErrAnnouncementState
	}
	if err = tx.Commit(); err != nil {
		return modelcatalog.Event{}, err
	}
	return modelcatalog.Event{}, modelcatalog.ErrAnnouncementPayload
}

func (r *ModelAnnouncementRepo) Complete(ctx context.Context, id, claim string, outcome modelcatalog.SendOutcome, at time.Time) error {
	return r.complete(ctx, id, claim, 0, outcome, at)
}
func (r *ModelAnnouncementRepo) CompletePart(ctx context.Context, id, claim string, index int, outcome modelcatalog.SendOutcome, at time.Time) error {
	if index < 1 {
		return modelcatalog.ErrAnnouncementState
	}
	return r.complete(ctx, id, claim, index, outcome, at)
}

func (r *ModelAnnouncementRepo) complete(ctx context.Context, id, claim string, index int, outcome modelcatalog.SendOutcome, at time.Time) error {
	if at.IsZero() || id == "" || claim == "" {
		return modelcatalog.ErrAnnouncementState
	}
	if outcome.State != modelcatalog.SendConfirmed && outcome.State != modelcatalog.SendDefiniteFailure && outcome.State != modelcatalog.SendAmbiguous && outcome.State != modelcatalog.SendDeferred {
		return modelcatalog.ErrAnnouncementState
	}
	if outcome.State == modelcatalog.SendDeferred && index != 0 {
		return modelcatalog.ErrAnnouncementState
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Ownership/expiry is checked by the first write, before reading part state.
	res, err := tx.ExecContext(ctx, `UPDATE model_announcement_events SET attempts=attempts WHERE id=? AND state='in_flight' AND claim_id=? AND lease_until>?`, id, claim, announcementTime(at))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return modelcatalog.ErrAnnouncementClaimLost
	}
	e, storedID, err := scanAnnouncement(tx.QueryRowContext(ctx, `SELECT `+announcementColumns+` FROM model_announcement_events WHERE id=?`, id))
	if err != nil {
		return err
	}
	if e.Payload.EventID != storedID || !validAnnouncementPlan(e) || index > e.PartCount {
		return modelcatalog.ErrAnnouncementState
	}
	if outcome.State == modelcatalog.SendDeferred {
		// ClaimPending reserves an attempt before I/O. A missing prerequisite did not
		// attempt the remaining part: undo that reservation, retaining earlier
		// failures and every confirmed part. Drain stops after this release, so
		// an immediately due event cannot spin in the same drain.
		e.State = modelcatalog.EventPending
		e.Attempts = max(0, e.Attempts-1)
		e.NextAttemptAt = at
	} else if outcome.State == modelcatalog.SendConfirmed {
		if index == 0 {
			if e.PartCount != 1 {
				return modelcatalog.ErrAnnouncementState
			}
			index = 1
		}
		if !slices.Contains(e.ConfirmedParts, index) {
			e.ConfirmedParts = append(e.ConfirmedParts, index)
			slices.Sort(e.ConfirmedParts)
		}
		if len(e.ConfirmedParts) == e.PartCount {
			e.State = modelcatalog.EventAnnounced
		}
	} else {
		if index != 0 && slices.Contains(e.ConfirmedParts, index) {
			return modelcatalog.ErrAnnouncementState
		}
		e.State = modelcatalog.EventPending
		if outcome.State == modelcatalog.SendAmbiguous {
			e.State = modelcatalog.EventDeliveryUnknown
		}
		e.NextAttemptAt = at.Add(announcementBackoff(e.Attempts))
	}
	leaseUntil := announcementTime(e.LeaseUntil)
	if e.State != modelcatalog.EventInFlight {
		e.ClaimID = ""
		leaseUntil = ""
	}
	next := announcementTime(e.NextAttemptAt)
	if e.State == modelcatalog.EventAnnounced {
		next = ""
	}
	confirmed, err := json.Marshal(e.ConfirmedParts)
	if err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `UPDATE model_announcement_events SET state=?,claim_id=?,lease_until=?,attempts=?,next_attempt_at=?,last_code=?,confirmed_parts=? WHERE id=?`, e.State, e.ClaimID, leaseUntil, e.Attempts, next, safeAnnouncementCode(outcome.Code), string(confirmed), id)
	if err != nil {
		return err
	}
	n, err = res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return modelcatalog.ErrAnnouncementState
	}
	return tx.Commit()
}

// Codes are diagnostics, never arbitrary sender errors or bearer destinations.
func safeAnnouncementCode(code string) string {
	switch code {
	case "http_2xx", "transport_uncertain", "destination_empty", "destination_invalid", "destination_blocked", "payload_invalid", "payload_too_large", "settings_unavailable":
		return code
	}
	if strings.HasPrefix(code, "http_") && len(code) == 8 {
		if status, err := strconv.Atoi(code[5:]); err == nil && status >= 100 && status <= 599 {
			return code
		}
	}
	return "send_result"
}

func (r *ModelAnnouncementRepo) RecoverClaims(ctx context.Context, at time.Time) error {
	if at.IsZero() {
		return modelcatalog.ErrAnnouncementState
	}
	// ClaimPending reserves a retry deadline from claim time. A crashed attempt
	// has no completion time; retain that deadline and all confirmed parts.
	_, err := r.db.ExecContext(ctx, `UPDATE model_announcement_events SET state='delivery_unknown',claim_id='',lease_until='',last_code='lease_expired' WHERE state='in_flight' AND lease_until<=?`, announcementTime(at))
	return err
}
func (r *ModelAnnouncementRepo) RenewClaim(ctx context.Context, id, claim string, at time.Time, lease time.Duration) error {
	if at.IsZero() || lease <= 0 || id == "" || claim == "" {
		return modelcatalog.ErrAnnouncementState
	}
	res, err := r.db.ExecContext(ctx, `UPDATE model_announcement_events SET lease_until=? WHERE id=? AND state='in_flight' AND claim_id=? AND lease_until>?`, announcementTime(at.Add(lease)), id, claim, announcementTime(at))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return modelcatalog.ErrAnnouncementClaimLost
	}
	return nil
}
