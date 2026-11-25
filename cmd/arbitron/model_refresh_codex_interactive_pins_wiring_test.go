package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const refreshCodexTalkOld, refreshCodexTalkNew = "gpt-5-sol", "gpt-5.1-sol"

// refreshCodexTalkScript is a fake codex that logs "$PWD $*", records each turn
// in the account's rollout for its session (appending on `exec resume <id>`)
// and serves the requested -m unless @FALLBACK@ names a silent fallback.
const refreshCodexTalkScript = `#!/bin/sh
if [ "$1" = '--version' ]; then echo 'codex-cli 0.171.0'; exit 0; fi
[ -z "$OPENAI_API_KEY" ] || exit 40
[ -f "$CODEX_HOME/models_cache.json" ] || exit 42
case "$PWD" in
 */.model-probe/check-*)
  case " $* " in *' --strict-config '*' mcp '*) echo 'Error: --strict-config is not supported for codex mcp' >&2; exit 1 ;; esac
  for restriction in 'features.plugins=false' 'features.apps=false' 'web_search="disabled"' 'sandbox_workspace_write.network_access=false'; do
   case " $* " in *" $restriction "*) ;; *) exit 43 ;; esac
  done
  case " $* " in *' mcp list --json '*) echo '[]'; exit 0 ;; esac
  case " $* " in *' --strict-config '*) ;; *) exit 43 ;; esac
  case " $* " in *' --sandbox read-only '*|*' -s read-only '*) ;; *) exit 44 ;; esac
  case " $* " in *' mcp_servers={} '*) ;; *) exit 45 ;; esac
  [ "$(cat)" = 'Reply with only your model identity on one line. Do not use tools or take any other action.' ] || exit 46
  ;;
 *) cat > /dev/null ;;
esac
echo "$PWD $*" >> '@ARGV@'
case "$*" in *dangerously-bypass*) exit 41 ;; esac
m=$(echo "$*" | sed -n 's/.* -m \([^ ]*\).*/\1/p')
if [ -f '@FALLBACK@' ]; then m=$(cat '@FALLBACK@'); fi
d="$CODEX_HOME/sessions/2026/10/19"
mkdir -p "$d"
if [ "$2" = 'resume' ]; then
  sid="$3"
else
  sid="talk-thread-$$"
  echo '{"type":"session_meta","payload":{"id":"'"$sid"'"}}' > "$d/rollout-t-$sid.jsonl"
fi
echo '{"type":"turn_context","payload":{"model":"'"$m"'"}}' >> "$d/rollout-t-$sid.jsonl"
echo '{"type":"thread.started","thread_id":"'"$sid"'"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"output from '"$m"'"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`

// refreshCodexTalkRoot boots the real root with an opted-in Codex interactive
// route and the following codex agent profile "builder" on
// refreshCodexTalkOld, consumed by one interactive_agent node.
func refreshCodexTalkRoot(t *testing.T) (ds *domainStack, closeStack func(), ctx context.Context, fallback, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "account")
	argvLog, fallback = filepath.Join(dir, "argv"), filepath.Join(dir, "fallback-model")
	script := strings.NewReplacer("@ARGV@", argvLog, "@FALLBACK@", fallback).Replace(refreshCodexTalkScript)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", home)
	t.Setenv("OPENAI_API_KEY", "must-not-reach-codex")
	t.Setenv(codexInteractiveVerifiedEnv, "true")
	writeCodexAccountModels(t, home, refreshCodexTalkOld, refreshCodexTalkNew)

	ds, closeStack = bootSinkRootWithClose(t, "llm:\n  defaultagentbackend: codex\nprofiles:\n"+
		"  - alias: builder\n    kind: agent\n    backend: codex\n    model: "+refreshCodexTalkOld+"\n    selection_mode: following\n"+
		"    follow_target:\n      provider: openai\n      tier: sol\n", func(workspace string) {
		writeWorkflow(t, workspace, refreshTalkWorkflow, `{"name":"`+refreshTalkWorkflow+`","inputs":[],"nodes":[{"id":"talk","type":"interactive_agent","entrypoint":true,"config":{"model_profile":"builder","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`)
		writePinTemplate(t, workspace, refreshTalkWorkflow)
		require.NoError(t, os.WriteFile(filepath.Join(workspace, "users", localDefaultUser.String(), refreshTalkWorkflow, "templates", "o.md"), []byte("orchestrate"), 0o600))
	})
	ctx = adminCtxForLocalUser(t, ds)
	storeConfiguredFollowing(t, ctx, ds, "builder")
	catalogURL := "https://catalog.example.test/api.json"
	_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
	require.NoError(t, err)
	return ds, closeStack, ctx, fallback, argvLog
}

// refreshCodexTalkAdvances runs one real refresh cycle through the root graph
// (fetch, read-only probes of every codex route, CommitCycle CAS, ProfileCache
// publication) and requires the published X -> Y advancement.
func refreshCodexTalkAdvances(t *testing.T, ctx context.Context, ds *domainStack) {
	t.Helper()
	deps := ds.refreshDeps
	deps.Fetcher = &fixtureFetcher{body: codexCatalogFixture}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	require.Equal(t, refreshCodexTalkOld, report.Changes[0].FromModel)
	require.Equal(t, refreshCodexTalkNew, report.Changes[0].ToModel)
	require.False(t, report.PublicationPending, "publication must be confirmed before the refresh reports success")
	stored, err := ds.profileSvc.Get(ctx, "builder")
	require.NoError(t, err)
	require.Equal(t, refreshCodexTalkNew, stored.Model)
}

