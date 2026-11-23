package telemetry

import (
	"strconv"
	"strings"
)

// CPUTimes is the aggregate jiffy counters from /proc/stat.
type CPUTimes struct {
	Idle, Total uint64
	Cores       int
}

// ParseProcStat reads the aggregate "cpu" line (user..steal; guest and
// guest_nice are already inside user/nice) and counts cpuN lines.
func ParseProcStat(content string) (CPUTimes, bool) {
	var c CPUTimes
	found := false
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "cpu":
			if len(fields) < 5 {
				return CPUTimes{}, false
			}
			vals := make([]uint64, 0, 8)
			for i, s := range fields[1:] {
				if i >= 8 {
					break
				}
				n, err := strconv.ParseUint(s, 10, 64)
				if err != nil {
					return CPUTimes{}, false
				}
				vals = append(vals, n)
			}
			for _, v := range vals {
				c.Total += v
			}
			c.Idle = vals[3]
			if len(vals) > 4 {
				c.Idle += vals[4]
			}
			found = true
		case strings.HasPrefix(fields[0], "cpu"):
			c.Cores++
		}
	}
	return c, found
}

// CPUPercentBetween is busy time over the interval a→b; no progress or a
// counter reset is unavailable.
func CPUPercentBetween(a, b CPUTimes) *float64 {
	if b.Total <= a.Total || b.Idle < a.Idle {
		return nil
	}
	dt := float64(b.Total - a.Total)
	di := float64(b.Idle - a.Idle)
	if di > dt {
		return nil
	}
	p := 100 * (1 - di/dt)
	return &p
}

func meminfoKB(val string) (uint64, bool) {
	f := strings.Fields(val)
	if len(f) == 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return n * 1024, true
}

// ParseMeminfo returns MemTotal and MemTotal − MemAvailable in bytes.
func ParseMeminfo(content string) (total, used *uint64) {
	var t, avail uint64
	var haveT, haveA bool
	for _, line := range strings.Split(content, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "MemTotal":
			t, haveT = meminfoKB(val)
		case "MemAvailable":
			avail, haveA = meminfoKB(val)
		}
	}
	if !haveT || t == 0 {
		return nil, nil
	}
	total = &t
	if haveA && avail <= t {
		u := t - avail
		used = &u
	}
	return total, used
}

const (
	psiWarnSomeAvg10     = 10.0
	psiCriticalFullAvg10 = 10.0
)

func psiAvg10(fields []string) (float64, bool) {
	for _, f := range fields {
		if v, ok := strings.CutPrefix(f, "avg10="); ok {
			n, err := strconv.ParseFloat(v, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// ParsePSI maps /proc/pressure/memory: full avg10 ≥ 10 → critical,
// some avg10 ≥ 10 → warn, otherwise normal. Unparseable is unavailable.
func ParsePSI(content string) *string {
	some, full := -1.0, -1.0
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, ok := psiAvg10(f[1:])
		if !ok {
			continue
		}
		switch f[0] {
		case "some":
			some = v
		case "full":
			full = v
		}
	}
	if some < 0 {
		return nil
	}
	s := "normal"
	if some >= psiWarnSomeAvg10 {
		s = "warn"
	}
	if full >= psiCriticalFullAvg10 {
		s = "critical"
	}
	return &s
}

// ParseLoadAvg returns the 1-minute load from /proc/loadavg.
func ParseLoadAvg(content string) *float64 {
	f := strings.Fields(content)
	if len(f) == 0 {
		return nil
	}
	l, err := strconv.ParseFloat(f[0], 64)
	if err != nil || l < 0 {
		return nil
	}
	return &l
}

// ParseUptime returns whole seconds from /proc/uptime.
func ParseUptime(content string) *uint64 {
	f := strings.Fields(content)
	if len(f) == 0 {
		return nil
	}
	s, err := strconv.ParseFloat(f[0], 64)
	if err != nil || s < 0 {
		return nil
	}
	u := uint64(s)
	return &u
}

// ThermalTrip is one /sys/class/thermal trip point.
type ThermalTrip struct {
	Type   string
	MilliC int64
}

// ThermalZone is one thermal zone's temperature and trip points.
type ThermalZone struct {
	MilliC int64
	Trips  []ThermalTrip
}

// ThermalFromZones judges each zone against its passive (fair), hot
// (serious) and critical (critical) trips; the worst judged zone wins.
// Zones without such trips cannot be judged; none judged is unavailable.
func ThermalFromZones(zones []ThermalZone) *string {
	state := ""
	for _, z := range zones {
		judged := false
		zs := "nominal"
		for _, tr := range z.Trips {
			var s string
			switch tr.Type {
			case "passive":
				s = "fair"
			case "hot":
				s = "serious"
			case "critical":
				s = "critical"
			default:
				continue
			}
			judged = true
			if z.MilliC >= tr.MilliC {
				zs = worseThermal(zs, s)
			}
		}
		if judged {
			state = worseThermal(state, zs)
		}
	}
	if state == "" {
		return nil
	}
	return &state
}
