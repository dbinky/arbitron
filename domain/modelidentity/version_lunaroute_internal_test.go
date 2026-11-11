package modelidentity

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteProductionIdentityEvidence(t *testing.T) {
	// P1-T11 source preparation has confirmed these spellings. The other two
	// decided candidates remain blocked pending authoritative identity evidence.
	for _, id := range []string{"glm-5.3", "claude-sonnet-5-lr1"} {
		require.Contains(t, lunaRouteReviewedIDs, id)
	}
	for id, entry := range lunaRouteReviewedIDs {
		t.Run(id, func(t *testing.T) {
			assert.True(t, lunaRouteFixed(id))
			assert.Equal(t, Family(entry.Vendor), ClassifyFamily("lunaroute", id))
			assert.Contains(t, []string{id, "alias:" + id}, entry.FixedTarget)
			path, anchor, ok := strings.Cut(entry.Evidence, "#")
			require.True(t, ok)
			require.Equal(t, "docs/evidence/lunaroute-provider/model-identities.md", path)
			require.Equal(t, id, anchor)
			note, err := os.ReadFile(filepath.Join("..", "..", path))
			require.NoError(t, err)
			assert.Contains(t, string(note), "\n## "+anchor+"\n")
		})
	}
}

// These are grammar fixtures, not additional production identity facts.
func installLunaRouteReviews(t *testing.T) {
	t.Helper()
	saved := lunaRouteReviewedIDs
	t.Cleanup(func() { lunaRouteReviewedIDs = saved })
	lunaRouteReviewedIDs = map[string]lunaRouteFixedID{
		"glm-98.1":             {Vendor: "zhipuai", FixedTarget: "glm-98.1", Evidence: "fixture#glm"},
		"claude-sonnet-98-lr1": {Vendor: "anthropic", FixedTarget: "alias:claude-sonnet-98-lr1", Evidence: "fixture#claude"},
	}
}

func resolveLunaRoute(model, backend string) (string, error) {
	return NewVersionResolver().SpecificVersion(context.Background(), VersionRequest{
		Model: model, RouteProvider: "lunaroute", ConnectionRef: "lr", Backend: backend,
	})
}

func TestLunaRouteReviewedFixedID(t *testing.T) {
	installLunaRouteReviews(t)
	for model := range lunaRouteReviewedIDs {
		for _, backend := range []string{"", "claude-code", "opencode", "codex", "pi"} {
			t.Run(model+"/"+backend, func(t *testing.T) {
				got, err := resolveLunaRoute(model, backend)
				require.NoError(t, err)
				assert.Equal(t, model, got)
			})
		}
	}
}

func TestLunaRouteFixedBackgroundExplicit(t *testing.T) {
	installLunaRouteReviews(t)
	background := "glm-98.1-background"
	_, err := resolveLunaRoute(background, "")
	require.ErrorIs(t, err, ErrSpecificVersionUnavailable)
	lunaRouteReviewedIDs[background] = lunaRouteFixedID{Vendor: "zhipuai", FixedTarget: background, Evidence: "fixture#background"}
	for _, model := range []string{"glm-98.1", background} {
		got, err := resolveLunaRoute(model, "")
		require.NoError(t, err)
		assert.Equal(t, model, got, "no automatic background suffix")
	}
}

func TestLunaRouteUnreviewedNumericAliasRefused(t *testing.T) {
	installLunaRouteReviews(t)
	for _, model := range []string{"glm-999.9", "claude-sonnet-5-lr999", "", " ", "latest", "GLM-98.1", "lunaroute/glm-98.1", "zhipuai/glm-98.1", "glm-98.1-background", "deepseek-4.1-flash", " glm-98.1", "glm-98.1 "} {
		t.Run(model, func(t *testing.T) {
			got, err := resolveLunaRoute(model, "")
			require.ErrorIs(t, err, ErrSpecificVersionUnavailable)
			assert.Empty(t, got)
		})
	}
}

func TestLunaRouteIncompleteReviewRefused(t *testing.T) {
	installLunaRouteReviews(t)
	for _, field := range []string{"vendor", "target", "evidence"} {
		t.Run(field, func(t *testing.T) {
			entry := lunaRouteFixedID{Vendor: "zhipuai", FixedTarget: "glm-98.2", Evidence: "fixture#glm"}
			switch field {
			case "vendor":
				entry.Vendor = ""
			case "target":
				entry.FixedTarget = ""
			case "evidence":
				entry.Evidence = ""
			}
			lunaRouteReviewedIDs["glm-98.2"] = entry
			got, err := resolveLunaRoute("glm-98.2", "")
			require.ErrorIs(t, err, ErrSpecificVersionUnavailable)
			assert.Empty(t, got)
			assert.Contains(t, err.Error(), "use a reviewed fixed LunaRoute id from connection_models and the reviewed-id table")
			assert.False(t, LunaRouteExecutedModelAccepted("glm-98.2", "glm-98.2"))
		})
	}
}

type noLunaRouteHTTP struct{ t *testing.T }

func (n noLunaRouteHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	n.t.Fatal("fixed identity resolution must not consult a catalog")
	return nil, nil
}

func TestLunaRouteNoCatalogNeeded(t *testing.T) {
	installLunaRouteReviews(t)
	saved := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = saved })
	http.DefaultTransport = noLunaRouteHTTP{t}
	got, err := NewVersionResolver().SpecificVersion(context.Background(), VersionRequest{Model: "glm-98.1", RouteProvider: " LunaRoute "})
	require.NoError(t, err)
	assert.Equal(t, "glm-98.1", got)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewVersionResolver().SpecificVersion(ctx, VersionRequest{Model: got, RouteProvider: "lunaroute"})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestLunaRouteExecutedModelAccepted(t *testing.T) {
	installLunaRouteReviews(t)
	for _, tc := range []struct {
		selected, reported string
		accepted           bool
	}{
		{"glm-98.1", "glm-98.1", true},
		{"glm-98.1", "glm-98.2", false},
		{"glm-98.1", "GLM-98.1", false},
		{"glm-98.1", "", false},
		{"claude-sonnet-98-lr1", "claude-sonnet-5-5", true},
		{"claude-sonnet-98-lr1", "claude-sonnet-5-6", true},
		{"claude-sonnet-98-lr1", "glm-5.3", false},
		{"claude-sonnet-98-lr1", "gpt-6.1-sol", false},
		{"claude-sonnet-98-lr1", "", false},
		{"claude-sonnet-98-lr1", "opaque-id", false},
		{"unreviewed-1", "unreviewed-1", false},
		{"", "", false},
	} {
		t.Run(strings.Join([]string{tc.selected, tc.reported}, "/"), func(t *testing.T) {
			assert.Equal(t, tc.accepted, LunaRouteExecutedModelAccepted(tc.selected, tc.reported))
		})
	}
}
