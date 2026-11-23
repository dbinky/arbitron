//go:build linux

package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
)

// New samples this Linux host, measuring free disk at the working directory.
func New() runner.MachineTelemetry { return NewForRoot(defaultRoot()) }

// NewForRoot samples this Linux host, measuring free disk on root's volume.
func NewForRoot(root string) runner.MachineTelemetry {
	return LinuxSampler{
		ReadFile: os.ReadFile,
		Glob:     filepath.Glob,
		Sleep:    sleepCtx,
		DiskFree: statfsFree,
		Root:     root,
		Now:      time.Now,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
