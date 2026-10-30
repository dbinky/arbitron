package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An awaited child whose agent node uses a pinned, no-connection pi profile
// (the live coder-pi-sol-high shape) must pin at spawn time. Before the pi
// grammar the spawn failed with "pin models for implement: ... (provider
// claude-cli) has no supported fixed-version spelling", because the
// no-connection stamp sent the pi model down the Claude subscription grammar.
func TestModelPinsWiringAwaitedChildPinsPiSubscriptionProfile(t *testing.T) {
	const alias, model = "coder-pi-sol-high", "openai-codex/gpt-6.1-sol"
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '1.3.0'; exit 0; fi\n" +
		"echo \"$*\" >> '" + argvLog + "'\n" +
		"cat > /dev/null\n" +
		"echo '{\"type\":\"session\",\"version\":3,\"id\":\"pi-spawn-session\"}'\n" +
		"echo '{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"provider\":\"openai-codex\",\"model\":\"gpt-6.1-sol\",\"stopReason\":\"stop\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}'\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ds := bootSinkRoot(t, "llm:\n  timeoutseconds: 60\n", func(ws string) {
		writeWorkflow(t, ws, "implement", `{"name":"implement","inputs":[],
  "nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"model_profile":"`+alias+`","templateFile":"templates/p.md"}}],
  "edges":[]}`)
		writePinTemplate(t, ws, "implement")
		writeWorkflow(t, ws, "pi-parent", `{"name":"pi-parent","inputs":[],
  "nodes":[
    {"id":"spawn-1","type":"spawn_workflow","entrypoint":true,"config":{"childWorkflow":"implement","spawnMode":"wait_for_child"}},
    {"id":"onSuccess","type":"transform","config":{"operations":[{"variable":"branch","operation":"set","value":"success"}]}},
    {"id":"onFailure","type":"transform","config":{"operations":[{"variable":"branch","operation":"set","value":"failure"}]}}
  ],
  "edges":[
    {"source":"spawn-1","target":"onSuccess","output":"success"},
    {"source":"spawn-1","target":"onFailure","output":"failure"}
  ]}`)
	})
	ctx := adminCtxForLocalUser(t, ds)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: alias, Kind: provider.ProfileKindAgent, Backend: "pi", Model: model})
	require.NoError(t, err)

	parentID, _, err := ds.eng.Trigger(ctx, "pi-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	var parent *instance.Instance
	require.Eventually(t, func() bool {
		parent, err = ds.instanceRepo.Get(ctx, parentID)
		return err == nil && parent.State.IsTerminal()
	}, 30*time.Second, 50*time.Millisecond, "the awaited spawn must finish")
	require.Equal(t, instance.StateCompleted, parent.State, "spawn error: %v", parent.Variables["spawn-1"])
	assert.Equal(t, "success", parent.Variables["branch"])

	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1, "the spawn must create the child, which it cannot without pins")
	child, err := ds.instanceRepo.Get(ctx, children[0].ID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateCompleted, child.State)
	assert.Equal(t, map[string]string{alias: model}, child.ModelPins, "the child pins the pi profile's prefixed model")
	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 1, "the child's mission reaches pi exactly once")
	assert.Contains(t, argv[0], "gpt-6.1-sol")
}
