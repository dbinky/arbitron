package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelPinsWiringInteractiveResumeSink drives the real initDomain for the
// interactive consumers: a run triggered on X parks at an interactive_agent
// node, the live agent profile is edited to Y, and the follow-up turn sent
// through the root-built ConversationHandler resumes the session (`--resume`)
// with `--model X` at the harness argv. A fresh run's follow-up carries Y.
// Dropping the ContextWithInstanceID call in conversationHandler.HandleMessage
// (or injecting the raw live resolver into NewConversationHandler) makes the old
// run's resumed turn carry Y and fails this test.
func TestModelPinsWiringInteractiveResumeSink(t *testing.T) {
	scratch := t.TempDir()
	argvLog := filepath.Join(scratch, "argv.log")
	goFile := filepath.Join(scratch, "go")
	require.NoError(t, os.WriteFile(goFile, nil, 0o600)) // never block the fake harness
	bin := fakeClaudeBinary(t, scratch, argvLog, goFile)

	ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "pin-chat", `{
  "name": "pin-chat",
  "description": "one interactive agent node",
  "inputs": [],
  "nodes": [
    {"id": "talk", "type": "interactive_agent", "entrypoint": true,
     "config": {"model_profile": "agent-alias", "orchestrationFile": "templates/o.md", "templateFile": "templates/p.md"}}
  ],
  "edges": []
}`)
		writePinTemplate(t, workspaceDir, "pin-chat")
		tpl := filepath.Join(workspaceDir, "users", localDefaultUser.String(), "pin-chat", "templates", "o.md")
		require.NoError(t, os.WriteFile(tpl, []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)

	agent := func(model string) provider.Profile {
		return provider.Profile{Alias: "agent-alias", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: model}
	}
	_, _, err := ds.profileSvc.Create(ctx, agent("claude-opus-4-1"))
	require.NoError(t, err)

	waitWaiting := func(t *testing.T, id [16]byte) {
		t.Helper()
		require.Eventually(t, func() bool {
			inst, gerr := ds.instanceRepo.Get(ctx, id)
			return gerr == nil && inst.State == instance.StateWaiting
		}, 20*time.Second, 50*time.Millisecond, "the run never parked at the interactive node")
	}

	id, _, err := ds.eng.Trigger(ctx, "pin-chat", map[string]any{}, "normal")
	require.NoError(t, err)
	waitWaiting(t, id)
	require.Len(t, readArgvLog(t, argvLog), 1, "first turn invokes the harness once")

	_, _, err = ds.profileSvc.Update(ctx, agent("claude-opus-4-5"))
	require.NoError(t, err)
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "follow-up"))

	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 2)
	assert.Contains(t, argv[0], "--model claude-opus-4-1", "first turn: %s", argv[0])
	assert.NotContains(t, argv[0], "--resume")
	assert.Contains(t, argv[1], "--resume s", "follow-up must resume the stored session: %s", argv[1])
	assert.Contains(t, argv[1], "--model claude-opus-4-1", "the resumed turn must carry the pinned model: %s", argv[1])
	assert.NotContains(t, argv[1], "claude-opus-4-5", "the resumed turn leaked the live edit: %s", argv[1])

	// A fresh run after the edit pins Y for its first and its resumed turn.
	id2, _, err := ds.eng.Trigger(ctx, "pin-chat", map[string]any{}, "normal")
	require.NoError(t, err)
	waitWaiting(t, id2)
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id2, "follow-up"))
	fresh := readArgvLog(t, argvLog)[2:]
	require.Len(t, fresh, 2)
	for _, line := range fresh {
		assert.Contains(t, line, "--model claude-opus-4-5", "a fresh run pins the edited model: %s", line)
	}
}

