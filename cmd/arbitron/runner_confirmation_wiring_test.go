package main

import (
	"encoding/json"
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

// Only the external CLI is replaced. Exhaustion is produced by a real workflow;
// recovery must traverse the root's confirmer, dispatcher, adapter and SQLite.
func TestRunnerWiring_ConfirmationRequiresExactExecutedModel(t *testing.T) {
	for _, tc := range []struct {
		name, evidence, state string
		capacity              runner.CapacityState
	}{
		{"wrong model", `,"modelUsage":{"claude-sonnet-4-5":{}}`, "failed", runner.CapacityProbation},
		{"missing evidence", "", "failed", runner.CapacityProbation},
		{"exact model", `,"modelUsage":{"claude-opus-4-1":{}}`, "completed", runner.CapacityAvailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds, fake := rwBoot(t, 1)
			ctx := adminCtxForLocalUser(t, ds)
			rwChatProfile(t, ctx, ds)
			rwTouch(t, fake, "exhausted")
			id, _, err := ds.eng.Trigger(ctx, "rr-block", map[string]any{}, "normal")
			require.NoError(t, err)
			t.Cleanup(func() { _ = ds.eng.Cancel(ctx, id) })
			require.Eventually(t, func() bool {
				return rwSourceHealth(t, ds, rwAnthropic).Capacity == runner.CapacityExhausted
			}, 10*time.Second, 20*time.Millisecond)
			require.NoError(t, ds.eng.Cancel(ctx, id))
			require.Eventually(t, func() bool {
				jobs, err := ds.runners.ledger.ListActive(ctx)
				return err == nil && len(jobs) == 0
			}, 10*time.Second, 20*time.Millisecond)

			// An earlier alias for another model must not be used as a substitute.
			_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "aaa-other", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-5"})
			require.NoError(t, err)
			script := `#!/bin/sh
case "$1" in
 --version) echo '2.1.330 (Claude Code)'; exit 0;;
 auth) echo '{"loggedIn":true}'; exit 0;;
esac
cat >/dev/null
echo "$*" >> '@DIR@/confirm.log'
echo '{"type":"result","is_error":false,"result":"OK","session_id":"s"@EVIDENCE@}'
`
			require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(strings.NewReplacer("@DIR@", fake, "@EVIDENCE@", tc.evidence).Replace(script)), 0o700))
			// The recorded episode came from the workflow; expire its reset without waiting
			// for the real quota window. No source/health repository is replaced.
			past := time.Now().Add(-time.Hour)
			require.NoError(t, ds.runners.monitor.ApplyOutcome(ctx, rwAnthropic, runner.SourceOutcome{
				Class: runner.OutcomeExhausted, Model: "claude-opus-4-1", ResetAt: &past,
			}))
			require.NoError(t, ds.runners.monitor.Tick(ctx))
			ds.runners.monitor.Wait()

			var state, outcome string
			require.NoError(t, ds.db.QueryRow(`SELECT j.state, a.outcome FROM runner_jobs j
 JOIN runner_job_attempts a ON a.job_id=j.id WHERE j.operation_id='probe/confirm' ORDER BY a.ended_at DESC LIMIT 1`).Scan(&state, &outcome))
			assert.Equal(t, tc.state, state, "only verified exact-model success completes a confirmation")
			if tc.state == "failed" {
				assert.NotEqual(t, "completed", outcome)
			} else {
				assert.Equal(t, "completed", outcome)
			}
			h := rwSourceHealth(t, ds, rwAnthropic)
			assert.Equal(t, tc.capacity, h.Capacity, "only verified confirmation may recover quota")
			assert.Equal(t, "claude-opus-4-1", h.ConfirmationModel)
			argv, err := os.ReadFile(filepath.Join(fake, "confirm.log"))
			require.NoError(t, err)
			assert.Contains(t, string(argv), "--model claude-opus-4-1")
			assert.Contains(t, string(argv), "--tools")
			assert.Equal(t, 1, strings.Count(string(argv), "\n"), "one confirmation, never a same-family substitution")
			var source, assigned, rawRequirements string
			require.NoError(t, ds.db.QueryRow(`SELECT source_id, runner_id, requirements FROM runner_jobs WHERE operation_id='probe/confirm'`).Scan(&source, &assigned, &rawRequirements))
			var requirements runner.ExecRequirements
			require.NoError(t, json.Unmarshal([]byte(rawRequirements), &requirements))
			assert.True(t, requirements.RequireExecutedModel)
			assert.Equal(t, rwAnthropic, requirements.RequiredSource)
			assert.Equal(t, "claude-opus-4-1", requirements.Model)
			assert.Equal(t, string(rwAnthropic), source)
			assert.Equal(t, string(runner.BuiltInRunnerID), assigned)
			jobs, err := ds.runners.ledger.ListActive(ctx)
			require.NoError(t, err)
			assert.Empty(t, jobs, "terminal probe releases its slot")
		})
	}
}
