package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A harness-created read-only directory must not defeat the private home's
// lifecycle. Removing the permission repair leaves the home behind on each exit.
func TestLunaRouteCodex_ReadOnlyHomeCleanup(t *testing.T) {
	for _, outcome := range []string{"success", "process error", "version error", "parse error"} {
		t.Run(outcome, func(t *testing.T) {
			tmp := isolateTemp(t)
			outside := t.TempDir()
			marker := filepath.Join(outside, "operator-login")
			require.NoError(t, os.WriteFile(marker, []byte("untouched"), 0400))
			require.NoError(t, os.Chmod(outside, 0500))
			t.Cleanup(func() { _ = os.Chmod(outside, 0700) })
			r := &lrSinkRunner{t: t}
			s := lrSuccessScript(t, r, "glm-5.3", "lunaroute")
			if outcome == "process error" {
				s.waitErr = context.Canceled
			}
			if outcome == "parse error" {
				s.lines = []string{"invalid JSON"}
			}
			onWait := s.onWait
			lockHome := func() {
				home := r.homes[len(r.homes)-1]
				// Test cleanup must still work when this regression fails.
				t.Cleanup(func() {
					_ = os.Chmod(home, 0700)
					_ = os.Chmod(filepath.Join(home, "locked"), 0700)
					_ = os.RemoveAll(home)
				})
				require.NoError(t, os.Mkdir(filepath.Join(home, "locked"), 0700))
				require.NoError(t, os.WriteFile(filepath.Join(home, "locked", "data"), []byte("private"), 0400))
				require.NoError(t, os.Symlink(outside, filepath.Join(home, "operator-link")))
				require.NoError(t, os.Chmod(filepath.Join(home, "locked"), 0000))
				require.NoError(t, os.Chmod(home, 0500))
			}
			s.onWait = func() { onWait(); lockHome() }
			r.inner.scripts = []runScript{compatibleProbe(), s}
			if outcome == "version error" {
				r.inner.scripts = []runScript{{waitErr: context.Canceled, onWait: lockHome}}
			}
			res, err := New(Config{Binding: lrCodexBinding(), Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
			if outcome == "success" {
				require.NoError(t, err)
				require.NotNil(t, res)
				assert.NotEmpty(t, res.ModelEvidence, "rollout must be read before cleanup")
			} else {
				require.Error(t, err)
				assert.Nil(t, res)
				if outcome != "parse error" {
					assert.ErrorIs(t, err, context.Canceled)
				}
			}
			lrNoLeftovers(t, tmp)
			got, err := os.ReadFile(marker)
			require.NoError(t, err)
			assert.Equal(t, "untouched", string(got))
			st, err := os.Stat(outside)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0500), st.Mode().Perm(), "never chmod a symlink's target")
		})
	}
}

// A parent directory outside the private home cannot be repaired by this
// adapter. Cleanup failure must invalidate success while retaining prior errors.
func TestLunaRouteCodex_CleanupFailureIsLocalAndPreservesCause(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "process error"}[failed], func(t *testing.T) {
			tmp := isolateTemp(t)
			t.Cleanup(func() { _ = os.Chmod(tmp, 0700) })
			r := &lrSinkRunner{t: t}
			s := lrSuccessScript(t, r, "glm-5.3", "lunaroute")
			if failed {
				s.waitErr = &claudecli.StderrError{Err: context.Canceled, Summary: lrKey, Tail: []byte(lrKey)}
			}
			onWait := s.onWait
			s.onWait = func() { onWait(); require.NoError(t, os.Chmod(tmp, 0500)) }
			r.inner.scripts = []runScript{compatibleProbe(), s}
			res, err := New(Config{Binding: lrCodexBinding(), Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
			require.Error(t, err, "a retained home cannot be reported as success")
			assert.Nil(t, res)
			assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
			assert.NotContains(t, err.Error(), lrKey)
			if failed {
				assert.True(t, errors.Is(err, context.Canceled))
			}
			st, statErr := os.Stat(tmp)
			require.NoError(t, statErr)
			assert.Equal(t, os.FileMode(0500), st.Mode().Perm(), "do not widen permissions outside the home")
		})
	}
}
