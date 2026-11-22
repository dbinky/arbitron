package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validRunners() RunnersConfig {
	return RunnersConfig{
		DefaultStepLimit:      DefaultRunnerStepLimit,
		ProbeIntervalSeconds:  DefaultRunnerProbeIntervalSeconds,
		ProbeJitterSeconds:    DefaultRunnerProbeJitterSeconds,
		TransientMaxAttempts:  DefaultRunnerTransientMaxAttempts,
		TransientBackoffMinMs: DefaultRunnerTransientBackoffMinMs,
		TransientBackoffMaxMs: DefaultRunnerTransientBackoffMaxMs,
		EventRetention:        DefaultRunnerEventRetention,
	}
}

func TestRunnersConfig_DefaultsAreValid(t *testing.T) {
	assert.Equal(t, 8, DefaultRunnerStepLimit, "the spec's initial execution limit is eight")
	require.NoError(t, validRunners().Validate())
}

func TestRunnersConfig_BoundariesAccepted(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*RunnersConfig)
	}{
		{"step limit 1", func(c *RunnersConfig) { c.DefaultStepLimit = 1 }},
		{"step limit 256", func(c *RunnersConfig) { c.DefaultStepLimit = 256 }},
		{"probe interval 5s", func(c *RunnersConfig) { c.ProbeIntervalSeconds = 5 }},
		{"jitter 0", func(c *RunnersConfig) { c.ProbeJitterSeconds = 0 }},
		{"transient attempts 1", func(c *RunnersConfig) { c.TransientMaxAttempts = 1 }},
		{"transient attempts 10", func(c *RunnersConfig) { c.TransientMaxAttempts = 10 }},
		{"backoff min == max", func(c *RunnersConfig) { c.TransientBackoffMinMs = 500; c.TransientBackoffMaxMs = 500 }},
		{"retention 100", func(c *RunnersConfig) { c.EventRetention = 100 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validRunners()
			tc.mut(&c)
			assert.NoError(t, c.Validate())
		})
	}
}

func TestRunnersConfig_ViolationsRejectedNamingTheKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*RunnersConfig)
		key  string
	}{
		{"step limit zero", func(c *RunnersConfig) { c.DefaultStepLimit = 0 }, "runners.defaultsteplimit"},
		{"step limit 257", func(c *RunnersConfig) { c.DefaultStepLimit = 257 }, "runners.defaultsteplimit"},
		{"probe interval 4", func(c *RunnersConfig) { c.ProbeIntervalSeconds = 4 }, "runners.probeintervalseconds"},
		{"jitter negative", func(c *RunnersConfig) { c.ProbeJitterSeconds = -1 }, "runners.probejitterseconds"},
		{"transient attempts 0", func(c *RunnersConfig) { c.TransientMaxAttempts = 0 }, "runners.transientmaxattempts"},
		{"transient attempts 11", func(c *RunnersConfig) { c.TransientMaxAttempts = 11 }, "runners.transientmaxattempts"},
		{"backoff min 0", func(c *RunnersConfig) { c.TransientBackoffMinMs = 0 }, "runners.transientbackoffminms"},
		{"backoff max < min", func(c *RunnersConfig) { c.TransientBackoffMinMs = 2000; c.TransientBackoffMaxMs = 1999 }, "runners.transientbackoffmaxms"},
		{"retention 99", func(c *RunnersConfig) { c.EventRetention = 99 }, "runners.eventretention"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validRunners()
			tc.mut(&c)
			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}

func TestRunnersConfig_EveryViolationReported(t *testing.T) {
	c := validRunners()
	c.DefaultStepLimit = 0
	c.EventRetention = 1
	err := c.Validate()
	require.Error(t, err)
	msg := err.Error()
	assert.True(t, strings.Contains(msg, "runners.defaultsteplimit") && strings.Contains(msg, "runners.eventretention"),
		"errors.Join must report every violation, got %q", msg)
}

func TestRunnersConfig_ZeroValueIsInvalid(t *testing.T) {
	// A config built without the loader (zero value) must never pass: a zero
	// step limit would park every step in a slot wait forever.
	require.Error(t, RunnersConfig{}.Validate())
}
