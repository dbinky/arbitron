// Package telemetry samples machine metrics for runner telemetry. Parsers are
// pure and OS-independent; build-tagged files only bind the real OS ports.
package telemetry

import (
	"os"
	"time"
)

const commandTimeout = 4 * time.Second

// defaultRoot is the directory whose volume New() measures for free disk.
func defaultRoot() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// diskFree measures root's volume; any failure is unavailable (nil).
func diskFree(fn func(string) (uint64, error), root string) *uint64 {
	if fn == nil || root == "" {
		return nil
	}
	n, err := fn(root)
	if err != nil {
		return nil
	}
	return &n
}

// loadPct converts a 1-minute load average into a percentage of cores.
func loadPct(load1 *float64, cores *int) *float64 {
	if load1 == nil || cores == nil || *cores <= 0 {
		return nil
	}
	p := *load1 / float64(*cores) * 100
	return &p
}
