package runner_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

func capReq() runner.ExecRequirements {
	r := anthropicReq(runner.AffinityNone)
	r.Op, r.Backend, r.Provider, r.EndpointFingerprint = runner.OpChat, "", "claude-cli", "ab"
	return r
}

func exactCap(r runner.ExecRequirements) runner.ExecutionCapability {
	return runner.ExecutionCapability{Op: r.Op, Backend: r.Backend, Provider: r.Provider, EndpointFingerprint: r.EndpointFingerprint,
		Families: []modelidentity.Family{r.Group.Family}, Models: []string{r.Model}, ExecutedModel: true}
}

func remoteWith(caps ...runner.ExecutionCapability) runner.Source {
	s := anthropicSub("mac-mini")
	s.Backends = nil
	s.Capabilities = caps
	return s
}

func TestPlace_RemoteNeedsExactCapability(t *testing.T) {
	req := capReq()
	runners := []runner.Runner{online("mac-mini")}

	d := runner.Place(req, snap(runners, []runner.Source{remoteWith(exactCap(req))}), runner.PlacementPolicy{})
	require.True(t, d.Placed, d.Reason)
	assert.Equal(t, runner.RunnerID("mac-mini"), d.Runner)

	d = runner.Place(req, snap(runners, []runner.Source{remoteWith()}), runner.PlacementPolicy{})
	require.NotNil(t, d.Wait, "family alone never proves a remote route")
	assert.Contains(t, d.Reason, "no source serves")

	for name, mutate := range map[string]func(*runner.ExecutionCapability){
		"op":          func(c *runner.ExecutionCapability) { c.Op = runner.OpStream },
		"backend":     func(c *runner.ExecutionCapability) { c.Backend = "claude-code" },
		"provider":    func(c *runner.ExecutionCapability) { c.Provider = "lunaroute" },
		"fingerprint": func(c *runner.ExecutionCapability) { c.EndpointFingerprint = "cd" },
		"family":      func(c *runner.ExecutionCapability) { c.Families = []modelidentity.Family{"openai"} },
		"model":       func(c *runner.ExecutionCapability) { c.Models = []string{"claude-opus-5-5"} },
	} {
		t.Run(name, func(t *testing.T) {
			c := exactCap(req)
			mutate(&c)
			d := runner.Place(req, snap(runners, []runner.Source{remoteWith(c)}), runner.PlacementPolicy{})
			assert.False(t, d.Placed)
		})
	}
}

func TestPlace_RemoteCapabilityAxesToolsAndEvidence(t *testing.T) {
	runners := []runner.Runner{online("mac-mini")}
	req := capReq()
	req.ContextWindow, req.Effort, req.Tools, req.RequireExecutedModel = provider.Context200K, provider.EffortHigh, []string{"Read"}, true
	c := exactCap(req)
	c.ContextWindows, c.Efforts, c.Tools = []provider.ContextWindow{provider.Context200K}, []provider.Effort{provider.EffortHigh}, []string{"Read", "Grep"}
	require.True(t, runner.Place(req, snap(runners, []runner.Source{remoteWith(c)}), runner.PlacementPolicy{}).Placed)

	for name, mutate := range map[string]func(*runner.ExecutionCapability){
		"context":  func(c *runner.ExecutionCapability) { c.ContextWindows = nil },
		"effort":   func(c *runner.ExecutionCapability) { c.Efforts = []provider.Effort{provider.EffortLow} },
		"tools":    func(c *runner.ExecutionCapability) { c.Tools = []string{"Grep"} },
		"evidence": func(c *runner.ExecutionCapability) { c.ExecutedModel = false },
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			mutate(&bad)
			assert.False(t, runner.Place(req, snap(runners, []runner.Source{remoteWith(bad)}), runner.PlacementPolicy{}).Placed)
		})
	}
	// Default axes need no advertisement.
	plain := capReq()
	assert.True(t, runner.Place(plain, snap(runners, []runner.Source{remoteWith(exactCap(plain))}), runner.PlacementPolicy{}).Placed)
}

func TestPlace_BuiltInSourcesNeedNoCapabilityEvidence(t *testing.T) {
	req := capReq()
	src := anthropicSub(runner.BuiltInRunnerID)
	require.Empty(t, src.Capabilities)
	d := runner.Place(req, snap([]runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{src}), runner.PlacementPolicy{})
	require.True(t, d.Placed, d.Reason)
	assert.Equal(t, runner.BuiltInRunnerID, d.Runner)
}
