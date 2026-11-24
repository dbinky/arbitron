package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runner"
)

// The hub's harness floors are the published SupportedVersions floors, never
// invented: each is one of the package's Min values AND passes that package's
// own CheckVersion (only the highest floor passes every floor).
func TestRemoteCompatTableUsesHarnessFloors(t *testing.T) {
	c, err := remoteCompatTable()
	require.NoError(t, err)
	assert.Equal(t, runnerproto.ProtocolVersion, c.ProtocolMin)
	assert.Equal(t, runnerproto.ProtocolVersion, c.ProtocolMax)
	assert.NotEmpty(t, c.RunnerMin, "released compatibility range must have a concrete floor")
	assert.NotEmpty(t, c.RunnerMaxExclusive, "future feature releases require explicit compatibility")

	var claudeMins, codexMins []string
	for _, f := range claudecli.SupportedVersions {
		claudeMins = append(claudeMins, f.Min)
	}
	for _, f := range codex.SupportedVersions {
		codexMins = append(codexMins, f.Min)
	}
	require.NotEmpty(t, c.Harness["claude"])
	require.NotEmpty(t, c.Harness["codex"])
	assert.Contains(t, claudeMins, c.Harness["claude"])
	assert.Contains(t, codexMins, c.Harness["codex"])
	ok, reason := claudecli.CheckVersion(c.Harness["claude"])
	assert.True(t, ok, reason)
	ok, reason = codex.CheckVersion(c.Harness["codex"])
	assert.True(t, ok, reason)

	// Behaviour through the hub's own check: an old claude is refused, the floor is admitted.
	hello := runner.HelloInfo{OS: "darwin", Arch: "arm64", ProtocolVersion: runnerproto.ProtocolVersion,
		HarnessVersions: map[string]string{"claude": "2.0.1"}, WorkspaceRoot: "/srv/ws", ScratchRoot: "/Users/op/.arbitron-runner/scratch"}
	got := c.Check(hello, "/srv/ws")
	assert.Equal(t, runner.CompatIncompatible, got.State)
	assert.Contains(t, got.Reason, "claude")
	hello.HarnessVersions["claude"] = c.Harness["claude"] + " (Claude Code)"
	assert.Equal(t, runner.CompatCompatible, c.Check(hello, "/srv/ws").State)
}

func TestHarnessFloorPicksTheFloorThatPassesEveryCheck(t *testing.T) {
	check := func(v string) (bool, string) { return v == "2.0.0", "" }
	assert.Equal(t, "2.0.0", harnessFloor([]string{"1.0.0", "2.0.0", "1.5.0"}, check))
	assert.Empty(t, harnessFloor([]string{"1.0.0"}, check))
}

func TestWireRemoteRunnersRejectsMissingDeps(t *testing.T) {
	_, err := wireRemoteRunners(remoteRunnerInputs{})
	require.Error(t, err)
	for _, name := range []string{"DB", "ServerRoot", "Registry", "Ledger", "Events", "Health", "Notifier", "Audit", "Builtin", "Logger"} {
		assert.Contains(t, err.Error(), name)
	}
}
