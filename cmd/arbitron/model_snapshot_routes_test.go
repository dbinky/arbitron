package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise the real deployment default, inventory, cache, SQLite and launcher.
// Only the codex process is substituted; it refuses an absent/wrong model.
func TestModelSnapshotDefaultCodexRoot(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	home := filepath.Join(dir, "account")
	t.Setenv("CODEX_HOME", home)
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then echo 'codex-cli 0.139.0'; exit 0; fi
printf '%%s\n' "$*" >> %q
case " $* " in *" -m gpt-5-sol "*) ;; *) exit 42 ;; esac
[ "$CODEX_HOME" = %q ] && [ -z "$OPENAI_API_KEY" ] || exit 43
cat >/dev/null
mkdir -p "$CODEX_HOME/sessions/2026/10/16"
printf '%%s\n' '{"type":"session_meta","payload":{"id":"snapshot-thread"}}' '{"type":"turn_context","payload":{"model":"gpt-5-sol"}}' > "$CODEX_HOME/sessions/2026/10/16/rollout-test-snapshot-thread.jsonl"
printf '%%s\n' '{"type":"thread.started","thread_id":"snapshot-thread"}' '{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`, argvLog, home)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ds := bootSinkRoot(t, "llm:\n  defaultagentbackend: codex\n", func(workspace string) {
		writeWorkflow(t, workspace, "snapshot-codex", `{"name":"snapshot-codex","nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"model_profile":"snapshot-agent","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, workspace, "snapshot-codex")
	})
	ctx := adminCtxForLocalUser(t, ds)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "snapshot-agent", Kind: provider.ProfileKindAgent, Model: "gpt-5-sol"})
	require.NoError(t, err)
	id, _, err := ds.eng.Trigger(ctx, "snapshot-codex", nil, "normal")
	require.NoError(t, err)
	saved, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
	assert.Equal(t, map[string]string{"snapshot-agent": "gpt-5-sol"}, saved.ModelPins)
	terminal := waitTerminal(t, ds, id)
	trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, terminal.State, "trace: %+v", trace)
	lines := readArgvLog(t, argvLog)
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "-m gpt-5-sol")
}

func TestModelSnapshotConflictingNodeOverrideRoot(t *testing.T) {
	dir := t.TempDir()
	argvLog, goFile := filepath.Join(dir, "argv.log"), filepath.Join(dir, "go")
	bin := fakeClaudeBinary(t, dir, argvLog, goFile)
	require.NoError(t, os.WriteFile(goFile, nil, 0o600))
	ds := bootSinkRoot(t, "llm:\n  binarypath: "+bin+"\n", func(workspace string) {
		// Even an unreachable branch must be included in pin validation.
		writeWorkflow(t, workspace, "snapshot-conflict", `{"name":"snapshot-conflict","nodes":[
   {"id":"work","type":"agent_call","entrypoint":true,"config":{"model_profile":"agent","templateFile":"templates/p.md"}},
   {"id":"other","type":"agent_call","config":{"model_profile":"agent","backend":"codex","templateFile":"templates/p.md"}}
  ],"edges":[]}`)
		writePinTemplate(t, workspace, "snapshot-conflict")
	})
	ctx := adminCtxForLocalUser(t, ds)
	storeProfileUnchecked(t, ctx, ds, provider.Profile{Alias: "agent", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-opus-4-1"})
	before := countInstances(t, ds)
	_, _, err := ds.eng.Trigger(ctx, "snapshot-conflict", nil, "normal")
	require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
	assert.Equal(t, before, countInstances(t, ds))
	ds.eng.Shutdown()
	assert.Empty(t, readArgvLog(t, argvLog), "invalid snapshot must not launch even its valid first node")
}

// A concurrent publication replaces both aliases as one cache generation.
// Assert SQLite AND the HTTP sink: X survives for the active run, Y is used by
// the waiting new trigger. No test-built snapshotter or resolver is injected.
func TestTriggerPinsCoherentGeneration(t *testing.T) {
	ds := bootSinkRoot(t, "", func(workspace string) {
		writeWorkflow(t, workspace, "snapshot-generation", `{"name":"snapshot-generation","nodes":[
		 {"id":"first","type":"llm_call","entrypoint":true,"config":{"model_profile":"first","templateFile":"templates/p.md"}},
		 {"id":"second","type":"llm_call","config":{"model_profile":"second","templateFile":"templates/p.md"}}
		],"edges":[{"source":"first","target":"second"}]}`)
		writePinTemplate(t, workspace, "snapshot-generation")
	})
	ctx := adminCtxForLocalUser(t, ds)
	sink, releaseSink := lifecycleSink(t, false)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "sink", Provider: "openrouter", BaseURL: sink.srv.URL}, "k"))
	for _, alias := range []string{"first", "second"} {
		_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: alias, Kind: provider.ProfileKindChat, ConnectionRef: "sink", Model: lifecycleModelX})
		require.NoError(t, err)
	}
	oldID, _, err := ds.eng.Trigger(ctx, "snapshot-generation", nil, "normal")
	require.NoError(t, err)
	awaitLifecycleRequest(t, sink)

	held, publish, mutationDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		mutationDone <- ds.executionGate.WithMutation(ctx, func(ctx context.Context) error {
			close(held)
			<-publish
			// Use the real persisted source and the root's real cache reloader.
			repo := adaptsqlite.NewProfileRepo(ds.db)
			for _, alias := range []string{"first", "second"} {
				p, err := repo.Get(ctx, alias)
				if err != nil {
					return err
				}
				p.Model = lifecycleModelY
				if err := repo.Update(ctx, p); err != nil {
					return err
				}
			}
			return ds.profileResolver.(interface{ Reload(context.Context) error }).Reload(ctx)
		})
	}()
	<-held
	type result struct {
		id  uuid.UUID
		err error
	}
	triggerDone := make(chan result, 1)
	go func() {
		id, _, err := ds.eng.Trigger(ctx, "snapshot-generation", nil, "normal")
		triggerDone <- result{id, err}
	}()
	select {
	case <-triggerDone:
		close(publish)
		<-mutationDone
		t.Fatal("trigger bypassed the publication lease")
	case <-time.After(100 * time.Millisecond):
	}
	close(publish)
	require.NoError(t, <-mutationDone)
	fresh := <-triggerDone
	require.NoError(t, fresh.err)
	releaseSink()
	for _, tc := range []struct {
		id    uuid.UUID
		model string
	}{{oldID, lifecycleModelX}, {fresh.id, lifecycleModelY}} {
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, tc.id).State)
		saved, err := ds.instanceRepo.Get(ctx, tc.id)
		require.NoError(t, err)
		assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
		assert.Equal(t, map[string]string{"first": tc.model, "second": tc.model}, saved.ModelPins)
		// Mutating a returned map cannot rewrite persistence or the live cache.
		saved.ModelPins["first"] = "sonnet"
		again, err := ds.instanceRepo.Get(ctx, tc.id)
		require.NoError(t, err)
		assert.Equal(t, tc.model, again.ModelPins["first"])
	}
	assert.ElementsMatch(t, []string{lifecycleModelX, lifecycleModelX, lifecycleModelY, lifecycleModelY}, sink.seen())
	binding, err := ds.profileResolver.Resolve(ctx, "first")
	require.NoError(t, err)
	assert.Equal(t, lifecycleModelY, binding.Model)
}

func TestModelSnapshotWriteFaultDoesNotEnqueue(t *testing.T) {
	ds := bootSinkStack(t)
	sink := newModelSink(t)
	ctx := installLifecycleProfile(t, ds, sink)
	_, err := ds.db.Exec(`CREATE TRIGGER reject_instance BEFORE INSERT ON instances BEGIN SELECT RAISE(ABORT, 'injected instance write fault'); END`)
	require.NoError(t, err)
	before := countInstances(t, ds)
	_, started, err := ds.eng.Trigger(ctx, "pin-llm", nil, "normal")
	require.ErrorContains(t, err, "injected instance write fault")
	assert.False(t, started)
	assert.Equal(t, before, countInstances(t, ds))
	ds.eng.Shutdown()
	assert.Empty(t, sink.seen())
}

func TestModelSnapshotRouteReadErrorRoot(t *testing.T) {
	ds := bootPinStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	require.NoError(t, err)
	// Instance storage is required for Create, independently of route capture.
	_, err = ds.db.ExecContext(ctx, `ALTER TABLE instances RENAME TO unavailable_instances`)
	require.NoError(t, err)
	defer func() {
		_, err := ds.db.ExecContext(context.Background(), `ALTER TABLE unavailable_instances RENAME TO instances`)
		require.NoError(t, err)
	}()
	_, _, err = ds.eng.Trigger(ctx, "pin-wf", nil, "normal")
	require.ErrorContains(t, err, "failed to create instance")
	var rows int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM unavailable_instances`).Scan(&rows))
	assert.Zero(t, rows)
}

