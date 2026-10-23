package modelcatalog

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoordinatorDoSharesRefreshAdmissionAndClose(t *testing.T) {
	started := make(chan struct{})
	c := NewRefreshCoordinator(func(context.Context) (RefreshReport, error) { return RefreshReport{}, nil })
	done := make(chan error, 1)
	go func() {
		done <- c.Do(context.Background(), func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() })
	}()
	<-started
	_, err := c.Refresh(context.Background())
	require.ErrorIs(t, err, ErrRefreshBusy)
	require.ErrorIs(t, c.Do(context.Background(), func(context.Context) error { t.Error("overlap executed"); return nil }), ErrRefreshBusy)
	c.Close()
	require.ErrorIs(t, <-done, context.Canceled)
	require.ErrorIs(t, c.Do(context.Background(), func(context.Context) error { return nil }), ErrRefreshClosed)
}
