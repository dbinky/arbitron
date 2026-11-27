package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/adapter/runner/builtin"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

// workspaceMobilityInputs are what main.go already built.
type workspaceMobilityInputs struct {
	DB           *sql.DB
	Runners      config.RunnersConfig
	Homes        *adaptsqlite.WorkspaceHomeRepo
	Hub          *runner.Hub
	Keys         adapthttp.TransferKeyAuthenticator // runnerPool.remote.Keys (*runner.KeyService)
	Registry     runner.Registry
	Ledger       runner.Ledger
	Events       *runner.EventRecorder
	Status       *runner.ExecutionStatusProjector
	Notifier     *runner.PoolNotifier
	Workspaces   engine.WorkspaceManager // undecorated wsMgr
	Capabilities provider.CapabilityRules
	Supervisor   *builtin.ProcessSupervisor // the ONE supervisor (P3 T10); main.go's builtinSup from Cycle 4b
	Sessions     runner.PinnedSessions      // Cycle 4c runner.InteractiveSessionInventory
	Audit        runner.AuditRecorder       // *audit.Service; decision D6 (AdoptDeps.Audit)
	Clock        func() time.Time
	BootID       string // one uuid.NewString() per process, created in main.go
	// Leases is the ONE lease repo main.go builds with BootID before
	// newRunnerStack, because the revocation releaser inside wireRemoteRunners
	// needs it before this function runs (unstick 2026-11-24). nil (unit tests
	// only) builds it here from DB and BootID.
	Leases *adaptsqlite.ResourceLeaseRepo
}

// workspaceMobility is the Phase 3 server stack. Every field is non-nil.
type workspaceMobility struct {
	Checkpoints *checkpoint.Store
	Local       *checkpoint.LocalStore // P3 T16 local copies of non-portable homes (unstick 2026-11-27)
	Homes       runner.HomeStore       // copy-tracking homes over the one WorkspaceHomeRepo
	Leases      *adaptsqlite.ResourceLeaseRepo
	BootID      string
	Sync        *runner.WorkspaceSync
	Baseline    *runner.BaselineInitializer
	Tokens      *runner.TransferTokenService
	Bundles     *checkpoint.BundleStore
	Transfers   *adapthttp.RunnerTransferHandler
	Commits     *adaptsqlite.StepCommitRepo
	Resources   *adaptsqlite.ResourceCommitRepo
	Supervisor  *builtin.ProcessSupervisor
	Recovery    *runner.PoolRecovery
	Adopter     *runner.JobAdopter
	Removal     *runner.RemovalNotifier
	Workspaces  engine.WorkspaceManager // wsMgr decorated with the removal hook
	Pulls       *runner.WorkspacePullCoordinator
	Drain       *runner.DrainController
	Resource    *runner.ServerWorkspaceResource
	Notices     *runner.RecoveryNoticeSource
	History     *runner.WorkspaceHistorySource
	Policy      runner.PlacementPolicy
}

// placementPolicy maps cfg.Runners onto runner.PlacementPolicy (contracts §7).
func placementPolicy(c config.RunnersConfig) runner.PlacementPolicy {
	return runner.PlacementPolicy{
		MoveLoadThresholdPct: c.MoveLoadThresholdPct,
		MoveSizeCapBytes:     c.MoveSizeCapBytes,
		TelemetryMaxAge:      3 * time.Duration(c.HeartbeatSeconds) * time.Second,
	}
}

func (in workspaceMobilityInputs) missing() []string {
	var m []string
	add := func(ok bool, name string) {
		if !ok {
			m = append(m, name)
		}
	}
	add(in.DB != nil, "DB")
	add(in.Homes != nil, "Homes")
	add(in.Hub != nil, "Hub")
	add(in.Keys != nil, "Keys")
	add(in.Registry != nil, "Registry")
	add(in.Ledger != nil, "Ledger")
	add(in.Events != nil, "Events")
	add(in.Status != nil, "Status")
	add(in.Notifier != nil, "Notifier")
	add(in.Workspaces != nil, "Workspaces")
	add(in.Supervisor != nil, "Supervisor")
	add(in.Sessions != nil, "Sessions")
	add(in.BootID != "", "BootID")
	add(in.Audit != nil, "Audit") // decision D6: JobAdopter audits reconnect-absence takeovers
	return m
}

