package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func catalogCycle(t *testing.T, db testDB) modelcatalog.CycleCommit {
	t.Helper()
	ctx := context.Background()
	profiles := NewProfileRepo(db.DB)
	require.NoError(t, profiles.Create(ctx, followingProfile("worker")))
	p, err := profiles.Get(ctx, "worker")
	require.NoError(t, err)
	req := advanceReq("advance-1", p, "vendor/version-y")
	req.CanonicalID = "vendor/y"
	at := req.At
	enabled := true
	return modelcatalog.CycleCommit{
		Report:   modelcatalog.RefreshReport{ID: "cycle-1", At: at, SliceStatus: map[string]string{"vendor": "ok"}, PathStatus: map[string]string{"path": "confirmed"}},
		Catalog:  modelcatalog.CatalogSnapshot{FetchedAt: at, Slices: []modelcatalog.CatalogSlice{{Provider: "vendor", Status: modelcatalog.SliceStatusOK, Raw: json.RawMessage(`{"models":{}}`), Models: []modelidentity.CatalogModel{{CatalogProvider: "vendor", NativeID: "version-y", CanonicalModelID: "vendor/y", ReleaseDate: "2026-10-01", SourceRef: "https://source.invalid/catalog"}}, Entries: []modelidentity.Entry{{CanonicalID: "vendor/y", Provider: "vendor", NativeID: "version-y", SpecificModel: req.Model, Tier: "premium", OrderKey: "2", Immutable: true, ReleaseDate: "2026-10-01", PolicyRevision: 1}}}}},
		Evidence: []modelcatalog.UsabilityEvidence{{CanonicalID: "vendor/y", PathKey: "path", NativeModel: req.Model, ConfigRevision: "config-1", CheckedAt: at, ProofRef: "/private/proof", State: "confirmed"}},
		Advances: []provider.AdvanceRequest{req}, WebhookConfigured: &enabled,
		ProbeCursor: map[string]string{"path/config-1": "vendor/y"}, UnmatchedTargets: []provider.FollowTarget{{Provider: "unknown", Tier: "unsupported"}},
	}
}

func catalogEvents(t *testing.T, db testDB) []modelcatalog.Event {
	t.Helper()
	rows, err := db.DB.Query(`SELECT payload, state FROM model_announcement_events ORDER BY canonical_id`)
	require.NoError(t, err)
	defer rows.Close()
	var events []modelcatalog.Event
	for rows.Next() {
		var raw, state string
		require.NoError(t, rows.Scan(&raw, &state))
		var e modelcatalog.Event
		require.NoError(t, json.Unmarshal([]byte(raw), &e.Payload))
		e.State = state
		events = append(events, e)
	}
	require.NoError(t, rows.Err())
	return events
}

func TestCycleCommitsModelJournalAndEvent(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewModelCatalogRepo(db.DB)
	c := catalogCycle(t, db)
	// Proposed report contents are not authoritative changes/observations.
	c.Report.Changes = []provider.Advancement{{Alias: "forged"}}
	c.Report.NewlyUsable = []modelcatalog.Observation{{Entry: modelidentity.Entry{CanonicalID: "forged"}}}
	report, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	p, err := NewProfileRepo(db.DB).Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, c.Advances[0].Model, p.Model)
	require.Len(t, report.Changes, 1)
	assert.Equal(t, p.Revision, report.Changes[0].Revision)
	assert.Equal(t, "worker", report.Changes[0].Alias)
	assert.True(t, report.PublicationPending)
	require.Len(t, report.NewlyUsable, 1)
	events := catalogEvents(t, db)
	require.Len(t, events, 1)
	assert.Equal(t, report.Changes, events[0].Payload.Changes)
	assert.Equal(t, c.Report.At, events[0].Payload.FirstConfirmedAt)
	assert.Equal(t, "pending", events[0].State)
	assert.NotEmpty(t, events[0].Payload.EventID)
	assert.Equal(t, 1, journalCount(t, db, "worker"))
	view, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, c.Catalog, view.Catalog)
	assert.Equal(t, c.Evidence, view.Evidence)
	assert.Equal(t, c.ProbeCursor, view.ProbeCursor)
	assert.Equal(t, c.UnmatchedTargets, view.UnmatchedTargets)
	// A cycle ID is idempotent, even when a caller repeats its old proposals.
	again, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	assert.Equal(t, report, again)
	assert.Equal(t, 1, journalCount(t, db, "worker"))
	assert.Len(t, catalogEvents(t, db), 1)
	pending, err := repo.Unpublished(ctx)
	require.NoError(t, err)
	assert.Equal(t, report.Changes, pending)
	require.NoError(t, repo.MarkPublished(ctx, report.ID))
	pending, err = repo.Unpublished(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)
	recovered, err := repo.Recover(ctx)
	require.NoError(t, err)
	assert.Empty(t, recovered)
	// Acknowledgement is not a new CAS or journal write.
	assert.Equal(t, 1, journalCount(t, db, "worker"))
}

