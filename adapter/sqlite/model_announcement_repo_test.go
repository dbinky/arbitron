package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var announcementAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// Events come from the real cycle transaction, not a second announcement producer.
func pendingAnnouncement(t *testing.T, db testDB, setting *bool) modelcatalog.AnnouncementPayload {
	t.Helper()
	c := catalogCycle(t, db)
	c.WebhookConfigured = setting
	_, err := NewModelCatalogRepo(db.DB).CommitCycle(context.Background(), c)
	require.NoError(t, err)
	return catalogEvents(t, db)[0].Payload
}

func storedAnnouncement(t *testing.T, db *sql.DB) modelcatalog.Event {
	t.Helper()
	var e modelcatalog.Event
	var payload, parts, confirmed, lease, next string
	require.NoError(t, db.QueryRow(`SELECT payload,state,claim_id,lease_until,attempts,next_attempt_at,last_code,parts,part_count,confirmed_parts FROM model_announcement_events`).Scan(&payload, &e.State, &e.ClaimID, &lease, &e.Attempts, &next, &e.LastCode, &parts, &e.PartCount, &confirmed))
	require.NoError(t, json.Unmarshal([]byte(payload), &e.Payload))
	require.NoError(t, json.Unmarshal([]byte(parts), &e.Parts))
	require.NoError(t, json.Unmarshal([]byte(confirmed), &e.ConfirmedParts))
	if lease != "" {
		var err error
		e.LeaseUntil, err = time.Parse(time.RFC3339Nano, lease)
		require.NoError(t, err)
	}
	if next != "" {
		var err error
		e.NextAttemptAt, err = time.Parse(time.RFC3339Nano, next)
		require.NoError(t, err)
	}
	return e
}

func TestAnnouncementClaimsOriginalPayloadAndOwnership(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	original := pendingAnnouncement(t, db, boolPointer(true))
	repo := NewModelAnnouncementRepo(db.DB)
	e, err := repo.ClaimPending(ctx, announcementAt, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, original, e.Payload)
	assert.Equal(t, 1, e.Attempts)
	assert.Equal(t, modelcatalog.EventInFlight, e.State)
	assert.NotEmpty(t, e.ClaimID)
	assert.Equal(t, announcementAt.Add(time.Minute), e.LeaseUntil)
	expected, err := notification.PartitionAnnouncement(original, modelcatalog.MaxAnnouncementBytes)
	require.NoError(t, err)
	assert.Equal(t, expected, e.Parts)
	assert.Equal(t, 1, e.PartCount)
	assert.Equal(t, e, storedAnnouncement(t, db.DB))
	_, err = repo.ClaimPending(ctx, announcementAt, time.Minute)
	require.ErrorIs(t, err, modelcatalog.ErrNoPendingAnnouncements)
	before := storedAnnouncement(t, db.DB)
	require.ErrorIs(t, repo.CompletePart(ctx, original.EventID, "wrong-owner", 1, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed}, announcementAt), modelcatalog.ErrAnnouncementClaimLost)
	require.Error(t, repo.CompletePart(ctx, original.EventID, e.ClaimID, 0, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed}, announcementAt))
	require.Error(t, repo.CompletePart(ctx, original.EventID, e.ClaimID, 1, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed}, time.Time{}))
	require.Error(t, repo.Complete(ctx, original.EventID, e.ClaimID, modelcatalog.SendOutcome{State: "invented"}, announcementAt))
	assert.Equal(t, before, storedAnnouncement(t, db.DB))
	require.NoError(t, repo.CompletePart(ctx, original.EventID, e.ClaimID, 1, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed, Code: "http_202"}, announcementAt))
	done := storedAnnouncement(t, db.DB)
	assert.Equal(t, modelcatalog.EventAnnounced, done.State)
	assert.Equal(t, []int{1}, done.ConfirmedParts)
	assert.Empty(t, done.ClaimID)
	assert.True(t, done.LeaseUntil.IsZero())
	assert.Equal(t, original, done.Payload)
	_, err = repo.ClaimPending(ctx, announcementAt.Add(48*time.Hour), time.Minute)
	require.ErrorIs(t, err, modelcatalog.ErrNoPendingAnnouncements)
}

func TestAnnouncementClaimsBackoffReopenAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "announcements.db")
	db, err := Open(path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	require.NoError(t, RunMigrations(db))
	original := pendingAnnouncement(t, testDB{db}, boolPointer(true))
	at := announcementAt
	for i, delay := range []time.Duration{4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour, 24 * time.Hour} {
		repo := NewModelAnnouncementRepo(db)
		e, err := repo.ClaimPending(ctx, at, time.Minute)
		require.NoError(t, err)
		require.Equal(t, i+1, e.Attempts)
		state := modelcatalog.SendDefiniteFailure
		want := modelcatalog.EventPending
		if i%2 == 1 {
			state = modelcatalog.SendAmbiguous
			want = modelcatalog.EventDeliveryUnknown
		}
		completed := at.Add(2 * time.Second)
		require.NoError(t, repo.Complete(ctx, original.EventID, e.ClaimID, modelcatalog.SendOutcome{State: state, Code: "http_503"}, completed))
		saved := storedAnnouncement(t, db)
		assert.Equal(t, want, saved.State)
		assert.Equal(t, completed.Add(delay), saved.NextAttemptAt)
		assert.Equal(t, original, saved.Payload)
		require.NoError(t, db.Close())
		db, err = Open(path)
		require.NoError(t, err)
		require.NoError(t, RunMigrations(db))
		assert.Equal(t, saved, storedAnnouncement(t, db))
		repo = NewModelAnnouncementRepo(db)
		_, err = repo.ClaimPending(ctx, saved.NextAttemptAt.Add(-time.Nanosecond), time.Minute)
		require.ErrorIs(t, err, modelcatalog.ErrNoPendingAnnouncements)
		at = saved.NextAttemptAt
	}
	repo := NewModelAnnouncementRepo(db)
	old, err := repo.ClaimPending(ctx, at, time.Minute)
	require.NoError(t, err)
	require.NoError(t, repo.RecoverClaims(ctx, at.Add(30*time.Second)))
	assert.Equal(t, modelcatalog.EventInFlight, storedAnnouncement(t, db).State)
	require.NoError(t, repo.RecoverClaims(ctx, at.Add(time.Minute)))
	recovered := storedAnnouncement(t, db)
	assert.Equal(t, modelcatalog.EventDeliveryUnknown, recovered.State)
	assert.Equal(t, old.Attempts, recovered.Attempts)
	assert.Equal(t, old.Payload, recovered.Payload)
	require.ErrorIs(t, repo.RenewClaim(ctx, original.EventID, old.ClaimID, at.Add(time.Minute), time.Minute), modelcatalog.ErrAnnouncementClaimLost)
	_, err = repo.ClaimPending(ctx, at.Add(time.Minute), time.Minute)
	require.ErrorIs(t, err, modelcatalog.ErrNoPendingAnnouncements)
	next, err := repo.ClaimPending(ctx, recovered.NextAttemptAt, time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, old.ClaimID, next.ClaimID)
	require.ErrorIs(t, repo.Complete(ctx, original.EventID, old.ClaimID, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed}, recovered.NextAttemptAt), modelcatalog.ErrAnnouncementClaimLost)
}

