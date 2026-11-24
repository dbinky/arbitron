package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	builtinrunner "github.com/arbitron/arbitron/adapter/runner/builtin"
	runnerinventory "github.com/arbitron/arbitron/adapter/runner/inventory"
	runnerprobe "github.com/arbitron/arbitron/adapter/runner/probe"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

// builtinSourceSyncDeps configures the built-in runner's registration.
type builtinSourceSyncDeps struct {
	Registry  runner.Registry
	Inventory runnerinventory.Config
	// StepLimit is cfg.Runners.DefaultStepLimit. It applies when the runner row
	// is first created; UpsertRunner keeps an operator's later edits.
	StepLimit     int
	RunnerVersion string
	WorkspaceRoot string
	Logger        *slog.Logger
	Clock         func() time.Time
	// Detect reads the local inventory. Production leaves it nil, which selects
	// runnerinventory.DetectLocal; unit tests inject a fixed inventory.
	Detect func(context.Context, runnerinventory.Config) (runner.InventoryInput, error)
}

// builtinSourceSync registers the built-in runner and replaces its sources from
// harness presence plus the server's connections. It runs at boot and after
// every connection write, so a new connection is placeable immediately.
type builtinSourceSync struct {
	mu sync.Mutex
	d  builtinSourceSyncDeps
}

func newBuiltinSourceSync(d builtinSourceSyncDeps) *builtinSourceSync {
	if d.Registry == nil || d.Inventory.Connections == nil || d.Logger == nil {
		panic("arbitron: the built-in source sync requires a registry, a connection lister and a logger")
	}
	if d.Detect == nil {
		d.Detect = runnerinventory.DetectLocal
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &builtinSourceSync{d: d}
}

// Sync (re)registers the runner row and its sources. A harness detection
// failure degrades to connections only (subscription sources are always
// registered, annotated harness_not_found); registry or connection-list
// failures are returned.
func (s *builtinSourceSync) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, err := s.d.Detect(ctx, s.d.Inventory)
	if err != nil {
		s.d.Logger.Error("runner inventory detection failed; registering built-in sources from connections only", "error", err)
		conns, lerr := s.d.Inventory.Connections.List(ctx)
		if lerr != nil {
			return fmt.Errorf("listing connections for the built-in runner: %w", lerr)
		}
		in.Connections = conns // retain successfully detected harness facts from the partial inventory
	}
	harness := map[string]string{}
	if in.ClaudeCLI != nil && in.ClaudeCLI.Version != "" {
		harness["claude-code"] = in.ClaudeCLI.Version
	}
	if in.Codex != nil && in.Codex.Version != "" {
		harness["codex"] = in.Codex.Version
	}
	now := s.d.Clock().UTC()
	if err := s.d.Registry.UpsertRunner(ctx, runner.Runner{
		ID: runner.BuiltInRunnerID, Name: "server", BuiltIn: true, StepLimit: s.d.StepLimit,
		Compat:   runner.Compatibility{State: runner.CompatCompatible},
		Liveness: runner.LivenessOnline, LastContact: &now,
		OS: runtime.GOOS, RunnerVersion: s.d.RunnerVersion, HarnessVersions: harness,
		WorkspaceRoot: s.d.WorkspaceRoot,
	}); err != nil {
		return fmt.Errorf("registering the built-in runner: %w", err)
	}
	if err := s.d.Registry.ReplaceSources(ctx, runner.BuiltInRunnerID, runner.BuiltInSources(in)); err != nil {
		return fmt.Errorf("registering the built-in runner's sources: %w", err)
	}
	return nil
}

// syncingConnStore is the store behind the connection admin service: after a
// successful create/update/delete it refreshes the built-in runner's sources.
// Reads pass straight through.
type syncingConnStore struct {
	provider.ConnectionStore
	sync func(context.Context) error
}

func newSyncingConnStore(store provider.ConnectionStore, sync func(context.Context) error) syncingConnStore {
	if store == nil || sync == nil {
		panic("arbitron: the syncing connection store requires a store and a sync function")
	}
	return syncingConnStore{ConnectionStore: store, sync: sync}
}

func (s syncingConnStore) Create(ctx context.Context, c provider.Connection, apiKey string) error {
	if err := s.ConnectionStore.Create(ctx, c, apiKey); err != nil {
		return err
	}
	return s.resync(ctx)
}

func (s syncingConnStore) Update(ctx context.Context, c provider.Connection, apiKey *string) error {
	if err := s.ConnectionStore.Update(ctx, c, apiKey); err != nil {
		return err
	}
	return s.resync(ctx)
}

func (s syncingConnStore) Delete(ctx context.Context, name string) error {
	if err := s.ConnectionStore.Delete(ctx, name); err != nil {
		return err
	}
	return s.resync(ctx)
}

func (s syncingConnStore) resync(ctx context.Context) error {
	if err := s.sync(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("connection change saved, but the built-in runner's sources were not refreshed (repeat the change to retry): %w", err)
	}
	return nil
}

func (s *runnerStack) startInventory(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(s.inventoryInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.refreshInventory(ctx); err != nil {
					if ctx.Err() == nil {
						slog.Warn("runner inventory refresh failed", "error", err)
					}
				} else {
					s.notifier.Notify()
				}
			}
		}
	}()
}