func TestCatalogCycleNoFollowersMultiPathAndFrozenPayload(t *testing.T) {
	for _, classification := range []struct {
		name, state string
		setting     *bool
	}{{"configured", "pending", boolPointer(true)}, {"empty", "suppressed", boolPointer(false)}, {"read_error", "awaiting_setting", nil}} {
		t.Run(classification.name, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			c := catalogCycle(t, db)
			c.Advances = nil
			c.WebhookConfigured = classification.setting
			c.Catalog.Slices[0].Entries[0].Tier = ""
			c.Catalog.Slices[0].Entries[0].OrderKey = ""
			second := c.Evidence[0]
			second.PathKey = "other-native-path"
			c.Evidence = append(c.Evidence, second)
			first, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			require.Len(t, first.NewlyUsable, 1)
			assert.Len(t, first.NewlyUsable[0].Paths, 2)
			events := catalogEvents(t, db)
			require.Len(t, events, 1)
			frozen := events[0].Payload
			assert.Empty(t, frozen.Changes)
			assert.Equal(t, classification.state, events[0].State)
			// Reappearance, a later advancement and a successful setting read may not
			// rewrite the first observation or its original empty changes/path payload.
			c.Report.ID = "cycle-2"
			c.Report.At = c.Report.At.Add(time.Hour)
			c.Advances = []provider.AdvanceRequest{advanceReq("later", cProfile(t, db), "vendor/version-y")}
			c.Advances[0].CanonicalID = "vendor/y"
			c.Catalog.Slices[0].Entries[0].Tier = "premium"
			c.Catalog.Slices[0].Entries[0].OrderKey = "2"
			c.WebhookConfigured = boolPointer(true)
			next, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			assert.Empty(t, next.NewlyUsable)
			require.Len(t, next.Changes, 1)
			events = catalogEvents(t, db)
			require.Len(t, events, 1)
			assert.Equal(t, frozen, events[0].Payload)
			if classification.state == "awaiting_setting" {
				assert.Equal(t, "pending", events[0].State)
			} else {
				assert.Equal(t, classification.state, events[0].State)
			}
		})
	}
}
func boolPointer(v bool) *bool { return &v }
func cProfile(t *testing.T, db testDB) provider.Profile {
	t.Helper()
	p, e := NewProfileRepo(db.DB).Get(context.Background(), "worker")
	require.NoError(t, e)
	return p
}

