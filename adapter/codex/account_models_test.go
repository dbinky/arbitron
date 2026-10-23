package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeModelsCache stores the account model list codex keeps under its home.
func writeModelsCache(t *testing.T, home string, slugs ...string) {
	t.Helper()
	models := make([]string, 0, len(slugs))
	for _, s := range slugs {
		models = append(models, `{"slug":"`+s+`","display_name":"x"}`)
	}
	require.NoError(t, os.MkdirAll(home, 0o700))
	body := `{"fetched_at":"2026-10-16T00:00:00Z","models":[` + strings.Join(models, ",") + `]}`
	require.NoError(t, os.WriteFile(filepath.Join(home, modelsCacheFile), []byte(body), 0o600))
}

// D4: only a slug in the account's models_cache.json may be probed. Anything
// that does not prove the model is listed refuses, secret-free.
func TestAccountListsModel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  *string
		model string
		ok    bool
	}{
		{"listed", ptr(`{"models":[{"slug":"gpt-5-mini"},{"slug":"gpt-5-sol"}]}`), "gpt-5-sol", true},
		{"absent", ptr(`{"models":[{"slug":"gpt-5-mini"}]}`), "gpt-5-sol", false},
		{"prefix only", ptr(`{"models":[{"slug":"gpt-5-sol-pro"}]}`), "gpt-5-sol", false},
		{"padded slug", ptr(`{"models":[{"slug":" gpt-5-sol "}]}`), "gpt-5-sol", false},
		{"empty list", ptr(`{"models":[]}`), "gpt-5-sol", false},
		{"no models key", ptr(`{"slug":"gpt-5-sol"}`), "gpt-5-sol", false},
		{"malformed", ptr(`{"models":[{"slug":"gpt-5-sol"}`), "gpt-5-sol", false},
		{"trailing garbage", ptr(`{"models":[{"slug":"gpt-5-sol"}]} {"models":[]}`), "gpt-5-sol", false},
		{"oversized", ptr(`{"models":[{"slug":"gpt-5-sol"}],"pad":"` + strings.Repeat("a", maxModelsCacheBytes) + `"}`), "gpt-5-sol", false},
		{"missing file", nil, "gpt-5-sol", false},
		{"empty model", ptr(`{"models":[{"slug":""}]}`), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.body != nil {
				require.NoError(t, os.WriteFile(filepath.Join(home, modelsCacheFile), []byte(*tc.body), 0o600))
			}
			err := accountListsModel([]string{"CODEX_HOME=" + home}, tc.model)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrModelEvidence)
			assert.NotContains(t, err.Error(), home, "errors never echo the account home")
		})
	}
	t.Run("home from HOME when CODEX_HOME unset", func(t *testing.T) {
		home := t.TempDir()
		writeModelsCache(t, filepath.Join(home, ".codex"), "gpt-5-sol")
		require.NoError(t, accountListsModel([]string{"HOME=" + home}, "gpt-5-sol"))
	})
	t.Run("no home at all", func(t *testing.T) {
		require.ErrorIs(t, accountListsModel(nil, "gpt-5-sol"), ErrModelEvidence)
	})
}

func ptr(s string) *string { return &s }

// The batch probe never launches codex (not even --version) for a model the
// account does not list, so a model outside the account cannot be confirmed by
// whatever codex silently routes it to.
func TestUsabilityCodexProbeRefusesModelOutsideAccountList(t *testing.T) {
	for name, slugs := range map[string][]string{
		"absent from the list": {"gpt-5-mini"},
		"no cache":             nil,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			if slugs != nil {
				writeModelsCache(t, home, slugs...)
			}
			runner := &multiRunner{}
			got, _, err := probeProvider(t, runner, true).ProbeExecutedModel(context.Background(), "reply ok", t.TempDir())
			require.ErrorIs(t, err, ErrModelEvidence)
			assert.Empty(t, got)
			assert.Empty(t, runner.calls, "nothing launches for a model outside the account list")
		})
	}
}

// The interactive probe turn (SuppressTools) is held to the same account list;
// a real interactive turn is not filtered, its rollout verification decides.
func TestInteractiveProbeTurnRefusesModelOutsideAccountList(t *testing.T) {
	pinned := codexBinding("gpt-5-sol", nil)
	pinned.Pinned = true
	start := func(t *testing.T, suppress bool) (*fakeTurnSession, error) {
		fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
		p := newInteractiveTestProvider(t, fake, pinned)
		ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
			Prompt: "p", WorkDir: "/tmp/ws", Model: "gpt-5-sol", SuppressTools: suppress, RequireExecutedModel: true,
		})
		if err == nil {
			collectChunks(t, ch)
		}
		return fake, err
	}

	t.Run("absent slug never starts the probe turn", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		writeModelsCache(t, home, "gpt-5-mini")
		fake, err := start(t, true)
		require.ErrorIs(t, err, ErrModelEvidence)
		assert.False(t, fake.startCalled)
	})
	t.Run("listed slug starts the read-only probe turn", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		writeModelsCache(t, home, "gpt-5-sol")
		fake, err := start(t, true)
		require.NoError(t, err)
		assert.True(t, fake.startCalled)
		assert.True(t, fake.gotCfg.ReadOnly)
	})
	t.Run("a real turn is not filtered by the list", func(t *testing.T) {
		t.Setenv("CODEX_HOME", t.TempDir())
		fake, err := start(t, false)
		require.NoError(t, err)
		assert.True(t, fake.startCalled)
	})
}