// runnerStackDeps are the root values the runner pool is assembled from.
type runnerStackDeps struct {
	Config              config.RunnersConfig
	RefreshInventory    func(context.Context) error
	DB                  *sql.DB
	Bus                 engine.EventBus
	Registry            runner.Registry
	Factory             *providersadapter.Factory        // undecorated chat + agent selectors
	Streams             engine.InteractiveStreamSelector // undecorated interactive selector
	DefaultAgentBackend string
	Process             engine.ProcessRunner // the local process runner
	Probers             map[runner.CredentialKind]runner.SourceProber
	Confirmer           *runnerprobe.DispatchConfirmer
	Alerts              *adaptnotif.SourceAlerts
	Audit               runner.AuditRecorder
	Instances           instance.InstanceRepository
	Clock               func() time.Time
}

// runnerStack is the assembled Phase 1 pool: one built-in runner behind the
// dispatcher, plus the routed ports the executors use.
type runnerStack struct {
	refreshInventory  func(context.Context) error
	inventoryInterval time.Duration
	stopWake          func()
	registry          runner.Registry
	ledger            *adaptsqlite.RunnerLedgerRepo
	recorder          *runner.EventRecorder
	notifier          *runner.PoolNotifier
	projector         *runner.ExecutionStatusProjector
	monitor           *runner.HealthMonitor
	dispatcher        *runner.StepDispatcher
	service           *runner.Service
	alerts            *adaptnotif.SourceAlerts
	// Routed: every model/process step the executors run goes through the dispatcher.
	chats   engine.ChatProviderSelector
	agents  engine.AgentProviderSelector
	streams engine.InteractiveStreamSelector
	process engine.ProcessRunner
	// Undecorated: the built-in runner executes with these, and read-only
	// probe/inventory/refresh paths keep them (they never consume a slot).
	innerChats   engine.ChatProviderSelector
	innerAgents  engine.AgentProviderSelector
	innerStreams engine.InteractiveStreamSelector
}

func newRunnerStack(d runnerStackDeps) *runnerStack {
	if err := d.Config.Validate(); err != nil {
		panic(err)
	}
	if d.RefreshInventory == nil || d.Registry == nil || d.Factory == nil || d.Streams == nil || d.Process == nil || d.Confirmer == nil || d.Alerts == nil || d.Audit == nil || d.Instances == nil {
		panic("arbitron: incomplete runner stack")
	}
	for _, kind := range []runner.CredentialKind{runner.CredAnthropicSubscription, runner.CredOpenAISubscription, runner.CredLunaRoute} {
		if d.Probers[kind] == nil {
			panic("arbitron: incomplete runner stack")
		}
	}
	clock := d.Clock
	if clock == nil {
		clock = time.Now
	}
	seconds := func(n int) time.Duration { return time.Duration(n) * time.Second }
	millis := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }

	ledger := adaptsqlite.NewRunnerLedgerRepo(d.DB)
	poolEvents := adaptsqlite.NewPoolEventRepo(d.DB)
	recorder := runner.NewEventRecorder(poolEvents, d.Bus, d.Config.EventRetention, clock)
	notifier := runner.NewPoolNotifier()
	projector := runner.NewExecutionStatusProjector(ledger)
	monitor := runner.NewHealthMonitor(runner.MonitorDeps{
		Registry: d.Registry, Probers: d.Probers, Confirm: d.Confirmer, Alerts: d.Alerts,
		Events: recorder, Notifier: notifier, Clock: clock,
		Interval: seconds(d.Config.ProbeIntervalSeconds), Jitter: seconds(d.Config.ProbeJitterSeconds),
	})
	builtin := builtinrunner.New(builtinrunner.Deps{Chats: d.Factory, Agents: d.Factory, Streams: d.Streams, Process: d.Process,
		Confirmations: runnerprobe.NewConfirmationExecutor(d.Factory)})
	// Root shutdown unsubscribes after execution drains.
	wake, stopWake := notifier.Subscribe()
	dispatcher := runner.NewStepDispatcher(runner.DispatcherDeps{
		Registry: d.Registry, Ledger: ledger,
		Runners: runner.NewHealthReportingRunnerSet(runner.StaticRunnerSet{runner.BuiltInRunnerID: builtin}, monitor),
		Events:  recorder, Status: projector, Health: monitor,
		Telemetry: runner.EmptyTelemetry{}, Policy: runner.PlacementPolicy{}, Clock: clock,
		TransientMaxAttempts: d.Config.TransientMaxAttempts,
		BackoffMin:           millis(d.Config.TransientBackoffMinMs),
		BackoffMax:           millis(d.Config.TransientBackoffMaxMs),
		Wake:                 wake,
	})
	d.Confirmer.Attach(dispatcher)
	service := runner.NewService(runner.ServiceDeps{
		Registry: d.Registry, Ledger: ledger, Events: poolEvents, Recorder: recorder, Audit: d.Audit,
		Visibility: runner.NewInstanceVisibility(d.Instances), Monitor: monitor,
		Telemetry: runner.EmptyTelemetry{}, Notifier: notifier, Clock: clock,
	})
	return &runnerStack{
		refreshInventory: d.RefreshInventory, inventoryInterval: seconds(d.Config.ProbeIntervalSeconds), stopWake: stopWake,
		registry: d.Registry, ledger: ledger, recorder: recorder, notifier: notifier, projector: projector,
		monitor: monitor, dispatcher: dispatcher, service: service, alerts: d.Alerts,
		chats:        runner.NewRoutingChatSelector(d.Factory, dispatcher, runner.BuiltInAffinity),
		agents:       runner.NewRoutingAgentSelector(d.Factory, dispatcher),
		streams:      runner.WithDefaultAgentBackend(runner.NewRoutingStreamSelector(d.Streams, dispatcher), d.DefaultAgentBackend),
		process:      runner.NewRoutedProcessRunner(dispatcher),
		innerChats:   d.Factory,
		innerAgents:  d.Factory,
		innerStreams: d.Streams,
	}
}