func TestCatalogCycleRejectsEmptyEvidenceAndStaleGlobalRevision(t *testing.T) {
	for _, name := range []string{"nil_evidence", "empty_confirmed", "unknown", "missing_proof", "wrong_native", "other_spelling", "stale_policy", "stale_connection", "missing_token", "stale_config_evidence", "no_path_refs", "unproved_path"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			c := catalogCycle(t, db)
			switch name {
			case "nil_evidence":
				c.Evidence = nil
			case "empty_confirmed":
				c.Evidence = []modelcatalog.UsabilityEvidence{{State: "confirmed"}}
			case "unknown":
				c.Evidence[0].State = "unknown"
			case "missing_proof":
				c.Evidence[0].ProofRef = ""
			case "wrong_native":
				c.Evidence[0].NativeModel = "different"
			case "other_spelling":
				// Paired row 4: the slice carries a second, routing-prefixed
				// spelling of the same canonical model. Proof of that spelling is
				// a real catalog identity, but the profile would execute the
				// advanced spelling, which was never probed on this path.
				sl := &c.Catalog.Slices[0]
				sl.Models = append(sl.Models, modelidentity.CatalogModel{CatalogProvider: "vendor", NativeID: "routed/version-y", CanonicalModelID: "vendor/y", ReleaseDate: "2026-10-01", SourceRef: "https://source.invalid/catalog"})
				sl.Entries = append(sl.Entries, modelidentity.Entry{CanonicalID: "vendor/y", Provider: "vendor", NativeID: "routed/version-y", SpecificModel: "routed/version-y", Tier: "premium", OrderKey: "2", Immutable: true, ReleaseDate: "2026-10-01", PolicyRevision: 1})
				c.Evidence[0].NativeModel = "routed/version-y"
			case "stale_policy":
				_, err := db.DB.Exec(`UPDATE model_identity_policy SET revision=revision+1`)
				require.NoError(t, err)
			case "stale_connection":
				c.Advances[0].ExecutionRevision.Connections = map[string]int64{"deleted": 1}
			case "missing_token":
				c.Advances[0].ExecutionRevision = provider.ExecutionRevision{}
			case "stale_config_evidence":
				// The probe confirmed Y before a credential rotation; the current
				// path configuration has no proof of its own.
				c.Evidence[0].ConfigRevision = "config-0"
			case "no_path_refs":
				c.Advances[0].Evidence = nil
			case "unproved_path":
				c.Advances[0].Evidence = append(c.Advances[0].Evidence, provider.PathEvidenceRef{PathKey: "other-path", ConfigRevision: "config-1"})
			}
			_, err := repo.CommitCycle(ctx, c)
			require.Error(t, err)
			assert.Equal(t, c.Advances[0].Expected, cProfile(t, db))
			assert.Zero(t, journalCount(t, db, "worker"))
			assert.Empty(t, catalogEvents(t, db))
			v, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Empty(t, v.Reports)
			assert.Empty(t, v.ProbeCursor)
		})
	}
	// Empty evidence is permitted for catalog-only discovery, never first usable.
	db := setupTestDB(t)
	repo := NewModelCatalogRepo(db.DB)
	c := catalogCycle(t, db)
	c.Advances = nil
	c.Evidence = nil
	report, err := repo.CommitCycle(context.Background(), c)
	require.NoError(t, err)
	assert.Empty(t, report.NewlyUsable)
	assert.Empty(t, catalogEvents(t, db))
}

func TestCatalogCycleConflictSkipsOnlyStaleProfile(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewModelCatalogRepo(db.DB)
	c := catalogCycle(t, db)
	profiles := NewProfileRepo(db.DB)
	require.NoError(t, profiles.Create(ctx, followingProfile("other")))
	other, err := profiles.Get(ctx, "other")
	require.NoError(t, err)
	req := advanceReq("advance-2", other, c.Advances[0].Model)
	req.CanonicalID = c.Advances[0].CanonicalID
	c.Advances = append(c.Advances, req)
	edited := c.Advances[0].Expected
	edited.AllowedTools = []string{"Read"}
	require.NoError(t, profiles.Update(ctx, edited))
	report, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	assert.Equal(t, "other", report.Changes[0].Alias)
	assert.Equal(t, "advance_conflict", report.PathStatus["profile:worker"])
	assert.Equal(t, report.Changes, catalogEvents(t, db)[0].Payload.Changes)
	assert.Zero(t, journalCount(t, db, "worker"))
}

