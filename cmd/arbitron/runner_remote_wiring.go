package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/adapter/runner/remote"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/adapter/telemetry"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/runner"
)

// remoteRunnerInputs are the Phase 1 pool objects (P1 T16) and existing
// server collaborators the Phase 2 remote-runner stack is built from.
type remoteRunnerInputs struct {
	DB         *sql.DB
	Runners    config.RunnersConfig
	ServerRoot string // absolute server execution root (wsMgr.BasePath())
	Registry   runner.Registry
	Ledger     runner.Ledger
	Events     *runner.EventRecorder
	Health     *runner.HealthMonitor // concrete: satisfies HubDeps.Health (HealthTransitioner; P2 T13 HubHealth adds ObserveProbe) and P2 T13 calls SetRemoteProber on it
	Notifier   *runner.PoolNotifier
	Audit      runner.AuditRecorder
	Builtin    runner.StepRunner
	Logger     *slog.Logger
	Clock      func() time.Time // nil = time.Now
}

// remoteRunnerStack is everything the server needs to admit, supervise and
// dispatch to remote runners.
type remoteRunnerStack struct {
	Hub       *runner.Hub
	Keys      *runner.KeyService
	Runners   runner.RunnerSet
	Connect   *adapthttp.RunnerConnectHandler
	Telemetry *runner.BuiltInTelemetryLoop
}

// wireRemoteRunners builds the hub, key service, remote StepRunner, connect
// endpoint and built-in telemetry loop. It is the only constructor of these
// objects in the server.
func wireRemoteRunners(in remoteRunnerInputs) (*remoteRunnerStack, error) {
	var missing []string
	if in.DB == nil {
		missing = append(missing, "DB")
	}
	if in.ServerRoot == "" {
		missing = append(missing, "ServerRoot")
	}
	if in.Registry == nil {
		missing = append(missing, "Registry")
	}
	if in.Ledger == nil {
		missing = append(missing, "Ledger")
	}
	if in.Events == nil {
		missing = append(missing, "Events")
	}
	if in.Health == nil {
		missing = append(missing, "Health")
	}
	if in.Notifier == nil {
		missing = append(missing, "Notifier")
	}
	if in.Audit == nil {
		missing = append(missing, "Audit")
	}
	if in.Builtin == nil {
		missing = append(missing, "Builtin")
	}
	if in.Logger == nil {
		missing = append(missing, "Logger")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("remote runners: missing %s", strings.Join(missing, ", "))
	}
	clock := in.Clock
	if clock == nil {
		clock = time.Now
	}
	compat, err := remoteCompatTable()
	if err != nil {
		return nil, err
	}
	hub := runner.NewHub(runner.HubDeps{
		Inbox:    adaptsqlite.NewRemoteInboxRepo(in.DB),
		Registry: in.Registry,
		Ledger:   in.Ledger,
		Events:   in.Events,
		Health:   in.Health,
		Notifier: in.Notifier,
		Clock:    clock,
		Config:   runner.HubConfigFromRunners(in.Runners, in.ServerRoot, compat),
	})
	keys := runner.NewKeyService(runner.KeyServiceDeps{
		Store:            adaptsqlite.NewRunnerKeyRepo(in.DB),
		Registry:         in.Registry,
		Audit:            in.Audit,
		Revoker:          hub,
		Clock:            clock,
		DefaultStepLimit: in.Runners.DefaultStepLimit,
	})
	return &remoteRunnerStack{
		Hub:     hub,
		Keys:    keys,
		Runners: runner.NewHubRunnerSet(in.Builtin, remote.New(hub), in.Registry),
		Connect: adapthttp.NewRunnerConnectHandler(keys, hub, adapthttp.RunnerConnectConfig{
			HeartbeatSeconds: in.Runners.HeartbeatSeconds,
			Logger:           in.Logger,
		}),
		Telemetry: runner.NewBuiltInTelemetryLoop(telemetry.NewForRoot(in.ServerRoot), hub, time.Duration(in.Runners.HeartbeatSeconds)*time.Second),
	}, nil
}

// remoteCompatTable is the server's admission table: exactly this protocol
// version, the release range from main.version, and the published harness floors.
func remoteCompatTable() (runner.CompatTable, error) {
	claudeMins := make([]string, 0, len(claudecli.SupportedVersions))
	for _, f := range claudecli.SupportedVersions {
		claudeMins = append(claudeMins, f.Min)
	}
	codexMins := make([]string, 0, len(codex.SupportedVersions))
	for _, f := range codex.SupportedVersions {
		codexMins = append(codexMins, f.Min)
	}
	claude := harnessFloor(claudeMins, claudecli.CheckVersion)
	cdx := harnessFloor(codexMins, codex.CheckVersion)
	if claude == "" || cdx == "" {
		return runner.CompatTable{}, fmt.Errorf("remote runners: no harness floor satisfies its own version table (claude %q, codex %q)", claude, cdx)
	}
	minRunner, maxRunner := runner.ReleaseRunnerRange(version)
	return runner.CompatTable{
		ProtocolMin:        runnerproto.ProtocolVersion,
		ProtocolMax:        runnerproto.ProtocolVersion,
		RunnerMin:          minRunner,
		RunnerMaxExclusive: maxRunner,
		Harness:            map[string]string{"claude": claude, "codex": cdx},
	}, nil
}

// harnessFloor returns the published Min that passes the package's own
// CheckVersion; only the highest floor passes every floor. "" when none does.
func harnessFloor(mins []string, check func(string) (bool, string)) string {
	for _, m := range mins {
		if ok, _ := check(m); ok {
			return m
		}
	}
	return ""
}
