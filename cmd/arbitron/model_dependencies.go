package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
)

// ModelDependencies replaces external I/O only, never model orchestration or persistence.
// Zero fields retain the production transport, launcher and clock defaults.
type ModelDependencies struct {
	FetchTransport        http.RoundTripper
	ProviderTransport     http.RoundTripper
	AnnouncementTransport http.RoundTripper
	Runner                claudecli.CommandRunner
	Now                   func() time.Time
	NewTicker             func(time.Duration) (<-chan time.Time, func())
	ModelStateSecret      []byte
}

// Start is the single serving lifecycle for HTTP and standalone MCP. Build has
// installed every required collaborator, including the final durable announcer,
// before any seed collection, recovery or background work is admitted here.
func (ds *domainStack) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ds.lifecycleMu.Lock()
	if ds.closed {
		ds.lifecycleMu.Unlock()
		return errors.New("domain stack closed")
	}
	if done := ds.startDone; done != nil {
		ds.lifecycleMu.Unlock()
		select {
		case <-done:
			return ds.startErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	ds.lifecycleCancel = cancel
	ds.startDone = make(chan struct{})
	ds.lifecycleMu.Unlock()
	// Bootstrap and manual work use the same coordinator-owned context. Closing
	// it on lifecycle cancellation also joins work whose caller has gone away.
	stop := context.AfterFunc(ctx, ds.modelRefresh.coordinator.Close)
	err := ds.startWork(ctx)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		cancel()
		ds.modelRefresh.coordinator.Close()
		stop()
	}
	ds.lifecycleMu.Lock()
	ds.startErr = err
	close(ds.startDone)
	ds.lifecycleMu.Unlock()
	return err
}

func (ds *domainStack) startRuntime(ctx context.Context, mode identity.Mode, spawn *engine.SpawnWaitReconciler, orphan *engine.OrphanedChildReconciler) error {
	// Repair direct and cycle publications before any recovered execution can
	// resolve a profile. This is local recovery only; discovery never gates
	// readiness (see startScheduled below).
	if err := ds.modelRefresh.startupPass(ctx); err != nil {
		return err
	}
	schedulerCtx, schedulerCancel := context.WithCancel(ctx)
	ds.schedulerCancel = schedulerCancel
	ds.scheduler.Start(schedulerCtx)
	if err := ds.eng.RecoverCrashedInstancesContext(ctx); err != nil {
		return fmt.Errorf("recovering crashed instances: %w", err)
	}
	// Source health probing starts after crash recovery, so its first tick
	// sees the recovered runs' placements. It stops with the lifecycle context.
	if ds.runners != nil {
		ds.runners.monitor.Start(ctx)
		ds.runners.startInventory(ctx, &ds.background)
	}
	if err := spawn.Reconcile(ctx); err != nil {
		ds.logger.Error("spawn-wait reconciliation incomplete", "error", err)
	}
	// Group recovery runs after crash recovery, so it sees crash-failed tasks and
	// parents. Every decision and wake is idempotent (the run's own state and the
	// continuation/actions flags guard it), so a task the queue admits meanwhile
	// is safe; a failed pass is retried by the sweep below.
	if ds.groupCoord != nil {
		if err := ds.groupCoord.Reconcile(ctx); err != nil {
			ds.logger.Error("parallel-group reconciliation incomplete", "error", err)
		}
	}
	// File gathers recover after crash-failed tasks are coordinated, under the
	// executor's shared gather gate.
	if ds.gatherReconciler != nil {
		if err := ds.gatherReconciler.Reconcile(ctx); err != nil {
			ds.logger.Error("parallel-group file-gather reconciliation incomplete", "error", err)
		}
	}
	if err := ds.queueMgr.RecoverTimers(ctx); err != nil {
		return fmt.Errorf("recovering timers: %w", err)
	}
	if err := ds.queueMgr.RecoverWebhooks(ctx); err != nil {
		return fmt.Errorf("recovering webhooks: %w", err)
	}
	if err := ds.scheduleEval.Recover(ctx); err != nil {
		ds.logger.Error("recovering schedules", "error", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ds.scheduleEval.Start(ctx)
	// The layout worker starts after recovery, like the evaluator; Close stops
	// it after execution drains and before the database closes.
	ds.layout.start(ds.logger)
	// Recovery sweeps run on recoveryWG so Close can join them with a bounded
	// wait before the database closes. The lifecycle context ends them.
	ds.recoveryWG.Go(func() { sweepSpawnReconcilers(ctx, spawn, orphan, spawnSweepInterval, ds.logger) })
	if ds.groupCoord != nil {
		ds.recoveryWG.Go(func() { sweepGroupReconciler(ctx, ds.groupCoord, groupSweepInterval, ds.logger) })
	}
	if ds.gatherReconciler != nil {
		ds.recoveryWG.Go(func() { sweepGroupReconciler(ctx, ds.gatherReconciler, groupSweepInterval, ds.logger) })
		// Subscribe so terminal events promptly abort/prune instead of waiting
		// for the periodic sweep.
		ds.groupWatchDone = ds.gatherReconciler.Watch(ctx)
	}
	if mode == identity.ModeOrg {
		ds.background.Add(2)
		go func() {
			defer ds.background.Done()
			adaptsqlite.SweepExpiredSessions(ctx, ds.sessionRepo, time.Duration(ds.cfg.Auth.Session.TTLSeconds)*time.Second, ds.logger)
		}()
		go func() {
			defer ds.background.Done()
			sweepExpiredDeviceCodes(ctx, ds.deviceSvc, deviceCodeSweepInterval, ds.logger)
		}()
	}
	// Discovery is an entrypoint policy: serve owns the automatic refresh loop
	// (immediate first pass in the background); stdio MCP starts none, so an
	// MCP client's handshake never waits on, or multiplies, catalog probes.
	if !ds.noAutomaticDiscovery {
		ds.modelRefresh.startScheduled(ctx)
		if ds.awaitInitialDiscovery {
			select {
			case <-ds.modelRefresh.initialPass():
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

// defaultAwaitInitialDiscovery seeds domainStack.awaitInitialDiscovery. It is
// false in production: readiness never waits on discovery. The test package
// enables it so root tests observe a settled first cycle deterministically;
// the startup-latency tests turn it off on their own stack.
var defaultAwaitInitialDiscovery = false

// disableAutomaticDiscovery is the stdio MCP entrypoint policy. Call it before
// Start: startup then performs local publication recovery only, prepares no
// seed that needs discovery, and registers no refresh loop. Explicit
// model_catalog_* tool calls keep their discovery behavior.
func (ds *domainStack) disableAutomaticDiscovery() {
	ds.noAutomaticDiscovery = true
}
