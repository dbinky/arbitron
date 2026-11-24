package runner_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
)

func TestReleaseRunnerRangeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, server, agent string
		compatible          bool
	}{
		{"same release", "v1.2.3", "1.2.3", true},
		{"compatible patch", "1.2.3", "1.2.4", true},
		{"older patch", "1.2.3", "1.2.2", false},
		{"future minor", "1.2.3", "1.3.0", false},
		{"future major", "1.2.3", "2.0.0", false},
		{"same prerelease", "v1.2.3-rc.1", "1.2.3-rc.1", true},
		{"prerelease mismatch", "1.2.3-rc.1", "1.2.3-rc.2", false},
		{"prerelease versus release", "1.2.3-rc.1", "1.2.3", false},
		{"release versus prerelease", "1.2.3", "1.2.4-rc.1", false},
		{"missing stamp", "dev", "1.2.3", false},
		{"malformed stamp", "1.2", "1.2.3", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			min, max := runner.ReleaseRunnerRange(tc.server)
			assert.NotEmpty(t, min)
			c := runner.CompatTable{ProtocolMin: 1, ProtocolMax: 1, RunnerMin: min, RunnerMaxExclusive: max}
			h := runner.HelloInfo{OS: "darwin", ProtocolVersion: 1, RunnerVersion: tc.agent, WorkspaceRoot: "/ws", ScratchRoot: "/scratch"}
			got := c.Check(h, "/ws")
			if tc.compatible {
				assert.Equal(t, runner.CompatCompatible, got.State)
			} else {
				assert.Equal(t, runner.CompatIncompatible, got.State)
				assert.NotEmpty(t, got.Reason)
			}
		})
	}
}
