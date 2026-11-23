package telemetry

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return string(b)
}

func TestParseSysctlFromFixture(t *testing.T) {
	f := ParseSysctl(fixture(t, "sysctl.txt"))
	require.NotNil(t, f.Cores)
	assert.Equal(t, 10, *f.Cores)
	require.NotNil(t, f.RAMBytes)
	assert.Equal(t, uint64(68719476736), *f.RAMBytes)
	require.NotNil(t, f.BootTime)
	assert.Equal(t, time.Unix(1763540000, 0).UTC(), *f.BootTime)
	require.NotNil(t, f.Load1)
	assert.InDelta(t, 2.31, *f.Load1, 1e-9)
	require.NotNil(t, f.PressureLevel)
	assert.Equal(t, 1, *f.PressureLevel)
}

func TestParseSysctlMissingKeysAreUnavailable(t *testing.T) {
	f := ParseSysctl("hw.ncpu: 8\nsysctl: unknown oid 'kern.memorystatus_vm_pressure_level'\n")
	require.NotNil(t, f.Cores)
	assert.Equal(t, 8, *f.Cores)
	assert.Nil(t, f.RAMBytes)
	assert.Nil(t, f.BootTime)
	assert.Nil(t, f.Load1)
	assert.Nil(t, f.PressureLevel)
}

func TestParseSysctlGarbageIsUnavailable(t *testing.T) {
	f := ParseSysctl("hw.ncpu: ten\nhw.memsize: -1\nkern.boottime: soon\nvm.loadavg: high\n")
	assert.Nil(t, f.Cores)
	assert.Nil(t, f.RAMBytes)
	assert.Nil(t, f.BootTime)
	assert.Nil(t, f.Load1)
}

func TestMemPressureFromLevel(t *testing.T) {
	cases := map[int]string{1: "normal", 2: "warn", 4: "critical"}
	for level, want := range cases {
		got := MemPressureFromLevel(level)
		require.NotNil(t, got, "level %d", level)
		assert.Equal(t, want, *got)
	}
	assert.Nil(t, MemPressureFromLevel(0), "unknown levels are unavailable, not normal")
	assert.Nil(t, MemPressureFromLevel(3))
}

func TestParseVMStatFromFixture(t *testing.T) {
	used := ParseVMStat(fixture(t, "vm_stat.txt"))
	require.NotNil(t, used)
	// (active 1203456 + wired 345678 + compressor 456789) * 16384
	assert.Equal(t, uint64(32865042432), *used)
}

func TestParseVMStatMissingPageSizeOrCountersIsUnavailable(t *testing.T) {
	assert.Nil(t, ParseVMStat("Pages active: 10.\nPages wired down: 10.\nPages occupied by compressor: 1.\n"))
	assert.Nil(t, ParseVMStat("Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages active: 10.\n"))
}

func TestParseTopCPUUsesLastIntervalSample(t *testing.T) {
	cpu := ParseTopCPU(fixture(t, "top.txt"))
	require.NotNil(t, cpu)
	assert.InDelta(t, 20.0, *cpu, 1e-9)
}

func TestParseTopCPUSingleSampleIsUnavailable(t *testing.T) {
	assert.Nil(t, ParseTopCPU("CPU usage: 3.10% user, 2.90% sys, 94.0% idle\n"), "a since-boot sample is not current CPU")
	assert.Nil(t, ParseTopCPU(""))
}

func TestParseThermalPressureObservedNominal(t *testing.T) {
	got := ParseThermalPressure(fixture(t, "thermal_pressure_observed_m2.txt"))
	require.NotNil(t, got)
	assert.Equal(t, "nominal", *got)
}

func TestParseThermalPressureLevels(t *testing.T) {
	cases := map[string]string{"0": "nominal", "1": "fair", "2": "serious", "3": "critical", "4": "critical"}
	for level, want := range cases {
		got := ParseThermalPressure("com.apple.system.thermalpressurelevel " + level + "\n")
		require.NotNil(t, got, level)
		assert.Equal(t, want, *got, level)
	}
}

func TestParseThermalPressureOutOfRangeIsUnavailable(t *testing.T) {
	for _, out := range []string{
		"com.apple.system.thermalpressurelevel 5\n",
		"com.apple.system.thermalpressurelevel -1\n",
		"com.apple.system.thermalpressurelevel 18446744073709551615\n",
	} {
		assert.Nil(t, ParseThermalPressure(out), out)
	}
}

func TestParseThermalPressureUnrecognisedIsUnavailable(t *testing.T) {
	for _, out := range []string{
		"",
		"com.apple.system.thermalpressurelevel\n",
		"com.apple.system.thermalpressurelevel high\n",
		"com.apple.system.thermalpressurelevel 1.5\n",
		"com.apple.system.otherkey 0\n",
		"com.apple.system.thermalpressurelevel 0\ncom.apple.system.thermalpressurelevel 2\n",
		"Note: No thermal warning level has been recorded\n", // pmset-style note text is not a source
	} {
		assert.Nil(t, ParseThermalPressure(out), "unrecognised output must not be reported as nominal: %q", out)
	}
}
