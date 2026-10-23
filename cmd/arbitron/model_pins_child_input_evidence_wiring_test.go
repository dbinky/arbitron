package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Paired row 4 (run pins ↔ Claude evidence) on the third batch consumer: the
// spawn_workflow child-input preparation sends a ChatRequest without
// RequireExecutedModel, so only the pinned binding produced by the root's
// PinningResolver makes the per-binding claude-cli ChatAdapter demand modelUsage
// evidence. A fallback-served or evidence-free reply must fail the spawn before
// any child is created; dropping binding.Pinned turns the fallback case green.
func TestModelPinsChildInputClaudeEvidenceRoot(t *testing.T) {
	const model = "claude-sonnet-4-6"
	for _, actual := range []string{"exact", "fallback", "missing"} {
		t.Run(actual, func(t *testing.T) {
			usage := map[string]any{}
			switch actual {
			case "exact":
				usage[model] = map[string]any{}
			case "fallback":
				usage["claude-opus-4-6"] = map[string]any{}
			}
			result, err := json.Marshal(map[string]any{"type": "result", "is_error": false, "result": `{"topic":"x"}`, "session_id": "s", "modelUsage": usage})
			require.NoError(t, err)
			dir := t.TempDir()
			argvLog := filepath.Join(dir, "argv.log")
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = '--version' ]; then echo '2.1.301'; exit 0; fi
printf '%%s\n' "$*" >> %q
case " $* " in *' --model claude-sonnet-4-6 '*) ;; *) exit 42 ;; esac
case " $* " in *' --fallback-model '*) exit 44 ;; esac
cat > /dev/null
printf '%%s\n' '%s'
`, argvLog, result)
			bin := filepath.Join(dir, "claude")
			require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
			ds := bootSinkRoot(t, "llm:\n  binarypath: "+bin+"\n", func(workspace string) {
				writeWorkflow(t, workspace, "evidence-child", `{"name":"evidence-child","description":"child","inputs":[{"name":"topic","type":"string","description":"topic","required":true}],"nodes":[{"id":"c1","type":"transform","entrypoint":true,"config":{"operations":[{"variable":"done","operation":"set","value":true}]}}],"edges":[]}`)
				writeWorkflow(t, workspace, "evidence-parent", `{"name":"evidence-parent","description":"spawn with prepared inputs","inputs":[],"nodes":[{"id":"spawn-1","type":"spawn_workflow","entrypoint":true,"config":{"childWorkflow":"evidence-child","spawnMode":"fire_and_forget","model_profile":"proof"}}],"edges":[]}`)
			})
			ctx := adminCtxForLocalUser(t, ds)
			_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "proof", Kind: provider.ProfileKindChat, Model: model})
			require.NoError(t, err)

			id, _, err := ds.eng.Trigger(ctx, "evidence-parent", map[string]any{}, "normal")
			require.NoError(t, err)
			saved := waitTerminal(t, ds, id)
			assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
			assert.Equal(t, map[string]string{"proof": model}, saved.ModelPins)
			argv := readArgvLog(t, argvLog)
			require.Len(t, argv, 1, "child-input preparation must invoke the harness exactly once")
			assert.Contains(t, " "+argv[0]+" ", " --model "+model+" ")

			if actual == "exact" {
				require.Equal(t, instance.StateCompleted, saved.State)
				require.Len(t, childrenOf(t, ds, id), 1, "verified preparation must spawn the child")
				return
			}
			require.Equal(t, instance.StateFailed, saved.State, "unverified child inputs were accepted")
			assert.Empty(t, childrenOf(t, ds, id), "a fallback-served preparation must not create a child")
			trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
			require.NoError(t, err)
			require.Len(t, trace, 1)
			assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
			assert.Contains(t, fmt.Sprint(trace[0].Error), "model execution evidence")
			assert.False(t, strings.Contains(fmt.Sprint(trace[0].Output), "topic"), "unverified output reached the node row")
		})
	}
}
