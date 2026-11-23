package telemetry

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
)

const defaultCPUWindow = 250 * time.Millisecond

// LinuxSampler samples a Linux host from /proc and /sys. Each metric fails
// independently to nil (unavailable).
type LinuxSampler struct {
	ReadFile  func(name string) ([]byte, error)
	Glob      func(pattern string) ([]string, error)
	Sleep     func(ctx context.Context, d time.Duration) error
	DiskFree  func(path string) (uint64, error)
	Root      string
	Now       func() time.Time
	CPUWindow time.Duration
}

var _ runner.MachineTelemetry = LinuxSampler{}

func (s LinuxSampler) window() time.Duration {
	if s.CPUWindow > 0 {
		return s.CPUWindow
	}
	return defaultCPUWindow
}

func (s LinuxSampler) read(name string) (string, bool) {
	b, err := s.ReadFile(name)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (s LinuxSampler) readInt(name string) (int64, bool) {
	c, ok := s.read(name)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(c), 10, 64)
	return n, err == nil
}

// Sample collects one telemetry sample.
func (s LinuxSampler) Sample(ctx context.Context) (runner.Telemetry, error) {
	t := runner.Telemetry{SampledAt: s.Now().UTC()}

	if c, ok := s.read("/proc/stat"); ok {
		if a, ok := ParseProcStat(c); ok {
			if a.Cores > 0 {
				cores := a.Cores
				t.Cores = &cores
			}
			if err := s.Sleep(ctx, s.window()); err == nil {
				if c2, ok := s.read("/proc/stat"); ok {
					if b, ok := ParseProcStat(c2); ok {
						t.CPUPercent = CPUPercentBetween(a, b)
					}
				}
			}
		}
	}
	if c, ok := s.read("/proc/meminfo"); ok {
		t.RAMBytes, t.MemUsedBytes = ParseMeminfo(c)
	}
	if c, ok := s.read("/proc/pressure/memory"); ok {
		t.MemPressure = ParsePSI(c)
	}
	if c, ok := s.read("/proc/loadavg"); ok {
		t.Load1Pct = loadPct(ParseLoadAvg(c), t.Cores)
	}
	if c, ok := s.read("/proc/uptime"); ok {
		t.UptimeSeconds = ParseUptime(c)
	}
	t.DiskFreeBytes = diskFree(s.DiskFree, s.Root)
	t.Thermal = ThermalFromZones(s.thermalZones())
	return t, nil
}

func (s LinuxSampler) thermalZones() []ThermalZone {
	if s.Glob == nil {
		return nil
	}
	dirs, err := s.Glob("/sys/class/thermal/thermal_zone*")
	if err != nil {
		return nil
	}
	sort.Strings(dirs)
	var zones []ThermalZone
	for _, d := range dirs {
		temp, ok := s.readInt(d + "/temp")
		if !ok {
			continue
		}
		z := ThermalZone{MilliC: temp}
		types, _ := s.Glob(d + "/trip_point_*_type")
		sort.Strings(types)
		for _, tp := range types {
			typ, ok := s.read(tp)
			if !ok {
				continue
			}
			v, ok := s.readInt(strings.TrimSuffix(tp, "_type") + "_temp")
			if !ok {
				continue
			}
			z.Trips = append(z.Trips, ThermalTrip{Type: strings.TrimSpace(typ), MilliC: v})
		}
		zones = append(zones, z)
	}
	return zones
}
