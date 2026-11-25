package main

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/codex"
	adaptprobe "github.com/arbitron/arbitron/adapter/probe"
	"github.com/arbitron/arbitron/adapter/process"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	runnerprobe "github.com/arbitron/arbitron/adapter/runner/probe"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
)

// Start from the same real leaves used by the application, then remove one
// load-bearing injection. A panic must be the explicit boot guard, not a later
// nil dereference or a silently inert health monitor.
func TestRunnerWiring_RejectsMissingDependencies(t *testing.T) {
	ds, _ := rwBoot(t, 8)
	deps := func() runnerStackDeps {
		return runnerStackDeps{
			Config: ds.cfg.Runners, RefreshInventory: ds.runners.refreshInventory,
			ServerRoot: ds.cfg.Workspace.Path, Logger: ds.logger,
			DB: ds.db, Bus: ds.bus, Registry: ds.runners.registry,
			Factory: ds.runners.innerChats.(*providersadapter.Factory), Streams: ds.runners.innerStreams,
			Process: process.NewLocalProcessRunner(),
			Probers: map[runner.CredentialKind]runner.SourceProber{
				runner.CredAnthropicSubscription: runnerprobe.NewClaudeAuthProber(ds.cfg.LLM.BinaryPath, nil),
				runner.CredOpenAISubscription:    runnerprobe.NewCodexProber(codex.Config{}),
				runner.CredLunaRoute:             runnerprobe.NewLunaRouteProber(ds.connStore, adaptprobe.NewProbeFactory(adaptprobe.Deps{Timeout: time.Minute})),
			},
			Confirmer: runnerprobe.NewDispatchConfirmer(adaptsqlite.NewProfileRepo(ds.db), t.TempDir(), time.Minute),
			Alerts:    ds.runners.alerts, Audit: ds.auditSvc, Instances: ds.instanceRepo,
		}
	}
	cases := map[string]func(*runnerStackDeps){
		"inventory":     func(d *runnerStackDeps) { d.RefreshInventory = nil },
		"registry":      func(d *runnerStackDeps) { d.Registry = nil },
		"factory":       func(d *runnerStackDeps) { d.Factory = nil },
		"streams":       func(d *runnerStackDeps) { d.Streams = nil },
		"process":       func(d *runnerStackDeps) { d.Process = nil },
		"confirmer":     func(d *runnerStackDeps) { d.Confirmer = nil },
		"alerts":        func(d *runnerStackDeps) { d.Alerts = nil },
		"audit":         func(d *runnerStackDeps) { d.Audit = nil },
		"instances":     func(d *runnerStackDeps) { d.Instances = nil },
		"nil probers":   func(d *runnerStackDeps) { d.Probers = nil },
		"empty probers": func(d *runnerStackDeps) { d.Probers = map[runner.CredentialKind]runner.SourceProber{} },
	}
	for _, kind := range []runner.CredentialKind{runner.CredAnthropicSubscription, runner.CredOpenAISubscription, runner.CredLunaRoute} {
		cases["missing prober "+string(kind)] = func(d *runnerStackDeps) { delete(d.Probers, kind) }
		cases["nil prober "+string(kind)] = func(d *runnerStackDeps) { d.Probers[kind] = nil }
	}
	t.Run("empty config", func(t *testing.T) {
		d := deps()
		d.Config = config.RunnersConfig{}
		assert.Panics(t, func() { newRunnerStack(d) })
	})
	for name, remove := range cases {
		t.Run(name, func(t *testing.T) {
			d := deps()
			remove(&d)
			assert.PanicsWithValue(t, "arbitron: incomplete runner stack", func() {
				s, _ := newRunnerStack(d)
				// Clean up if a missing guard incorrectly permitted construction.
				s.stopWake()
				s.dispatcher.Close()
			})
		})
	}
}
