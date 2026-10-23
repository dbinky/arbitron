package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An ordinary pi mission with no node timeout is bounded by the configured
// llm.timeoutseconds, as opencode and codex missions are (G37). The root must
// feed Deps.PiTimeout; without it procenv.WithTimeout gets 0 and a hung pi
// subprocess holds the pinned run until the harness exits on its own. The
// control case proves the deadline is the configured one, not a spurious cap.
func TestModelPinsWiringPiMissionHonorsConfiguredTimeout(t *testing.T) {
	const pinned = "claude-sonnet-4-5"
	for _, tc := range []struct {
		name    string
		timeout int
		hang    bool
		want    instance.State
	}{
		{name: "configured deadline bounds a hung mission", timeout: 1, hang: true, want: instance.StateFailed},
		{name: "a prompt reply inside the deadline completes", timeout: 60, want: instance.StateCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argvLog, hang := filepath.Join(dir, "argv"), filepath.Join(dir, "hang")
			if tc.hang {
				require.NoError(t, os.WriteFile(hang, nil, 0o600))
			}
			script := "#!/bin/sh\n" +
				"if [ \"$1\" = \"--version\" ]; then echo '1.3.0'; exit 0; fi\n" +
				"echo \"$*\" >> '" + argvLog + "'\n" +
				"cat > /dev/null\n" +
				"if [ -f '" + hang + "' ]; then exec sleep 25; fi\n" +
				"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
				"echo '{\"type\":\"session\",\"version\":3,\"id\":\"pi-timeout-session\"}'\n" +
				"echo '{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"provider\":\"anthropic\",\"model\":\"'\"$m\"'\",\"stopReason\":\"stop\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}'\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

			ds := bootSinkRoot(t, fmt.Sprintf("llm:\n  timeoutseconds: %d\n", tc.timeout), func(ws string) {
				writeWorkflow(t, ws, "pin-pi", `{"name":"pin-pi","inputs":[],"nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"model_profile":"pi-agent","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, ws, "pin-pi")
			})
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "pi-anthropic", Provider: provider.CanonicalAnthropicAPI, BaseURL: "https://api.anthropic.com"}, "pi-timeout-secret"))
			_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "pi-agent", Kind: provider.ProfileKindAgent, Backend: "pi", ConnectionRef: "pi-anthropic", Model: pinned})
			require.NoError(t, err)

			started := time.Now()
			id, _, err := ds.eng.Trigger(ctx, "pin-pi", map[string]any{}, "normal")
			require.NoError(t, err)
			var inst *instance.Instance
			require.Eventually(t, func() bool {
				inst, err = ds.instanceRepo.Get(ctx, id)
				return err == nil && inst.State.IsTerminal()
			}, 10*time.Second, 50*time.Millisecond, "the pi mission must end within the configured deadline")
			assert.Equal(t, tc.want, inst.State)
			if tc.hang {
				assert.Less(t, time.Since(started), 10*time.Second)
			}
			argv := readArgvLog(t, argvLog)
			require.Len(t, argv, 1, "the mission must reach the pi binary exactly once")
			assert.Contains(t, argv[0], "--model "+pinned)
			assert.Equal(t, map[string]string{"pi-agent": pinned}, inst.ModelPins)
		})
	}
}