// Paired seam paired:2, Area 6 interactive continuation <-> Area 4 refresh advancement,
// Codex sibling of TestModelRefreshAdvancementKeepsInteractiveContinuationPinned.
// A Codex interactive run started on X before a real refresh advances and
// publishes the following profile to Y resumes its stored session on X, both
// in-process and after a restart with a cold pin cache; a resumed turn whose
// rollout records Y is refused and not persisted; a run started after the
// refresh pins Y.
func TestModelRefreshAdvancementKeepsCodexInteractiveContinuationPinned(t *testing.T) {
	t.Run("in-process follow-up after advancement", func(t *testing.T) {
		ds, _, ctx, _, argvLog := refreshCodexTalkRoot(t)
		r1 := triggerParkedTalk(t, ctx, ds)
		refreshCodexTalkAdvances(t, ctx, ds)

		require.NoError(t, ds.conversationHandler.HandleMessage(ctx, r1, "follow-up"))
		turns := runTurns(t, argvLog)
		require.Len(t, turns, 2)
		assert.Contains(t, turns[0], "-m "+refreshCodexTalkOld)
		assert.Contains(t, turns[1], "exec resume talk-thread-", "the follow-up resumes the stored session: %s", turns[1])
		assert.Contains(t, turns[1], "-m "+refreshCodexTalkOld+" ", "the resumed turn keeps the trigger-time pin: %s", turns[1])
		assert.NotContains(t, turns[1], refreshCodexTalkNew, "the resumed turn leaked the advanced model: %s", turns[1])
		ne, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, r1, "talk")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(engine.SessionID(ne.LLMConversation), "talk-thread-"), "the resumed session is recorded")
		assert.NotContains(t, fmt.Sprint(ne.LLMConversation), "output from "+refreshCodexTalkNew)
		assertTalkPin(t, ds, r1, refreshCodexTalkOld)

		r2 := triggerParkedTalk(t, ctx, ds)
		require.NoError(t, ds.conversationHandler.HandleMessage(ctx, r2, "follow-up"))
		fresh := runTurns(t, argvLog)[2:]
		require.Len(t, fresh, 2)
		for _, line := range fresh {
			assert.Contains(t, line, "-m "+refreshCodexTalkNew, "a run started after published advancement pins Y: %s", line)
		}
		assertTalkPin(t, ds, r2, refreshCodexTalkNew)
		assertTalkPin(t, ds, r1, refreshCodexTalkOld)
	})

	t.Run("resumed turn served by the advanced model is refused", func(t *testing.T) {
		ds, _, ctx, fallback, _ := refreshCodexTalkRoot(t)
		r1 := triggerParkedTalk(t, ctx, ds)
		refreshCodexTalkAdvances(t, ctx, ds)
		before, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, r1, "talk")
		require.NoError(t, err)

		require.NoError(t, os.WriteFile(fallback, []byte(refreshCodexTalkNew), 0o600))
		err = ds.conversationHandler.HandleMessage(ctx, r1, "follow-up")
		require.ErrorContains(t, err, "executed-model evidence rejected", "a resumed X turn whose rollout records Y must be refused")
		after, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, r1, "talk")
		require.NoError(t, err)
		assert.Equal(t, before.LLMConversation, after.LLMConversation, "the refused turn is not recorded")
		inst, err := ds.instanceRepo.Get(ctx, r1)
		require.NoError(t, err)
		assert.Equal(t, instance.StateWaiting, inst.State)
		assertTalkPin(t, ds, r1, refreshCodexTalkOld)
	})

	t.Run("follow-up after restart reloads the pin", func(t *testing.T) {
		ds, closeStack, ctx, _, argvLog := refreshCodexTalkRoot(t)
		r1 := triggerParkedTalk(t, ctx, ds)
		refreshCodexTalkAdvances(t, ctx, ds)
		configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
		closeStack()

		reopened, err := initDomain(context.Background(), configPath)
		require.NoError(t, err)
		t.Cleanup(func() { reopened.Close(context.Background()) })
		stored, err := reopened.profileSvc.Get(ctx, "builder")
		require.NoError(t, err)
		require.Equal(t, refreshCodexTalkNew, stored.Model, "the advancement is durable across restart")
		require.False(t, reopened.pinLookup.Cached(r1), "a restarted root starts with a cold pin cache")

		require.NoError(t, reopened.conversationHandler.HandleMessage(ctx, r1, "continue after restart"))
		turns := runTurns(t, argvLog)
		require.Len(t, turns, 2)
		assert.Contains(t, turns[1], "exec resume talk-thread-")
		assert.Contains(t, turns[1], "-m "+refreshCodexTalkOld+" ", "the pin must reload from SQLite, not the advanced row: %s", turns[1])
		assert.NotContains(t, turns[1], refreshCodexTalkNew)
		assertTalkPin(t, reopened, r1, refreshCodexTalkOld)
	})
}
