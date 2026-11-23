package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunnersConfig_ValidatePhase4(t *testing.T) {
	valid := RunnersConfig{TurnLeaseSeconds: 60, SessionBundleRetention: 1, RebuildMaxAttempts: 2}
	assert.Empty(t, valid.validatePhase4(), "defaults are valid")

	cases := []struct {
		name    string
		mutate  func(*RunnersConfig)
		wantKey string
	}{
		{"lease too short", func(c *RunnersConfig) { c.TurnLeaseSeconds = 14 }, "runners.turnleaseseconds"},
		{"lease zero", func(c *RunnersConfig) { c.TurnLeaseSeconds = 0 }, "runners.turnleaseseconds"},
		{"lease too long", func(c *RunnersConfig) { c.TurnLeaseSeconds = 3601 }, "runners.turnleaseseconds"},
		{"retention zero", func(c *RunnersConfig) { c.SessionBundleRetention = 0 }, "runners.sessionbundleretention"},
		{"retention too big", func(c *RunnersConfig) { c.SessionBundleRetention = 17 }, "runners.sessionbundleretention"},
		{"rebuild zero", func(c *RunnersConfig) { c.RebuildMaxAttempts = 0 }, "runners.rebuildmaxattempts"},
		{"rebuild too many", func(c *RunnersConfig) { c.RebuildMaxAttempts = 6 }, "runners.rebuildmaxattempts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.mutate(&c)
			errs := c.validatePhase4()
			if assert.Len(t, errs, 1) {
				assert.Contains(t, errs[0].Error(), tc.wantKey)
			}
		})
	}

	// Every violation is reported, not just the first.
	bad := RunnersConfig{}
	assert.Len(t, bad.validatePhase4(), 3)
}

func TestRunnersConfig_ValidateIncludesPhase4(t *testing.T) {
	c := RunnersConfig{TurnLeaseSeconds: 1}
	err := c.Validate()
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "runners.turnleaseseconds")
	}
}
