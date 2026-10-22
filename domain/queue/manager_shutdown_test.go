package queue_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/stretchr/testify/require"
)

type shutdownQueue struct {
	instance.QueueRepository
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (q *shutdownQueue) NextEligible(context.Context, int, int) (*instance.Instance, error) {
	q.calls.Add(1)
	q.entered <- struct{}{}
	<-q.release
	return nil, nil
}
func TestManagerStopJoinsCapacityNotifications(t *testing.T) {
	repo := &shutdownQueue{entered: make(chan struct{}, 2), release: make(chan struct{})}
	mgr := queue.NewManager(repo, nil, &config.Config{}, nil)
	mgr.NotifyCapacityFreed(context.Background())
	select {
	case <-repo.entered:
	case <-time.After(time.Second):
		t.Fatal("no evaluation")
	}
	stopped := make(chan struct{})
	go func() { mgr.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned before queue I/O completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not join")
	}
	mgr.NotifyCapacityFreed(context.Background())
	mgr.Stop()
	require.EqualValues(t, 1, repo.calls.Load(), "no DB use may be scheduled after Stop")
}
