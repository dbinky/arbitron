package telemetry

import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
)

// CommandRunner runs a command and returns its stdout. Stdout is returned
// even when err != nil so partial output (e.g. sysctl with an unknown key)
// is still parsed.
type CommandRunner func(ctx context.Context, name string, args ...string) (stdout string, err error)

var (
	macSysctlCmd = []string{"/usr/sbin/sysctl", "hw.ncpu", "hw.memsize", "kern.boottime", "vm.loadavg", "kern.memorystatus_vm_pressure_level"}
	macVMStatCmd = []string{"/usr/bin/vm_stat"}
	macTopCmd    = []string{"/usr/bin/top", "-l", "2", "-n", "0", "-s", "1"}
	macThermalCmd = []string{"/usr/bin/notifyutil", "-g", "com.apple.system.thermalpressurelevel"}
)

// MacSampler samples macOS without cgo. Each metric fails independently to
// nil (unavailable).
type MacSampler struct {
	Run      CommandRunner
	DiskFree func(path string) (uint64, error)
	Root     string
	Now      func() time.Time
}

var _ runner.MachineTelemetry = MacSampler{}

func (m MacSampler) run(ctx context.Context, argv []string) string {
	out, _ := m.Run(ctx, argv[0], argv[1:]...)
	return out
}

// Sample collects one telemetry sample.
func (m MacSampler) Sample(ctx context.Context) (runner.Telemetry, error) {
	now := m.Now().UTC()
	t := runner.Telemetry{SampledAt: now}

	f := ParseSysctl(m.run(ctx, macSysctlCmd))
	t.Cores = f.Cores
	t.RAMBytes = f.RAMBytes
	t.Load1Pct = loadPct(f.Load1, f.Cores)
	if f.PressureLevel != nil {
		t.MemPressure = MemPressureFromLevel(*f.PressureLevel)
	}
	if f.BootTime != nil && now.After(*f.BootTime) {
		up := uint64(now.Sub(*f.BootTime) / time.Second)
		t.UptimeSeconds = &up
	}

	t.MemUsedBytes = ParseVMStat(m.run(ctx, macVMStatCmd))
	t.CPUPercent = ParseTopCPU(m.run(ctx, macTopCmd))
	// Thermal: unlike the partial-output metrics above, a non-zero exit or
	// timeout is unavailable even if stdout looks valid (DECISION RR-06).
	if out, err := m.Run(ctx, macThermalCmd[0], macThermalCmd[1:]...); err == nil {
		t.Thermal = ParseThermalPressure(out)
	}
	t.DiskFreeBytes = diskFree(m.DiskFree, m.Root)
	return t, nil
}
