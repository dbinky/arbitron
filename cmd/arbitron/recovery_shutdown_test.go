package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rollbackReconciler stands in for a gather recovery pass whose rollback runs
// under context.WithoutCancel: once started it ignores cancellation, finishes
// only when released, and then persists its report through the database.
type rollbackReconciler struct {
	db       interface{ PingContext(context.Context) error }
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	storeErr error
}

func (r *rollbackReconciler) Reconcile(ctx context.Context) error {
	r.once.Do(func() { close(r.started) })
	<-r.release
	r.storeErr = r.db.PingContext(context.WithoutCancel(ctx))
	return nil
}

func TestDomainStackClose_WaitsForABlockedRecoveryRollbackBeforeClosingTheDB(t *testing.T) {
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "shutdown.db"))
	require.NoError(t, err)
	rec := &rollbackReconciler{db: db, started: make(chan struct{}), release: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}
	wg.Go(func() { sweepGroupReconciler(ctx, rec, time.Millisecond, discardLogger()) })
	watchDone := make(chan struct{})
	wg.Go(func() { <-ctx.Done(); close(watchDone) })
	ds := &domainStack{logger: discardLogger(), db: db, groupSweepCancel: cancel, recoveryWG: wg, groupWatchDone: watchDone}

	<-rec.started
	closed := make(chan struct{})
	go func() { ds.Close(context.Background()); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while a recovery rollback was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(rec.release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the rollback finished")
	}
	assert.NoError(t, rec.storeErr, "the rollback persisted its report before the database closed")
	assert.Error(t, db.Ping(), "Close closed the database after the workers")
}

// A worker that never returns cannot hold shutdown forever.
func TestWaitRecoveryWorkers_GivesUpAfterTheTimeout(t *testing.T) {
	wg := &sync.WaitGroup{}
	stuck := make(chan struct{})
	defer close(stuck)
	wg.Go(func() { <-stuck })
	start := time.Now()
	assert.False(t, waitRecoveryWorkers(wg, nil, 50*time.Millisecond, discardLogger()))
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.True(t, waitRecoveryWorkers(&sync.WaitGroup{}, nil, time.Second, discardLogger()))
}