func TestAnnouncementDeferredClaimOwnershipAndRollback(t *testing.T) {
	for _, fault := range []string{"none", "wrong_owner", "expired", "part_index", "aborted", "ignored"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			p := pendingAnnouncement(t, db, boolPointer(true))
			repo := NewModelAnnouncementRepo(db.DB)
			e, err := repo.ClaimPending(ctx, announcementAt, time.Minute)
			require.NoError(t, err)
			before := storedAnnouncement(t, db.DB)
			claim, at := e.ClaimID, announcementAt
			switch fault {
			case "wrong_owner":
				claim = "stale-owner"
			case "expired":
				at = at.Add(time.Minute)
			case "aborted", "ignored":
				raise := "IGNORE"
				if fault == "aborted" {
					raise = "ABORT,'release fault'"
				}
				_, err = db.DB.Exec(`CREATE TRIGGER fail_release BEFORE UPDATE OF next_attempt_at ON model_announcement_events BEGIN SELECT RAISE(` + raise + `); END`)
				require.NoError(t, err)
			}
			outcome := modelcatalog.SendOutcome{State: "deferred", Code: "settings_unavailable"}
			if fault == "part_index" {
				err = repo.CompletePart(ctx, p.EventID, claim, 1, outcome, at)
			} else {
				err = repo.Complete(ctx, p.EventID, claim, outcome, at)
			}
			if fault != "none" {
				require.Error(t, err)
				if fault == "wrong_owner" || fault == "expired" {
					require.ErrorIs(t, err, modelcatalog.ErrAnnouncementClaimLost)
				}
				assert.Equal(t, before, storedAnnouncement(t, db.DB))
				return
			}
			require.NoError(t, err)
			saved := storedAnnouncement(t, db.DB)
			assert.Equal(t, modelcatalog.EventPending, saved.State)
			assert.Zero(t, saved.Attempts)
			assert.Equal(t, at, saved.NextAttemptAt)
			assert.Empty(t, saved.ClaimID)
			assert.True(t, saved.LeaseUntil.IsZero())
			assert.Equal(t, before.Payload, saved.Payload)
			assert.Equal(t, before.Parts, saved.Parts)
			assert.Equal(t, before.ConfirmedParts, saved.ConfirmedParts)
			_, err = repo.ClaimPending(ctx, at, time.Minute)
			require.NoError(t, err)
		})
	}
}

func TestAnnouncementClaimsMultipartFrozenPlan(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	payload := pendingAnnouncement(t, db, boolPointer(true))
	// Large immutable original changes are produced by the same schema; expand the
	// fixture before its first claim, never after any acknowledgement.
	payload.Changes = nil
	for i := 0; i < 180; i++ {
		payload.Changes = append(payload.Changes, provider.Advancement{Alias: fmt.Sprintf("alias-%03d-%s", i, strings.Repeat("a", 150)), FromModel: "old-specific", ToModel: payload.CanonicalID})
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE model_announcement_events SET payload=?`, string(raw))
	require.NoError(t, err)
	repo := NewModelAnnouncementRepo(db.DB)
	e, err := repo.ClaimPending(ctx, announcementAt, time.Minute)
	require.NoError(t, err)
	require.Greater(t, e.PartCount, 1)
	expected, err := notification.PartitionAnnouncement(payload, modelcatalog.MaxAnnouncementBytes)
	require.NoError(t, err)
	require.Equal(t, expected, e.Parts)
	// Simulate a previous serializer that packed the last card less densely.
	// It is a valid frozen plan, deliberately unlike today's partitioner. A
	// restart must not repartition it and reinterpret an acknowledged index.
	last := e.Parts[len(e.Parts)-1]
	require.Greater(t, len(last.Payload.Changes), 1)
	left, right := last, last
	middle := len(last.Payload.Changes) / 2
	left.Payload.Changes = slices.Clone(last.Payload.Changes[:middle])
	right.Payload.Paths = nil
	right.Payload.Changes = slices.Clone(last.Payload.Changes[middle:])
	expected = append(slices.Clone(e.Parts[:len(e.Parts)-1]), left, right)
	for i := range expected {
		expected[i].Index, expected[i].Count = i+1, len(expected)
	}
	legacyRaw, err := json.Marshal(expected)
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE model_announcement_events SET parts=?,part_count=?`, string(legacyRaw), len(expected))
	require.NoError(t, err)
	require.Error(t, repo.Complete(ctx, payload.EventID, e.ClaimID, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed}, announcementAt))
	require.NoError(t, repo.CompletePart(ctx, payload.EventID, e.ClaimID, 1, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed}, announcementAt))
	saved := storedAnnouncement(t, db.DB)
	assert.Equal(t, modelcatalog.EventInFlight, saved.State)
	assert.Equal(t, []int{1}, saved.ConfirmedParts)
	renewedAt := announcementAt.Add(40 * time.Second)
	require.NoError(t, repo.RenewClaim(ctx, payload.EventID, e.ClaimID, renewedAt, time.Minute))
	require.NoError(t, repo.RecoverClaims(ctx, announcementAt.Add(time.Minute)))
	assert.Equal(t, modelcatalog.EventInFlight, storedAnnouncement(t, db.DB).State)
	require.NoError(t, repo.CompletePart(ctx, payload.EventID, e.ClaimID, 2, modelcatalog.SendOutcome{State: modelcatalog.SendAmbiguous, Code: "transport_uncertain"}, renewedAt))
	saved = storedAnnouncement(t, db.DB)
	assert.Equal(t, modelcatalog.EventDeliveryUnknown, saved.State)
	assert.Equal(t, []int{1}, saved.ConfirmedParts)
	retry, err := repo.ClaimPending(ctx, saved.NextAttemptAt, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, expected, retry.Parts)
	assert.Equal(t, []int{1}, retry.ConfirmedParts)
	for i := 2; i <= retry.PartCount; i++ {
		require.NoError(t, repo.CompletePart(ctx, payload.EventID, retry.ClaimID, i, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed}, saved.NextAttemptAt))
	}
	final := storedAnnouncement(t, db.DB)
	assert.Equal(t, modelcatalog.EventAnnounced, final.State)
	assert.Len(t, final.ConfirmedParts, retry.PartCount)
	assert.Equal(t, payload, final.Payload)
}

