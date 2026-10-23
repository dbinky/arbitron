package notification_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type modelReceiverTransport func(*http.Request) (*http.Response, error)

func (f modelReceiverTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// This boundary test consumes actual transaction-produced events and internal
// settings after reopen. It does not replace the later root/claim/refresh test.
func TestModelAnnouncementPersistedOriginalPayloadToReceiver(t *testing.T) {
	for _, followers := range []int{0, 2} {
		t.Run(fmt.Sprint(followers), func(t *testing.T) {
			ctx := context.Background()
			admin := identity.WithIdentity(ctx, identity.Identity{Role: identity.RoleAdmin})
			dbPath := filepath.Join(t.TempDir(), "models.db")
			db, err := sqlite.Open(dbPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			require.NoError(t, sqlite.RunMigrations(db))
			settingsService := settings.NewService(sqlite.NewSettingsRepo(db))
			defaults, err := settingsService.ReadInternal(ctx)
			require.NoError(t, err)
			require.Empty(t, defaults.TeamsWebhookURL)
			rawDestination := "https://receiver.example/original-secret?sig=exact"
			_, err = settingsService.Update(admin, settings.Patch{TeamsWebhookURL: &rawDestination})
			require.NoError(t, err)

			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			enabled := true
			cycle := modelcatalog.CycleCommit{
				Report: modelcatalog.RefreshReport{ID: "cycle-original", At: at},
				Catalog: modelcatalog.CatalogSnapshot{FetchedAt: at, Slices: []modelcatalog.CatalogSlice{{
					Provider: "vendor", Status: modelcatalog.SliceStatusOK, Raw: json.RawMessage(`{"models":{}}`),
					Models:  []modelidentity.CatalogModel{{CatalogProvider: "vendor", NativeID: "model-2", CanonicalModelID: "vendor/model-2"}},
					Entries: []modelidentity.Entry{{CanonicalID: "vendor/model-2", Provider: "vendor", NativeID: "model-2", SpecificModel: "vendor/model-2", Tier: "premium", OrderKey: "2", Immutable: true, PolicyRevision: 1}},
				}}},
				Evidence:          []modelcatalog.UsabilityEvidence{{CanonicalID: "vendor/model-2", PathKey: "opaque-path", ConfigRevision: "config-1", NativeModel: "vendor/model-2", ProofRef: "/private/account/secret-proof", CheckedAt: at, State: modelcatalog.UsabilityConfirmed}},
				WebhookConfigured: &enabled,
			}
			profiles := sqlite.NewProfileRepo(db)
			for i := 0; i < followers; i++ {
				p := provider.Profile{Alias: fmt.Sprintf("alias-%d", i), Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: fmt.Sprintf("vendor/model-old-%d", i), SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "vendor", Tier: "premium"}}
				require.NoError(t, profiles.Create(ctx, p))
				expected, err := profiles.Get(ctx, p.Alias)
				require.NoError(t, err)
				cycle.Advances = append(cycle.Advances, provider.AdvanceRequest{ID: fmt.Sprintf("advance-%d", i), RefreshID: cycle.Report.ID, Expected: expected, Model: "vendor/model-2", CanonicalID: "vendor/model-2", PathRevision: "inventory-1", ExecutionRevision: provider.ExecutionRevision{ID: "inventory-1", PolicyRevision: 1}, Evidence: []provider.PathEvidenceRef{{PathKey: "opaque-path", ConfigRevision: "config-1"}}, At: at})
			}
			report, err := sqlite.NewModelCatalogRepo(db).CommitCycle(ctx, cycle)
			require.NoError(t, err)
			require.Len(t, report.Changes, followers)
			// Today's aliases are deliberately different from the original event.
			for _, change := range report.Changes {
				p, err := profiles.Get(ctx, change.Alias)
				require.NoError(t, err)
				p.Model = "vendor/model-later"
				require.NoError(t, profiles.Update(ctx, p))
			}
			require.NoError(t, db.Close())
			db, err = sqlite.Open(dbPath)
			require.NoError(t, err)
			require.NoError(t, sqlite.RunMigrations(db))
			var raw, state string
			require.NoError(t, db.QueryRow(`SELECT payload,state FROM model_announcement_events`).Scan(&raw, &state))
			require.Equal(t, modelcatalog.EventPending, state)
			var payload modelcatalog.AnnouncementPayload
			require.NoError(t, json.Unmarshal([]byte(raw), &payload))
			require.NotEmpty(t, payload.EventID)
			require.Equal(t, report.Changes, payload.Changes)
			require.Equal(t, at, payload.FirstConfirmedAt)

			type fact struct{ Title, Value string }
			var cards [][]fact
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, rawDestination[len("https://receiver.example"):], r.URL.RequestURI())
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				var msg struct {
					Attachments []struct {
						Content struct{ Body []struct{ Facts []fact } }
					}
				}
				if !assert.NoError(t, json.Unmarshal(body, &msg)) {
					w.WriteHeader(400)
					return
				}
				var facts []fact
				for _, a := range msg.Attachments {
					for _, b := range a.Content.Body {
						facts = append(facts, b.Facts...)
					}
				}
				cards = append(cards, facts)
				assert.NotContains(t, string(body), "secret-proof")
				assert.NotContains(t, string(body), "model-later")
				assert.NotContains(t, string(body), "original-secret")
				w.WriteHeader(202)
			}))
			defer receiver.Close()
			target, err := url.Parse(receiver.URL)
			require.NoError(t, err)
			// Replace only network routing. Reject an absent/redacted/stale destination
			// before forwarding the real serialized request to the non-deduplicating sink.
			transport := modelReceiverTransport(func(r *http.Request) (*http.Response, error) {
				if !assert.Equal(t, rawDestination, r.URL.String()) {
					return nil, errors.New("unexpected raw destination")
				}
				forwarded := r.Clone(r.Context())
				u := *r.URL
				u.Scheme, u.Host = target.Scheme, target.Host
				forwarded.URL = &u
				return receiver.Client().Transport.RoundTrip(forwarded)
			})
			sender := notification.NewTeamsModelSender(&http.Client{Transport: transport})
			settingsService = settings.NewService(sqlite.NewSettingsRepo(db))
			for _, destination := range []string{rawDestination, "https://receiver.example/changed-secret?sig=next"} {
				rawDestination = destination
				_, err := settingsService.Update(admin, settings.Patch{TeamsWebhookURL: &destination})
				require.NoError(t, err)
				internal, err := settingsService.ReadInternal(ctx)
				require.NoError(t, err)
				public, err := settingsService.Get(admin)
				require.NoError(t, err)
				require.NotEqual(t, public.TeamsWebhookURL, internal.TeamsWebhookURL)
				outcome, err := sender.Send(ctx, internal.TeamsWebhookURL, payload)
				require.NoError(t, err)
				require.Equal(t, modelcatalog.SendConfirmed, outcome.State)
			}
			require.Len(t, cards, 2)
			assert.Equal(t, cards[0], cards[1], "a destination change does not reconstruct the frozen card")
			assert.Contains(t, cards[0], fact{Title: "Event", Value: payload.EventID})
			assert.Contains(t, cards[0], fact{Title: "Model", Value: "vendor/model-2"})
			assert.Contains(t, cards[0], fact{Title: "Path", Value: "opaque-path (config-1)"})
			for _, change := range report.Changes {
				assert.Contains(t, cards[0], fact{Title: change.Alias, Value: change.FromModel + " → " + change.ToModel})
			}
			if followers == 0 {
				assert.Contains(t, cards[0], fact{Title: "Profiles", Value: "No profiles changed"})
			}
		})
	}
}
