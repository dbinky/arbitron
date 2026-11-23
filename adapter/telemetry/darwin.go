//go:build darwin

package telemetry

import (
	"time"

	"github.com/arbitron/arbitron/domain/runner"
)

// New samples this Mac, measuring free disk at the working directory.
func New() runner.MachineTelemetry { return NewForRoot(defaultRoot()) }

// NewForRoot samples this Mac, measuring free disk on root's volume.
func NewForRoot(root string) runner.MachineTelemetry {
	return MacSampler{Run: execCommand, DiskFree: statfsFree, Root: root, Now: time.Now}
}
