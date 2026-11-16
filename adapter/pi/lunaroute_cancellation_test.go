package pi

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRoutePi_CancelDuringPreparation(t *testing.T) {
	for _, stage := range []string{"reveal", "mkdir", "chmod", "write"} {
		t.Run(stage, func(t *testing.T) {
			tmp := isolatePiTemp(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := lrPiBinding()
			fs := piFileSystem{os.MkdirTemp, os.Chmod, os.WriteFile, os.RemoveAll}
			removed := 0
			fs.removeAll = func(dir string) error { removed++; return os.RemoveAll(dir) }
			switch stage {
			case "reveal":
				b.RevealKey = func(context.Context) (string, error) { cancel(); return lrPiKey, nil }
			case "mkdir":
				fs.mkdirTemp = func(dir, pattern string) (string, error) {
					path, err := os.MkdirTemp(dir, pattern)
					cancel()
					return path, err
				}
			case "chmod":
				fs.chmod = func(path string, mode os.FileMode) error {
					cancel()
					return os.Chmod(path, mode)
				}
			case "write":
				fs.writeFile = func(path string, data []byte, mode os.FileMode) error {
					err := os.WriteFile(path, data, mode)
					cancel()
					return err
				}
			}
			launch, err := prepareLunaRoutePiLaunchWithFS(ctx, b, lrRules(), fs)
			// Prevent a failed regression from leaving its own directory behind.
			if launch.cleanup != nil {
				t.Cleanup(launch.cleanup)
			}
			assert.ErrorIs(t, err, context.Canceled)
			assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
			assert.Empty(t, launch.dir)
			assert.Zero(t, len(launch.env))
			assert.Empty(t, piLeftovers(t, tmp))
			if stage == "reveal" {
				assert.Zero(t, removed, "cancelled reveal must not create a directory")
			} else {
				assert.Equal(t, 1, removed, "preparation owns rollback until it succeeds")
			}
		})
	}
}

// A process can exit cleanly as cancellation arrives. A nil wait error must
// not override the context verdict, either after the probe or after the turn.
type cancelOnWaitRunner struct {
	inner  *capturingRunner
	cancel context.CancelFunc
	probe  bool
}

func (r *cancelOnWaitRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	lines, wait, err := r.inner.Run(ctx, spec)
	if err != nil {
		return lines, wait, err
	}
	return lines, func() error {
		err := wait()
		if (spec.Args[0] == "--version") == r.probe {
			r.cancel()
		}
		return err
	}, nil
}

func TestLunaRoutePi_CancelWithCleanExit(t *testing.T) {
	for _, probe := range []bool{true, false} {
		name := "turn"
		if probe {
			name = "probe"
		}
		t.Run(name, func(t *testing.T) {
			tmp := isolatePiTemp(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &cancelOnWaitRunner{inner: lrPiRunner(lrAssistant), cancel: cancel, probe: probe}
			res, err := lrPiProvider(r, lrPiBinding(), lrRules()).RunAgent(ctx, piMission())
			require.ErrorIs(t, err, context.Canceled)
			assert.Nil(t, res)
			assert.Empty(t, piLeftovers(t, tmp))
			if probe {
				assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
				assert.Len(t, r.inner.inner.calls, 1, "no mission launch after a cancelled probe")
			} else {
				assert.Len(t, r.inner.inner.calls, 2)
			}
		})
	}
}

func TestLunaRoutePi_DeadlineCleansDirectory(t *testing.T) {
	for _, missionTimeout := range []bool{true, false} {
		t.Run(map[bool]string{true: "mission", false: "default"}[missionTimeout], func(t *testing.T) {
			tmp := isolatePiTemp(t)
			r := lrPiRunner(lrAssistant)
			r.inner.scripts[1].blockOnCtx = true
			p := lrPiProvider(r, lrPiBinding(), lrRules())
			m := piMission()
			if missionTimeout {
				m.Timeout = 20 * time.Millisecond
				p.timeout = time.Hour
			} else {
				p.timeout = 20 * time.Millisecond
			}
			res, err := p.RunAgent(context.Background(), m)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Nil(t, res)
			require.NotEmpty(t, r.dir)
			assert.Empty(t, piLeftovers(t, tmp))
		})
	}
}
