package modelcatalog

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type budgetFetch struct {
	deadline  time.Time
	immediate bool
}

func (f *budgetFetch) Fetch(ctx context.Context, _ string) ([]byte, error) {
	f.deadline, _ = ctx.Deadline()
	if f.deadline.IsZero() {
		return nil, context.DeadlineExceeded
	}
	if f.immediate {
		return nil, context.DeadlineExceeded
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRefreshCollectionReservesCommitTimeFromCallerDeadline(t *testing.T) {
	f := newCandidateFixture(t)
	fetch := &budgetFetch{immediate: true}
	f.svc.d.Pipeline.d.Fetcher = fetch
	ctx, cancel := context.WithTimeout(context.Background(), CommitReserve+time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_, err := f.svc.d.Pipeline.Collect(ctx, nil)
	require.NoError(t, err)
	assert.False(t, fetch.deadline.After(deadline.Add(-CommitReserve)), "collection must reserve time even when prior recovery consumed the cycle")
}

func TestRefreshBudgetIncludesFetchAndLeavesCommitContextLive(t *testing.T) {
	fetch := &budgetFetch{}
	pipeline, err := NewDiscoveryPipeline(DiscoveryDeps{
		Settings: faultSettings{}, Fetcher: fetch, Normalizer: struct{ CatalogNormalizer }{}, Mapper: struct{ EntryMapper }{}, Inventory: struct{ PathInventory }{}, Profiles: struct{ ProfileLister }{}, Store: faultStore{}, Probe: &stubProbe{}, AdvanceValidator: allowAdvance{}, Budget: time.Millisecond,
	})
	require.NoError(t, err)
	ctx := context.Background()
	commit, err := pipeline.Collect(ctx, nil)
	require.NoError(t, err)
	assert.False(t, fetch.deadline.IsZero(), "fetch must share collection's budget")
	assert.Equal(t, "budget_exhausted", commit.Report.PathStatus["catalog"])
	assert.NoError(t, ctx.Err(), "completed evidence can still be committed")
	assert.Equal(t, 3*time.Hour, DefaultCycleBudget)
}
