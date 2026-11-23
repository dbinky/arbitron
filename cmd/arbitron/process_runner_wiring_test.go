package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Drive real node config and machine env through the root-installed executors
// and local process port, then read the persisted execution sink.
func TestLocalProcessRunnerWiring_PersistsScriptAndToolOutputs(t *testing.T) {
	root := t.TempDir()
	configPath := writeInvocationRoot(t, root)
	cfg, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, append(cfg, []byte("script:\n  envpassthrough: [RR_WIRING_PASS]\n")...), 0o600))
	t.Setenv("RR_WIRING_PASS", "machine-value")
	workspace := filepath.Join(root, "workspaces")
	for _, kind := range []string{"script", "tool_call"} {
		name := "process-" + kind
		command := `printf '{"value":"%s|%s"}' "$NODE_VALUE" "$RR_WIRING_PASS"`
		var processConfig string
		if kind == "script" {
			processConfig = `"scriptFile":"run.sh",`
		} else {
			processConfig = fmt.Sprintf(`"command":"/bin/sh","args":["-c",%q],`, command)
		}
		writeWorkflow(t, workspace, name, fmt.Sprintf(`{
  "name":%q,"description":"process wiring","inputs":[],
  "nodes":[
    {"id":"seed","type":"transform","entrypoint":true,"config":{"operations":[{"variable":"payload","operation":"set","value":"rendered-value"}]}},
    {"id":"execute","type":%q,"config":{%s "env":{"NODE_VALUE":"{{payload}}"}}}
  ],"edges":[{"source":"seed","target":"execute"}]
}`, name, kind, processConfig))
		if kind == "script" {
			scriptPath := filepath.Join(workspace, "users", localDefaultUser.String(), name, "run.sh")
			require.NoError(t, os.WriteFile(scriptPath, []byte("cat >/dev/null\n"+command+"\n"), 0o600))
		}
	}
	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	for _, kind := range []string{"script", "tool_call"} {
		t.Run(kind, func(t *testing.T) {
			id, _, err := ds.eng.Trigger(context.Background(), "process-"+kind, map[string]any{}, "normal")
			require.NoError(t, err)
			waitState(t, ds, id, instance.StateCompleted)
			var stdout, output string
			var exitCode sql.NullInt64
			require.NoError(t, ds.db.QueryRow(`SELECT script_stdout, output, script_exit_code FROM node_executions WHERE instance_id = ? AND node_id = 'execute'`, id.String()).Scan(&stdout, &output, &exitCode))
			assert.JSONEq(t, `{"value":"rendered-value|machine-value"}`, stdout)
			assert.JSONEq(t, `{"value":"rendered-value|machine-value"}`, output)
			assert.False(t, exitCode.Valid, "the engine persists exit codes only for nonzero exits")
		})
	}
}
