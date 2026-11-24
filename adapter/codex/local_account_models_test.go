package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalAccountModels_UsesExecutionCodexHome(t *testing.T) {
	codexHome, home := t.TempDir(), t.TempDir()
	writeModelsCache(t, codexHome, "gpt-6.1-sol", "gpt-5-mini", "gpt-6.1-sol")
	writeModelsCache(t, filepath.Join(home, ".codex"), "only-in-default-home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	got, err := LocalAccountModels()
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-6.1-sol", "gpt-5-mini"}, got)
	require.NoError(t, accountListsModel(codexEnv(), "gpt-5-mini"))
	t.Setenv("CODEX_HOME", "")
	got, err = LocalAccountModels()
	require.NoError(t, err)
	assert.Equal(t, []string{"only-in-default-home"}, got)
}

func TestLocalAccountModels_MalformedEmptyAndOversizeAreNotWildcard(t *testing.T) {
	for _, tc := range []struct {
		name string
		body *string
	}{
		{"missing file", nil},
		{"malformed", ptr(`{"models":[{"slug":"gpt-5-sol"}`)},
		{"trailing garbage", ptr(`{"models":[{"slug":"gpt-5-sol"}]} {"models":[]}`)},
		{"empty list", ptr(`{"models":[]}`)},
		{"no models key", ptr(`{"fetched_at":"2026-11-23T00:00:00Z"}`)},
		{"only blank or padded slugs", ptr(`{"models":[{"slug":""},{"slug":" gpt-5-sol "}]}`)},
		{"oversized", ptr(`{"models":[{"slug":"gpt-5-sol"}],"pad":"` + strings.Repeat("a", maxModelsCacheBytes) + `"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			if tc.body != nil {
				require.NoError(t, os.WriteFile(filepath.Join(home, modelsCacheFile), []byte(*tc.body), 0o600))
			}
			got, err := LocalAccountModels()
			require.ErrorIs(t, err, ErrModelEvidence)
			assert.Nil(t, got)
			assert.NotContains(t, err.Error(), home)
		})
	}
}