// buildWorkspaceMobility is the ONLY constructor of the Phase 3 server
// objects. It builds one lease repo, one checkpoint store and one copy-tracking
// home store and feeds every consumer from them.
func buildWorkspaceMobility(in workspaceMobilityInputs) (*workspaceMobility, error) {
	if m := in.missing(); len(m) > 0 {
		return nil, fmt.Errorf("workspace mobility: missing %s", strings.Join(m, ", "))
	}
	clock := in.Clock
	if clock == nil {
		clock = time.Now
	}
	rc := in.Runners
	m := &workspaceMobility{BootID: in.BootID, Supervisor: in.Supervisor, Policy: placementPolicy(rc)}

	// One lease repo bound to this process's boot ID (contracts §25).
	leases := in.Leases
	if leases == nil {
		leases = adaptsqlite.NewResourceLeaseRepo(in.DB, in.BootID)
	}
	m.Leases = leases

	// Checkpoints (P3 T2) and script bundles (P3 T8).
	m.Checkpoints = checkpoint.NewStore(rc.CheckpointDir, checkpoint.WithCatalog(adaptsqlite.NewCheckpointCatalogRepo(in.DB)))
	bundles, err := checkpoint.NewBundleStore(filepath.Join(rc.CheckpointDir, "bundles"))
	if err != nil {
		return nil, fmt.Errorf("workspace mobility: bundle store: %w", err)
	}
	m.Bundles = bundles
	// P3 T16 (RR-17 B): plain local copies of non-portable homes, under the same
	// root. Required: NewWorkspaceSync panics without SyncDeps.Local.
	m.Local = checkpoint.NewLocalStore(filepath.Join(rc.CheckpointDir, "local"))

	// Copy fencing (P3 T11): the hub reconciles held copies on every hello, and
	// every home change goes through the copy-tracking store.
	copies := adaptsqlite.NewRunnerCopyRepo(in.DB)
	keeper, err := runner.NewCopyKeeper(runner.CopyKeeperDeps{Inventory: copies, Homes: in.Homes, Deleter: in.Hub, Leases: leases})
	if err != nil {
		return nil, fmt.Errorf("workspace mobility: copy keeper: %w", err)
	}
	in.Hub.SetCopyReconciler(keeper)
	m.Homes = runner.NewCopyTrackingHomes(in.Homes, keeper)

	// Transfer tokens (P3 T7): live job fence AND live resource claim.
	m.Tokens = runner.NewTransferTokenService(adaptsqlite.NewTransferSessionRepo(in.DB), in.Ledger, leases, clock)

	// Workspace sync (P3 T6) and baseline (P3 T6 Cycle 2).
	m.Sync = runner.NewWorkspaceSync(runner.SyncDeps{
		Local: m.Local, Checkpoints: m.Checkpoints, Homes: m.Homes, Leases: leases, Ledger: in.Ledger, Tokens: m.Tokens,
		Events: in.Events, Status: in.Status, Sizes: in.Workspaces, Clock: clock,
	})
	m.Baseline = runner.NewWorkspaceBaselineInitializer(runner.BaselineDeps{
		Checkpoints: m.Checkpoints, Ledger: in.Ledger, Leases: leases, Homes: m.Homes, Local: m.Local,
	})

	// Remote agent/process jobs (P3 T8).
	in.Hub.SetWorkspaceJobs(runner.WorkspaceJobDeps{
		Sync: m.Sync, Tokens: m.Tokens, Capabilities: in.Capabilities, PartBytes: rc.TransferPartBytes,
	})

	// Transfer endpoints (P3 T7).
	m.Transfers = adapthttp.NewRunnerTransferHandler(adapthttp.TransferHandlerDeps{
		Keys: in.Keys, Tokens: m.Tokens, Checkpoints: m.Checkpoints, Sync: m.Sync, Bundles: bundles,
		StagingDir: rc.StagingDir, PartBytes: rc.TransferPartBytes,
		StagingRetention: time.Duration(rc.StagingRetentionSeconds) * time.Second, Clock: clock,
	})

	// Durable commits (P3 T5 steps; Cycle 3a group mutations).
	m.Commits = adaptsqlite.NewStepCommitRepo(in.DB)
	m.Resources = adaptsqlite.NewResourceCommitRepo(in.DB)

	// Recovery and adoption (P3 T10).
	if m.Recovery, err = runner.NewPoolRecovery(runner.RecoveryDeps{
		Inbox:  adaptsqlite.NewRemoteInboxRepo(in.DB), // stateless over the same DB as the hub's inbox (P2 T10)
		Leases: leases, Stopper: in.Supervisor, Ledger: in.Ledger, Homes: m.Homes, Restorer: m.Sync,
		Commits: m.Commits, Grace: in.Hub, Events: in.Events, Status: in.Status, Clock: clock,
		RestartGrace: time.Duration(rc.RestartGraceSeconds) * time.Second,
	}); err != nil {
		return nil, fmt.Errorf("workspace mobility: pool recovery: %w", err)
	}
	if m.Adopter, err = runner.NewJobAdopter(runner.AdoptDeps{
		Ledger: in.Ledger, Registry: in.Registry, Hub: in.Hub, Leases: leases, Stopper: in.Supervisor, // P3 T10 Cycle 6 (unstick 2026-11-24)
		Events: in.Events, Status: in.Status, Audit: in.Audit, Clock: clock, // Audit: decision D6 (P3 T10 Cycle 6)
	}); err != nil {
		return nil, fmt.Errorf("workspace mobility: job adopter: %w", err)
	}
	m.Notices = runner.NewRecoveryNoticeSource(in.Ledger)

	// Removal (P3 T11): the engine fires the hook; group code gets the decorated manager.
	if m.Removal, err = runner.NewRemovalNotifier(runner.RemovalDeps{
		Inventory: copies, Homes: in.Homes, Checkpoints: m.Checkpoints, Deleter: in.Hub, Leases: leases, // undecorated homes: the notifier sends its own deletes (P3 T11)
	}); err != nil {
		return nil, fmt.Errorf("workspace mobility: removal notifier: %w", err)
	}
	m.Workspaces = engine.WithWorkspaceRemovalHook(in.Workspaces, m.Removal)

	// Drain (P3 T12): pull-only maintenance transfers, durable items, session blockers.
	drainItems := adaptsqlite.NewDrainItemRepo(in.DB)
	if m.Pulls, err = runner.NewWorkspacePullCoordinator(runner.PullCoordinatorDeps{
		Hub: in.Hub, Tokens: m.Tokens, Leases: leases, Homes: in.Homes, Workspaces: in.Workspaces, Clock: clock, // P3 T12 Cycle 2b deps
	}); err != nil {
		return nil, fmt.Errorf("workspace mobility: pull coordinator: %w", err)
	}
	in.Hub.SetPullReceiver(m.Pulls) // pull receipts (live and hello replays) reach the coordinator (P3 T12 Cycle 2c)
	if m.Drain, err = runner.NewDrainController(runner.DrainDeps{
		Pulls: m.Pulls, Sessions: in.Sessions, Leases: leases, Registry: in.Registry, Ledger: in.Ledger,
		Requirements: adaptsqlite.NewRunnerLedgerRepo(in.DB), Checkpoints: m.Checkpoints, Telemetry: in.Hub, // P3 T12 Cycle 2 (unstick 2026-11-24)
		Homes: m.Homes, Store: drainItems, Events: in.Events, Notifier: in.Notifier, Policy: m.Policy,
		Sizes: m.Sync, Clock: clock,
	}); err != nil {
		return nil, fmt.Errorf("workspace mobility: drain controller: %w", err)
	}
	m.History = runner.NewWorkspaceHistorySource(m.Homes)

	// Group mutations (Cycle 3) share the SAME lease repo as every step.
	if m.Resource, err = runner.NewServerWorkspaceResource(runner.ServerWorkspaceDeps{
		Leases: leases, Commits: m.Resources, Homes: m.Homes, Checkpoints: m.Checkpoints, Ledger: in.Ledger,
	}); err != nil {
		return nil, fmt.Errorf("workspace mobility: group workspace resource: %w", err)
	}
	return m, nil
}