func TestAnnouncementClaimsClassificationAndPayloadFailure(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			original := pendingAnnouncement(t, db, nil)
			repo := NewModelAnnouncementRepo(db.DB)
			require.NoError(t, repo.ClassifyAwaiting(ctx, enabled))
			want := modelcatalog.EventSuppressed
			if enabled {
				want = modelcatalog.EventPending
			}
			require.Equal(t, want, storedAnnouncement(t, db.DB).State)
			require.NoError(t, repo.ClassifyAwaiting(ctx, !enabled))
			assert.Equal(t, want, storedAnnouncement(t, db.DB).State)
			assert.Equal(t, original, storedAnnouncement(t, db.DB).Payload)
		})
	}
	ctx := context.Background()
	db := setupTestDB(t)
	p := pendingAnnouncement(t, db, boolPointer(true))
	p.CanonicalID = strings.Repeat("x", modelcatalog.MaxAnnouncementBytes)
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE model_announcement_events SET payload=?`, string(raw))
	require.NoError(t, err)
	repo := NewModelAnnouncementRepo(db.DB)
	_, err = repo.ClaimPending(ctx, announcementAt, time.Minute)
	require.ErrorIs(t, err, modelcatalog.ErrAnnouncementPayload)
	saved := storedAnnouncement(t, db.DB)
	assert.Equal(t, modelcatalog.EventPending, saved.State)
	assert.Equal(t, 1, saved.Attempts)
	assert.Equal(t, "payload_invalid", saved.LastCode)
	assert.Equal(t, announcementAt.Add(4*time.Hour), saved.NextAttemptAt)
	_, err = repo.ClaimPending(ctx, announcementAt, time.Minute)
	require.ErrorIs(t, err, modelcatalog.ErrNoPendingAnnouncements)
}

func TestAnnouncementClaimsInvalidIdentityBacksOff(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	p := pendingAnnouncement(t, db, boolPointer(true))
	p.EventID = ""
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE model_announcement_events SET payload=?`, string(raw))
	require.NoError(t, err)
	repo := NewModelAnnouncementRepo(db.DB)
	_, err = repo.ClaimPending(ctx, announcementAt, time.Minute)
	require.ErrorIs(t, err, modelcatalog.ErrAnnouncementPayload)
	saved := storedAnnouncement(t, db.DB)
	assert.Equal(t, 1, saved.Attempts)
	assert.Equal(t, "payload_invalid", saved.LastCode)
	assert.Equal(t, announcementAt.Add(4*time.Hour), saved.NextAttemptAt)
	// The same invalid row cannot spin inside a drain.
	_, err = repo.ClaimPending(ctx, announcementAt, time.Minute)
	require.ErrorIs(t, err, modelcatalog.ErrNoPendingAnnouncements)
}

