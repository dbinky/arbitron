package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

var (
	_ modelcatalog.CatalogStore   = (*ModelCatalogRepo)(nil)
	_ provider.PublicationJournal = (*ModelCatalogRepo)(nil)
)

// ModelCatalogRepo shares the profile repository's database: model CAS,
// original journal results, observations, frozen events and reports commit in
// one transaction. No cache publication or network I/O occurs inside it.
type ModelCatalogRepo struct{ db *sql.DB }

func NewModelCatalogRepo(db *sql.DB) *ModelCatalogRepo { return &ModelCatalogRepo{db: db} }

func (r *ModelCatalogRepo) CommitCycle(ctx context.Context, c modelcatalog.CycleCommit) (modelcatalog.RefreshReport, error) {
	if c.Report.ID == "" || c.Report.At.IsZero() {
		return modelcatalog.RefreshReport{}, errors.New("catalog cycle: missing report identity/time")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return modelcatalog.RefreshReport{}, err
	}
	defer tx.Rollback()
	if prior, found, err := catalogReportTx(ctx, tx, c.Report.ID); err != nil {
		return modelcatalog.RefreshReport{}, err
	} else if found {
		return prior, nil
	}
	// Validate global tokens before any writes. A shared revision change aborts
	// the entire cycle, unlike a per-profile expected-row conflict below. The
	// classification check also covers discovery-only and stale-advance
	// fallback cycles, which carry no advancement token.
	if err := checkCyclePolicyTx(ctx, tx, c); err != nil {
		return modelcatalog.RefreshReport{}, err
	}
	ids := make(map[string]bool, len(c.Advances))
	for _, req := range c.Advances {
		if req.ID == "" || ids[req.ID] || req.Model == "" || req.CanonicalID == "" || req.Model == req.Expected.Model {
			return modelcatalog.RefreshReport{}, errors.New("catalog cycle: invalid advancement proposal")
		}
		ids[req.ID] = true
		if req.PathRevision == "" || req.PathRevision != req.ExecutionRevision.ID {
			return modelcatalog.RefreshReport{}, provider.ErrExecutionRevisionUnavailable
		}
		if err := checkExecutionRevisionTx(ctx, tx, req.ExecutionRevision); err != nil {
			return modelcatalog.RefreshReport{}, err
		}
	}
	report := c.Report
	report.SliceStatus = maps.Clone(c.Report.SliceStatus)
	if report.SliceStatus == nil {
		report.SliceStatus = map[string]string{}
	}
	report.PathStatus = maps.Clone(c.Report.PathStatus)
	if report.PathStatus == nil {
		report.PathStatus = map[string]string{}
	}
	report.Changes = nil
	report.NewlyUsable = nil
	report.PublicationPending = true
	if err := persistCatalogTx(ctx, tx, c, &report); err != nil {
		return modelcatalog.RefreshReport{}, err
	}
	view, err := catalogSnapshotTx(ctx, tx)
	if err != nil {
		return modelcatalog.RefreshReport{}, err
	}
	// Discovery-only cycles need the same exact-model identity check as
	// advancements. Complete proof fields alone cannot identify a catalog model.
	for _, ev := range c.Evidence {
		if ev.Confirmed() && !catalogEvidenceIdentified(view.HistoricalEntries, ev) {
			return modelcatalog.RefreshReport{}, errors.New("catalog cycle: confirmed evidence does not match a catalog model")
		}
	}
	// A model whose every proposal lost its CAS is deferred like one whose
	// proposals were dropped before the transaction (DeferDiscovery).
	deferred := make(map[string]bool, len(c.DeferDiscovery))
	for _, canonical := range c.DeferDiscovery {
		deferred[canonical] = true
	}
	committed := make(map[string]bool, len(c.Advances))
	for _, req := range c.Advances {
		exp, err := provider.NormalizeSelection(req.Expected)
		if err != nil {
			return modelcatalog.RefreshReport{}, err
		}
		if exp.SelectionMode != provider.SelectionFollowing {
			return modelcatalog.RefreshReport{}, provider.ErrAdvanceConflict
		}
		if status, observed := report.SliceStatus[exp.FollowTarget.Provider]; observed && status != modelcatalog.SliceStatusOK {
			report.PathStatus["profile:"+exp.Alias] = "invalid_slice"
			continue
		}
		if !catalogAdvanceEvidenced(view, req) {
			return modelcatalog.RefreshReport{}, errors.New("catalog cycle: advancement lacks confirmed ordered model evidence")
		}
		adv, found, err := journalByID(ctx, tx, req)
		if err != nil {
			return modelcatalog.RefreshReport{}, err
		}
		if !found {
			adv, err = commitAdvanceTx(ctx, tx, exp, req)
			if errors.Is(err, provider.ErrAdvanceConflict) {
				report.PathStatus["profile:"+exp.Alias] = "advance_conflict"
				deferred[req.CanonicalID] = true
				continue
			}
			if err != nil {
				return modelcatalog.RefreshReport{}, err
			}
		}
		committed[req.CanonicalID] = true
		report.Changes = append(report.Changes, adv)
	}
	for canonical := range committed {
		delete(deferred, canonical)
	}
	if err := observeCatalogTx(ctx, tx, view, c.WebhookConfigured, deferred, &report); err != nil {
		return modelcatalog.RefreshReport{}, err
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return modelcatalog.RefreshReport{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO model_refresh_reports(id,report,publication_pending) VALUES(?,?,1)`, report.ID, string(raw)); err != nil {
		return modelcatalog.RefreshReport{}, err
	}
	for _, change := range report.Changes {
		if _, err = tx.ExecContext(ctx, `INSERT INTO model_refresh_advancements(report_id,advancement_id) VALUES(?,?)`, report.ID, change.ID); err != nil {
			return modelcatalog.RefreshReport{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return modelcatalog.RefreshReport{}, err
	}
	return report, nil
}

// checkCyclePolicyTx refuses fresh classifications mapped under a policy
// revision other than the persisted one, or under more than one revision.
func checkCyclePolicyTx(ctx context.Context, tx *sql.Tx, c modelcatalog.CycleCommit) error {
	if c.PolicyRevision == 0 {
		return nil
	}
	for _, sl := range c.Catalog.Slices {
		if sl.Status != modelcatalog.SliceStatusOK {
			continue
		}
		for _, e := range sl.Entries {
			if e.PolicyRevision != c.PolicyRevision {
				return fmt.Errorf("%w: catalog entry %s/%s mapped under policy revision %d, cycle %d", provider.ErrExecutionRevisionStale, e.Provider, e.NativeID, e.PolicyRevision, c.PolicyRevision)
			}
		}
	}
	var policy int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM model_identity_policy WHERE id = 1`).Scan(&policy); err != nil {
		return fmt.Errorf("%w: reading identity policy revision: %w", provider.ErrExecutionRevisionUnavailable, err)
	}
	if policy != c.PolicyRevision {
		return fmt.Errorf("%w: identity policy revision %d, cycle %d", provider.ErrExecutionRevisionStale, policy, c.PolicyRevision)
	}
	return nil
}

func persistCatalogTx(ctx context.Context, tx *sql.Tx, c modelcatalog.CycleCommit, report *modelcatalog.RefreshReport) error {
	for _, slice := range c.Catalog.Slices {
		report.SliceStatus[slice.Provider] = slice.Status
		if slice.Status != modelcatalog.SliceStatusOK {
			continue
		}
		if slice.Provider == "" {
			return errors.New("catalog cycle: missing slice provider")
		}
		entries := make(map[string]modelidentity.Entry, len(slice.Entries))
		for _, entry := range slice.Entries {
			if entry.Provider != slice.Provider || entry.NativeID == "" || entry.CanonicalID == "" {
				return errors.New("catalog cycle: invalid catalog entry")
			}
			entries[entry.NativeID] = entry
		}
		for _, cm := range slice.Models {
			entry, ok := entries[cm.NativeID]
			if !ok || cm.CatalogProvider != slice.Provider {
				return errors.New("catalog cycle: model has no matching identity entry")
			}
			raw, err := json.Marshal(entry)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO model_catalog_models(provider,native_id,canonical_model_id,release_date,source_ref,policy_revision,entry_json)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(provider,native_id) DO UPDATE SET canonical_model_id=excluded.canonical_model_id,release_date=excluded.release_date,source_ref=excluded.source_ref,policy_revision=excluded.policy_revision,entry_json=excluded.entry_json`, cm.CatalogProvider, cm.NativeID, cm.CanonicalModelID, cm.ReleaseDate, cm.SourceRef, entry.PolicyRevision, string(raw)); err != nil {
				return err
			}
		}
		if len(entries) != len(slice.Models) {
			return errors.New("catalog cycle: entries lack source model inputs")
		}
		raw, err := json.Marshal(slice)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO model_catalog_slices(provider,slice_json) VALUES(?,?) ON CONFLICT(provider) DO UPDATE SET slice_json=excluded.slice_json`, slice.Provider, string(raw)); err != nil {
			return err
		}
	}
	for _, ev := range c.Evidence {
		if ev.CanonicalID == "" || ev.PathKey == "" || ev.ConfigRevision == "" || ev.CheckedAt.IsZero() || (ev.State == modelcatalog.UsabilityConfirmed && !ev.Confirmed()) {
			return errors.New("catalog cycle: incomplete usability evidence")
		}
		// Unknown/unavailable checks cannot erase a prior confirmation for the
		// unchanged path configuration. A changed revision has its own row.
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_usability_evidence(canonical_id,path_key,config_revision,native_model,checked_at,proof_ref,state,catalog_provider,failures)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(canonical_id,path_key,config_revision) DO UPDATE SET native_model=excluded.native_model,checked_at=excluded.checked_at,proof_ref=excluded.proof_ref,state=excluded.state,catalog_provider=excluded.catalog_provider,failures=excluded.failures WHERE model_usability_evidence.state <> 'confirmed'`, ev.CanonicalID, ev.PathKey, ev.ConfigRevision, ev.NativeModel, ev.CheckedAt.UTC().Format(time.RFC3339Nano), ev.ProofRef, ev.State, ev.Provider, ev.Failures); err != nil {
			return err
		}
	}
	diagnostics, err := json.Marshal(c.UnmatchedTargets)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO model_catalog_state(id,fetched_at,unmatched_targets) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET fetched_at=excluded.fetched_at,unmatched_targets=excluded.unmatched_targets`, c.Catalog.FetchedAt.UTC().Format(time.RFC3339Nano), string(diagnostics)); err != nil {
		return err
	}
	for key, cursor := range c.ProbeCursor {
		if key == "" || cursor == "" {
			return errors.New("catalog cycle: empty probe cursor")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO model_probe_cursors(path_config,cursor) VALUES(?,?) ON CONFLICT(path_config) DO UPDATE SET cursor=excluded.cursor`, key, cursor); err != nil {
			return err
		}
	}
	return nil
}

func catalogEvidenceIdentified(entries []modelidentity.Entry, ev modelcatalog.UsabilityEvidence) bool {
	for _, entry := range entries {
		model := entry.SpecificModel
		if model == "" {
			model = entry.NativeID
		}
		if entry.CanonicalID == ev.CanonicalID && model == ev.NativeModel && (ev.Provider == "" || entry.Provider == ev.Provider) {
			return true
		}
	}
	return false
}

func catalogAdvanceEvidenced(view modelcatalog.CatalogView, req provider.AdvanceRequest) bool {
	if req.Expected.FollowTarget == nil {
		return false
	}
	ordered := false
	// Current membership is required for the candidate; historical metadata is
	// for remapping/comparing the old model, not permission to select an absent one.
	for _, slice := range view.Catalog.Slices {
		for _, e := range slice.Entries {
			if e.CanonicalID == req.CanonicalID && e.Provider == req.Expected.FollowTarget.Provider && e.Tier == req.Expected.FollowTarget.Tier && e.Tier != "" && e.OrderKey != "" && e.Immutable && e.PolicyRevision == req.ExecutionRevision.PolicyRevision {
				ordered = true
			}
		}
	}
	if !ordered {
		return false
	}
	// Every consuming path at the snapshot needs confirmed proof of this exact
	// model under that path's current configuration revision. Proof recorded for
	// an earlier revision (before a credential rotation) never satisfies it.
	if len(req.Evidence) == 0 {
		return false
	}
	for _, ref := range req.Evidence {
		proved := false
		for _, ev := range view.Evidence {
			if ev.CanonicalID == req.CanonicalID && ev.NativeModel == req.Model && ev.PathKey == ref.PathKey && ev.ConfigRevision == ref.ConfigRevision && ev.Confirmed() {
				proved = true
				break
			}
		}
		if !proved {
			return false
		}
	}
	return true
}

func observeCatalogTx(ctx context.Context, tx *sql.Tx, view modelcatalog.CatalogView, setting *bool, deferred map[string]bool, report *modelcatalog.RefreshReport) error {
	var policy int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM model_identity_policy WHERE id = 1`).Scan(&policy); err != nil {
		// Fresh classifications/advancements already passed their policy checks.
		// A kept-catalog cycle must still persist its failure diagnostics when
		// policy is unreadable. Reserve no observations or events until a later
		// cycle can validate the retained classifications against current policy.
		return nil
	}
	observed, err := observedCatalogIdentitiesTx(ctx, tx, view.HistoricalModels)
	if err != nil {
		return err
	}
	paths := make(map[string][]modelcatalog.UsabilityEvidence)
	for _, ev := range view.Evidence {
		if ev.Confirmed() {
			paths[ev.CanonicalID] = append(paths[ev.CanonicalID], ev)
		}
	}
	keys := slices.Sorted(maps.Keys(paths))
	for _, canonical := range keys {
		if deferred[canonical] || observed[canonical] {
			continue
		}
		entry, ok := confirmingEntry(view.HistoricalEntries, paths[canonical])
		if !ok {
			// Legacy proof can match several offerings of the same spelling.
			// Keep it durable without reserving a lifetime observation/event;
			// a later provider-stamped confirmation can resolve the ambiguity.
			continue
		}
		// Failed fetches and invalid provider slices retain old classifications,
		// even when the cycle has no fresh PolicyRevision to check. Keep their
		// proof, but reserve no lifetime observation until this offering has
		// been classified under the policy read in the same transaction.
		if entry.PolicyRevision != policy {
			continue
		}
		obs := modelcatalog.Observation{Entry: entry, Paths: paths[canonical], FirstConfirmedAt: report.At}
		raw, err := json.Marshal(obs)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO model_first_observations(canonical_id,observation) VALUES(?,?)`, canonical, string(raw)); err != nil {
			return err
		}
		payload := modelcatalog.AnnouncementPayload{EventID: uuid.NewString(), CanonicalID: canonical, Provider: entry.Provider, Tier: entry.Tier, FirstConfirmedAt: obs.FirstConfirmedAt, Paths: obs.Paths}
		for _, change := range report.Changes {
			if change.CanonicalID == canonical {
				payload.Changes = append(payload.Changes, change)
			}
		}
		raw, err = json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO model_announcement_events(id,canonical_id,payload,state) VALUES(?,?,?,?)`, payload.EventID, canonical, string(raw), discoveryEventState(setting)); err != nil {
			return err
		}
		report.NewlyUsable = append(report.NewlyUsable, obs)
	}
	if setting != nil {
		// Classification retries preserve the original observation and payload.
		_, err := tx.ExecContext(ctx, `UPDATE model_announcement_events SET state=? WHERE state='awaiting_setting'`, discoveryEventState(setting))
		return err
	}
	return nil
}

// confirmingEntry chooses the catalog offering a card names: the first
// confirmed proof (ordered by path) whose recorded provider and spelling match
// a catalog entry. Proof stored before migration 036 has no provider and can
// identify an offering only when its spelling has exactly one catalog match.
func confirmingEntry(entries []modelidentity.Entry, proofs []modelcatalog.UsabilityEvidence) (modelidentity.Entry, bool) {
	match := func(legacy bool) (modelidentity.Entry, bool) {
		for _, ev := range proofs {
			if legacy != (ev.Provider == "") {
				continue
			}
			var match modelidentity.Entry
			found := false
			for _, e := range entries {
				if e.CanonicalID == ev.CanonicalID && catalogEvidenceIdentified([]modelidentity.Entry{e}, ev) {
					if !legacy {
						return e, true
					}
					if found {
						found = false
						break
					}
					match, found = e, true
				}
			}
			if found {
				return match, true
			}
		}
		return modelidentity.Entry{}, false
	}
	if e, ok := match(false); ok {
		return e, true
	}
	if e, ok := match(true); ok {
		return e, true
	}
	return modelidentity.Entry{}, false
}

// A routing-prefix upgrade can change a canonical key while the stable catalog
// provider/native pair remains the same. Resolve the frozen observation's row
// through retained source inputs, including models absent from today's slice.
// This is lifetime deduplication only: original observations/events (including
// suppression, claims and delivery retries) and old route proof stay untouched.
func observedCatalogIdentitiesTx(ctx context.Context, tx *sql.Tx, models []modelidentity.CatalogModel) (map[string]bool, error) {
	observed := map[string]bool{}
	err := readCatalogRows(ctx, tx, `SELECT canonical_id FROM model_first_observations`, func(rows *sql.Rows) error {
		var canonical string
		if err := rows.Scan(&canonical); err != nil {
			return err
		}
		observed[canonical] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, cm := range models {
		current, err := modelidentity.CanonicalID(cm.CatalogProvider, cm.NativeID, cm.CanonicalModelID)
		if err != nil {
			// Retained metadata may no longer be verifiable under the stricter
			// identity grammar. It cannot establish another identity's history.
			continue
		}
		// Before provider-prefix recognition, these rows used provider/native
		// (or explicit source identity) as their lifetime observation key.
		fallback := cm.CatalogProvider + "/" + cm.NativeID
		source := strings.ToLower(strings.TrimSpace(cm.CanonicalModelID))
		if observed[fallback] || source != "" && observed[source] {
			observed[current] = true
		}
	}
	return observed, nil
}

func discoveryEventState(setting *bool) string {
	if setting == nil {
		return modelcatalog.EventAwaitingSetting
	}
	if *setting {
		return modelcatalog.EventPending
	}
	return modelcatalog.EventSuppressed
}

// Snapshot reads one coherent internal view; callers never receive shared maps
// or credential-bearing execution bindings.
func (r *ModelCatalogRepo) Snapshot(ctx context.Context) (modelcatalog.CatalogView, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return modelcatalog.CatalogView{}, err
	}
	defer tx.Rollback()
	view, err := catalogSnapshotTx(ctx, tx)
	if err != nil {
		return modelcatalog.CatalogView{}, err
	}
	return view, tx.Commit()
}

func catalogSnapshotTx(ctx context.Context, tx *sql.Tx) (modelcatalog.CatalogView, error) {
	view := modelcatalog.CatalogView{ProbeCursor: map[string]string{}}
	var fetched, diagnostics string
	err := tx.QueryRowContext(ctx, `SELECT fetched_at,unmatched_targets FROM model_catalog_state WHERE id=1`).Scan(&fetched, &diagnostics)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	if err == nil {
		if view.Catalog.FetchedAt, err = time.Parse(time.RFC3339Nano, fetched); err != nil {
			return view, err
		}
		if err = json.Unmarshal([]byte(diagnostics), &view.UnmatchedTargets); err != nil {
			return view, fmt.Errorf("decoding catalog diagnostics: %w", err)
		}
	}
	if err = readCatalogRows(ctx, tx, `SELECT slice_json FROM model_catalog_slices ORDER BY provider`, func(rows *sql.Rows) error {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var slice modelcatalog.CatalogSlice
		if err := json.Unmarshal([]byte(raw), &slice); err != nil {
			return fmt.Errorf("decoding catalog slice: %w", err)
		}
		view.Catalog.Slices = append(view.Catalog.Slices, slice)
		return nil
	}); err != nil {
		return view, err
	}
	if err = readCatalogRows(ctx, tx, `SELECT provider,native_id,canonical_model_id,release_date,source_ref,policy_revision,entry_json FROM model_catalog_models ORDER BY provider,native_id`, func(rows *sql.Rows) error {
		var cm modelidentity.CatalogModel
		var raw string
		var policyRevision int64
		if err := rows.Scan(&cm.CatalogProvider, &cm.NativeID, &cm.CanonicalModelID, &cm.ReleaseDate, &cm.SourceRef, &policyRevision, &raw); err != nil {
			return err
		}
		var entry modelidentity.Entry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			return fmt.Errorf("decoding historical catalog identity: %w", err)
		}
		entry.PolicyRevision = policyRevision
		view.HistoricalModels = append(view.HistoricalModels, cm)
		view.HistoricalEntries = append(view.HistoricalEntries, entry)
		return nil
	}); err != nil {
		return view, err
	}
	if err = readCatalogRows(ctx, tx, `SELECT canonical_id,path_key,native_model,config_revision,proof_ref,checked_at,state,catalog_provider,failures FROM model_usability_evidence ORDER BY canonical_id,path_key,config_revision`, func(rows *sql.Rows) error {
		var ev modelcatalog.UsabilityEvidence
		var at string
		if err := rows.Scan(&ev.CanonicalID, &ev.PathKey, &ev.NativeModel, &ev.ConfigRevision, &ev.ProofRef, &at, &ev.State, &ev.Provider, &ev.Failures); err != nil {
			return err
		}
		var err error
		if ev.CheckedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return err
		}
		view.Evidence = append(view.Evidence, ev)
		return nil
	}); err != nil {
		return view, err
	}
	if err = readCatalogRows(ctx, tx, `SELECT r.id,r.report,r.publication_pending FROM model_refresh_reports r
WHERE NOT EXISTS (SELECT 1 FROM model_refresh_report_quarantine q WHERE q.report_id=r.id AND q.report=r.report)
ORDER BY r.rowid`, func(rows *sql.Rows) error {
		report, err := scanCatalogReport(rows)
		if err != nil {
			return err
		}
		view.Reports = append(view.Reports, report)
		return nil
	}); err != nil {
		return view, err
	}
	err = readCatalogRows(ctx, tx, `SELECT path_config,cursor FROM model_probe_cursors ORDER BY path_config`, func(rows *sql.Rows) error {
		var key, cursor string
		if err := rows.Scan(&key, &cursor); err != nil {
			return err
		}
		view.ProbeCursor[key] = cursor
		return nil
	})
	return view, err
}

