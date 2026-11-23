package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
)

func TestRunnerWiring_InventoryRefreshNotifiesAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifier := runner.NewPoolNotifier()
	wake, stop := notifier.Subscribe()
	defer stop()
	calls := make(chan struct{}, 1)
	stack := &runnerStack{inventoryInterval: time.Millisecond, notifier: notifier,
		refreshInventory: func(ctx context.Context) error {
			select {
			case calls <- struct{}{}:
			default:
			}
			return ctx.Err()
		}}
	var wg sync.WaitGroup
	stack.startInventory(ctx, &wg)
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("successful inventory did not wake placement")
	}
	select {
	case <-calls:
	default:
		t.Fatal("inventory producer was not called")
	}
	cancel()
	joined := make(chan struct{})
	go func() { wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("inventory did not join")
	}
}
