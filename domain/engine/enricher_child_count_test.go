package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingSpawnLinks records every ChildCountsByParents call so the batching
// contract can be asserted, and HONOURS the visibility argument — a fake that
// ignored it would make the non-disclosure test unfailable.
type countingSpawnLinks struct {
	counts    map[uuid.UUID]int
	visible   func(instance.ChildVisibility) bool
	err       error
	callCount int
	gotIDs    [][]uuid.UUID
	gotVis    []instance.ChildVisibility
}

func (c *countingSpawnLinks) ChildCountsByParents(_ context.Context, ids []uuid.UUID, vis instance.ChildVisibility) (map[uuid.UUID]int, error) {
	c.callCount++
	c.gotIDs = append(c.gotIDs, ids)
	c.gotVis = append(c.gotVis, vis)
	if c.err != nil {
		return nil, c.err
	}
	if c.visible != nil && !c.visible(vis) {
		return map[uuid.UUID]int{}, nil
	}
	out := map[uuid.UUID]int{}
	for _, id := range ids {
		if n, ok := c.counts[id]; ok && n > 0 {
			out[id] = n
		}
	}
	return out, nil
}

func (c *countingSpawnLinks) ListActiveChildren(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

var _ instance.SpawnLinkRepository = (*countingSpawnLinks)(nil)

func summariesFor(ids ...uuid.UUID) []instance.InstanceSummary {
	out := make([]instance.InstanceSummary, 0, len(ids))
	for _, id := range ids {
		out = append(out, instance.InstanceSummary{
			ID: id, OwnerID: uuid.New(), WorkflowName: "wf", State: instance.StateCompleted,
		})
	}
	return out
}

func newCountingEnricher(t *testing.T, links *countingSpawnLinks) *engine.InstanceEnricher {
	t.Helper()
	en := engine.NewInstanceEnricher(
		newMockInstanceRepo(), newMockNodeExecutionRepo(), newMockUserRepo(),
		engine.NoopQueuePositionProvider{}, "",
	)
	en.SetSpawnLinks(links)
	return en
}

// ---------- happy path ----------

func TestEnricherChildCount_StampsCountsOnRows(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	links := &countingSpawnLinks{counts: map[uuid.UUID]int{a: 3, b: 1}}
	en := newCountingEnricher(t, links)

	rows, err := en.Enrich(adminCtx(uuid.New()), summariesFor(a, b, c))
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, 3, rows[0].ChildCount)
	assert.Equal(t, 1, rows[1].ChildCount)
	assert.Equal(t, 0, rows[2].ChildCount, "a run with no children reports zero (no expander)")
}

// ---------- success: batching ----------

// The count query must resolve a whole page's expander state in ONE round trip,
// mirroring AggregateByInstances and UsersByID.
func TestEnricherChildCount_OneBatchedCallPerPage(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	links := &countingSpawnLinks{counts: map[uuid.UUID]int{ids[0]: 1}}
	en := newCountingEnricher(t, links)

	_, err := en.Enrich(adminCtx(uuid.New()), summariesFor(ids...))
	require.NoError(t, err)

	assert.Equal(t, 1, links.callCount, "child counts must be batched, never one call per row")
	require.Len(t, links.gotIDs, 1)
	assert.Len(t, links.gotIDs[0], 5, "every row on the page must be in the single batch")
}

// ---------- success: post-authorization visibility ----------

// The visibility passed to the count query must be the SAME service-set scope
// that authorized the page.
func TestEnricherChildCount_PassesTheCallersServiceSetVisibility(t *testing.T) {
	uid := uuid.New()
	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uid, Role: "user", Departments: []string{"engineering"},
	})
	links := &countingSpawnLinks{counts: map[uuid.UUID]int{}}
	en := newCountingEnricher(t, links)

	_, err := en.Enrich(ctx, summariesFor(uuid.New()))
	require.NoError(t, err)

	require.Len(t, links.gotVis, 1)
	vis := links.gotVis[0]
	assert.False(t, vis.Unconstrained)
	require.NotNil(t, vis.OwnerID)
	assert.Equal(t, uid, *vis.OwnerID)
	assert.Equal(t, []string{"engineering"}, vis.Departments)
	assert.True(t, vis.IncludePublic)
}