// Global route enumeration must not let an unrelated workflow or run stop a
// valid trigger. Restoring VersionRequests in ModelSnapshotter makes this fail.
func TestModelSnapshotTriggerIgnoresUnrelatedInventoryFaults(t *testing.T) {
	for _, fault := range []string{"manifest", "route", "run pins"} {
		t.Run(fault, func(t *testing.T) {
			var workspace string
			ds := bootSinkRoot(t, "", func(dir string) {
				workspace = dir
				writeWorkflow(t, dir, "isolated", `{"name":"isolated","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"pin-alias","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, dir, "isolated")
			})
			sink := newModelSink(t)
			ctx := installLifecycleProfile(t, ds, sink)
			switch fault {
			case "manifest":
				writeWorkflow(t, workspace, "broken", `{truncated`)
			case "route":
				writeWorkflow(t, workspace, "broken", `{"name":"broken","nodes":[{"id":"wrong-kind","type":"agent_call","config":{"model_profile":"pin-alias"}}],"edges":[]}`)
			case "run pins":
				_, err := ds.db.ExecContext(ctx, `INSERT INTO instances (id,owner_id,workflow_name,workflow_snapshot,state,priority,variables,original_variables,model_pins,model_pins_version) VALUES (?,?,'broken','{}','failed','normal','{}','{}','{corrupt',1)`, uuid.New().String(), localDefaultUser.String())
				require.NoError(t, err)
			}
			id, _, err := ds.eng.Trigger(ctx, "isolated", nil, "normal")
			require.NoError(t, err)
			require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
			saved, err := ds.instanceRepo.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"pin-alias": lifecycleModelX}, saved.ModelPins)
			assert.Equal(t, []string{lifecycleModelX}, sink.seen())

			// The same invalid route in THIS trigger still fails before persistence.
			writeWorkflow(t, workspace, "isolated", `{"name":"isolated","nodes":[{"id":"ask","type":"agent_call","entrypoint":true,"config":{"model_profile":"pin-alias","templateFile":"templates/p.md"}}],"edges":[]}`)
			before := countInstances(t, ds)
			_, started, err := ds.eng.Trigger(ctx, "isolated", nil, "normal")
			require.ErrorIs(t, err, engine.ErrBindingKindMismatch)
			assert.False(t, started)
			assert.Equal(t, before, countInstances(t, ds))
			ds.eng.Shutdown()
			assert.Equal(t, []string{lifecycleModelX}, sink.seen())
		})
	}
}
