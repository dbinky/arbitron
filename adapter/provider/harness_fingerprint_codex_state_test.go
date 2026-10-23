package provider_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// These are synthetic, unsigned tokens. Only the real CLI authenticates them;
// the fingerprinter uses their identity claims solely to invalidate old proof.
func codexAuthState(account, user, plan, generation string) string {
	claims := fmt.Sprintf(`{"sub":%q,"exp":%q,"https://api.openai.com/auth":{"chatgpt_account_id":%q,"chatgpt_user_id":%q,"chatgpt_plan_type":%q}}`, user, generation, account, user, plan)
	jwt := "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature-" + generation
	return fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"account_id":%q,"id_token":%q,"access_token":%q,"refresh_token":%q},"last_refresh":%q}`, account, jwt, "access-"+generation, "refresh-"+generation, generation)
}

func TestHarnessFingerprintCodexIgnoresOnlyRoutineRefreshState(t *testing.T) {
	h := newHarnessHome(t)
	fp := h.fingerprinter()
	auth := filepath.Join(h.home, ".codex", "auth.json")
	cache := filepath.Join(h.home, ".codex", "models_cache.json")
	write := func(path, body string) { t.Helper(); require.NoError(t, os.WriteFile(path, []byte(body), 0600)) }
	original := codexAuthState("account-a", "user-a", "pro", "before")
	write(auth, original)
	write(cache, `{"fetched_at":"before","identity":"tenant-a","client_version":"0.163.0","models":[{"slug":"gpt-5-sol","context_window":200000}]}`)
	before, err := fp(workflow.BackendCodex)
	require.NoError(t, err)
	// Token rotation, ID-token expiry/signature, cache TTL and JSON key order all
	// change during normal harness use without changing the authenticated user.
	write(auth, codexAuthState("account-a", "user-a", "pro", "after"))
	write(cache, `{"models":[{"context_window":200000,"slug":"gpt-5-sol"}],"client_version":"0.163.0","identity":"tenant-a","fetched_at":"after"}`)
	after, err := fp(workflow.BackendCodex)
	require.NoError(t, err)
	require.Equal(t, before, after, "routine CLI rewrites must not invalidate exact-model proof")

	for _, tc := range []struct{ name, path, body string }{
		{"workspace switch", auth, codexAuthState("account-b", "user-a", "pro", "after")},
		{"user switch within workspace", auth, codexAuthState("account-a", "user-b", "pro", "after")},
		{"subscription change", auth, codexAuthState("account-a", "user-a", "free", "after")},
		{"API-key auth", auth, `{"auth_mode":"apikey","OPENAI_API_KEY":"another-key"}`},
		{"config edit", filepath.Join(h.home, ".codex", "config.toml"), `model_provider="other"`},
		{"model removal", cache, `{"models":[]}`},
		{"model capabilities", cache, `{"models":[{"slug":"gpt-5-sol","context_window":1000000}]}`},
		{"cache tenant", cache, `{"identity":"tenant-b","client_version":"0.163.0","models":[{"slug":"gpt-5-sol","context_window":200000}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous, readErr := os.ReadFile(tc.path)
			require.True(t, readErr == nil || os.IsNotExist(readErr))
			write(tc.path, tc.body)
			changed, err := fp(workflow.BackendCodex)
			require.NoError(t, err)
			require.NotEqual(t, after, changed, "execution-affecting changes must still invalidate proof")
			if readErr == nil {
				write(tc.path, string(previous))
			} else {
				require.NoError(t, os.Remove(tc.path))
			}
		})
	}
}

func TestHarnessFingerprintCodexPartialStateIsSecretFreeError(t *testing.T) {
	for _, body := range []string{"", "{", "null", "[]", `{"secret":"sentinel-secret"} {}`, `{"tokens":{"id_token":"sentinel-secret"}}`} {
		t.Run(body, func(t *testing.T) {
			h := newHarnessHome(t)
			require.NoError(t, os.WriteFile(filepath.Join(h.home, ".codex", "auth.json"), []byte(body), 0600))
			got, err := h.fingerprinter()(workflow.BackendCodex)
			require.Error(t, err)
			require.Empty(t, got, "partial state cannot supply a partial fingerprint")
			require.NotContains(t, err.Error(), "sentinel-secret")
		})
	}
}