// readCatalogRows closes each result before the next query, including faults.
func readCatalogRows(ctx context.Context, tx *sql.Tx, query string, scan func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

type catalogReportScanner interface{ Scan(...any) error }

func scanCatalogReport(row catalogReportScanner) (modelcatalog.RefreshReport, error) {
	var report modelcatalog.RefreshReport
	var id, raw string
	var pending bool
	if err := row.Scan(&id, &raw, &pending); err != nil {
		return report, err
	}
	return decodeCatalogReport(id, raw, pending)
}

func decodeCatalogReport(id, raw string, pending bool) (modelcatalog.RefreshReport, error) {
	var report modelcatalog.RefreshReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return report, fmt.Errorf("decoding catalog report: %w", err)
	}
	if id == "" || report.ID != id || report.At.IsZero() {
		return report, errors.New("catalog report: invalid identity/time")
	}
	report.PublicationPending = pending
	return report, nil
}
func catalogReportTx(ctx context.Context, tx *sql.Tx, id string) (modelcatalog.RefreshReport, bool, error) {
	report, err := scanCatalogReport(tx.QueryRowContext(ctx, `SELECT id,report,publication_pending FROM model_refresh_reports WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return report, false, nil
	}
	return report, err == nil, err
}

// Recover isolates malformed pending reports without acknowledging them. The
// original bytes and report/advancement links remain intact; RefreshService
// replays their independently durable journal rows after the healthy reports.
// Every cycle retries decoding, so repairing a row restores normal publication.
func (r *ModelCatalogRepo) Recover(ctx context.Context) ([]modelcatalog.RefreshReport, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,report,publication_pending FROM model_refresh_reports WHERE publication_pending=1 ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type storedReport struct {
		id, raw string
		pending bool
	}
	var stored, damaged []storedReport
	var reports []modelcatalog.RefreshReport
	for rows.Next() {
		var row storedReport
		if err := rows.Scan(&row.id, &row.raw, &row.pending); err != nil {
			return nil, err
		}
		stored = append(stored, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Close the report cursor before reading its relational journal intent in
	// the same transaction. Read/storage faults must not masquerade as damage.
	for _, row := range stored {
		report, err := decodeCatalogReport(row.id, row.raw, row.pending)
		if err == nil {
			var matches bool
			matches, err = catalogReportChangesMatchTx(ctx, tx, report)
			if err != nil {
				return nil, err
			}
			if matches {
				reports = append(reports, report)
				continue
			}
		}
		damaged = append(damaged, row)
	}
	for _, d := range damaged {
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_refresh_report_quarantine(report_id,report) VALUES(?,?) ON CONFLICT(report_id,report) DO NOTHING`, d.id, d.raw); err != nil {
			return nil, fmt.Errorf("quarantining catalog report: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, d := range damaged {
		slog.Warn("malformed catalog report quarantined; publication retained in advancement journal", "report_id", d.id)
	}
	return reports, nil
}

// catalogReportChangesMatchTx treats relational links as the report's intent,
// never IDs supplied by its JSON. Compare every journal field as well as the
// complete ID set: omissions and fabricated cache revisions are corrupt too.
// A replay may link a journal row created by an earlier refresh, so its original
// RefreshID is preserved rather than assumed to equal this report's ID.
func catalogReportChangesMatchTx(ctx context.Context, tx *sql.Tx, report modelcatalog.RefreshReport) (bool, error) {
	expected := make(map[string]provider.Advancement, len(report.Changes))
	for _, change := range report.Changes {
		if change.ID == "" {
			return false, nil
		}
		if _, duplicate := expected[change.ID]; duplicate {
			return false, nil
		}
		expected[change.ID] = change
	}
	rows, err := tx.QueryContext(ctx, `SELECT j.id,j.refresh_id,j.alias,j.from_model,j.to_model,j.canonical_id,j.revision,j.at
FROM model_refresh_advancements a JOIN profile_advancement_journal j ON j.id=a.advancement_id WHERE a.report_id=?`, report.ID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var got provider.Advancement
		var at string
		if err := rows.Scan(&got.ID, &got.RefreshID, &got.Alias, &got.FromModel, &got.ToModel, &got.CanonicalID, &got.Revision, &at); err != nil {
			return false, err
		}
		got.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return false, err
		}
		want, ok := expected[got.ID]
		if !ok || got.RefreshID != want.RefreshID || got.Alias != want.Alias || got.FromModel != want.FromModel || got.ToModel != want.ToModel || got.CanonicalID != want.CanonicalID || got.Revision != want.Revision || !got.At.Equal(want.At) {
			return false, nil
		}
		delete(expected, got.ID)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return len(expected) == 0, nil
}

// MarkPublished acknowledges precisely the report's actual journal rows. It
// never changes the frozen event payload or performs another profile update.
func (r *ModelCatalogRepo) MarkPublished(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	report, found, err := catalogReportTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("catalog report not found")
	}
	matches, err := catalogReportChangesMatchTx(ctx, tx, report)
	if err != nil {
		return err
	}
	if !matches {
		return errors.New("catalog publication: report changes disagree with journal intent")
	}
	ids := make([]string, 0, len(report.Changes))
	for _, change := range report.Changes {
		ids = append(ids, change.ID)
	}
	if err := acknowledgeCatalogTx(ctx, tx, ids); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE model_refresh_reports SET publication_pending=0 WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *ModelCatalogRepo) Unpublished(ctx context.Context) ([]provider.Advancement, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT j.id,j.refresh_id,j.alias,j.from_model,j.to_model,j.canonical_id,j.revision,j.at FROM profile_advancement_journal j LEFT JOIN model_publication_acks a ON a.advancement_id=j.id WHERE a.advancement_id IS NULL ORDER BY j.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []provider.Advancement
	for rows.Next() {
		var change provider.Advancement
		var at string
		if err := rows.Scan(&change.ID, &change.RefreshID, &change.Alias, &change.FromModel, &change.ToModel, &change.CanonicalID, &change.Revision, &at); err != nil {
			return nil, err
		}
		if change.At, err = time.Parse(time.RFC3339, at); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}
func (r *ModelCatalogRepo) Acknowledge(ctx context.Context, ids []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := acknowledgeCatalogTx(ctx, tx, ids); err != nil {
		return err
	}
	return tx.Commit()
}
func acknowledgeCatalogTx(ctx context.Context, tx *sql.Tx, ids []string) error {
	for _, id := range ids {
		if id == "" {
			return errors.New("catalog publication: missing advancement id")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_publication_acks(advancement_id) VALUES(?) ON CONFLICT(advancement_id) DO NOTHING`, id); err != nil {
			return err
		}
	}
	return nil
}
