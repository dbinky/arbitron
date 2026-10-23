package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type provenanceClock struct{ at time.Time }

func (c provenanceClock) Now() time.Time { return c.at }

// A first-spelling match must not reserve an event naming an unproved offering.
// Exercise transaction-produced events through the real queue and HTTP sender.
func TestCatalogAmbiguousLegacyProofDefersAnnouncement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	sqlDB, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { sqlDB.Close() })
	require.NoError(t, RunMigrations(sqlDB))
	db := testDB{DB: sqlDB}
	repo := NewModelCatalogRepo(sqlDB)
	c := provenanceCycle(t, db, "vendor/version-y")
	c.PolicyRevision = 1

	var mu sync.Mutex
	var cards []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || !json.Valid(raw) {
			http.Error(w, "invalid card", http.StatusBadRequest)
			return
		}
		mu.Lock()
		cards = append(cards, string(raw))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(receiver.Close)
	// Only the HTTP leaf is local; settings, claims, partitioning and markers
	// remain production implementations. Validation is covered at the root.
	_, err = sqlDB.Exec(`UPDATE system_settings SET value=? WHERE key='model_announcements.teams_webhook_url'`, receiver.URL)
	require.NoError(t, err)
	drain := func() {
		t.Helper()
		service := modelcatalog.NewAnnouncementService(NewModelAnnouncementRepo(sqlDB), settings.NewService(NewSettingsRepo(sqlDB)), notification.NewTeamsModelSender(receiver.Client()), provenanceClock{c.Report.At})
		require.NoError(t, service.Drain(ctx))
	}
	assertDeferred := func() {
		t.Helper()
		assert.Empty(t, catalogEvents(t, db), "ambiguous proof cannot freeze either offering")
		var observations int
		require.NoError(t, sqlDB.QueryRow(`SELECT COUNT(*) FROM model_first_observations`).Scan(&observations))
		assert.Zero(t, observations, "a deferred observation must remain retryable")
		drain()
		mu.Lock()
		defer mu.Unlock()
		assert.Empty(t, cards, "no unproved offering reaches Teams")
	}
	first, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	assert.Empty(t, first.NewlyUsable)
	assertDeferred()
	view, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, c.Evidence, view.Evidence, "legacy proof is retained, not relabelled")
	assert.Len(t, view.Reports, 1, "deferral must not abort the cycle")

	require.NoError(t, sqlDB.Close())
	sqlDB, err = Open(path)
	require.NoError(t, err)
	db.DB, repo = sqlDB, NewModelCatalogRepo(sqlDB)
	c.Report.ID, c.Report.At = "legacy-retry", c.Report.At.Add(time.Hour)
	c.Evidence = nil
	_, err = repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	assertDeferred()

	// A new configuration produces genuinely provider-stamped proof. Merely
	// replaying the legacy row above cannot supply this missing provenance.
	fresh := view.Evidence[0]
	fresh.Provider, fresh.ConfigRevision, fresh.CheckedAt = "vendor", "config-2", c.Report.At
	fresh.ProofRef = "/private/fresh-proof"
	c.Evidence = []modelcatalog.UsabilityEvidence{fresh}
	c.Report.ID = "fresh-proof"
	_, err = sqlDB.Exec(`CREATE TRIGGER fail_event BEFORE INSERT ON model_announcement_events BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)
	_, err = repo.CommitCycle(ctx, c)
	require.Error(t, err)
	assertDeferred()
	after, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, view.Evidence, after.Evidence, "event insertion failure rolls back fresh proof too")
	_, err = sqlDB.Exec(`DROP TRIGGER fail_event`)
	require.NoError(t, err)
	report, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	require.Len(t, report.NewlyUsable, 1)
	drain()
	drain()
	events := catalogEvents(t, db)
	require.Len(t, events, 1)
	assert.Equal(t, modelcatalog.EventAnnounced, events[0].State)
	assert.Equal(t, "vendor", events[0].Payload.Provider)
	assert.Equal(t, "premium", events[0].Payload.Tier)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, cards, 1)
	assert.Contains(t, cards[0], `"title":"Provider","value":"vendor"`)
	assert.Contains(t, cards[0], `"title":"Tier","value":"premium"`)
	assert.NotContains(t, cards[0], "/private/")
}

// provenanceCycle offers canonical vendor/y from two catalog providers. alpha
// sorts first and carries a different tier, so a card built from the first
// historical offering names the wrong provider and tier (G9, paired row 11).
func provenanceCycle(t *testing.T, db testDB, alphaSpelling string) modelcatalog.CycleCommit {
	t.Helper()
	c := catalogCycle(t, db)
	c.Advances = nil
	at := c.Report.At
	c.Report.SliceStatus["alpha"] = "ok"
	c.Catalog.Slices = append([]modelcatalog.CatalogSlice{{Provider: "alpha", Status: modelcatalog.SliceStatusOK, Raw: json.RawMessage(`{"models":{}}`),
		Models:  []modelidentity.CatalogModel{{CatalogProvider: "alpha", NativeID: alphaSpelling, CanonicalModelID: "vendor/y", ReleaseDate: "2026-10-01", SourceRef: "https://source.invalid/catalog"}},
		Entries: []modelidentity.Entry{{CanonicalID: "vendor/y", Provider: "alpha", NativeID: alphaSpelling, Tier: "economy", OrderKey: "1", Immutable: true, ReleaseDate: "2026-10-01", PolicyRevision: 1}},
	}}, c.Catalog.Slices...)
	c.Evidence[0].CheckedAt = at
	return c
}

func TestCatalogEventNamesConfirmingProvider(t *testing.T) {
	ctx := context.Background()
	t.Run("recorded provenance selects the confirming offering", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "provenance.db")
		sqlDB, err := Open(path)
		require.NoError(t, err)
		t.Cleanup(func() { sqlDB.Close() })
		require.NoError(t, RunMigrations(sqlDB))
		db := testDB{DB: sqlDB}
		repo := NewModelCatalogRepo(sqlDB)
		c := provenanceCycle(t, db, "alpha/version-y")
		c.PolicyRevision = 1
		c.Evidence[0].Provider = "vendor"
		first, err := repo.CommitCycle(ctx, c)
		require.NoError(t, err)
		require.Len(t, first.NewlyUsable, 1)
		assert.Equal(t, c.Catalog.Slices[1].Entries[0], first.NewlyUsable[0].Entry)
		assert.Equal(t, c.Evidence, first.NewlyUsable[0].Paths)
		events := catalogEvents(t, db)
		require.Len(t, events, 1)
		frozen := events[0]
		assert.Equal(t, "vendor", frozen.Payload.Provider)
		assert.Equal(t, "premium", frozen.Payload.Tier)
		assert.Equal(t, c.Evidence, frozen.Payload.Paths)
		assert.Empty(t, frozen.Payload.Changes)
		assert.Equal(t, c.Report.At, frozen.Payload.FirstConfirmedAt)
		var frozenJSON, observationJSON string
		require.NoError(t, sqlDB.QueryRow(`SELECT payload FROM model_announcement_events`).Scan(&frozenJSON))
		require.NoError(t, sqlDB.QueryRow(`SELECT observation FROM model_first_observations`).Scan(&observationJSON))

		require.NoError(t, sqlDB.Close())
		sqlDB, err = Open(path)
		require.NoError(t, err)
		db.DB, repo = sqlDB, NewModelCatalogRepo(sqlDB)
		view, err := repo.Snapshot(ctx)
		require.NoError(t, err)
		assert.Equal(t, c.Catalog, view.Catalog, "both distinct source offerings survive reopen")
		assert.Equal(t, c.Evidence, view.Evidence, "the exact confirming proof survives reopen")
		assert.Equal(t, []modelcatalog.Event{frozen}, catalogEvents(t, db))

		// A later path confirms the formerly unconfirmed, first-sorting offering
		// under a new classification policy. Neither can rewrite the first card.
		_, err = sqlDB.Exec(`UPDATE model_identity_policy SET revision=2 WHERE id=1`)
		require.NoError(t, err)
		c.PolicyRevision = 2
		c.Report.ID, c.Report.At = "later-confirmation", c.Report.At.Add(time.Hour)
		for i := range c.Catalog.Slices {
			entry := &c.Catalog.Slices[i].Entries[0]
			entry.PolicyRevision, entry.OrderKey, entry.Tier = 2, "99", "edited-tier"
		}
		later := c.Evidence[0]
		later.Provider, later.NativeModel, later.PathKey = "alpha", "alpha/version-y", "a-later-path"
		later.ConfigRevision, later.ProofRef, later.CheckedAt = "config-2", "/private/later-proof", c.Report.At
		c.Evidence = []modelcatalog.UsabilityEvidence{later}
		next, err := repo.CommitCycle(ctx, c)
		require.NoError(t, err)
		assert.Empty(t, next.NewlyUsable)
		assert.Empty(t, next.Changes)
		view, err = repo.Snapshot(ctx)
		require.NoError(t, err)
		require.Len(t, view.Evidence, 2, "the later confirmation really persisted")
		assert.Contains(t, view.Evidence, later)
		assert.Equal(t, []modelcatalog.Event{frozen}, catalogEvents(t, db))
		var payload, observation string
		require.NoError(t, sqlDB.QueryRow(`SELECT payload FROM model_announcement_events`).Scan(&payload))
		require.NoError(t, sqlDB.QueryRow(`SELECT observation FROM model_first_observations`).Scan(&observation))
		assert.Equal(t, frozenJSON, payload, "frozen bytes are never reserialized from today's offering")
		assert.Equal(t, observationJSON, observation)
	})
	t.Run("first proof with provenance wins when several providers confirm", func(t *testing.T) {
		db := setupTestDB(t)
		repo := NewModelCatalogRepo(db.DB)
		c := provenanceCycle(t, db, "vendor/version-y")
		c.Evidence[0].Provider = "vendor"
		alpha := c.Evidence[0]
		alpha.PathKey, alpha.Provider = "z-path", "alpha"
		c.Evidence = append(c.Evidence, alpha)
		_, err := repo.CommitCycle(ctx, c)
		require.NoError(t, err)
		events := catalogEvents(t, db)
		require.Len(t, events, 1, "one canonical event across confirming providers")
		assert.Equal(t, "vendor", events[0].Payload.Provider, "ordered by path, the provider is one that confirmed")
		assert.Len(t, events[0].Payload.Paths, 2)
	})
	t.Run("legacy proof without provenance matches by spelling", func(t *testing.T) {
		db := setupTestDB(t)
		repo := NewModelCatalogRepo(db.DB)
		c := provenanceCycle(t, db, "alpha/version-y")
		_, err := repo.CommitCycle(ctx, c)
		require.NoError(t, err)
		events := catalogEvents(t, db)
		require.Len(t, events, 1)
		assert.Equal(t, "vendor", events[0].Payload.Provider)
		view, err := repo.Snapshot(ctx)
		require.NoError(t, err)
		assert.Equal(t, c.Evidence, view.Evidence, "legacy empty provenance stays empty")
		assert.Equal(t, c.Evidence, events[0].Payload.Paths)
	})
	for _, name := range []string{"wrong_offering", "nonexistent_provider", "forged_spelling"} {
		t.Run(name, func(t *testing.T) {
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			c := provenanceCycle(t, db, "alpha/version-y")
			c.Evidence[0].Provider = "vendor"
			switch name {
			case "wrong_offering":
				c.Evidence[0].Provider = "alpha"
			case "nonexistent_provider":
				c.Evidence[0].Provider = "forged"
			case "forged_spelling":
				c.Evidence[0].NativeModel = "vendor/never-executed"
			}
			_, err := repo.CommitCycle(ctx, c)
			require.Error(t, err)
			assert.Empty(t, catalogEvents(t, db), "the refused cycle rolls back")
			view, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Empty(t, view.Evidence)
			assert.Empty(t, view.Reports)
			assert.Empty(t, view.HistoricalEntries)
		})
	}
	for _, table := range []string{"model_usability_evidence", "model_announcement_events"} {
		t.Run("rollback_"+table, func(t *testing.T) {
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			c := provenanceCycle(t, db, "alpha/version-y")
			c.Evidence[0].Provider = "vendor"
			_, err := db.DB.Exec(fmt.Sprintf(`CREATE TRIGGER fail_provenance BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT,'injected'); END`, table))
			require.NoError(t, err)
			_, err = repo.CommitCycle(ctx, c)
			require.Error(t, err)
			view, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Empty(t, view.Evidence)
			assert.Empty(t, view.Catalog.Slices)
			assert.Empty(t, view.Reports)
			assert.Empty(t, catalogEvents(t, db))
			var observations int
			require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM model_first_observations`).Scan(&observations))
			assert.Zero(t, observations)
			_, err = db.DB.Exec(`DROP TRIGGER fail_provenance`)
			require.NoError(t, err)
			_, err = repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			view, err = repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Equal(t, c.Evidence, view.Evidence)
			events := catalogEvents(t, db)
			require.Len(t, events, 1)
			assert.Equal(t, "vendor", events[0].Payload.Provider)
			assert.Equal(t, c.Evidence, events[0].Payload.Paths)
		})
	}
	t.Run("031 upgrade preserves legacy proof and frozen event", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "upgrade.db")
		sqlDB, err := Open(path)
		require.NoError(t, err)
		t.Cleanup(func() { sqlDB.Close() })
		_, err = sqlDB.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)`)
		require.NoError(t, err)
		files, err := migrationsFS.ReadDir("migrations")
		require.NoError(t, err)
		for _, file := range files {
			name := file.Name()
			if !strings.HasSuffix(name, ".up.sql") {
				continue
			}
			var version int
			_, err := fmt.Sscanf(name, "%d_", &version)
			require.NoError(t, err)
			if version > 35 {
				continue
			}
			raw, err := migrationsFS.ReadFile("migrations/" + name)
			require.NoError(t, err)
			_, err = sqlDB.Exec(string(raw))
			require.NoError(t, err)
			_, err = sqlDB.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, version)
			require.NoError(t, err)
		}
		db := testDB{DB: sqlDB}
		c := provenanceCycle(t, db, "alpha/version-y")
		legacy := c.Evidence[0]
		obs := modelcatalog.Observation{Entry: c.Catalog.Slices[1].Entries[0], Paths: c.Evidence, FirstConfirmedAt: c.Report.At}
		frozen := modelcatalog.AnnouncementPayload{EventID: "legacy-event", CanonicalID: legacy.CanonicalID, Provider: "vendor", Tier: "premium", FirstConfirmedAt: c.Report.At, Paths: []modelcatalog.UsabilityEvidence{legacy}}
		observation, err := json.Marshal(obs)
		require.NoError(t, err)
		payload, err := json.Marshal(frozen)
		require.NoError(t, err)
		// Genuine pre-032 SQL has no catalog_provider column or inferred provider.
		_, err = sqlDB.Exec(`INSERT INTO model_usability_evidence(canonical_id,path_key,config_revision,native_model,checked_at,proof_ref,state) VALUES(?,?,?,?,?,?,?)`, legacy.CanonicalID, legacy.PathKey, legacy.ConfigRevision, legacy.NativeModel, legacy.CheckedAt.Format(time.RFC3339Nano), legacy.ProofRef, legacy.State)
		require.NoError(t, err)
		_, err = sqlDB.Exec(`INSERT INTO model_first_observations(canonical_id,observation) VALUES(?,?)`, legacy.CanonicalID, string(observation))
		require.NoError(t, err)
		_, err = sqlDB.Exec(`INSERT INTO model_announcement_events(id,canonical_id,payload,state) VALUES(?,?,?,'pending')`, frozen.EventID, frozen.CanonicalID, string(payload))
		require.NoError(t, err)
		require.NoError(t, RunMigrations(sqlDB))
		require.NoError(t, RunMigrations(sqlDB))
		require.NoError(t, sqlDB.Close())
		sqlDB, err = Open(path)
		require.NoError(t, err)
		db.DB = sqlDB
		repo := NewModelCatalogRepo(sqlDB)
		view, err := repo.Snapshot(ctx)
		require.NoError(t, err)
		assert.Equal(t, c.Evidence, view.Evidence)
		// Today's provider is not a backfill for an unchanged legacy proof.
		c.Evidence[0].Provider = "vendor"
		_, err = repo.CommitCycle(ctx, c)
		require.NoError(t, err)
		view, err = repo.Snapshot(ctx)
		require.NoError(t, err)
		assert.Equal(t, []modelcatalog.UsabilityEvidence{legacy}, view.Evidence)
		events := catalogEvents(t, db)
		require.Len(t, events, 1)
		assert.Equal(t, frozen, events[0].Payload)
		var raw string
		require.NoError(t, sqlDB.QueryRow(`SELECT payload FROM model_announcement_events`).Scan(&raw))
		assert.Equal(t, string(payload), raw)
	})
}