// Only the model is pinned: continuation tools must follow the current profile,
// not the starting profile or the streaming adapter's global fallback.
func TestModelPinsWiringInteractiveResumeLiveTools(t *testing.T) {
	scratch := t.TempDir()
	argvLog := filepath.Join(scratch, "argv.log")
	goFile := filepath.Join(scratch, "go")
	require.NoError(t, os.WriteFile(goFile, nil, 0o600))
	bin := fakeClaudeBinary(t, scratch, argvLog, goFile)

	ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n  allowedtools: [Read, Bash]\n", func(dir string) {
		writeWorkflow(t, dir, "pin-live-tools", `{
  "name":"pin-live-tools","inputs":[],"nodes":[
    {"id":"talk","type":"interactive_agent","entrypoint":true,
     "config":{"model_profile":"agent-alias","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}
  ],"edges":[]}`)
		writePinTemplate(t, dir, "pin-live-tools")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "users", localDefaultUser.String(), "pin-live-tools", "templates", "o.md"), []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)
	profile := provider.Profile{
		Alias: "agent-alias", Kind: provider.ProfileKindAgent, Backend: "claude-code",
		Model: "claude-opus-4-1", AllowedTools: []string{"Read", "Grep"},
	}
	_, _, err := ds.profileSvc.Create(ctx, profile)
	require.NoError(t, err)

	trigger := func() [16]byte {
		id, _, err := ds.eng.Trigger(ctx, "pin-live-tools", map[string]any{}, "normal")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			inst, err := ds.instanceRepo.Get(ctx, id)
			return err == nil && inst.State == instance.StateWaiting
		}, 20*time.Second, 25*time.Millisecond)
		return id
	}
	id := trigger()
	profile.Model = "claude-opus-4-5"
	profile.AllowedTools = []string{"Glob"}
	_, _, err = ds.profileSvc.Update(ctx, profile)
	require.NoError(t, err)
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "continue with the current tool scope"))
	fresh := trigger()
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, fresh, "continue"))

	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 4)
	assert.Contains(t, argv[0], "--allowedTools Read,Grep")
	assert.Contains(t, argv[0], "--model claude-opus-4-1")
	for i, line := range argv[1:] {
		assert.Contains(t, line, "--allowedTools Glob", "call %d must use the live profile scope", i+1)
		assert.NotContains(t, line, "Bash", "no global tool fallback")
		assert.NotContains(t, line, "Read,Grep", "no stale starting scope")
	}
	assert.Contains(t, argv[1], "--resume s")
	assert.Contains(t, argv[1], "--model claude-opus-4-1", "old run retains its model despite live tools")
	for _, line := range argv[2:] {
		assert.Contains(t, line, "--model claude-opus-4-5", "fresh run pins the live model")
	}
	for run, model := range map[[16]byte]string{id: "claude-opus-4-1", fresh: "claude-opus-4-5"} {
		saved, err := ds.instanceRepo.Get(ctx, run)
		require.NoError(t, err)
		assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
		assert.Equal(t, map[string]string{"agent-alias": model}, saved.ModelPins)
	}
}

// TestModelPinsWiringInteractiveResumeRejectsFallback drives the same root: the
// resumed turn's harness reports (via modelUsage) that it silently ran another
// model. The pinned run must refuse the turn rather than accept Y's output.
func TestModelPinsWiringInteractiveResumeRejectsFallback(t *testing.T) {
	scratch := t.TempDir()
	argvLog := filepath.Join(scratch, "argv.log")
	goFile := filepath.Join(scratch, "go")
	require.NoError(t, os.WriteFile(goFile, nil, 0o600))
	bin := fakeClaudeBinary(t, scratch, argvLog, goFile)

	ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "pin-chat", `{
  "name": "pin-chat",
  "description": "one interactive agent node",
  "inputs": [],
  "nodes": [
    {"id": "talk", "type": "interactive_agent", "entrypoint": true,
     "config": {"model_profile": "agent-alias", "orchestrationFile": "templates/o.md", "templateFile": "templates/p.md"}}
  ],
  "edges": []
}`)
		writePinTemplate(t, workspaceDir, "pin-chat")
		tpl := filepath.Join(workspaceDir, "users", localDefaultUser.String(), "pin-chat", "templates", "o.md")
		require.NoError(t, os.WriteFile(tpl, []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "agent-alias", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-opus-4-1"})
	require.NoError(t, err)

	id, _, err := ds.eng.Trigger(ctx, "pin-chat", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		inst, gerr := ds.instanceRepo.Get(ctx, id)
		return gerr == nil && inst.State == instance.StateWaiting
	}, 20*time.Second, 50*time.Millisecond, "the run never parked at the interactive node")

	before, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
	require.NoError(t, err)
	beforeJSON, err := json.Marshal(before)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(scratch, "executed-model"), []byte("claude-haiku-fallback"), 0o600))
	err = ds.conversationHandler.HandleMessage(ctx, id, "follow-up")
	require.Error(t, err, "a resumed turn that ran another model must be refused")
	assert.Contains(t, err.Error(), "claude-opus-4-1")
	assert.Contains(t, err.Error(), "modelUsage")
	after, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
	require.NoError(t, err)
	afterJSON, err := json.Marshal(after)
	require.NoError(t, err)
	assert.JSONEq(t, string(beforeJSON), string(afterJSON), "late model rejection must not persist the streamed response as success")
	inst, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, inst.State)
}

// TestModelPinsWiringEndSessionReleasesPinCache proves the root wires the
// conversation handler to the shared pin cache: a turn caches the run's pins,
// and EndSession drops them (the next read reloads from SQLite).
func TestModelPinsWiringEndSessionReleasesPinCache(t *testing.T) {
	scratch := t.TempDir()
	argvLog := filepath.Join(scratch, "argv.log")
	goFile := filepath.Join(scratch, "go")
	require.NoError(t, os.WriteFile(goFile, nil, 0o600))
	bin := fakeClaudeBinary(t, scratch, argvLog, goFile)

	ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "pin-chat", `{
  "name": "pin-chat",
  "description": "one interactive agent node",
  "inputs": [],
  "nodes": [
    {"id": "talk", "type": "interactive_agent", "entrypoint": true,
     "config": {"model_profile": "agent-alias", "orchestrationFile": "templates/o.md", "templateFile": "templates/p.md"}}
  ],
  "edges": []
}`)
		writePinTemplate(t, workspaceDir, "pin-chat")
		tpl := filepath.Join(workspaceDir, "users", localDefaultUser.String(), "pin-chat", "templates", "o.md")
		require.NoError(t, os.WriteFile(tpl, []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "agent-alias", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-opus-4-1"})
	require.NoError(t, err)

	id, _, err := ds.eng.Trigger(ctx, "pin-chat", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		inst, gerr := ds.instanceRepo.Get(ctx, id)
		return gerr == nil && inst.State == instance.StateWaiting
	}, 20*time.Second, 50*time.Millisecond, "the run never parked at the interactive node")

	require.True(t, ds.pinLookup.Cached(id), "the first turn resolves through the shared pin cache")
	_, err = ds.conversationHandler.EndSession(ctx, id)
	require.NoError(t, err)
	assert.False(t, ds.pinLookup.Cached(id), "EndSession must release the run's cached pins")
}

