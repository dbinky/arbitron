package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	refreshTalkWorkflow            = "refresh-pin-talk"
	refreshTalkOld, refreshTalkNew = "claude-sonnet-4-5", "claude-sonnet-4-6"
)

// refreshTalkRoot boots the real root with a fake claude that logs "$PWD $*"
// and reports the requested --model in modelUsage, unless dir/executed-model
// names a silent fallback. The following agent profile "builder" on
// refreshTalkOld is consumed by one interactive_agent node.
func refreshTalkRoot(t *testing.T) (ds *domainStack, closeStack func(), ctx context.Context, dir, argvLog string) {
	t.Helper()
	dir = t.TempDir()
	argvLog = filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"echo \"$PWD $*\" >> '" + argvLog + "'\n" +
		"cat > /dev/null\n" +
		"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
		"if [ -f '" + filepath.Join(dir, "executed-model") + "' ]; then m=$(cat '" + filepath.Join(dir, "executed-model") + "'); fi\n" +
		"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"s\",\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))

	ds, closeStack = bootSinkRootWithClose(t, "llm:\n  binarypath: \""+bin+"\"\n  allowedtools: [Bash]\nprofiles:\n"+
		"  - alias: builder\n    kind: agent\n    backend: claude-code\n    model: "+refreshTalkOld+"\n    allowedtools: [Read]\n"+
		"    selection_mode: following\n    follow_target:\n      provider: anthropic\n      tier: sonnet\n", func(workspace string) {
		writeWorkflow(t, workspace, refreshTalkWorkflow, `{"name":"`+refreshTalkWorkflow+`","inputs":[],"nodes":[{"id":"talk","type":"interactive_agent","entrypoint":true,"config":{"model_profile":"builder","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`)
		writePinTemplate(t, workspace, refreshTalkWorkflow)
		require.NoError(t, os.WriteFile(filepath.Join(workspace, "users", localDefaultUser.String(), refreshTalkWorkflow, "templates", "o.md"), []byte("orchestrate"), 0o600))
	})
	ctx = adminCtxForLocalUser(t, ds)
	storeConfiguredFollowing(t, ctx, ds, "builder")
	catalogURL := "https://catalog.example.test/api.json"
	_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
	require.NoError(t, err)
	return ds, closeStack, ctx, dir, argvLog
}

// runTurns returns the logged harness invocations of run turns, excluding
// refresh probes (which run in the workspace's .model-probe directory).
func runTurns(t *testing.T, argvLog string) []string {
	t.Helper()
	var turns []string
	for _, line := range readArgvLog(t, argvLog) {
		if !isProbeTurn(line) {
			turns = append(turns, line)
		}
	}
	return turns
}

func triggerParkedTalk(t *testing.T, ctx context.Context, ds *domainStack) uuid.UUID {
	t.Helper()
	id, _, err := ds.eng.Trigger(ctx, refreshTalkWorkflow, map[string]any{}, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, id)
		return err == nil && inst.State == instance.StateWaiting
	}, 20*time.Second, 25*time.Millisecond, "the run never parked at the interactive node")
	return id
}

// refreshTalkAdvances runs one real refresh cycle through the root graph (fetch,
// tool-free probes of both claude-code routes, CommitCycle CAS, ProfileCache
// publication) and requires the published X -> Y advancement.
func refreshTalkAdvances(t *testing.T, ctx context.Context, ds *domainStack) {
	t.Helper()
	deps := ds.refreshDeps
	deps.Fetcher = &fixtureFetcher{body: claudeCatalogFixture}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	require.Equal(t, refreshTalkOld, report.Changes[0].FromModel)
	require.Equal(t, refreshTalkNew, report.Changes[0].ToModel)
	require.False(t, report.PublicationPending, "publication must be confirmed before the refresh reports success")
	stored, err := ds.profileSvc.Get(ctx, "builder")
	require.NoError(t, err)
	require.Equal(t, refreshTalkNew, stored.Model)
}

func assertTalkPin(t *testing.T, ds *domainStack, id uuid.UUID, model string) {
	t.Helper()
	inst, err := ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, inst.ModelPinsVersion)
	assert.Equal(t, map[string]string{"builder": model}, inst.ModelPins)
}