func TestAnnouncementClaimsRejectMissingPlanAndDatabaseFaults(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	p := pendingAnnouncement(t, db, boolPointer(true))
	repo := NewModelAnnouncementRepo(db.DB)
	e, err := repo.ClaimPending(ctx, announcementAt, time.Minute)
	require.NoError(t, err)
	require.Error(t, repo.CompletePart(ctx, p.EventID, e.ClaimID, e.PartCount+1, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed}, announcementAt))
	_, err = db.DB.Exec(`UPDATE model_announcement_events SET parts='[]'`)
	require.NoError(t, err)
	before := storedAnnouncement(t, db.DB)
	require.Error(t, repo.CompletePart(ctx, p.EventID, e.ClaimID, 1, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed}, announcementAt))
	assert.Equal(t, before, storedAnnouncement(t, db.DB))
	require.NoError(t, repo.RecoverClaims(ctx, announcementAt.Add(time.Minute)))
	_, err = repo.ClaimPending(ctx, e.NextAttemptAt, time.Minute)
	require.ErrorIs(t, err, modelcatalog.ErrAnnouncementPayload)
	assert.Equal(t, 2, storedAnnouncement(t, db.DB).Attempts)
	// A failed marker is rolled back; it cannot partially acknowledge a send.
	parts, err := json.Marshal(e.Parts)
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE model_announcement_events SET parts=?,state='in_flight',claim_id=?,lease_until=?`, string(parts), e.ClaimID, announcementTime(e.LeaseUntil))
	require.NoError(t, err)
	_, err = db.DB.Exec(`CREATE TRIGGER fail_announcement_marker BEFORE UPDATE OF confirmed_parts ON model_announcement_events BEGIN SELECT RAISE(ABORT,'marker fault'); END`)
	require.NoError(t, err)
	before = storedAnnouncement(t, db.DB)
	require.Error(t, repo.CompletePart(ctx, p.EventID, e.ClaimID, 1, modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed}, announcementAt))
	assert.Equal(t, before, storedAnnouncement(t, db.DB))
	_, err = db.DB.Exec(`DROP TRIGGER fail_announcement_marker`)
	require.NoError(t, err)
	// No raw sender diagnostic can enter durable last_code.
	require.NoError(t, repo.Complete(ctx, p.EventID, e.ClaimID, modelcatalog.SendOutcome{State: modelcatalog.SendAmbiguous, Code: "https://destination/SECRET"}, announcementAt))
	assert.Equal(t, "send_result", storedAnnouncement(t, db.DB).LastCode)
}

func TestAnnouncementMigrationPreservesExistingEvents(t *testing.T) {
	db, err := Open(":memory:")
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)`)
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
		if version >= 30 {
			continue
		}
		raw, err := migrationsFS.ReadFile("migrations/" + name)
		require.NoError(t, err)
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, version)
		require.NoError(t, err)
	}
	// Produce the frozen payload through a real cycle on the current schema,
	// then seed only columns present before 026. Current profile writers require
	// later migrations and must not be used to populate an old installation.
	source := setupTestDB(t)
	original := pendingAnnouncement(t, source, boolPointer(true))
	require.NotEmpty(t, original.Changes)
	var observation, payload string
	require.NoError(t, source.DB.QueryRow(`SELECT observation FROM model_first_observations WHERE canonical_id=?`, original.CanonicalID).Scan(&observation))
	require.NoError(t, source.DB.QueryRow(`SELECT payload FROM model_announcement_events WHERE id=?`, original.EventID).Scan(&payload))
	_, err = db.Exec(`INSERT INTO model_first_observations(canonical_id,observation) VALUES(?,?)`, original.CanonicalID, observation)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO model_announcement_events(id,canonical_id,payload,state) VALUES(?,?,?,'pending')`, original.EventID, original.CanonicalID, payload)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	require.NoError(t, RunMigrations(db))
	saved := storedAnnouncement(t, db)
	assert.Equal(t, original, saved.Payload)
	assert.Equal(t, modelcatalog.EventPending, saved.State)
	assert.Zero(t, saved.Attempts)
	assert.Empty(t, saved.Parts)
	e, err := NewModelAnnouncementRepo(db).ClaimPending(context.Background(), announcementAt, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, original, e.Payload)
}

func TestAnnouncementClaimsConcurrentConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims.db")
	one, err := Open(path)
	require.NoError(t, err)
	defer one.Close()
	require.NoError(t, RunMigrations(one))
	original := pendingAnnouncement(t, testDB{one}, boolPointer(true))
	two, err := Open(path)
	require.NoError(t, err)
	defer two.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	events := make(chan modelcatalog.Event, 2)
	for _, db := range []*sql.DB{one, two} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := NewModelAnnouncementRepo(db).ClaimPending(context.Background(), announcementAt, time.Minute)
			if err == nil {
				events <- e
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	close(events)
	wins, empty := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, modelcatalog.ErrNoPendingAnnouncements) {
			empty++
		} else {
			require.NoError(t, err)
		}
	}
	assert.Equal(t, 1, wins)
	assert.Equal(t, 1, empty)
	for e := range events {
		assert.Equal(t, original, e.Payload)
		assert.Equal(t, 1, e.Attempts)
		assert.Equal(t, e, storedAnnouncement(t, one))
	}
}

func TestAnnouncementCorruptDeferralWriteRollsBack(t *testing.T) {
	for _, corruption := range []struct{ name, set, attempts string }{
		{"json", "parts='{'", "0"},
		{"attempts", "attempts='broken'", "broken"},
		{"part_count", "part_count='broken'", "0"},
	} {
		for _, fault := range []struct{ name, raise string }{
			{"aborted", "ABORT,'deferral fault'"},
			{"ignored", "IGNORE"},
		} {
			t.Run(corruption.name+"/"+fault.name, func(t *testing.T) {
				ctx := context.Background()
				db := setupTestDB(t)
				p := pendingAnnouncement(t, db, boolPointer(true))
				_, err := db.DB.Exec(`UPDATE model_announcement_events SET ` + corruption.set)
				require.NoError(t, err)
				_, err = db.DB.Exec(`CREATE TRIGGER fail_corrupt_deferral BEFORE UPDATE OF next_attempt_at ON model_announcement_events BEGIN SELECT RAISE(` + fault.raise + `); END`)
				require.NoError(t, err)
				repo := NewModelAnnouncementRepo(db.DB)
				_, err = repo.ClaimPending(ctx, announcementAt, time.Minute)
				require.Error(t, err)
				require.NotErrorIs(t, err, modelcatalog.ErrAnnouncementPayload, "only a durable deferral permits Drain to continue")
				var attempts, count, next, parts, payload, claim string
				require.NoError(t, db.DB.QueryRow(`SELECT attempts,next_attempt_at,parts,payload,claim_id,part_count FROM model_announcement_events`).Scan(&attempts, &next, &parts, &payload, &claim, &count))
				assert.Equal(t, corruption.attempts, attempts)
				assert.Empty(t, next)
				assert.Empty(t, claim)
				if corruption.name == "json" {
					assert.Equal(t, "{", parts)
				} else {
					assert.Equal(t, "[]", parts)
				}
				if corruption.name == "part_count" {
					assert.Equal(t, "broken", count)
				} else {
					assert.Equal(t, "0", count)
				}
				var original modelcatalog.AnnouncementPayload
				require.NoError(t, json.Unmarshal([]byte(payload), &original))
				assert.Equal(t, p, original)
				_, err = db.DB.Exec(`DROP TRIGGER fail_corrupt_deferral`)
				require.NoError(t, err)
				_, err = repo.ClaimPending(ctx, announcementAt, time.Minute)
				require.ErrorIs(t, err, modelcatalog.ErrAnnouncementPayload)
				require.NoError(t, db.DB.QueryRow(`SELECT attempts,next_attempt_at FROM model_announcement_events`).Scan(&attempts, &next))
				assert.Equal(t, "1", attempts)
				assert.Equal(t, announcementAt.Add(4*time.Hour).Format(announcementTimeLayout), next)
			})
		}
	}
}
