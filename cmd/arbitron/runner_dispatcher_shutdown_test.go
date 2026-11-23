package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dispatcherForwarders identifies the actual wake goroutines, rather than
// inferring cleanup from a constructor read-back or a global goroutine count.
func dispatcherForwarders() map[string]bool {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n == len(buf) {
			buf = make([]byte, len(buf)*2)
			continue
		}
		found := map[string]bool{}
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(stack, "domain/runner.NewStepDispatcher.func1(") {
				found[strings.Fields(stack)[1]] = true // ID is stable as the goroutine state changes.
			}
		}
		return found
	}
}

func TestRunnerWiring_DispatcherShutdownDrainsAndJoins(t *testing.T) {
	before := dispatcherForwarders()
	ds, fake := rwBoot(t, 8)
	// Cleanup must release the leaf before rwBoot's root cleanup, even on failure.
	t.Cleanup(func() { rwTouch(t, fake, "release-all") })
	require.NoError(t, os.Mkdir(filepath.Join(fake, "token"), 0o700))
	ctx := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, ctx, ds)
	statuses := rwCollectStatuses(t, ds.bus)
	var owned string
	require.Eventually(t, func() bool {
		for id := range dispatcherForwarders() {
			if !before[id] {
				owned = id
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "root must start a real dispatcher wake forwarder")

	id, _, err := ds.eng.Trigger(ctx, "rr-block", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return rwCalls(fake) == 1 && rwCountJobs(ds.db, "state = 'running'") == 1
	}, 30*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		for _, st := range statuses() {
			if st.Run == id && st.State == instance.ExecRunning {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "real dispatch publishes running to the event sink")

	closed := make(chan struct{})
	go func() { ds.Close(context.Background()); close(closed) }()
	select {
	case <-closed:
		t.Fatal("root closed before the active execution drained")
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, ds.db.Ping(), "execution still needs its ledger")
	assert.True(t, dispatcherForwarders()[owned], "wake forwarding lives until execution drains")
	rwTouch(t, fake, "release-all")
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("root shutdown did not join active execution")
	}
	assert.False(t, dispatcherForwarders()[owned], "Close must join the root's wake goroutine")
	require.Error(t, ds.db.Ping(), "database closes only after draining")
	ds.runners.notifier.Notify()  // late pool signals cannot revive the closed forwarder
	ds.runners.dispatcher.Close() // idempotent after root shutdown
	assert.False(t, dispatcherForwarders()[owned])

	// Reopen the durable sink: completion and the placement fact must survive
	// shutdown, not merely have been observed in memory before Close.
	db, err := adaptsqlite.Open(filepath.Join(filepath.Dir(fake), "arbitron.db"))
	require.NoError(t, err)
	defer db.Close()
	jobs := rwJobs(t, db, id)
	require.True(t, rwEveryOpPlacedOnBuiltin(jobs, map[string]string{"ask": "chat"}), "%+v", jobs)
	assert.Equal(t, string(rwAnthropic), jobs[0].SourceID)
	execs, err := adaptsqlite.NewNodeExecutionRepo(db).ListByInstance(ctx, id)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	require.NotNil(t, execs[0].RunnerID)
	assert.Equal(t, "builtin", *execs[0].RunnerID)
	assert.Equal(t, map[string]any{"response": "hello"}, execs[0].Output)
}