// Paired seam paired:2, Area 6 interactive continuation <-> Area 4 refresh advancement.
// An interactive run started on X before a real refresh advances and publishes
// the following profile to Y resumes its session on X, with live tools, both
// in-process and after a restart with a cold pin cache; a resumed turn whose
// harness reports the advanced Y is refused; a run started after the refresh
// pins Y for its first and resumed turns.
func TestModelRefreshAdvancementKeepsInteractiveContinuationPinned(t *testing.T) {
	t.Run("in-process follow-up after advancement", func(t *testing.T) {
		ds, _, ctx, _, argvLog := refreshTalkRoot(t)
		r1 := triggerParkedTalk(t, ctx, ds)
		refreshTalkAdvances(t, ctx, ds)

		require.NoError(t, ds.conversationHandler.HandleMessage(ctx, r1, "follow-up"))
		turns := runTurns(t, argvLog)
		require.Len(t, turns, 2)
		assert.Contains(t, turns[0], "--model "+refreshTalkOld)
		assert.Contains(t, turns[1], "--resume s", "the follow-up resumes the stored session: %s", turns[1])
		assert.Contains(t, turns[1], "--model "+refreshTalkOld, "the resumed turn keeps the trigger-time pin: %s", turns[1])
		assert.NotContains(t, turns[1], refreshTalkNew, "the resumed turn leaked the advanced model: %s", turns[1])
		assert.Contains(t, turns[1], "--allowedTools Read", "tools stay live from the profile: %s", turns[1])
		assert.NotContains(t, turns[1], "Bash", "no global tool fallback: %s", turns[1])
		ne, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, r1, "talk")
		require.NoError(t, err)
		assert.Equal(t, "s", engine.SessionID(ne.LLMConversation), "the resumed session is recorded")
		assertTalkPin(t, ds, r1, refreshTalkOld)

		r2 := triggerParkedTalk(t, ctx, ds)
		require.NoError(t, ds.conversationHandler.HandleMessage(ctx, r2, "follow-up"))
		fresh := runTurns(t, argvLog)[2:]
		require.Len(t, fresh, 2)
		for _, line := range fresh {
			assert.Contains(t, line, "--model "+refreshTalkNew, "a run started after published advancement pins Y: %s", line)
		}
		assertTalkPin(t, ds, r2, refreshTalkNew)
		assertTalkPin(t, ds, r1, refreshTalkOld)
	})

	t.Run("resumed turn served by the advanced model is refused", func(t *testing.T) {
		ds, _, ctx, dir, _ := refreshTalkRoot(t)
		r1 := triggerParkedTalk(t, ctx, ds)
		refreshTalkAdvances(t, ctx, ds)
		before, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, r1, "talk")
		require.NoError(t, err)

		require.NoError(t, os.WriteFile(filepath.Join(dir, "executed-model"), []byte(refreshTalkNew), 0o600))
		err = ds.conversationHandler.HandleMessage(ctx, r1, "follow-up")
		require.Error(t, err, "a resumed X turn that the harness served on Y must be refused")
		assert.Contains(t, err.Error(), refreshTalkOld)
		after, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, r1, "talk")
		require.NoError(t, err)
		assert.Equal(t, before.LLMConversation, after.LLMConversation, "the refused turn is not recorded")
		inst, err := ds.instanceRepo.Get(ctx, r1)
		require.NoError(t, err)
		assert.Equal(t, instance.StateWaiting, inst.State)
	})

	t.Run("follow-up after restart reloads the pin", func(t *testing.T) {
		ds, closeStack, ctx, _, argvLog := refreshTalkRoot(t)
		r1 := triggerParkedTalk(t, ctx, ds)
		refreshTalkAdvances(t, ctx, ds)
		configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
		closeStack()

		reopened, err := initDomain(context.Background(), configPath)
		require.NoError(t, err)
		t.Cleanup(func() { reopened.Close(context.Background()) })
		stored, err := reopened.profileSvc.Get(ctx, "builder")
		require.NoError(t, err)
		require.Equal(t, refreshTalkNew, stored.Model, "the advancement is durable across restart")
		require.False(t, reopened.pinLookup.Cached(r1), "a restarted root starts with a cold pin cache")

		require.NoError(t, reopened.conversationHandler.HandleMessage(ctx, r1, "continue after restart"))
		turns := runTurns(t, argvLog)
		require.Len(t, turns, 2)
		assert.Contains(t, turns[1], "--resume s")
		assert.Contains(t, turns[1], "--model "+refreshTalkOld, "the pin must reload from SQLite, not the advanced row: %s", turns[1])
		assert.NotContains(t, turns[1], refreshTalkNew)
		assertTalkPin(t, reopened, r1, refreshTalkOld)
	})
}