// A model whose proposals were all dropped (DeferDiscovery) or all lost their
// CAS is not first-observed by that cycle: its evidence persists, and the cycle
// that commits the advancement freezes the one event with that change.
func TestCatalogCycleDefersDiscoveryUntilAdvancementCommits(t *testing.T) {
	for _, name := range []string{"dropped_before_commit", "every_proposal_conflicts"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			c := catalogCycle(t, db)
			proposal := c.Advances[0]
			proposalProof := c.Evidence[0]
			// Unrelated discovery must not wait for this model's proposal.
			unrelated := c.Catalog.Slices[0].Entries[0]
			unrelated.CanonicalID, unrelated.NativeID, unrelated.SpecificModel = "vendor/no-followers", "no-followers", "vendor/no-followers"
			unrelated.Tier, unrelated.OrderKey, unrelated.Immutable = "", "", false
			c.Catalog.Slices[0].Entries = append(c.Catalog.Slices[0].Entries, unrelated)
			cm := c.Catalog.Slices[0].Models[0]
			cm.NativeID, cm.CanonicalModelID = unrelated.NativeID, unrelated.CanonicalID
			c.Catalog.Slices[0].Models = append(c.Catalog.Slices[0].Models, cm)
			proof := c.Evidence[0]
			proof.CanonicalID, proof.NativeModel = unrelated.CanonicalID, unrelated.SpecificModel
			c.Evidence = append(c.Evidence, proof)
			c.ProbeCursor["unrelated/config-1"] = unrelated.CanonicalID
			switch name {
			case "dropped_before_commit":
				c.Advances = nil
				c.DeferDiscovery = []string{proposal.CanonicalID}
			case "every_proposal_conflicts":
				edited := proposal.Expected
				edited.AllowedTools = []string{"Read"}
				require.NoError(t, NewProfileRepo(db.DB).Update(ctx, edited))
			}
			first, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			assert.Empty(t, first.Changes)
			require.Len(t, first.NewlyUsable, 1)
			assert.Equal(t, unrelated, first.NewlyUsable[0].Entry)
			events := catalogEvents(t, db)
			require.Len(t, events, 1, "only the unrelated no-follower discovery is announced")
			unrelatedEvent := events[0]
			assert.Equal(t, unrelated.CanonicalID, unrelatedEvent.Payload.CanonicalID)
			assert.Empty(t, unrelatedEvent.Payload.Changes)
			assert.Equal(t, []modelcatalog.UsabilityEvidence{proof}, unrelatedEvent.Payload.Paths)
			assert.Zero(t, journalCount(t, db, "worker"))
			assert.Equal(t, proposal.Expected.Model, cProfile(t, db).Model)
			view, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.ElementsMatch(t, c.Evidence, view.Evidence, "both confirmations are kept")
			assert.Equal(t, c.ProbeCursor, view.ProbeCursor)

			c.Report.ID, c.Report.At = "cycle-2", c.Report.At.Add(time.Hour)
			c.DeferDiscovery = nil
			c.Evidence = nil // The retry must consume the durable unchanged proof.
			c.Advances = []provider.AdvanceRequest{advanceReq("advance-2", cProfile(t, db), proposal.Model)}
			c.Advances[0].CanonicalID = proposal.CanonicalID
			next, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			require.Len(t, next.Changes, 1)
			require.Len(t, next.NewlyUsable, 1)
			events = catalogEvents(t, db)
			require.Len(t, events, 2)
			assert.Equal(t, unrelatedEvent, events[0], "unrelated payload remains frozen")
			assert.Equal(t, proposal.CanonicalID, events[1].Payload.CanonicalID)
			assert.Equal(t, next.Changes, events[1].Payload.Changes)
			assert.Equal(t, c.Report.At, events[1].Payload.FirstConfirmedAt)
			assert.Equal(t, []modelcatalog.UsabilityEvidence{proposalProof}, events[1].Payload.Paths)
			assert.Equal(t, proposal.Model, cProfile(t, db).Model)
			assert.Equal(t, 1, journalCount(t, db, "worker"))
			again, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			assert.Equal(t, next, again)
			assert.Equal(t, events, catalogEvents(t, db))
			assert.Equal(t, 1, journalCount(t, db, "worker"))
		})
	}
}

func TestCatalogCycleWriteFaultsRollBackEverything(t *testing.T) {
	for _, table := range []string{"model_announcement_events", "model_refresh_reports", "profile_advancement_journal", "model_probe_cursors"} {
		t.Run(table, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			c := catalogCycle(t, db)
			_, err := db.DB.Exec(fmt.Sprintf(`CREATE TRIGGER fail_cycle BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT,'injected'); END`, table))
			require.NoError(t, err)
			_, err = repo.CommitCycle(ctx, c)
			require.Error(t, err)
			assert.Equal(t, c.Advances[0].Expected, cProfile(t, db))
			assert.Zero(t, journalCount(t, db, "worker"))
			assert.Empty(t, catalogEvents(t, db))
			view, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Empty(t, view.Catalog.Slices)
			assert.Empty(t, view.Evidence)
			assert.Empty(t, view.ProbeCursor)
		})
	}
}

