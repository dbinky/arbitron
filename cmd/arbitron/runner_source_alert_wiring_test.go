package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerWiring_LoggedOutReachesNotificationEventSurfacesAndTeams(t *testing.T) {
	cfg, fake := rwWriteRoot(t, 8)
	fixture, err := os.ReadFile(filepath.Join("..", "..", "adapter", "claudecli", "testdata", "outcomes", "authentication_failed.jsonl"))
	require.NoError(t, err)
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo '2.1.330 (Claude Code)'; exit 0;;\nauth) echo '{\"loggedIn\":true}'; exit 0;;\nesac\ncat >/dev/null\ncat <<'OUTCOME_FIXTURE'\n" + string(fixture) + "\nOUTCOME_FIXTURE\n"
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(script), 0o700))
	teams := newAnnouncementReceiver(t)
	ds, err := initDomainWithModelDependencies(context.Background(), cfg, ModelDependencies{
		FetchTransport: catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errOfflineCatalog }),
		TeamsTransport: teams.transport,
	})
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	ctx := adminCtxForLocalUser(t, ds)
	setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/source-alerts?token=private-setting")
	result, _ := mcpToolCaller(t, ctx, ds)("system_update_teams_config", map[string]any{"enabled": true, "webhookUrl": "https://teams.example.test/source-alerts?token=private-setting"})
	require.False(t, result.IsError, "%+v", result.Content)
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "rr-chat", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-5"})
	require.NoError(t, err)

	id, _, err := ds.eng.Trigger(ctx, "rr-block", map[string]any{}, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ds.eng.Cancel(ctx, id) })
	require.Eventually(t, func() bool { return rwSourceHealth(t, ds, rwAnthropic).Auth == runner.AuthLoggedOut },
		20*time.Second, 20*time.Millisecond, "the classified outcome reaches runner_sources")
	// The health row is written before alerts/events; wait for the external sink
	// before asserting exact counts, rather than racing the outcome callback.
	require.Eventually(t, func() bool { return len(teams.cards("source")) == 1 }, 10*time.Second, 20*time.Millisecond, "one Teams card")
	ds.runners.alerts.Wait()

	var notes int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE type = 'source_logged_out' AND instance_id IS NULL`).Scan(&notes))
	assert.Equal(t, 1, notes, "exactly one instance-less notification")
	var events int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM pool_events WHERE kind = 'source.logged_out' AND source_id = ?`, string(rwAnthropic)).Scan(&events))
	assert.Equal(t, 1, events, "exactly one pool event")
	require.Eventually(t, func() bool { return len(teams.cards("source")) == 1 }, 10*time.Second, 20*time.Millisecond, "one Teams card")

	isErr, text := mcpToolCallerAs(t, ds, ctx)("runner_list", map[string]any{})
	require.False(t, isErr, text)
	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, []byte(text)))
	assert.Contains(t, compact.String(), `"auth":"logged_out"`, "MCP shows the logged-out source")
	srv := httptest.NewServer(adapthttp.NewRouterFromDeps(ds.httpDeps()))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/api/v1/runners")
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"auth":"logged_out"`, "REST shows the logged-out source")
	for _, leak := range []string{"private-setting", "Failed to authenticate", "OAuth"} {
		assert.NotContains(t, string(raw), leak)
		assert.NotContains(t, text, leak)
	}
}

func TestRunnerWiring_StreamEOFAndCancelNeverReportSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, tail string
		cancel     bool
	}{
		{"eof_without_result", "exit 0\n", false},
		{"error_after_partial_token", "echo 'boom' >&2; exit 1\n", false},
		{"cancelled_mid_stream", "while [ ! -f '@DIR@/never' ]; do sleep 0.05; done\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, fake := rwWriteRoot(t, 8)
			script := "#!/bin/sh\ncase \"$1\" in\n--version) echo '2.1.330 (Claude Code)'; exit 0;;\nauth) echo '{\"loggedIn\":true}'; exit 0;;\nesac\ncat >/dev/null\n" +
				"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"par\"}]}}'\n" +
				strings.ReplaceAll(tc.tail, "@DIR@", fake)
			require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(script), 0o700))
			ds, err := initDomainWithModelDependencies(context.Background(), cfg, ModelDependencies{
				FetchTransport: catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errOfflineCatalog }),
			})
			require.NoError(t, err)
			t.Cleanup(func() { ds.Close(context.Background()) })
			ctx := adminCtxForLocalUser(t, ds)
			_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "rr-agent", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-opus-4-1"})
			require.NoError(t, err)
			id, _, err := ds.eng.Trigger(ctx, "rr-talk", map[string]any{}, "normal")
			require.NoError(t, err)
			t.Cleanup(func() { _ = ds.eng.Cancel(ctx, id) })
			require.Eventually(t, func() bool { return rwCountJobs(ds.db, "op = 'stream'") > 0 }, 20*time.Second, 20*time.Millisecond, "the turn was placed")
			if tc.cancel {
				require.Eventually(t, func() bool { return rwCountJobs(ds.db, "op = 'stream' AND state = 'running'") == 1 }, 20*time.Second, 20*time.Millisecond)
				require.NoError(t, ds.eng.Cancel(ctx, id))
			}
			require.Eventually(t, func() bool { return rwCountJobs(ds.db, "state IN ('offered','running')") == 0 },
				20*time.Second, 20*time.Millisecond, "the slot is released")
			ds.runners.monitor.Wait()
			var completed int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM runner_job_attempts a JOIN runner_jobs j ON j.id = a.job_id WHERE j.run_id = ? AND a.outcome = 'completed'`, id.String()).Scan(&completed))
			assert.Zero(t, completed, "no completed attempt without a terminal result")
			h := rwSourceHealth(t, ds, rwAnthropic)
			assert.NotEqual(t, runner.OutcomeNone, h.LastOutcome, "no success outcome recorded")
			assert.NotEqual(t, runner.AuthOK, h.Auth, "a non-terminal stream is not success evidence (inventory registers AuthUnknown; only success sets AuthOK)")
		})
	}
}
