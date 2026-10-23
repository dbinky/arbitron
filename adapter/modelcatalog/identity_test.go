package modelcatalog

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type identityEvidence struct {
	Source string `json:"source"`
	Models []struct {
		Provider string `json:"provider"`
		NativeID string `json:"native_id"`
		Tier     string `json:"tier"`
		OrderKey string `json:"order_key"`
		Specific bool   `json:"specific"`
	} `json:"models"`
	Newest map[string]string `json:"newest"`
}

type shippedPolicy struct{}

func (shippedPolicy) Snapshot(context.Context) (modelidentity.IdentityPolicy, error) {
	return modelidentity.IdentityPolicy{Revision: 1, Rules: modelidentity.InitialRules()}, nil
}

func loadIdentityEvidence(t *testing.T) identityEvidence {
	t.Helper()
	raw, err := os.ReadFile("testdata/identity-evidence.json")
	require.NoError(t, err)
	var ev identityEvidence
	require.NoError(t, json.Unmarshal(raw, &ev))
	require.NotEmpty(t, ev.Source, "the fixture must record where its rows come from")
	require.NotEmpty(t, ev.Models, "an empty evidence map cannot confirm any version or tier")
	return ev
}

// The shipped D2 table maps every recorded catalog row to the recorded tier and
// version, and the newest follow target per (provider, tier) is the recorded one.
func TestIdentityEvidenceFixtureMapsAsRecorded(t *testing.T) {
	ev := loadIdentityEvidence(t)
	mapper, err := modelidentity.NewIdentityMapper(shippedPolicy{})
	require.NoError(t, err)

	newest := map[string]modelidentity.Entry{}
	for _, row := range ev.Models {
		e, err := mapper.Map(context.Background(), modelidentity.CatalogModel{
			CatalogProvider: row.Provider, NativeID: row.NativeID, SourceRef: ev.Source,
		})
		require.NoError(t, err, row.NativeID)
		assert.Equal(t, row.Tier, e.Tier, row.NativeID)
		assert.Equal(t, row.OrderKey, e.OrderKey, row.NativeID)
		assert.Equal(t, row.Specific, e.Immutable, row.NativeID)
		assert.Equal(t, ev.Source, e.SourceRef, row.NativeID)
		if e.Tier == "" {
			continue
		}
		k := e.Provider + "/" + e.Tier
		if cur, ok := newest[k]; !ok {
			newest[k] = e
		} else if c, err := modelidentity.Compare(e, cur); err == nil && c > 0 {
			newest[k] = e
		}
	}
	for k, want := range ev.Newest {
		require.Contains(t, newest, k)
		assert.Equal(t, want, newest[k].NativeID, k)
	}
}

// The version grammar recognizes only recorded fixed spellings, independently
// of tier policy. An excluded variant is not automatically a fixed version.
func TestVersionGrammarRecognisesEvidenceRows(t *testing.T) {
	ev := loadIdentityEvidence(t)
	r := modelidentity.NewVersionResolver()
	for _, row := range ev.Models {
		got, err := r.SpecificVersion(context.Background(), modelidentity.VersionRequest{
			Model: row.NativeID, RouteProvider: row.Provider, ConnectionRef: row.Provider,
		})
		if row.Specific {
			require.NoError(t, err, row.NativeID)
			assert.Equal(t, row.NativeID, got)
		} else {
			require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable, row.NativeID)
			assert.Empty(t, got)
		}
	}
}
