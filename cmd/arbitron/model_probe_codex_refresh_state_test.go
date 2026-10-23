package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/require"
)

func probeCodexAuthState(account, generation string) string {
	claims := fmt.Sprintf(`{"sub":"user-a","exp":%q,"https://api.openai.com/auth":{"chatgpt_account_id":%q,"chatgpt_user_id":"user-a","chatgpt_plan_type":"pro"}}`, generation, account)
	jwt := "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature-" + generation
	return fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"account_id":%q,"id_token":%q,"access_token":%q,"refresh_token":%q},"last_refresh":%q}`, account, jwt, generation, generation, generation)
}

// Raw file hashing makes renewal fail at confirmCurrentBinding. Dropping auth,
// config or model metadata entirely instead makes the edit cases wrongly pass.
// Bypassing rollout verification makes the wrong-rollout renewal wrongly pass.
func TestModelProbeCodexSelfRefreshAtRoot(t *testing.T) {
	for _, mode := range []string{modelcatalog.ExecutionModeBatch, modelcatalog.ExecutionModeInteractive} {
		for _, outcome := range []string{"renewal", "account switch", "config edit", "model metadata edit", "partial auth", "wrong rollout"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				dir := t.TempDir()
				home := filepath.Join(dir, "codex-home")
				t.Setenv("HOME", dir)
				t.Setenv("CODEX_HOME", home)
				t.Setenv(codexInteractiveVerifiedEnv, "true")
				const model = "gpt-native-preview-20261018"
				writeCodexAccountModels(t, home, model)
				require.NoError(t, os.WriteFile(filepath.Join(home, "auth.json"), []byte(probeCodexAuthState("account-a", "before")), 0600))
				renewed := probeCodexAuthState("account-a", "after")
				if outcome == "account switch" {
					renewed = probeCodexAuthState("account-b", "after")
				}
				if outcome == "partial auth" {
					renewed = `{"tokens":{"id_token":"sentinel-secret"`
				}
				authFile := filepath.Join(dir, "renewed-auth")
				require.NoError(t, os.WriteFile(authFile, []byte(renewed), 0600))
				cache := `{"fetched_at":"renewed","models":[{"slug":"` + model + `"}]}`
				if outcome == "model metadata edit" {
					cache = `{"models":[{"slug":"` + model + `","context_window":1000000}]}`
				}
				cacheFile := filepath.Join(dir, "renewed-cache")
				require.NoError(t, os.WriteFile(cacheFile, []byte(cache), 0600))
				fallback := filepath.Join(dir, "fallback")
				if outcome == "wrong rollout" {
					require.NoError(t, os.WriteFile(fallback, []byte("fallback-model"), 0600))
				}
				log := filepath.Join(dir, "argv")
				script := strings.NewReplacer("@ARGV@", log, "@FALLBACK@", fallback).Replace(codexProbeScript)
				// Mutate the harness's own files after admission and before its successful
				// terminal record. Renames mirror a completed credential refresh, avoiding
				// accidentally testing a torn write instead of stable identity semantics.
				mutation := fmt.Sprintf(`
cat '%s' > "$CODEX_HOME/auth-$$.tmp"
mv "$CODEX_HOME/auth-$$.tmp" "$CODEX_HOME/auth.json"
cat '%s' > "$CODEX_HOME/cache-$$.tmp"
mv "$CODEX_HOME/cache-$$.tmp" "$CODEX_HOME/models_cache.json"
`, authFile, cacheFile)
				if outcome == "config edit" {
					mutation += `echo 'model_provider="other"' > "$CODEX_HOME/config.toml"` + "\n"
				}
				script = strings.Replace(script, `d="$CODEX_HOME/sessions/2026/10/16"`, mutation+`d="$CODEX_HOME/sessions/2026/10/16"`, 1)
				bin := filepath.Join(dir, "codex")
				require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				ds := bootSinkRoot(t, "profiles: []\nllm:\n  defaultagentbackend: codex\n", func(ws string) {
					writeWorkflow(t, ws, "self-refresh", `{"name":"self-refresh","nodes":[{"id":"talk","type":"interactive_agent","entrypoint":true,"config":{"model_profile":"builder"}}],"edges":[]}`)
				})
				ctx := adminCtxForLocalUser(t, ds)
				_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "builder", Kind: provider.ProfileKindAgent, Backend: "codex", Model: "gpt-5-sol"})
				require.NoError(t, err)
				snap, err := ds.pathInventory.Snapshot(ctx)
				require.NoError(t, err)
				var path modelcatalog.ExecutionPath
				for _, p := range snap.Paths {
					if p.Backend == "codex" && p.Mode == mode {
						for _, alias := range p.Aliases {
							if alias == "builder" {
								path = p
							}
						}
					}
				}
				require.NotEmpty(t, path.Key)
				ev, err := ds.modelRefresh.probe.Check(ctx, path, modelidentity.Entry{Provider: "openai", CanonicalID: "openai/" + model, NativeID: model})
				require.NoError(t, err)
				require.Equal(t, outcome == "renewal", ev.Confirmed(), "post-launch %s must yield the right evidence state", outcome)
				if outcome == "renewal" {
					require.NotEmpty(t, ev.ProofRef)
					current, err := ds.pathInventory.Snapshot(ctx)
					require.NoError(t, err)
					require.Equal(t, snap.Revision, current.Revision, "normal CLI writes cannot invalidate publication's gate revision")
				} else {
					require.Empty(t, ev.ProofRef)
				}
				require.NotEmpty(t, readArgvLog(t, log), "the post-launch mutation must actually run")
			})
		}
	}
}
