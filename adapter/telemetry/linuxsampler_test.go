package telemetry

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFS serves files by absolute path; /proc/stat returns successive
// contents on successive reads so the CPU delta is real.
type fakeFS struct {
	mu        sync.Mutex
	files     map[string]string
	stat      []string
	statReads int
	slept     []time.Duration
}

func (f *fakeFS) ReadFile(name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "/proc/stat" && len(f.stat) > 0 {
		i := f.statReads
		if i >= len(f.stat) {
			i = len(f.stat) - 1
		}
		f.statReads++
		return []byte(f.stat[i]), nil
	}
	c, ok := f.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(c), nil
}

// Glob matches the pattern against every file path and every ancestor
// directory of a file path.
func (f *fakeFS) Glob(pattern string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	for name := range f.files {
		for p := name; p != "/" && p != "."; p = path.Dir(p) {
			if ok, _ := path.Match(pattern, p); ok {
				seen[p] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeFS) Sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.slept = append(f.slept, d)
	f.mu.Unlock()
	return ctx.Err()
}

func linuxFixtureFS(t *testing.T) *fakeFS {
	return &fakeFS{
		stat: []string{fixture(t, "proc_stat_1.txt"), fixture(t, "proc_stat_2.txt")},
		files: map[string]string{
			"/proc/meminfo":                                   fixture(t, "meminfo.txt"),
			"/proc/pressure/memory":                           fixture(t, "pressure_memory.txt"),
			"/proc/loadavg":                                   fixture(t, "loadavg.txt"),
			"/proc/uptime":                                    fixture(t, "uptime.txt"),
			"/sys/class/thermal/thermal_zone0/temp":           "85000\n",
			"/sys/class/thermal/thermal_zone0/trip_point_0_type": "passive\n",
			"/sys/class/thermal/thermal_zone0/trip_point_0_temp": "80000\n",
			"/sys/class/thermal/thermal_zone0/trip_point_1_type": "critical\n",
			"/sys/class/thermal/thermal_zone0/trip_point_1_temp": "105000\n",
			"/sys/class/thermal/thermal_zone1/temp":           "40000\n",
		},
	}
}

func TestLinuxSamplerFromFixtures(t *testing.T) {
	fsys := linuxFixtureFS(t)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	var diskAsked string
	s := LinuxSampler{
		ReadFile: fsys.ReadFile, Glob: fsys.Glob, Sleep: fsys.Sleep,
		DiskFree: func(p string) (uint64, error) { diskAsked = p; return 123_456_789, nil },
		Root:     "/var/lib/arbitron/workspaces",
		Now:      func() time.Time { return now },
	}
	var _ runner.MachineTelemetry = s

	tel, err := s.Sample(context.Background())
	require.NoError(t, err)
	assert.Equal(t, now, tel.SampledAt)
	require.NotNil(t, tel.CPUPercent)
	assert.InDelta(t, 25.0, *tel.CPUPercent, 1e-9)
	assert.Equal(t, []time.Duration{250 * time.Millisecond}, fsys.slept, "CPU is measured over the default window")
	require.NotNil(t, tel.Cores)
	assert.Equal(t, 2, *tel.Cores)
	require.NotNil(t, tel.RAMBytes)
	assert.Equal(t, uint64(16694710272), *tel.RAMBytes)
	require.NotNil(t, tel.MemUsedBytes)
	assert.Equal(t, uint64(8347355136), *tel.MemUsedBytes)
	require.NotNil(t, tel.MemPressure)
	assert.Equal(t, "warn", *tel.MemPressure)
	require.NotNil(t, tel.Load1Pct)
	assert.InDelta(t, 26.0, *tel.Load1Pct, 1e-9)
	require.NotNil(t, tel.UptimeSeconds)
	assert.Equal(t, uint64(354321), *tel.UptimeSeconds)
	require.NotNil(t, tel.DiskFreeBytes)
	assert.Equal(t, uint64(123_456_789), *tel.DiskFreeBytes)
	assert.Equal(t, "/var/lib/arbitron/workspaces", diskAsked)
	require.NotNil(t, tel.Thermal)
	assert.Equal(t, "fair", *tel.Thermal)
}

func TestLinuxSamplerMissingSourcesAreIndividuallyUnavailable(t *testing.T) {
	fsys := &fakeFS{files: map[string]string{"/proc/meminfo": fixture(t, "meminfo.txt")}}
	s := LinuxSampler{ReadFile: fsys.ReadFile, Glob: fsys.Glob, Sleep: fsys.Sleep,
		DiskFree: func(string) (uint64, error) { return 0, errors.New("statfs failed") }, Root: "/", Now: time.Now}
	tel, err := s.Sample(context.Background())
	require.NoError(t, err)
	require.NotNil(t, tel.RAMBytes, "available metrics are still reported")
	require.NotNil(t, tel.MemUsedBytes)
	assert.Nil(t, tel.CPUPercent)
	assert.Nil(t, tel.Cores)
	assert.Nil(t, tel.Load1Pct, "load percent needs cores")
	assert.Nil(t, tel.MemPressure, "no PSI means unavailable, not normal")
	assert.Nil(t, tel.UptimeSeconds)
	assert.Nil(t, tel.DiskFreeBytes)
	assert.Nil(t, tel.Thermal, "no thermal zones means unavailable, not nominal")
	assert.Empty(t, fsys.slept, "no CPU window without /proc/stat")
}

func TestLinuxSamplerCancelledDuringCPUWindowSkipsCPU(t *testing.T) {
	fsys := linuxFixtureFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := LinuxSampler{ReadFile: fsys.ReadFile, Glob: fsys.Glob, Sleep: fsys.Sleep, Root: "/", Now: time.Now,
		DiskFree: func(string) (uint64, error) { return 1, nil }}
	tel, err := s.Sample(ctx)
	require.NoError(t, err)
	assert.Nil(t, tel.CPUPercent)
	assert.NotNil(t, tel.Cores)
}

func TestLinuxSamplerThermalZoneFilesAreReadFromSysfs(t *testing.T) {
	fsys := linuxFixtureFS(t)
	fsys.files["/sys/class/thermal/thermal_zone0/temp"] = "106000\n"
	s := LinuxSampler{ReadFile: fsys.ReadFile, Glob: fsys.Glob, Sleep: fsys.Sleep, Root: "/", Now: time.Now,
		DiskFree: func(string) (uint64, error) { return 1, nil }}
	tel, err := s.Sample(context.Background())
	require.NoError(t, err)
	require.NotNil(t, tel.Thermal)
	assert.Equal(t, "critical", *tel.Thermal, "zone0 at 106°C crosses its 105°C critical trip read from sysfs")
}
