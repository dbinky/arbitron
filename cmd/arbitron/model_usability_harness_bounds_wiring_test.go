package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real root -> factory/selector -> launcher -> actual bounded stdout reader ->
// actual identity parser. The process leaf validates auth/model/mode/restrictions.
// OpenCode is absent: its probe fails closed before any launch (G46).
func TestUsabilityProbeIndependentHarnessBoundsRoot(t *testing.T) {
	for _, backend := range []string{"claude-code", "pi", "codex"} {
		modes := []string{modelcatalog.ExecutionModeBatch}
		if backend == "claude-code" || backend == "codex" {
			modes = append(modes, modelcatalog.ExecutionModeInteractive)
		}
		for _, mode := range modes {
			for _, configured := range []struct {
				max          int64
				timeout      int
				wantDeadline time.Duration
			}{{4 * probeOutputLimit, 120, 30 * time.Second}, {0, 0, 30 * time.Second}, {4 * probeOutputLimit, 5, 5 * time.Second}} {
				t.Run(fmt.Sprintf("%s/%s/%d/%ds", backend, mode, configured.max, configured.timeout), func(t *testing.T) {
					dir := t.TempDir()
					home := filepath.Join(dir, "account")
					require.NoError(t, os.MkdirAll(home, 0700))
					binary, version, model, initial := backend, "1.3.0", "vendor/native-bounds", "claude-sonnet-4-6"
					if backend == "claude-code" {
						binary, version, model = "claude", "2.1.301", "claude-native-bounds"
						t.Setenv("CLAUDE_CONFIG_DIR", home)
					}
					if backend == "codex" {
						version, model, initial = "codex-cli 0.163.0", "gpt-native-bounds", "gpt-5-sol"
						t.Setenv("CODEX_HOME", home)
						t.Setenv(codexInteractiveVerifiedEnv, "true")
						writeCodexAccountModels(t, home, model)
					}
					bin := filepath.Join(dir, binary)
					require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho '"+version+"'\n"), 0700))
					t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
					var size atomic.Int64
					size.Store(probeOutputLimit)
					var noNewline atomic.Bool
					var target atomic.Value
					target.Store("stdout")
					var calls atomic.Int32
					var expectedRoot string
					var cancelCase atomic.Bool
					workdirs := make(chan string, 8)
					waitEntered, releaseWait := make(chan struct{}), make(chan struct{})
					var release sync.Once
					defer release.Do(func() { close(releaseWait) })
					runner := lifecycleRunner(func(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
						deadline, ok := ctx.Deadline()
						if !ok || time.Until(deadline) > configured.wantDeadline {
							return nil, nil, fmt.Errorf("missing probe deadline at process reader")
						}
						if !slices.Contains(spec.Args, "--version") {
							if spec.Bin != bin || filepath.Dir(spec.Dir) != expectedRoot {
								return nil, nil, fmt.Errorf("missing binary/workdir")
							}
							calls.Add(1)
							workdirs <- spec.Dir
							outputSize := int64(512)
							if target.Load() == "stdout" {
								outputSize = size.Load()
							}
							spec.Env = append(spec.Env, fmt.Sprintf("BOUNDS_SIZE=%d", outputSize))
							if noNewline.Load() {
								spec.Env = append(spec.Env, "BOUNDS_NO_NEWLINE=1")
							}
							if target.Load() == "truncated" {
								spec.Env = append(spec.Env, "BOUNDS_TRUNCATE=1")
							}
							if cancelCase.Load() && !slices.Contains(spec.Args, "mcp") {
								spec.Env = append(spec.Env, "BOUNDS_CANCEL=1")
							}
						}
						lines, wait, err := claudecli.NewExecRunner().Run(ctx, spec)
						if err != nil || slices.Contains(spec.Args, "--version") || slices.Contains(spec.Args, "mcp") {
							return lines, wait, err
						}
						return lines, func() error {
							err := wait()
							if err == nil && target.Load() == "rollout" {
								path := filepath.Join(home, "sessions", "2026", "10", "16", "rollout-t-bounds-correlated-thread.jsonl")
								raw, readErr := os.ReadFile(path)
								if readErr != nil {
									return readErr
								}
								raw = append(raw, []byte(strings.Repeat(" ", int(size.Load())-len(raw)))...)
								if err := os.WriteFile(path, raw, 0600); err != nil {
									return err
								}
							}
							if cancelCase.Load() {
								close(waitEntered)
								<-releaseWait
							}
							return err
						}, nil
					})
					cfg := fmt.Sprintf("profiles: []\nllm:\n  defaultagentbackend: %s\n  timeoutseconds: %d\n  maxresponsebytes: %d\n", backend, configured.timeout, configured.max)
					if backend == "claude-code" {
						cfg += "  binarypath: " + bin + "\n"
					}
					ds, _, _ := buildLifecycleRootWithDependencies(t, cfg, &lifecycleHTTP{}, ModelDependencies{Runner: runner})
					expectedRoot = ds.refreshDeps.ProbeDir
					ctx := adminCtxForLocalUser(t, ds)
					auth := "[ \"$ANTHROPIC_API_KEY\" = 'bounds-secret' ] || exit 44"
					conn := "bounds"
					if backend == "codex" {
						conn = ""
						auth = "[ \"$CODEX_HOME\" = '" + home + "' ] && [ -z \"$OPENAI_API_KEY\" ] || exit 44"
					} else {
						require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: conn, Provider: provider.CanonicalAnthropicAPI, BaseURL: "https://api.anthropic.com"}, "bounds-secret"))
					}
					kind := "agent_call"
					if mode == modelcatalog.ExecutionModeInteractive {
						kind = "interactive_agent"
					}
					writeWorkflow(t, ds.cfg.Workspace.Path, "bounds", fmt.Sprintf(`{"name":"bounds","nodes":[{"id":"ask","type":%q,"entrypoint":true,"config":{"model_profile":"bounds","templateFile":"templates/p.md"}}],"edges":[]}`, kind))
					_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "bounds", Kind: provider.ProfileKindAgent, Backend: backend, ConnectionRef: conn, Model: initial})
					require.NoError(t, err)
					log := filepath.Join(dir, "argv")
					script := strings.NewReplacer("@BACKEND@", backend, "@VERSION@", version, "@MODEL@", model, "@OUTCOME@", "exact", "@ARGV@", log, "@WORKDIR@", ds.refreshDeps.ProbeDir, "@AUTH@", auth).Replace(nativeHarnessScript)
					if backend == "codex" {
						script = strings.NewReplacer("@ARGV@", log, "@FALLBACK@", filepath.Join(dir, "fallback")).Replace(codexProbeScript)
						script = strings.Replace(script, `sid="probe-thread-$$"`, `sid="bounds-correlated-thread"`, 1)
						script = strings.Replace(script, "m=$(echo", auth+"\n[ \"$(cat)\" = 'Reply with only your model identity on one line. Do not use tools or take any other action.' ] || exit 45\ncase \" $* \" in *' --sandbox read-only '*|*' -s read-only '*) ;; *) exit 47 ;; esac\nm=$(echo", 1)
					}
					original := filepath.Join(dir, "producer")
					require.NoError(t, os.WriteFile(original, []byte(script), 0700))
					wrapper := fmt.Sprintf(`#!/bin/sh
if [ "$1" = --version ]; then echo '%s'; exit 0; fi
case " $* " in *' mcp list --json '*) exec '%s' "$@" ;; esac
result=$('%s' "$@") || exit $?
touch "$PWD/probe-scratch"
if [ "$BOUNDS_TRUNCATE" = 1 ]; then printf '{'; exit 0; fi
if [ "$BOUNDS_CANCEL" = 1 ]; then exec sleep 10; fi
n=${#result}
ending=1
[ "$BOUNDS_NO_NEWLINE" != 1 ] || ending=0
pad=$((BOUNDS_SIZE - n - ending))
[ "$pad" -ge 0 ] || exit 53
head -c "$pad" /dev/zero | tr '\000' ' '
if [ "$BOUNDS_NO_NEWLINE" = 1 ]; then printf '%%s' "$result"; else printf '%%s\n' "$result"; fi
`, version, original, original)
					require.NoError(t, os.WriteFile(bin, []byte(wrapper), 0700))
					snap, err := ds.pathInventory.Snapshot(ctx)
					require.NoError(t, err)
					var path modelcatalog.ExecutionPath
					for _, p := range snap.Paths {
						if p.Backend == backend && p.Mode == mode && slices.Contains(p.Aliases, "bounds") {
							path = p
							break
						}
					}
					require.NotEmpty(t, path.Key)
					e := modelidentity.Entry{CanonicalID: "anthropic/" + model, Provider: "anthropic", NativeID: model}
					if backend == "codex" {
						e.Provider = "openai"
						e.CanonicalID = "openai/" + model
					}
					targets := []string{"stdout"}
					if backend == "codex" {
						targets = append(targets, "rollout")
					}
					sizes := []int64{probeOutputLimit, probeOutputLimit + 1}
					validSize := int64(probeOutputLimit)
					if configured.timeout > 0 && configured.timeout < 30 {
						// The short-budget case proves the root feed at version I/O;
						// do not spend that budget race-instrumenting 1MiB of JSON.
						sizes, validSize = []int64{512}, 512
					}
					for _, output := range targets {
						target.Store(output)
						for _, n := range sizes {
							size.Store(n)
							before := calls.Load()
							ev, err := ds.modelRefresh.probe.Check(ctx, path, e)
							require.NoError(t, err)
							// stdout stays capped at probeOutputLimit. The codex rollout is
							// streamed record by record under a far larger whole-file cap
							// (31a77e98), so one byte over the stdout limit is still valid
							// rollout evidence.
							wantConfirmed := n <= probeOutputLimit || output == "rollout"
							assert.Equal(t, wantConfirmed, ev.Confirmed(), "%s size %d evidence %+v", output, n, ev)
							assert.Greater(t, calls.Load(), before, "must reach the actual launcher and parser")
							for len(workdirs) > 0 {
								wd := <-workdirs
								assert.NoFileExists(t, filepath.Join(wd, "probe-scratch"), "scratch files must be removed on success and overflow")
							}
						}
						noNewline.Store(true)
						size.Store(validSize)
						ev, err := ds.modelRefresh.probe.Check(ctx, path, e)
						require.NoError(t, err)
						assert.True(t, ev.Confirmed(), "%s %d bytes without final newline: %+v", output, validSize, ev)
						for len(workdirs) > 0 {
							<-workdirs
						}
						noNewline.Store(false)
					}
					target.Store("truncated")
					ev, err := ds.modelRefresh.probe.Check(ctx, path, e)
					require.NoError(t, err)
					assert.False(t, ev.Confirmed(), "truncated output must not confirm")
					for len(workdirs) > 0 {
						<-workdirs
					}
					target.Store("stdout")
					cancelCase.Store(true)
					checkCtx, cancel := context.WithCancel(ctx)
					defer cancel()
					returned := make(chan error, 1)
					go func() {
						ev, err := ds.modelRefresh.probe.Check(checkCtx, path, e)
						if ev.Confirmed() {
							err = fmt.Errorf("cancelled probe confirmed")
						}
						returned <- err
					}()
					var wd string
					select {
					case wd = <-workdirs:
					case <-time.After(3 * time.Second):
						t.Fatal("cancel probe did not launch")
					}
					require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(wd, "probe-scratch")); return err == nil }, 3*time.Second, time.Millisecond)
					cancel()
					select {
					case <-waitEntered:
					case <-time.After(3 * time.Second):
						t.Fatal("process was not reaped")
					}
					select {
					case err := <-returned:
						t.Error("probe returned before process wait completed")
						returned <- err
					case <-time.After(50 * time.Millisecond):
					}
					release.Do(func() { close(releaseWait) })
					select {
					case err := <-returned:
						assert.ErrorIs(t, err, context.Canceled)
					case <-time.After(3 * time.Second):
						t.Fatal("probe did not join cancellation")
					}
					assert.NoFileExists(t, filepath.Join(wd, "probe-scratch"))
				})
			}
		}
	}
}
