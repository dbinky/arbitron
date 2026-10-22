package layout_test

import (
	"context"
	"errors"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestMemStoreContract(t *testing.T) {
	layouttest.RunStoreContract(t, func(t *testing.T) layout.Store { return layouttest.NewMemStore() })
}
func TestMemStoreFailure(t *testing.T) {
	m := layouttest.NewMemStore()
	m.SetFailure(errors.New("offline"))
	ctx := context.Background()
	now := time.Now()
	f := layout.Fence{}
	_, _, err := m.Request(ctx, layout.NewRequest{})
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = m.Get(ctx, "")
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = m.ListActive(ctx, layout.Destination{})
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = m.Claimable(ctx, now, 0)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = m.Claim(ctx, "", "", now, now)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	assert.ErrorIs(t, m.Progress(ctx, f, "", 0, now, now), layout.ErrStoreUnavailable)
	assert.ErrorIs(t, m.Complete(ctx, f, layout.Completion{}, now), layout.ErrStoreUnavailable)
	assert.ErrorIs(t, m.Checkpoint(ctx, f, layout.StoredResult{}, false, now), layout.ErrStoreUnavailable)
	assert.ErrorIs(t, m.Supersede(ctx, "", now), layout.ErrStoreUnavailable)
	_, err = m.Retry(ctx, "", 1, now)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = m.Relocate(ctx, layout.Destination{}, layout.Destination{}, now)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = m.Result(ctx, "", 0)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	_, err = m.PendingCache(ctx, 0)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	assert.ErrorIs(t, m.MarkCached(ctx, f, 0, layout.Destination{}), layout.ErrStoreUnavailable)
	assert.ErrorIs(t, m.InvalidateResult(ctx, f, 0, "", now), layout.ErrStoreUnavailable)
	m.SetFailure(nil)
	_, err = m.Get(ctx, "")
	assert.ErrorIs(t, err, layout.ErrRecordNotFound)
}
