package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Acceptance for the existing adapter: only the executable is replaced. Each
// terminal attempt must come through the real root and its SQLite health sink.
func TestRunnerWiring_CodexOutcomeFixturesReachSQLite(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			fixture string
			outcome runner.OutcomeClass
		}{
			{"rollout_logged_out_401.jsonl", runner.OutcomeLoggedOut},
			{"rollout_usage_limit_u2019.jsonl", runner.OutcomeExhausted},
			{"rollout_server_overloaded.jsonl", runner.OutcomeTransient},
			{"rollout_unknown_info.jsonl", runner.OutcomeOther},
			{"", runner.OutcomeOther}, // Identical stdout prose without rollout is not evidence.
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.fixture), func(t *testing.T) {
				cfg, fake := rwWriteRoot(t, 8)
				home := filepath.Join(fake, "account")
				sessions := filepath.Join(home, "sessions", "2026", "11", "20")
				require.NoError(t, os.MkdirAll(sessions, 0o700))
				fixtureDir := filepath.Join("..", "..", "adapter", "codex", "testdata", "outcomes")
				rollout := ""
				if tc.fixture != "" {
					data, err := os.ReadFile(filepath.Join(fixtureDir, tc.fixture))
					require.NoError(t, err)
					rollout = "cat <<'ROLLOUT' > " + sessions + "/rollout-test-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl\n" + string(data) + "\nROLLOUT\n"
				}
				stdout, err := os.ReadFile(filepath.Join(fixtureDir, "exec_usage_limit.jsonl"))
				require.NoError(t, err)
				rpc, err := os.ReadFile(filepath.Join(fixtureDir, "app_server_observed_logged_in.jsonl"))
				require.NoError(t, err)
				responses := strings.SplitN(string(rpc), "\n", 2)
				rpcScript := "if [ \"$1\" = app-server ]; then\nread -r init\ncat <<'INIT'\n" + responses[0] + "\nINIT\nread -r initialized\nread -r request\ncat <<'LIMITS'\n" + responses[1] + "\nLIMITS\nread -r eof\nexit 0\nfi\n"
				script := "#!/bin/sh\n" + rpcScript + "case \"$1\" in\n--version) echo 'codex-cli 0.170.0'; exit 0;;\nlogin) echo 'Logged in using ChatGPT'; exit 0;;\nesac\ncat >/dev/null\necho call >> " + filepath.Join(fake, "codex-calls") + "\n" + rollout + "cat <<'STDOUT'\n" + string(stdout) + "\nSTDOUT\nexit 1\n"
				require.NoError(t, os.WriteFile(filepath.Join(fake, "codex"), []byte(script), 0o700))
				t.Setenv("PATH", fake+string(os.PathListSeparator)+"/usr/bin:/bin")
				t.Setenv("CODEX_HOME", home)
				t.Setenv(codexInteractiveVerifiedEnv, "true")
				ws := filepath.Join(filepath.Dir(cfg), "workspaces")
				writeWorkflow(t, ws, "rr-codex-agent", `{"name":"rr-codex-agent","inputs":[],"nodes":[{"id":"run","type":"agent_call","entrypoint":true,"config":{"model_profile":"rr-agent","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, ws, "rr-codex-agent")
				ds, err := initDomainWithModelDependencies(context.Background(), cfg, ModelDependencies{FetchTransport: catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errOfflineCatalog })})
				require.NoError(t, err)
				t.Cleanup(func() { ds.Close(context.Background()) })
				ctx := adminCtxForLocalUser(t, ds)
				alias, workflowName, op := "rr-agent", "rr-codex-agent", "agent"
				if stream {
					workflowName, op = "rr-talk", "stream"
				}
				_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: alias, Kind: provider.ProfileKindAgent, Backend: "codex", Model: "gpt-5-sol"})
				require.NoError(t, err)
				source := runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredOpenAISubscription, "")
				probed, err := ds.runners.monitor.ProbeNow(ctx, source)
				require.NoError(t, err)
				assert.Equal(t, runner.AuthOK, probed.Auth)
				require.NotNil(t, probed.Headroom)
				assert.InDelta(t, 0.7, *probed.Headroom, 0.0001)
				persisted := rwSourceHealth(t, ds, source)
				require.NotNil(t, persisted.Headroom)
				assert.InDelta(t, 0.7, *persisted.Headroom, 0.0001)
				var unavailable string
				require.NoError(t, ds.db.QueryRow(`SELECT unavailable_reason FROM runner_sources WHERE id=?`, string(source)).Scan(&unavailable))
				assert.Empty(t, unavailable, "the installed inventory must validate the supported harness")
				id, _, err := ds.eng.Trigger(ctx, workflowName, nil, "normal")
				require.NoError(t, err)
				t.Cleanup(func() { _ = ds.eng.Cancel(ctx, id) })
				t.Cleanup(func() {
					if t.Failed() {
						rows, err := ds.db.Query(`SELECT a.outcome,a.source_id FROM runner_job_attempts a JOIN runner_jobs j ON j.id=a.job_id WHERE j.run_id=?`, id.String())
						if err == nil {
							defer rows.Close()
							for rows.Next() {
								var o, s string
								_ = rows.Scan(&o, &s)
								t.Log(o, s)
							}
						}
					}
				})
				require.Eventually(t, func() bool {
					var n int
					err := ds.db.QueryRow(`SELECT COUNT(*) FROM runner_job_attempts a JOIN runner_jobs j ON j.id=a.job_id WHERE j.run_id=? AND j.op=? AND a.source_id=? AND a.outcome=? AND a.ended_at IS NOT NULL`, id.String(), op, string(source), string(tc.outcome)).Scan(&n)
					return err == nil && n > 0
				}, 20*time.Second, 20*time.Millisecond)
				calls, err := os.ReadFile(filepath.Join(fake, "codex-calls"))
				require.NoError(t, err)
				assert.NotEmpty(t, calls)
				for _, table := range []string{"runner_sources", "pool_events", "audit_events"} {
					rows, err := ds.db.Query("SELECT detail FROM " + table)
					require.NoError(t, err)
					for rows.Next() {
						var detail string
						require.NoError(t, rows.Scan(&detail))
						for _, raw := range []string{"Missing bearer", "chatgpt.com/settings/usage", "codex_error_info", "cf-ray"} {
							assert.NotContains(t, detail, raw, table)
						}
					}
					require.NoError(t, rows.Err())
					require.NoError(t, rows.Close())
				}
				if tc.outcome == runner.OutcomeLoggedOut || tc.outcome == runner.OutcomeExhausted {
					h := rwSourceHealth(t, ds, source)
					assert.Equal(t, tc.outcome, h.LastOutcome)
					if tc.outcome == runner.OutcomeLoggedOut {
						assert.Equal(t, runner.AuthLoggedOut, h.Auth)
					} else {
						require.NotNil(t, h.ResetAt)
						assert.Equal(t, int64(1795262400), h.ResetAt.Unix())
					}
				}
			})
		}
	}
}
