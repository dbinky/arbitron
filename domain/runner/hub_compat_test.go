// domain/runner/hub_compat_test.go
package runner_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/runner"
)

func compatTable() runner.CompatTable {
	return runner.CompatTable{ProtocolMin: 1, ProtocolMax: 1, RunnerMin: "0.2.0", RunnerMaxExclusive: "0.3.0",
		Harness: map[string]string{"claude": "2.1.0", "codex": "0.50.0"}}
}

func compatHello() runner.HelloInfo {
	return runner.HelloInfo{OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "0.2.0",
		HarnessVersions: map[string]string{"claude": "2.1.30 (Claude Code)"},
		WorkspaceRoot:   "/srv/ws", ScratchRoot: "/Users/op/.arbitron-runner/scratch"}
}

func TestCompatTable_Check(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(h *runner.HelloInfo)
		server string
		state  runner.CompatState
		reason string
	}{
		{"compatible", func(*runner.HelloInfo) {}, "/srv/ws", runner.CompatCompatible, ""},
		{"trailing slash is the same root", func(h *runner.HelloInfo) { h.WorkspaceRoot = "/srv/ws/" }, "/srv/ws", runner.CompatCompatible, ""},
		{"linux refused", func(h *runner.HelloInfo) { h.OS = "linux" }, "/srv/ws", runner.CompatIncompatible, "darwin"},
		{"future protocol", func(h *runner.HelloInfo) { h.ProtocolVersion = 2 }, "/srv/ws", runner.CompatIncompatible, "protocol version 2"},
		{"protocol zero", func(h *runner.HelloInfo) { h.ProtocolVersion = 0 }, "/srv/ws", runner.CompatIncompatible, "protocol version 0"},
		{"old runner", func(h *runner.HelloInfo) { h.RunnerVersion = "0.1.9" }, "/srv/ws", runner.CompatIncompatible, "runner version"},
		{"future minor refused", func(h *runner.HelloInfo) { h.RunnerVersion = "v0.3.0" }, "/srv/ws", runner.CompatIncompatible, "runner version"},
		{"unparseable runner", func(h *runner.HelloInfo) { h.RunnerVersion = "dev" }, "/srv/ws", runner.CompatIncompatible, "runner version"},
		{"old claude", func(h *runner.HelloInfo) { h.HarnessVersions["claude"] = "2.0.99" }, "/srv/ws", runner.CompatIncompatible, "claude"},
		{"absent codex is fine", func(*runner.HelloInfo) {}, "/srv/ws", runner.CompatCompatible, ""},
		{"old codex", func(h *runner.HelloInfo) { h.HarnessVersions["codex"] = "0.49.1" }, "/srv/ws", runner.CompatIncompatible, "codex"},
		{"relative scratch", func(h *runner.HelloInfo) { h.ScratchRoot = "scratch" }, "/srv/ws", runner.CompatIncompatible, "scratch root"},
		{"scratch inside workspace", func(h *runner.HelloInfo) { h.ScratchRoot = "/srv/ws/.scratch" }, "/srv/ws", runner.CompatIncompatible, "outside"},
		{"scratch equals workspace", func(h *runner.HelloInfo) { h.ScratchRoot = "/srv/ws" }, "/srv/ws", runner.CompatIncompatible, "outside"},
		{"root mismatch", func(h *runner.HelloInfo) { h.WorkspaceRoot = "/Users/op/ws" }, "/srv/ws", runner.CompatChatOnly, "/Users/op/ws"},
		{"empty runner root", func(h *runner.HelloInfo) { h.WorkspaceRoot = "" }, "/srv/ws", runner.CompatChatOnly, "hermetic chat only"},
		{"empty server root", func(*runner.HelloInfo) {}, "", runner.CompatChatOnly, "hermetic chat only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := compatHello()
			tc.mut(&h)
			got := compatTable().Check(h, tc.server)
			assert.Equal(t, tc.state, got.State, got.Reason)
			if tc.reason == "" {
				assert.Empty(t, got.Reason)
			} else {
				assert.Contains(t, got.Reason, tc.reason)
			}
		})
	}
}

func TestCompatTable_UnstampedBuildIsVisibleAndIncompatible(t *testing.T) {
	c := compatTable()
	c.RunnerMin = ""
	h := compatHello()
	h.RunnerVersion = ""
	assert.Equal(t, runner.CompatIncompatible, c.Check(h, "/srv/ws").State)
}

func TestHubConfigFromRunners(t *testing.T) {
	rc := config.RunnersConfig{HeartbeatSeconds: 5, SuspectAfterSeconds: 30, LostAfterSeconds: 120, OfferTTLSeconds: 30, TelemetryWindow: 60}
	cfg := runner.HubConfigFromRunners(rc, "./workspaces", compatTable())
	assert.Equal(t, 5*time.Second, cfg.Heartbeat)
	assert.Equal(t, 30*time.Second, cfg.SuspectAfter)
	assert.Equal(t, 120*time.Second, cfg.LostAfter)
	assert.Equal(t, 30*time.Second, cfg.OfferTTL)
	assert.Equal(t, 60, cfg.TelemetryWindow)
	assert.True(t, filepath.IsAbs(cfg.ServerWorkspaceRoot), cfg.ServerWorkspaceRoot)
	assert.Equal(t, "workspaces", filepath.Base(cfg.ServerWorkspaceRoot))
	assert.Equal(t, compatTable(), cfg.Compat)
}
