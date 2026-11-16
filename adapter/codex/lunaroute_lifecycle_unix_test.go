//go:build !windows

package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise RunAgent's context and cleanup ordering with the production runner.
// A surviving process or removing cleanup would independently fail this test.
func TestLunaRouteCodex_RealProcessCancellationCleanup(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			tmp := isolateTemp(t)
			ws := t.TempDir()
			bin := filepath.Join(t.TempDir(), "codex")
			require.NoError(t, os.WriteFile(bin, []byte(`#!/bin/sh
set -eu
if [ "$1" = --version ]; then
 [ -z "${LUNAROUTE_API_KEY:-}" ]
 printf 'codex-cli 0.167.2\n'
 exit 0
fi
[ "$LUNAROUTE_API_KEY" = lr_live_codexTestKey123 ]
mkdir "$CODEX_HOME/locked"
printf private > "$CODEX_HOME/locked/data"
chmod 000 "$CODEX_HOME/locked"
printf '%s\n%s\n' "$CODEX_HOME" "$$" > started
exec sleep 30
`), 0700))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mission := lrMission()
			mission.WorkDir = ws
			if mode == "timeout" {
				mission.Timeout = time.Second
			}
			type outcome struct {
				result *engine.AgentResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				res, err := New(Config{Binding: lrCodexBinding(), BinaryPath: bin, CapabilityRules: lrCodexRules()}).RunAgent(ctx, mission)
				done <- outcome{res, err}
			}()
			var fields []string
			require.Eventually(t, func() bool {
				raw, err := os.ReadFile(filepath.Join(ws, "started"))
				if err != nil {
					return false
				}
				fields = strings.Fields(string(raw))
				return len(fields) == 2
			}, 3*time.Second, 5*time.Millisecond, "the inference process must start")
			pid, err := strconv.Atoi(fields[1])
			require.NoError(t, err)
			t.Cleanup(func() { p, _ := os.FindProcess(pid); _ = p.Kill() })
			if mode == "cancel" {
				cancel()
			}
			select {
			case out := <-done:
				require.Error(t, out.err)
				assert.Nil(t, out.result)
				if mode == "cancel" {
					assert.ErrorIs(t, out.err, context.Canceled)
				} else {
					assert.ErrorIs(t, out.err, context.DeadlineExceeded)
				}
				assert.NotContains(t, out.err.Error(), lrKey)
			case <-time.After(3 * time.Second):
				cancel()
				p, _ := os.FindProcess(pid)
				_ = p.Kill()
				<-done
				t.Fatal("mission did not tear down promptly")
			}
			state, err := exec.Command("ps", "-o", "stat=", "-p", fields[1]).Output()
			assert.True(t, err != nil || strings.HasPrefix(strings.TrimSpace(string(state)), "Z"), "inference process must be stopped before returning")
			lrNoLeftovers(t, tmp)
		})
	}
}
