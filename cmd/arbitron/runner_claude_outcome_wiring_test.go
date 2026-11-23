package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are acceptance tests for existing production behavior: only the external
// executable is replaced. Workflow execution supplies bindings and identities;
// the root's real adapters, dispatcher, health monitor and SQLite remain intact.
func TestRunnerWiring_ClaudeOutcomeFixturesReachSQLite(t *testing.T) {
	for _, stream := range []bool{false, true} {
		path := "collect"
		if stream {
			path = "stream"
		}
		for _, tc := range []struct {
			fixture string
			outcome runner.OutcomeClass
			success bool
		}{
			{"allowed_warning.jsonl", runner.OutcomeNone, true},
			{"allowed_warning_no_utilization.jsonl", runner.OutcomeNone, true},
			{"authentication_failed.jsonl", runner.OutcomeLoggedOut, false},
			{"invalid_api_key_401.jsonl", runner.OutcomeLoggedOut, false},
			{"usage_limit_seven_day.jsonl", runner.OutcomeExhausted, false},
			{"usage_limit_five_hour_ms.jsonl", runner.OutcomeExhausted, false},
			{"overloaded_529.jsonl", runner.OutcomeTransient, false},
			{"server_throttle_429.jsonl", runner.OutcomeTransient, false},
			{"malformed_fields.jsonl", runner.OutcomeOther, false},
			{"malformed_line.jsonl", runner.OutcomeOther, false},
		} {
			t.Run(path+"/"+tc.fixture, func(t *testing.T) {
				cfg, fake := rwWriteRoot(t, 8)
				fixture, err := os.ReadFile(filepath.Join("..", "..", "adapter", "claudecli", "testdata", "outcomes", tc.fixture))
				require.NoError(t, err)
				script := "#!/bin/sh\ncase \"$1\" in\n--version) echo '2.1.330 (Claude Code)'; exit 0;;\nauth) echo '{\"loggedIn\":true}'; exit 0;;\nesac\ncat >/dev/null\necho call >> '" + filepath.Join(fake, "calls.log") + "'\ncat <<'OUTCOME_FIXTURE'\n" + string(fixture) + "\nOUTCOME_FIXTURE\n"
				require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(script), 0o700))
				ds, err := initDomainWithModelDependencies(context.Background(), cfg, ModelDependencies{
					FetchTransport: catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errOfflineCatalog }),
				})
				require.NoError(t, err)
				t.Cleanup(func() { ds.Close(context.Background()) })
				ctx := adminCtxForLocalUser(t, ds)
				profile := provider.Profile{Alias: "rr-chat", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-5"}
				workflow, op := "rr-block", "chat"
				if stream {
					profile.Alias, profile.Kind, profile.Backend = "rr-agent", provider.ProfileKindAgent, "claude-code"
					workflow, op = "rr-talk", "stream"
				}
				_, _, err = ds.profileSvc.Create(ctx, profile)
				require.NoError(t, err)
				id, _, err := ds.eng.Trigger(ctx, workflow, map[string]any{}, "normal")
				require.NoError(t, err)
				t.Cleanup(func() { _ = ds.eng.Cancel(ctx, id) })

				attemptOutcome := string(tc.outcome)
				if tc.success {
					attemptOutcome = "completed"
				}
				// A durable attempt proves the fixture traversed placement, not a parser or
				// an undecorated provider. Query until the terminal outcome is committed.
				require.Eventually(t, func() bool {
					var n int
					err := ds.db.QueryRow(`SELECT COUNT(*) FROM runner_job_attempts a JOIN runner_jobs j ON j.id=a.job_id
      WHERE j.run_id=? AND j.op=? AND a.runner_id=? AND a.source_id=? AND a.outcome=? AND a.ended_at IS NOT NULL`,
						id.String(), op, string(runner.BuiltInRunnerID), string(rwAnthropic), attemptOutcome).Scan(&n)
					return err == nil && n > 0
				}, 20*time.Second, 20*time.Millisecond, "classified fixture must reach the persisted placed-attempt sink")
				assert.Greater(t, rwCalls(fake), 0)
				// Classification must not copy structured provider records or prose to
				// the public health/event details or the administrative audit trail.
				for _, table := range []string{"runner_sources", "pool_events", "audit_events"} {
					rows, err := ds.db.Query("SELECT detail FROM " + table)
					require.NoError(t, err)
					for rows.Next() {
						var detail string
						require.NoError(t, rows.Scan(&detail))
						for _, raw := range []string{"OAuth", "Failed to authenticate", "Invalid API key", "api_error_status", "isApiErrorMessage", "quotaLimits"} {
							assert.NotContains(t, detail, raw, table)
						}
					}
					require.NoError(t, rows.Err())
					require.NoError(t, rows.Close())
				}
				if tc.success || tc.outcome == runner.OutcomeLoggedOut || tc.outcome == runner.OutcomeExhausted {
					h := rwSourceHealth(t, ds, rwAnthropic)
					assert.Equal(t, tc.outcome, h.LastOutcome)
					switch tc.outcome {
					case runner.OutcomeLoggedOut:
						assert.Equal(t, runner.AuthLoggedOut, h.Auth)
					case runner.OutcomeExhausted:
						require.NotNil(t, h.ResetAt)
						assert.Equal(t, int64(1795262400), h.ResetAt.Unix())
					default:
						assert.Equal(t, runner.AuthOK, h.Auth)
						if tc.fixture == "allowed_warning.jsonl" {
							require.NotNil(t, h.Headroom)
							assert.InDelta(t, 0.15, *h.Headroom, 0.0001)
						} else {
							assert.Nil(t, h.Headroom, "absent utilization is not metric evidence")
						}
					}
				}
			})
		}
	}
}