func TestCatalogCycleReopenRetainsHistoryEvidenceCursorAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	sqlDB, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(sqlDB))
	db := testDB{DB: sqlDB}
	repo := NewModelCatalogRepo(sqlDB)
	c := catalogCycle(t, db)
	first, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	c.Report.ID = "cycle-2"
	c.Advances = nil
	c.Catalog.Slices[0].Status = modelcatalog.SliceStatusInvalid
	c.Catalog.Slices[0].Entries = nil
	c.Catalog.Slices[0].Models = nil
	c.Evidence = nil
	_, err = repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	sqlDB, err = Open(path)
	require.NoError(t, err)
	defer sqlDB.Close()
	repo = NewModelCatalogRepo(sqlDB)
	db.DB = sqlDB
	view, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, view.Catalog.Slices, 1)
	assert.Equal(t, modelcatalog.SliceStatusOK, view.Catalog.Slices[0].Status)
	assert.Equal(t, "2026-10-01", view.Catalog.Slices[0].Models[0].ReleaseDate)
	assert.Equal(t, "vendor/y", view.Catalog.Slices[0].Models[0].CanonicalModelID)
	assert.Equal(t, c.ProbeCursor, view.ProbeCursor)
	assert.Len(t, view.Evidence, 1)
	assert.Equal(t, c.UnmatchedTargets, view.UnmatchedTargets)
	reports, err := repo.Recover(ctx)
	require.NoError(t, err)
	require.Len(t, reports, 2)
	assert.Equal(t, first, reports[0])
	// Disappeared offerings retain source model and policy/order metadata.
	c.Report.ID = "cycle-3"
	c.Catalog.Slices = []modelcatalog.CatalogSlice{{Provider: "vendor", Status: modelcatalog.SliceStatusOK}}
	_, err = repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	view, err = repo.Snapshot(ctx)
	require.NoError(t, err)
	assert.Empty(t, view.Catalog.Slices[0].Entries)
	require.Len(t, view.HistoricalModels, 1)
	assert.Equal(t, "https://source.invalid/catalog", view.HistoricalModels[0].SourceRef)
	require.Len(t, view.HistoricalEntries, 1)
	assert.Equal(t, int64(1), view.HistoricalEntries[0].PolicyRevision)
	assert.Equal(t, "2", view.HistoricalEntries[0].OrderKey)
	// Acknowledgement failure is recoverable and atomic, not a second CAS.
	_, err = sqlDB.Exec(`CREATE TRIGGER fail_ack BEFORE INSERT ON model_publication_acks BEGIN SELECT RAISE(ABORT,'ack fault'); END`)
	require.NoError(t, err)
	require.Error(t, repo.MarkPublished(ctx, first.ID))
	reports, err = repo.Recover(ctx)
	require.NoError(t, err)
	assert.Len(t, reports, 3)
	_, err = sqlDB.Exec(`DROP TRIGGER fail_ack`)
	require.NoError(t, err)
	require.NoError(t, repo.MarkPublished(ctx, first.ID))
	require.NoError(t, repo.MarkPublished(ctx, first.ID))
	require.Error(t, repo.MarkPublished(ctx, "missing"))
	assert.Equal(t, 1, journalCount(t, db, "worker"))
	assert.Len(t, catalogEvents(t, db), 1)
}

