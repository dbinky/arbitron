package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/adapter/checkpoint"
)

// checkpointPruneInterval is the cadence for reclaiming orphaned artifacts.
const checkpointPruneInterval = time.Hour

type checkpointPruner interface {
	Prune(ctx context.Context, now time.Time) (checkpoint.PruneReport, error)
}

// startCheckpointPruner runs once at boot and on each tick. Its completion
// channel lets the serving lifecycle join it before closing the catalog DB.
func startCheckpointPruner(ctx context.Context, p checkpointPruner, ticks <-chan time.Time, now func() time.Time, log *slog.Logger) <-chan struct{} {
	if p == nil {
		panic("arbitron: checkpoint pruner is required when workspace mobility is enabled")
	}
	if log == nil {
		log = slog.Default()
	}
	run := func(at time.Time) {
		rep, err := p.Prune(ctx, at)
		if err != nil {
			log.Warn("checkpoint prune failed; retrying next tick", "error", err)
			return
		}
		log.Info("checkpoint prune", "archives", rep.Archives, "markers", rep.Markers, "cache", rep.CacheEntries, "scratch", rep.ScratchDirs, "bytes", rep.Bytes, "skippedHeldRuns", rep.SkippedHeldRuns)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		run(now())
		for {
			select {
			case <-ctx.Done():
				return
			case at, ok := <-ticks:
				if !ok {
					return
				}
				run(at)
			}
		}
	}()
	return stopped
}