func TestEnricherChildCount_AdminVisibilityIsUnconstrained(t *testing.T) {
	links := &countingSpawnLinks{counts: map[uuid.UUID]int{}}
	en := newCountingEnricher(t, links)

	_, err := en.Enrich(adminCtx(uuid.New()), summariesFor(uuid.New()))
	require.NoError(t, err)

	require.Len(t, links.gotVis, 1)
	assert.True(t, links.gotVis[0].Unconstrained)
	assert.Nil(t, links.gotVis[0].OwnerID)
}

// A parent whose children are ALL invisible to the caller reports a zero count
// and therefore renders no expander, so no existence is leaked.
func TestEnricherChildCount_InvisibleChildrenReportZero(t *testing.T) {
	parent := uuid.New()
	links := &countingSpawnLinks{
		counts:  map[uuid.UUID]int{parent: 4},
		visible: func(v instance.ChildVisibility) bool { return v.Unconstrained },
	}
	en := newCountingEnricher(t, links)

	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: "user", Departments: []string{"other"},
	})
	rows, err := en.Enrich(ctx, summariesFor(parent))
	require.NoError(t, err)
	assert.Equal(t, 0, rows[0].ChildCount,
		"a count must reflect children the CALLER may see, never all children that exist")
}

// ---------- error / degradation ----------

// A child-count query failure degrades the ROW (no expander) rather than
// failing the whole page: a missing expander is a degraded but usable list; a
// failed page is not.
func TestEnricherChildCount_QueryFailureDegradesTheRowNotThePage(t *testing.T) {
	links := &countingSpawnLinks{err: errors.New("db down")}
	en := newCountingEnricher(t, links)

	rows, err := en.Enrich(adminCtx(uuid.New()), summariesFor(uuid.New(), uuid.New()))
	require.NoError(t, err, "a count failure must never fail the list")
	require.Len(t, rows, 2)
	assert.Equal(t, 0, rows[0].ChildCount)
	assert.Equal(t, 0, rows[1].ChildCount)
	assert.NotEmpty(t, rows[0].RunName, "every other enriched field must still be composed")
}

// ---------- edge cases ----------

func TestEnricherChildCount_NoRepositoryWiredReportsZero(t *testing.T) {
	en := engine.NewInstanceEnricher(
		newMockInstanceRepo(), newMockNodeExecutionRepo(), newMockUserRepo(),
		engine.NoopQueuePositionProvider{}, "",
	)
	rows, err := en.Enrich(adminCtx(uuid.New()), summariesFor(uuid.New()))
	require.NoError(t, err)
	assert.Equal(t, 0, rows[0].ChildCount)
}

func TestEnricherChildCount_EmptyPageTouchesNoRepository(t *testing.T) {
	links := &countingSpawnLinks{counts: map[uuid.UUID]int{}}
	en := newCountingEnricher(t, links)

	rows, err := en.Enrich(adminCtx(uuid.New()), nil)
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.Equal(t, 0, links.callCount)
}

// With no identity on the context there is no authorized scope, so counting is
// skipped entirely rather than defaulting to unconstrained.
func TestEnricherChildCount_NoIdentitySkipsCounting(t *testing.T) {
	links := &countingSpawnLinks{counts: map[uuid.UUID]int{}}
	en := newCountingEnricher(t, links)

	rows, err := en.Enrich(context.Background(), summariesFor(uuid.New()))
	require.NoError(t, err)
	assert.Equal(t, 0, rows[0].ChildCount)
	assert.Equal(t, 0, links.callCount,
		"without an authorized scope the count must be skipped, never run unconstrained")
}
