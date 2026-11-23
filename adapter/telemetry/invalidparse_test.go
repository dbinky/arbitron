package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMalformedNumbersAreUnavailable(t *testing.T) {
	for _, n := range []string{"NaN", "+Inf", "-Inf"} {
		assert.Nil(t, ParsePSI("some avg10="+n+"\nfull avg10=0\n"), n)
		assert.Nil(t, ParseLoadAvg(n), n)
		assert.Nil(t, ParseUptime(n), n)
	}
	assert.Nil(t, ParsePSI("some avg10=0\n"), "missing full pressure cannot establish normal")
}
