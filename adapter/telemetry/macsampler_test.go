package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cmdResult struct {
	out string
	err error
}

// fakeCommands answers by exact command line, so the sampler must invoke the
// absolute paths and arguments it promises.
type fakeCommands struct {
	mu      sync.Mutex
	answers map[string]cmdResult
	calls   []string
}

func (f *fakeCommands) run(ctx context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, line)
	r, ok := f.answers[line]
	if !ok {
		return "", errors.New("unexpected command: " + line)
	}
	return r.out, r.err
}

const (
	sysctlLine  = "/usr/sbin/sysctl hw.ncpu hw.memsize kern.boottime vm.loadavg kern.memorystatus_vm_pressure_level"
	vmStatLine  = "/usr/bin/vm_stat"
	topLine     = "/usr/bin/top -l 2 -n 0 -s 1"
	thermalLine = "/usr/bin/notifyutil -g com.apple.system.thermalpressurelevel"
)

// macFixtures answers every macOS command from fixtures; thermal is the raw
// notifyutil stdout to return.
func macFixtures(t *testing.T, thermal string) *fakeCommands {
	return &fakeCommands{answers: map[string]cmdResult{
		sysctlLine:  {out: fixture(t, "sysctl.txt")},
		vmStatLine:  {out: fixture(t, "vm_stat.txt")},
		topLine:     {out: fixture(t, "top.txt")},
		thermalLine: {out: thermal},
	}}
}

func TestMacSamplerFromFixtures(t *testing.T) {
	now := time.Unix(1763540000+86400, 0).UTC()
	cmds := macFixtures(t, fixture(t, "thermal_pressure_observed_m2.txt"))
	var diskAsked string
	s := MacSampler{
		Run:      cmds.run,
		DiskFree: func(p string) (uint64, error) { diskAsked = p; return 500_000_000_000, nil },
		Root:     "/Users/op/.arbitron-runner/workspaces",
		Now:      func() time.Time { return now },
	}
	var _ runner.MachineTelemetry = s

	tel, err := s.Sample(context.Background())
	require.NoError(t, err)
	assert.Equal(t, now, tel.SampledAt)
	require.NotNil(t, tel.Cores)
	assert.Equal(t, 10, *tel.Cores)
	require.NotNil(t, tel.RAMBytes)
	assert.Equal(t, uint64(68719476736), *tel.RAMBytes)
	require.NotNil(t, tel.Load1Pct)
	assert.InDelta(t, 23.1, *tel.Load1Pct, 1e-9)
	require.NotNil(t, tel.UptimeSeconds)
	assert.Equal(t, uint64(86400), *tel.UptimeSeconds)
	require.NotNil(t, tel.MemPressure)
	assert.Equal(t, "normal", *tel.MemPressure)
	require.NotNil(t, tel.MemUsedBytes)
	assert.Equal(t, uint64(32865042432), *tel.MemUsedBytes)
	require.NotNil(t, tel.CPUPercent)
	assert.InDelta(t, 20.0, *tel.CPUPercent, 1e-9)
	require.NotNil(t, tel.Thermal)
	assert.Equal(t, "nominal", *tel.Thermal)
	require.NotNil(t, tel.DiskFreeBytes)
	assert.Equal(t, uint64(500_000_000_000), *tel.DiskFreeBytes)
	assert.Equal(t, "/Users/op/.arbitron-runner/workspaces", diskAsked, "disk is measured on the workspace root's volume")
	assert.ElementsMatch(t, []string{sysctlLine, vmStatLine, topLine, thermalLine}, cmds.calls)
}

func TestMacSamplerUnrecognisedThermalIsUnavailable(t *testing.T) {
	s := MacSampler{Run: macFixtures(t, "com.apple.system.thermalpressurelevel high\n").run, Root: "/", DiskFree: func(string) (uint64, error) { return 1, nil }, Now: time.Now}
	tel, err := s.Sample(context.Background())
	require.NoError(t, err)
	assert.Nil(t, tel.Thermal)
	assert.NotNil(t, tel.CPUPercent, "other metrics are unaffected")
}

func TestMacSamplerThermalCommandFailureIsUnavailable(t *testing.T) {
	cmds := macFixtures(t, "")
	// Valid-looking stdout with a non-zero exit (or a timeout) is still unavailable.
	cmds.answers[thermalLine] = cmdResult{out: "com.apple.system.thermalpressurelevel 0\n", err: errors.New("exit status 1")}
	s := MacSampler{Run: cmds.run, Root: "/", DiskFree: func(string) (uint64, error) { return 1, nil }, Now: time.Now}
	tel, err := s.Sample(context.Background())
	require.NoError(t, err)
	assert.Nil(t, tel.Thermal, "a failed thermal command is unavailable, never nominal")
	assert.NotNil(t, tel.CPUPercent, "other metrics are unaffected")
}

func TestMacSamplerCommandFailuresLeaveFieldsUnavailable(t *testing.T) {
	cmds := &fakeCommands{answers: map[string]cmdResult{}} // every command fails
	s := MacSampler{Run: cmds.run, Root: "/", DiskFree: func(string) (uint64, error) { return 0, errors.New("statfs: no such file") }, Now: time.Now}
	tel, err := s.Sample(context.Background())
	require.NoError(t, err)
	assert.False(t, tel.SampledAt.IsZero())
	assertAllMetricsNil(t, tel)
}

func TestMacSamplerUsesPartialSysctlOutputOnError(t *testing.T) {
	cmds := macFixtures(t, fixture(t, "thermal_pressure_observed_m2.txt"))
	cmds.answers[sysctlLine] = cmdResult{
		out: "hw.ncpu: 8\nhw.memsize: 17179869184\n",
		err: errors.New("exit status 1"), // unknown oid on older macOS
	}
	s := MacSampler{Run: cmds.run, Root: "/", DiskFree: func(string) (uint64, error) { return 1, nil }, Now: time.Now}
	tel, err := s.Sample(context.Background())
	require.NoError(t, err)
	require.NotNil(t, tel.Cores)
	assert.Equal(t, 8, *tel.Cores)
	assert.Nil(t, tel.MemPressure, "missing pressure key is unavailable, not normal")
	assert.Nil(t, tel.Load1Pct)
	assert.Nil(t, tel.UptimeSeconds)
}
