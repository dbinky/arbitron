//go:build !darwin && !linux

package telemetry

import "github.com/arbitron/arbitron/domain/runner"

// New is the unsupported-OS fallback: every metric unavailable.
func New() runner.MachineTelemetry { return Null{} }

// NewForRoot is the unsupported-OS fallback: every metric unavailable.
func NewForRoot(string) runner.MachineTelemetry { return Null{} }