func TestCatalogPublicationJournalWithoutReport(t *testing.T) {
	ctx := context.Background()
	profiles, db := newProfileRepo(t)
	require.NoError(t, profiles.Create(ctx, followingProfile("worker")))
	req := advanceReq("direct", cProfile(t, db), "vendor/version-y")
	adv, err := profiles.CommitAdvance(ctx, req)
	require.NoError(t, err)
	repo := NewModelCatalogRepo(db.DB)
	pending, err := repo.Unpublished(ctx)
	require.NoError(t, err)
	assert.Equal(t, []provider.Advancement{adv}, pending)
	require.Error(t, repo.Acknowledge(ctx, []string{adv.ID, "not-journaled"}))
	pending, err = repo.Unpublished(ctx)
	require.NoError(t, err)
	assert.Len(t, pending, 1)
	require.NoError(t, repo.Acknowledge(ctx, []string{adv.ID, adv.ID}))
	require.NoError(t, repo.Acknowledge(ctx, []string{adv.ID}))
	pending, err = repo.Unpublished(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)
	assert.Zero(t, func() int {
		var n int
		require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM model_refresh_reports`).Scan(&n))
		return n
	}())
}

func TestCatalogMigrationFillsGapWithoutResettingSecretState(t *testing.T) {
	sqlDB, err := Open(":memory:")
	require.NoError(t, err)
	defer sqlDB.Close()
	db := testDB{DB: sqlDB}
	_, err = sqlDB.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	// Apply the actual historical migration files, omitting the task-order
	// gaps. 024's forward-only down migration cannot recreate this state.
	files, err := migrationsFS.ReadDir("migrations")
	require.NoError(t, err)
	for _, file := range files {
		name := file.Name()
		if !strings.HasSuffix(name, ".up.sql") || strings.HasPrefix(name, "024_") || strings.HasPrefix(name, "026_") || strings.HasPrefix(name, "030_") {
			continue
		}
		var version int
		_, err := fmt.Sscanf(name, "%d_", &version)
		require.NoError(t, err)
		// The gap predates 036, which alters 026's table; RunMigrations applies
		// 026 before it in version order.
		if version >= 36 {
			continue
		}
		raw, err := migrationsFS.ReadFile("migrations/" + name)
		require.NoError(t, err)
		_, err = sqlDB.Exec(string(raw))
		require.NoError(t, err)
		_, err = sqlDB.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, version)
		require.NoError(t, err)
	}
	_, err = sqlDB.Exec(`UPDATE model_state_meta SET secret_initialized=1`)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db.DB))
	require.NoError(t, RunMigrations(db.DB))
	var initialized, n int
	require.NoError(t, db.DB.QueryRow(`SELECT secret_initialized FROM model_state_meta WHERE id=1`).Scan(&initialized))
	assert.Equal(t, 1, initialized)
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&n))
	assert.Zero(t, n)
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM model_usability_evidence WHERE catalog_provider = ''`).Scan(&n))
	assert.Zero(t, n)
}

