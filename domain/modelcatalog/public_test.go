package modelcatalog_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogProjectionRedactsInternalEvidenceAndOwnsFields(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	internal := modelcatalog.CatalogView{
		Catalog:          modelcatalog.CatalogSnapshot{Slices: []modelcatalog.CatalogSlice{{Provider: "vendor", Status: "ok", Raw: json.RawMessage(`{"key":"RAW_SECRET"}`), Models: []modelidentity.CatalogModel{{SourceRef: "https://account.invalid/URL_SECRET"}}, Entries: []modelidentity.Entry{{CanonicalID: "vendor/y", Provider: "vendor", NativeID: "y", Tier: "premium", SpecificModel: "y", SourceRef: "/private/ACCOUNT_SECRET"}}}}},
		Evidence:         []modelcatalog.UsabilityEvidence{{CanonicalID: "vendor/y", PathKey: "chat:vendor", ConfigRevision: "rev-1", NativeModel: "y", State: "confirmed", CheckedAt: at, ProofRef: "/home/private/PROOF_SECRET"}},
		UnmatchedTargets: []provider.FollowTarget{{Provider: "unsupported", Tier: "unknown"}},
		Reports:          []modelcatalog.RefreshReport{{ID: "r1", At: at, SliceStatus: map[string]string{"vendor": "https://DESTINATION_SECRET"}, PathStatus: map[string]string{"chat:vendor": "confirmed"}, Changes: []provider.Advancement{{ID: "a1", Alias: "worker", FromModel: "x", ToModel: "y"}}, AnnouncementError: "CREDENTIAL_SECRET", PublicationPending: true}},
	}
	public := modelcatalog.ProjectCatalog(internal)
	require.Len(t, public.Entries, 1)
	assert.Equal(t, "confirmed", public.Entries[0].Usability)
	require.Len(t, public.Entries[0].Paths, 1)
	assert.Equal(t, at, public.Entries[0].Paths[0].CheckedAt)
	assert.Equal(t, internal.Reports[0].Changes, public.Reports[0].Changes)
	assert.Equal(t, "unknown", public.Reports[0].SliceStatus["vendor"])
	assert.Equal(t, "confirmed", public.Reports[0].PathStatus["chat:vendor"])
	assert.Equal(t, "announcement_error", public.Reports[0].AnnouncementError)
	raw, err := json.Marshal(public)
	require.NoError(t, err)
	for _, secret := range []string{"RAW_SECRET", "URL_SECRET", "ACCOUNT_SECRET", "PROOF_SECRET", "DESTINATION_SECRET", "CREDENTIAL_SECRET", "ProofRef", "SourceRef", "Raw"} {
		assert.NotContains(t, string(raw), secret)
	}
	public.UnmatchedTargets[0].Tier = "changed"
	public.Reports[0].SliceStatus["vendor"] = "changed"
	public.Reports[0].Changes[0].Alias = "changed"
	public.Entries[0].Paths[0].State = "changed"
	assert.Equal(t, "unknown", internal.UnmatchedTargets[0].Tier)
	assert.Equal(t, "https://DESTINATION_SECRET", internal.Reports[0].SliceStatus["vendor"])
	assert.Equal(t, "worker", internal.Reports[0].Changes[0].Alias)
	assert.Equal(t, "confirmed", internal.Evidence[0].State)
}

func TestCatalogProjectionRequiresCompletePathProof(t *testing.T) {
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, missing := range []string{"none", "proof", "native model", "checked at"} {
		t.Run(missing, func(t *testing.T) {
			ev := modelcatalog.UsabilityEvidence{CanonicalID: "vendor/native", PathKey: "path", ConfigRevision: "config", NativeModel: "native", ProofRef: "execution-proof", CheckedAt: at, State: modelcatalog.UsabilityConfirmed}
			switch missing {
			case "proof":
				ev.ProofRef = ""
			case "native model":
				ev.NativeModel = ""
			case "checked at":
				ev.CheckedAt = time.Time{}
			}
			view := modelcatalog.CatalogView{
				Catalog:  modelcatalog.CatalogSnapshot{Slices: []modelcatalog.CatalogSlice{{Entries: []modelidentity.Entry{{CanonicalID: ev.CanonicalID, Provider: "vendor", NativeID: "native"}}}}},
				Evidence: []modelcatalog.UsabilityEvidence{ev},
			}
			public := modelcatalog.ProjectCatalog(view)
			require.Len(t, public.Entries, 1)
			require.Len(t, public.Entries[0].Paths, 1)
			want := modelcatalog.UsabilityUnknown
			if missing == "none" {
				want = modelcatalog.UsabilityConfirmed
			}
			assert.Equal(t, want, public.Entries[0].Usability)
			assert.Equal(t, want, public.Entries[0].Paths[0].State, "a path cannot advertise confirmation without complete execution proof")
			assert.Empty(t, public.Entries[0].Tier, "unknown metadata does not prevent exact-model discovery")
		})
	}
}

func TestCatalogProjectionDoesNotInventUsability(t *testing.T) {
	for _, evidence := range [][]modelcatalog.UsabilityEvidence{nil, {{CanonicalID: "vendor/y", State: "confirmed"}}, {{CanonicalID: "vendor/y", PathKey: "p", ConfigRevision: "rev", NativeModel: "y", State: "unknown", ProofRef: "proof"}}} {
		v := modelcatalog.CatalogView{Catalog: modelcatalog.CatalogSnapshot{Slices: []modelcatalog.CatalogSlice{{Entries: []modelidentity.Entry{{CanonicalID: "vendor/y"}}}}}, Evidence: evidence}
		public := modelcatalog.ProjectCatalog(v)
		require.Len(t, public.Entries, 1)
		assert.Equal(t, "unknown", public.Entries[0].Usability)
	}
}
