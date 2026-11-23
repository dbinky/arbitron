package telemetry

import (
	"context"
	"reflect"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertAllMetricsNil fails if any pointer metric in t is set.
func assertAllMetricsNil(t *testing.T, tel runner.Telemetry) {
	t.Helper()
	v := reflect.ValueOf(tel)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.Ptr {
			assert.True(t, f.IsNil(), "%s must be unavailable (nil)", v.Type().Field(i).Name)
		}
	}
}

func TestNullReportsEveryMetricUnavailable(t *testing.T) {
	var m runner.MachineTelemetry = Null{}
	tel, err := m.Sample(context.Background())
	require.NoError(t, err)
	assert.False(t, tel.SampledAt.IsZero(), "the sample time is still reported")
	assertAllMetricsNil(t, tel)
}
