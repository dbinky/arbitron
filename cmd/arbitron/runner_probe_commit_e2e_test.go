package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The recovery-time snapshot, not a later poll, must see the committed probe
// result and closed attempt. Only the native CLI leaf is replaced.
func TestPhase3E2E_HealthConfirmationReleasesSlot(t *testing.T) {
	for _, failCommit := range []bool{false, true} {
		name := "committed before recovery"
		if failCommit {
			name = "commit failure leaves probation"
		}
		t.Run(name, func(t *testing.T) {
			ds, fake := rwBoot(t, 1)
			ctx := adminCtxForLocalUser(t, ds)
			rwChatProfile(t, ctx, ds)
			rwTouch(t, fake, "exhausted")
			run, _, err := ds.eng.Trigger(ctx, "rr-block", map[string]any{}, "normal")
			require.NoError(t, err)
			t.Cleanup(func() { _ = ds.eng.Cancel(ctx, run) })
			require.Eventually(t, func() bool {
				return rwSourceHealth(t, ds, rwAnthropic).Capacity == runner.CapacityExhausted
			}, 10*time.Second, 20*time.Millisecond)
			require.NoError(t, ds.eng.Cancel(ctx, run))
			require.Eventually(t, func() bool {
				jobs, err := ds.runners.ledger.ListActive(ctx)
				return err == nil && len(jobs) == 0
			}, 10*time.Second, 20*time.Millisecond)

			_, err = ds.db.Exec(`CREATE TABLE probe_recovery_snapshot (state TEXT, result TEXT, open_attempts INTEGER, held_slots INTEGER);
CREATE TRIGGER snapshot_probe_recovery AFTER UPDATE OF capacity ON runner_sources
WHEN OLD.capacity = 'probation' AND NEW.capacity = 'available'
BEGIN
 INSERT INTO probe_recovery_snapshot
 SELECT j.state, j.result_ref,
  (SELECT COUNT(*) FROM runner_job_attempts a WHERE a.job_id=j.id AND a.ended_at IS NULL),
  (SELECT COUNT(*) FROM runner_jobs h WHERE h.runner_id=j.runner_id AND h.state IN ('offered','running'))
 FROM runner_jobs j WHERE j.operation_id='probe/confirm';
END;`)
			require.NoError(t, err)
			if failCommit {
				_, err = ds.db.Exec(`CREATE TRIGGER fail_probe_commit BEFORE UPDATE OF state ON runner_jobs
WHEN NEW.operation_id='probe/confirm' AND NEW.state='completed'
BEGIN SELECT RAISE(ABORT, 'probe commit disk failure'); END;`)
				require.NoError(t, err)
			}
			script := `#!/bin/sh
case "$1" in
 --version) echo '2.1.330 (Claude Code)'; exit 0;;
 auth) echo '{"loggedIn":true}'; exit 0;;
esac
cat >/dev/null
echo "$*" >> '@DIR@/probe-native.log'
echo '{"type":"result","is_error":false,"result":"OK","session_id":"probe-session","modelUsage":{"claude-opus-4-1":{}}}'
`
			require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(strings.ReplaceAll(script, "@DIR@", fake)), 0o700))
			past := time.Now().Add(-time.Hour)
			require.NoError(t, ds.runners.monitor.ApplyOutcome(ctx, rwAnthropic, runner.SourceOutcome{
				Class: runner.OutcomeExhausted, Model: "claude-opus-4-1", ResetAt: &past,
			}))
			require.NoError(t, ds.runners.monitor.Tick(ctx))
			ds.runners.monitor.Wait()

			argv, err := os.ReadFile(filepath.Join(fake, "probe-native.log"))
			require.NoError(t, err)
			assert.Equal(t, 1, strings.Count(string(argv), "\n"))
			assert.Contains(t, string(argv), "--model claude-opus-4-1")
			assert.Contains(t, string(argv), "--tools")
			var state, result, outcome, source string
			var ended bool
			require.NoError(t, ds.db.QueryRow(`SELECT j.state, j.result_ref, a.outcome, a.ended_at IS NOT NULL, j.source_id
FROM runner_jobs j JOIN runner_job_attempts a ON a.job_id=j.id AND a.generation=j.generation
WHERE j.operation_id='probe/confirm'`).Scan(&state, &result, &outcome, &ended, &source))
			assert.Equal(t, string(rwAnthropic), source)
			assert.True(t, ended)
			jobs, err := ds.runners.ledger.ListActive(ctx)
			require.NoError(t, err)
			assert.Empty(t, jobs, "the cap-one slot is released on success and on commit failure")
			if failCommit {
				assert.Equal(t, "failed", state)
				assert.NotEqual(t, "completed", outcome)
				assert.Empty(t, result)
				assert.Equal(t, runner.CapacityProbation, rwSourceHealth(t, ds, rwAnthropic).Capacity)
				var recovered int
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM probe_recovery_snapshot`).Scan(&recovered))
				assert.Zero(t, recovered, "failed durable completion never reports recovery")
			} else {
				assert.Equal(t, "completed", state)
				assert.Equal(t, "completed", outcome)
				assert.Equal(t, "probe:claude-opus-4-1", result, "the explicit probe owner wrote its validated result")
				assert.Equal(t, runner.CapacityAvailable, rwSourceHealth(t, ds, rwAnthropic).Capacity)
				var snapshotState, snapshotResult string
				var open, held int
				require.NoError(t, ds.db.QueryRow(`SELECT state, result, open_attempts, held_slots FROM probe_recovery_snapshot`).Scan(&snapshotState, &snapshotResult, &open, &held))
				assert.Equal(t, "completed", snapshotState)
				assert.Equal(t, result, snapshotResult)
				assert.Zero(t, open)
				assert.Zero(t, held)
			}
		})
	}
}
