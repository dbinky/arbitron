package telemetry

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SysctlFacts is parsed from
// `sysctl hw.ncpu hw.memsize kern.boottime vm.loadavg kern.memorystatus_vm_pressure_level`.
type SysctlFacts struct {
	Cores         *int
	RAMBytes      *uint64
	BootTime      *time.Time
	Load1         *float64
	PressureLevel *int
}

var (
	bootSecRe = regexp.MustCompile(`sec\s*=\s*(\d+)`)
	loadAvgRe = regexp.MustCompile(`^\{\s*([0-9]+(?:\.[0-9]+)?)`)
)

// ParseSysctl reads "key: value" lines; an absent or malformed key is nil.
func ParseSysctl(out string) SysctlFacts {
	var f SysctlFacts
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "hw.ncpu":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				f.Cores = &n
			}
		case "hw.memsize":
			if n, err := strconv.ParseUint(val, 10, 64); err == nil && n > 0 {
				f.RAMBytes = &n
			}
		case "kern.boottime":
			if m := bootSecRe.FindStringSubmatch(val); m != nil {
				if s, err := strconv.ParseInt(m[1], 10, 64); err == nil && s > 0 {
					bt := time.Unix(s, 0).UTC()
					f.BootTime = &bt
				}
			}
		case "vm.loadavg":
			if m := loadAvgRe.FindStringSubmatch(val); m != nil {
				if l, err := strconv.ParseFloat(m[1], 64); err == nil && l >= 0 {
					f.Load1 = &l
				}
			}
		case "kern.memorystatus_vm_pressure_level":
			if n, err := strconv.Atoi(val); err == nil {
				f.PressureLevel = &n
			}
		}
	}
	return f
}

// MemPressureFromLevel maps the kernel's memorystatus pressure level
// (1 normal, 2 warn, 4 critical); any other value is unavailable.
func MemPressureFromLevel(level int) *string {
	var s string
	switch level {
	case 1:
		s = "normal"
	case 2:
		s = "warn"
	case 4:
		s = "critical"
	default:
		return nil
	}
	return &s
}

var pageSizeRe = regexp.MustCompile(`page size of (\d+) bytes`)

// ParseVMStat returns used memory: (active + wired + compressor-occupied
// pages) × page size. Missing page size or any counter is unavailable.
func ParseVMStat(out string) *uint64 {
	m := pageSizeRe.FindStringSubmatch(out)
	if m == nil {
		return nil
	}
	page, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil || page == 0 {
		return nil
	}
	pages := map[string]uint64{}
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(val), "."), 10, 64)
		if err != nil {
			continue
		}
		pages[strings.TrimSpace(key)] = n
	}
	var total uint64
	for _, k := range []string{"Pages active", "Pages wired down", "Pages occupied by compressor"} {
		n, ok := pages[k]
		if !ok {
			return nil
		}
		total += n
	}
	used := total * page
	return &used
}

var topCPURe = regexp.MustCompile(`CPU usage:.*?([0-9]+(?:\.[0-9]+)?)% idle`)

// ParseTopCPU returns 100 − idle from the last "CPU usage" line of
// `top -l 2`. The first sample is since boot, so fewer than two samples is
// unavailable.
func ParseTopCPU(out string) *float64 {
	all := topCPURe.FindAllStringSubmatch(out, -1)
	if len(all) < 2 {
		return nil
	}
	idle, err := strconv.ParseFloat(all[len(all)-1][1], 64)
	if err != nil || idle < 0 || idle > 100 {
		return nil
	}
	used := 100 - idle
	return &used
}

var (
	thermalPressureRe = regexp.MustCompile(`^com\.apple\.system\.thermalpressurelevel (\d+)$`)
	// thermalRank and worseThermal are shared with ThermalFromZones (Linux, Cycle 5).
	thermalRank = map[string]int{"nominal": 0, "fair": 1, "serious": 2, "critical": 3}
)

func worseThermal(a, b string) string {
	if a == "" {
		return b
	}
	if thermalRank[b] > thermalRank[a] {
		return b
	}
	return a
}

// ParseThermalPressure maps `notifyutil -g com.apple.system.thermalpressurelevel`
// (OSThermalPressureLevel: 0 Nominal, 1 Moderate, 2 Heavy, 3 Trapping,
// 4 Sleeping) to a thermal state. Anything other than exactly one line
// "com.apple.system.thermalpressurelevel <0-4>" is unavailable — never
// "nominal" (DECISION RR-06, docs/evidence/remote-runners/macos-runner-host.md).
func ParseThermalPressure(out string) *string {
	m := thermalPressureRe.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return nil
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return nil
	}
	var state string
	switch n {
	case 0:
		state = "nominal"
	case 1:
		state = "fair"
	case 2:
		state = "serious"
	case 3, 4:
		state = "critical"
	default:
		return nil
	}
	return &state
}