// G8/paired:7: a discovery-only cycle carries no advancement token, so its
// classification revision is the only thing that can refuse a stale tier.
func TestCatalogCycleDiscoveryOnlyRejectsStaleClassification(t *testing.T) {
	for _, name := range []string{"policy_moved", "mixed_entries", "unreadable_policy", "policy_storage_fault", "catalog_storage_fault"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "policy-race.db")
			sqlDB, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { sqlDB.Close() })
			require.NoError(t, RunMigrations(sqlDB))
			db := testDB{DB: sqlDB}
			repo := NewModelCatalogRepo(sqlDB)
			c := catalogCycle(t, db)
			c.Advances = nil
			c.PolicyRevision = 1
			c.Evidence[0].Provider = "vendor"
			// A separate connection bypasses process-local locks, as an external
			// policy writer can do after the producer captured classification.
			writer, err := Open(path)
			require.NoError(t, err)
			defer writer.Close()
			_, err = writer.Exec(`CREATE TABLE saved_policy AS SELECT * FROM model_identity_policy`)
			require.NoError(t, err)
			want := provider.ErrExecutionRevisionStale
			switch name {
			case "policy_moved":
				policyRepo := NewModelIdentityPolicyRepo(writer)
				policy, err := policyRepo.Snapshot(ctx)
				require.NoError(t, err)
				require.NotEmpty(t, policy.Rules)
				policy.Rules[0].Tier = "edited-tier"
				_, err = policyRepo.Replace(ctx, policy.Revision, policy.Rules)
				require.NoError(t, err)
			case "mixed_entries":
				c.Catalog.Slices[0].Entries[0].PolicyRevision = 2
			case "unreadable_policy":
				_, err := writer.Exec(`DELETE FROM model_identity_policy`)
				require.NoError(t, err)
				want = provider.ErrExecutionRevisionUnavailable
			case "policy_storage_fault":
				_, err := writer.Exec(`ALTER TABLE model_identity_policy RENAME TO unavailable_policy`)
				require.NoError(t, err)
				want = provider.ErrExecutionRevisionUnavailable
			case "catalog_storage_fault":
				_, err := writer.Exec(`CREATE TRIGGER fail_discovery BEFORE INSERT ON model_catalog_models BEGIN SELECT RAISE(ABORT,'injected'); END`)
				require.NoError(t, err)
			}
			_, err = repo.CommitCycle(ctx, c)
			if name == "catalog_storage_fault" {
				assert.ErrorContains(t, err, "injected")
			} else {
				assert.ErrorIs(t, err, want)
			}
			assert.Empty(t, catalogEvents(t, db), "no stale tier is frozen")
			v, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Empty(t, v.Reports)
			assert.Empty(t, v.Catalog.Slices)
			assert.Empty(t, v.HistoricalEntries)
			assert.Empty(t, v.Evidence)
			assert.Empty(t, v.ProbeCursor)
			for _, table := range []string{"model_catalog_state", "model_catalog_models", "model_first_observations", "model_refresh_advancements"} {
				var n int
				require.NoError(t, sqlDB.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n))
				assert.Zero(t, n, table)
			}
			assert.Zero(t, journalCount(t, db, "worker"))

			switch name {
			case "unreadable_policy":
				_, err = writer.Exec(`INSERT INTO model_identity_policy SELECT * FROM saved_policy`)
				require.NoError(t, err)
			case "policy_storage_fault":
				_, err = writer.Exec(`ALTER TABLE unavailable_policy RENAME TO model_identity_policy`)
				require.NoError(t, err)
			case "catalog_storage_fault":
				_, err = writer.Exec(`DROP TRIGGER fail_discovery`)
				require.NoError(t, err)
			}
			policy, err := NewModelIdentityPolicyRepo(writer).Snapshot(ctx)
			require.NoError(t, err)
			c.PolicyRevision = policy.Revision
			c.Catalog.Slices[0].Entries[0].PolicyRevision = policy.Revision
			c.Catalog.Slices[0].Entries[0].Tier = "current-tier"
			// Same report ID: a failed stale transaction reserved nothing.
			report, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			require.Len(t, report.NewlyUsable, 1)
			assert.Equal(t, c.Catalog.Slices[0].Entries[0], report.NewlyUsable[0].Entry)
			assert.Empty(t, report.Changes)
			v, err = repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Equal(t, c.Catalog, v.Catalog)
			assert.Equal(t, c.Evidence, v.Evidence)
			assert.Equal(t, c.ProbeCursor, v.ProbeCursor)
			assert.Equal(t, []modelcatalog.RefreshReport{report}, v.Reports)
			events := catalogEvents(t, db)
			require.Len(t, events, 1)
			assert.Equal(t, "current-tier", events[0].Payload.Tier)
			assert.Equal(t, c.Evidence, events[0].Payload.Paths)
			assert.Empty(t, events[0].Payload.Changes)
		})
	}
	// Kept catalogs can still commit diagnostics after a policy edit, but a
	// superseded classification must never become a first observation.
	for _, revision := range []int64{1, 0} {
		db := setupTestDB(t)
		repo := NewModelCatalogRepo(db.DB)
		c := catalogCycle(t, db)
		c.Advances = nil
		c.PolicyRevision = revision
		_, err := db.DB.Exec(`UPDATE model_identity_policy SET revision=?`, 1+(1-revision))
		require.NoError(t, err)
		report, err := repo.CommitCycle(context.Background(), c)
		require.NoError(t, err, "revision %d", revision)
		if revision == 0 {
			assert.Empty(t, report.NewlyUsable)
			assert.Empty(t, catalogEvents(t, db))
			continue
		}
		require.Len(t, report.NewlyUsable, 1)
		assert.Equal(t, c.Catalog.Slices[0].Entries[0], report.NewlyUsable[0].Entry)
		events := catalogEvents(t, db)
		require.Len(t, events, 1)
		assert.Equal(t, c.Evidence, events[0].Payload.Paths)
		assert.Equal(t, c.Report.At, events[0].Payload.FirstConfirmedAt)
		assert.Empty(t, events[0].Payload.Changes)
	}
}

// The consecutive-failure count that drives probe backoff and retirement
// survives a commit/snapshot round trip.
func TestCatalogCyclePersistsEvidenceFailureCount(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewModelCatalogRepo(db.DB)
	c := catalogCycle(t, db)
	c.Advances = nil
	failed := c.Evidence[0]
	failed.PathKey, failed.State, failed.ProofRef, failed.Failures = "failing-path", modelcatalog.UsabilityUnknown, "", 3
	c.Evidence = append(c.Evidence, failed)
	_, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	view, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	got := map[string]int{}
	for _, ev := range view.Evidence {
		got[ev.PathKey] = ev.Failures
	}
	assert.Equal(t, map[string]int{"path": 0, "failing-path": 3}, got)
}