// TestModelPinsWiringInteractiveContinuationAfterRestart is the combined SQLite
// continuation fixture (A2l): two runs park at an interactive node on X, one is
// downgraded to a legacy NULL-pin row, the profile is edited to Y (model and
// tool scope), and the process restarts over the same database. The reopened
// root has a cold pin cache, so the pinned run's follow-up can only carry X by
// reloading its pins from SQLite, while its tools follow the live profile and
// the legacy run follows the live model. EndSession then releases the reloaded
// pins. Dropping the persisted pins, the ContextWithInstanceID call or the
// live tool scope fails one of these assertions.
func TestModelPinsWiringInteractiveContinuationAfterRestart(t *testing.T) {
	scratch := t.TempDir()
	argvLog := filepath.Join(scratch, "argv.log")
	goFile := filepath.Join(scratch, "go")
	require.NoError(t, os.WriteFile(goFile, nil, 0o600))
	bin := fakeClaudeBinary(t, scratch, argvLog, goFile)

	ds, closeStack := bootSinkRootWithClose(t, "llm:\n  binarypath: \""+bin+"\"\n  allowedtools: [Read, Bash]\n", func(dir string) {
		writeWorkflow(t, dir, "pin-restart-chat", `{
  "name":"pin-restart-chat","inputs":[],"nodes":[
    {"id":"talk","type":"interactive_agent","entrypoint":true,
     "config":{"model_profile":"agent-alias","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}
  ],"edges":[]}`)
		writePinTemplate(t, dir, "pin-restart-chat")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "users", localDefaultUser.String(), "pin-restart-chat", "templates", "o.md"), []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)
	profile := provider.Profile{
		Alias: "agent-alias", Kind: provider.ProfileKindAgent, Backend: "claude-code",
		Model: "claude-opus-4-1", AllowedTools: []string{"Read", "Grep"},
	}
	_, _, err := ds.profileSvc.Create(ctx, profile)
	require.NoError(t, err)

	trigger := func() uuid.UUID {
		id, _, err := ds.eng.Trigger(ctx, "pin-restart-chat", map[string]any{}, "normal")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			inst, err := ds.instanceRepo.Get(ctx, id)
			return err == nil && inst.State == instance.StateWaiting
		}, 20*time.Second, 25*time.Millisecond, "the run never parked at the interactive node")
		return id
	}
	pinned, legacy := trigger(), trigger()
	_, err = ds.db.ExecContext(ctx, `UPDATE instances SET model_pins = NULL, model_pins_version = 0 WHERE id = ?`, legacy.String())
	require.NoError(t, err)
	profile.Model, profile.AllowedTools = "claude-opus-4-5", []string{"Glob"}
	_, _, err = ds.profileSvc.Update(ctx, profile)
	require.NoError(t, err)
	require.Len(t, readArgvLog(t, argvLog), 2)

	configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
	closeStack()
	reopened, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { reopened.Close(context.Background()) })
	require.False(t, reopened.pinLookup.Cached(pinned), "a restarted root starts with a cold pin cache")

	require.NoError(t, reopened.conversationHandler.HandleMessage(ctx, pinned, "continue after restart"))
	require.NoError(t, reopened.conversationHandler.HandleMessage(ctx, legacy, "continue legacy"))
	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 4)
	assert.Contains(t, argv[2], "--resume s")
	assert.Contains(t, argv[2], "--model claude-opus-4-1", "the pin must reload from SQLite: %s", argv[2])
	assert.NotContains(t, argv[2], "claude-opus-4-5", "the restarted turn leaked the live edit: %s", argv[2])
	assert.Contains(t, argv[3], "--model claude-opus-4-5", "a legacy NULL-pin run follows the live profile: %s", argv[3])
	for _, line := range argv[2:] {
		assert.Contains(t, line, "--allowedTools Glob", "tools stay live across the restart: %s", line)
		assert.NotContains(t, line, "Read,Grep")
		assert.NotContains(t, line, "Bash")
	}

	require.True(t, reopened.pinLookup.Cached(pinned), "the continuation turn reloads the pins into the cache")
	_, err = reopened.conversationHandler.EndSession(ctx, pinned)
	require.NoError(t, err)
	assert.False(t, reopened.pinLookup.Cached(pinned), "EndSession must release the reloaded pins")
}
