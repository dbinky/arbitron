package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProcStatFromFixture(t *testing.T) {
	c, ok := ParseProcStat(fixture(t, "proc_stat_1.txt"))
	require.True(t, ok)
	assert.Equal(t, uint64(93800), c.Total, "user..steal only; guest is already in user")
	assert.Equal(t, uint64(80500), c.Idle, "idle + iowait")
	assert.Equal(t, 2, c.Cores)
}

func TestParseProcStatMalformedIsNotOK(t *testing.T) {
	_, ok := ParseProcStat("intr 1\n")
	assert.False(t, ok)
	_, ok = ParseProcStat("cpu  a b c d\n")
	assert.False(t, ok)
}

func TestCPUPercentBetweenFixtures(t *testing.T) {
	a, _ := ParseProcStat(fixture(t, "proc_stat_1.txt"))
	b, _ := ParseProcStat(fixture(t, "proc_stat_2.txt"))
	p := CPUPercentBetween(a, b)
	require.NotNil(t, p)
	assert.InDelta(t, 25.0, *p, 1e-9)
}

func TestCPUPercentBetweenNoProgressIsUnavailable(t *testing.T) {
	a, _ := ParseProcStat(fixture(t, "proc_stat_1.txt"))
	assert.Nil(t, CPUPercentBetween(a, a))
	assert.Nil(t, CPUPercentBetween(CPUTimes{Idle: 10, Total: 100}, CPUTimes{Idle: 5, Total: 200}), "counter reset")
}

func TestParseMeminfoFromFixture(t *testing.T) {
	total, used := ParseMeminfo(fixture(t, "meminfo.txt"))
	require.NotNil(t, total)
	require.NotNil(t, used)
	assert.Equal(t, uint64(16694710272), *total)
	assert.Equal(t, uint64(8347355136), *used)
}

func TestParseMeminfoWithoutMemAvailableReportsTotalOnly(t *testing.T) {
	total, used := ParseMeminfo("MemTotal: 1024 kB\nMemFree: 512 kB\n")
	require.NotNil(t, total)
	assert.Equal(t, uint64(1024*1024), *total)
	assert.Nil(t, used, "MemFree is not used memory; without MemAvailable usage is unavailable")
}

func TestParsePSIFromFixtureIsWarn(t *testing.T) {
	got := ParsePSI(fixture(t, "pressure_memory.txt"))
	require.NotNil(t, got)
	assert.Equal(t, "warn", *got)
}

func TestParsePSIBands(t *testing.T) {
	normal := ParsePSI("some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	require.NotNil(t, normal)
	assert.Equal(t, "normal", *normal)
	critical := ParsePSI("some avg10=40.00 avg60=0 avg300=0 total=1\nfull avg10=15.00 avg60=0 avg300=0 total=1\n")
	require.NotNil(t, critical)
	assert.Equal(t, "critical", *critical)
	assert.Nil(t, ParsePSI("garbage\n"))
}

func TestParseLoadAvgAndUptime(t *testing.T) {
	l := ParseLoadAvg(fixture(t, "loadavg.txt"))
	require.NotNil(t, l)
	assert.InDelta(t, 0.52, *l, 1e-9)
	u := ParseUptime(fixture(t, "uptime.txt"))
	require.NotNil(t, u)
	assert.Equal(t, uint64(354321), *u)
	assert.Nil(t, ParseLoadAvg(""))
	assert.Nil(t, ParseUptime("x"))
}

func TestThermalFromZonesWorstJudgedZoneWins(t *testing.T) {
	zones := []ThermalZone{
		{MilliC: 85000, Trips: []ThermalTrip{{Type: "passive", MilliC: 80000}, {Type: "critical", MilliC: 105000}}},
		{MilliC: 40000, Trips: []ThermalTrip{{Type: "active", MilliC: 30000}}}, // fan trip only: not judged
	}
	got := ThermalFromZones(zones)
	require.NotNil(t, got)
	assert.Equal(t, "fair", *got)

	zones = append(zones, ThermalZone{MilliC: 101000, Trips: []ThermalTrip{{Type: "hot", MilliC: 100000}}})
	got = ThermalFromZones(zones)
	require.NotNil(t, got)
	assert.Equal(t, "serious", *got)

	cool := ThermalFromZones([]ThermalZone{{MilliC: 50000, Trips: []ThermalTrip{{Type: "critical", MilliC: 105000}}}})
	require.NotNil(t, cool)
	assert.Equal(t, "nominal", *cool)
}

func TestThermalFromZonesWithoutTripPointsIsUnavailable(t *testing.T) {
	assert.Nil(t, ThermalFromZones(nil))
	assert.Nil(t, ThermalFromZones([]ThermalZone{{MilliC: 99000}}))
}
