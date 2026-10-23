package main

import (
	"context"
	"fmt"
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

func TestModelPinsWiringRejectsIncompatibleLiveBackend(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprintf("interactive=%v", interactive), func(t *testing.T) {
			dir := t.TempDir()
			argvLog, goFile := filepath.Join(dir, "claude-argv"), filepath.Join(dir, "go")
			claude := fakeClaudeBinary(t, dir, argvLog, goFile)
			require.NoError(t, os.WriteFile(goFile, nil, 0o600))
			codexLog := filepath.Join(dir, "codex-argv")
			// Record EVERY spawn, including readiness/version calls. Even a
			// failing process is too late to reject an incompatible pinned route.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte(fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\nexit 42\n", codexLog)), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(codexInteractiveVerifiedEnv, "true")
			kind := "agent_call"
			if interactive {
				kind = "interactive_agent"
			}
			ds := bootSinkRoot(t, "llm:\n  binarypath: "+claude+"\n", func(workspace string) {
				writeWorkflow(t, workspace, "pin-backend", fmt.Sprintf(`{"name":"pin-backend","nodes":[{"id":"ask","type":%q,"entrypoint":true,"config":{"model_profile":"agent","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`, kind))
				writePinTemplate(t, workspace, "pin-backend")
				require.NoError(t, os.WriteFile(filepath.Join(workspace, "users", localDefaultUser.String(), "pin-backend", "templates", "o.md"), []byte("orchestrate"), 0o600))
			})
			ctx := adminCtxForLocalUser(t, ds)
			profile := provider.Profile{Alias: "agent", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-sonnet-4-6"}
			_, _, err := ds.profileSvc.Create(ctx, profile)
			require.NoError(t, err)
			if !interactive {
				ds.queueMgr.UpdateConfig(1, 1)
				require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
					ID: uuid.New(), OwnerID: localDefaultUser, WorkflowName: "holder",
					State: instance.StateRunning, Priority: instance.PriorityNormal,
					WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
					ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
				}))
			}
			id, started, err := ds.eng.Trigger(ctx, "pin-backend", nil, "normal")
			require.NoError(t, err)
			var transcript string
			if interactive {
				require.Eventually(t, func() bool {
					saved, err := ds.instanceRepo.Get(ctx, id)
					return err == nil && saved.State == instance.StateWaiting
				}, 20*time.Second, 50*time.Millisecond)
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				transcript = fmt.Sprint(trace[0].LLMConversation)
				argv := readArgvLog(t, argvLog)
				require.Len(t, argv, 1)
				require.Contains(t, argv[0], "--model claude-sonnet-4-6")
			} else {
				require.False(t, started)
			}
			profile.Backend, profile.Model = "codex", "gpt-6-sol"
			_, _, err = ds.profileSvc.Update(ctx, profile)
			require.NoError(t, err)
			if interactive {
				assert.ErrorContains(t, ds.conversationHandler.HandleMessage(ctx, id, "incompatible backend"), "specific model version")
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				assert.Equal(t, transcript, fmt.Sprint(trace[0].LLMConversation))
				assert.Len(t, readArgvLog(t, argvLog), 1)
			} else {
				require.NoError(t, ds.eng.StartInstance(context.Background(), id))
				require.Equal(t, instance.StateFailed, waitTerminal(t, ds, id).State)
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				assert.Contains(t, fmt.Sprint(trace[0].Error), "specific model version")
				assert.Empty(t, trace[0].Output)
				assert.Empty(t, readArgvLog(t, argvLog))
			}
			assert.NoFileExists(t, codexLog, "incompatible pinned model must fail before spawning the new backend")
			saved, err := ds.instanceRepo.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"agent": "claude-sonnet-4-6"}, saved.ModelPins)
		})
	}
}
