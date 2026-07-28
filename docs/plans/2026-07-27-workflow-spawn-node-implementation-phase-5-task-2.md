# Phase 5 — Task 2: batched, post-authorization child counts on enriched rows

**Goal:** Give every enriched list row a `ChildCount`, resolved for the whole page in **one** batched call and restricted to children the caller may actually see.

**Architecture:** `InstanceEnricher` gains an optional `SpawnLinkRepository`, wired at the composition root. Counts flow through the enricher because that is already the place list rows are decorated with derived presentation data, and the enricher already batches (`AggregateByInstances`, `UsersByID`) for exactly this reason — the queue list must not issue N+1 queries per page. The caller's visibility is taken from the **same** `listConstraint(ctx)` `ListEnriched` used to authorize the page, so a count can never include a child the caller cannot see: otherwise an expander appears for a run whose children are invisible, and expanding it yields an empty list — leaking existence. A count failure degrades only that field; the page still renders.

**Tech Stack:** Go 1.25.5, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 2: child counts through the enricher

**Files:**
- Modify: `domain/instance/instance.go` (`EnrichedInstanceSummary.ChildCount`)
- Modify: `domain/engine/enricher.go:39-79` (field + setter), `:84-157` (`Enrich`)
- Modify: `cmd/arbitron/main.go` (wire the repo onto the enricher)
- Test:   `domain/engine/enricher_child_count_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `domain/engine/enricher_child_count_test.go`:

```go
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

func adminCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleAdmin,
	})
}

// ---------- happy path ----------

func TestEnricherChildCount_StampsCountsOnRows(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	links := &countingSpawnLinks{counts: map[uuid.UUID]int{a: 3, b: 1}}
	en := newCountingEnricher(t, links)

	rows, err := en.Enrich(adminCtx(), summariesFor(a, b, c))
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

	_, err := en.Enrich(adminCtx(), summariesFor(ids...))
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

	_, err := en.Enrich(adminCtx(), summariesFor(uuid.New()))
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

	rows, err := en.Enrich(adminCtx(), summariesFor(uuid.New(), uuid.New()))
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
	rows, err := en.Enrich(adminCtx(), summariesFor(uuid.New()))
	require.NoError(t, err)
	assert.Equal(t, 0, rows[0].ChildCount)
}

func TestEnricherChildCount_EmptyPageTouchesNoRepository(t *testing.T) {
	links := &countingSpawnLinks{counts: map[uuid.UUID]int{}}
	en := newCountingEnricher(t, links)

	rows, err := en.Enrich(adminCtx(), nil)
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
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run TestEnricherChildCount
```

Expected output:

```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/enricher_child_count_test.go:...: en.SetSpawnLinks undefined
domain/engine/enricher_child_count_test.go:...: rows[0].ChildCount undefined
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [ ] **Step 3: Add the field to the enriched summary**

In `domain/instance/instance.go`, add to `EnrichedInstanceSummary`:

```go
type EnrichedInstanceSummary struct {
	InstanceSummary
	RunName       string
	Submitter     Submitter
	FriendlyStep  string
	Model         string
	Backend       string
	RetryCount    int
	QueuePosition int
	FailedNodeID  *string
	// ChildCount is the number of spawned children the CALLER may see. It drives
	// the hierarchy expander: a row with 0 renders none. It is computed
	// post-authorization, so a run whose children are all invisible to the caller
	// reports 0 and leaks no existence.
	ChildCount int
}
```

- [ ] **Step 4: Add the enricher field, setter, and batched call**

In `domain/engine/enricher.go`, add the field to `InstanceEnricher`:

```go
	queuePos           QueuePositionProvider // nil -> no position
	// spawnLinks is the OPTIONAL link-addressed read surface. nil -> every row
	// reports ChildCount 0 and renders no expander.
	spawnLinks         instance.SpawnLinkRepository
	defaultAgentBackend string
```

Add the setter after `SetQueuePositionProvider`:

```go
// SetSpawnLinks wires the link-addressed read surface used for batched child
// counts. Kept as a setter (like SetQueuePositionProvider) so the composition
// root can construct the enricher before deciding whether the capability is
// available. nil is valid: rows then report no children.
func (e *InstanceEnricher) SetSpawnLinks(repo instance.SpawnLinkRepository) {
	e.spawnLinks = repo
}

// batchChildCounts resolves the whole page's child counts in ONE round trip,
// restricted to what the CALLER may see.
//
// The visibility is taken from the SAME listConstraint that authorized the page,
// so the count is post-authorization by construction. A count that included
// children the caller cannot see would render an expander whose expansion yields
// an empty list — leaking the existence of runs the caller may not view.
//
// A failure degrades only this field (an empty map => no expanders); the page
// still renders. With no identity on the context there is no authorized scope,
// so counting is skipped entirely rather than defaulting to unconstrained.
func (e *InstanceEnricher) batchChildCounts(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]int {
	if e.spawnLinks == nil || len(ids) == 0 {
		return map[uuid.UUID]int{}
	}
	lc, ok := listConstraint(ctx)
	if !ok {
		return map[uuid.UUID]int{}
	}
	counts, err := e.spawnLinks.ChildCountsByParents(ctx, ids, instance.ChildVisibility{
		Unconstrained: lc.Unconstrained,
		OwnerID:       lc.OwnerID,
		Departments:   lc.Departments,
		IncludePublic: lc.IncludePublic,
	})
	if err != nil {
		slog.Warn("enricher: child-count lookup failed; hierarchy expanders degraded", "error", err)
		return map[uuid.UUID]int{}
	}
	return counts
}
```

In `Enrich`, add the batched call beside the other batched lookups (after the
queue-position block, before the row loop):

```go
	// One batched child-count call for the whole page (never N+1).
	childCounts := e.batchChildCounts(ctx, ids)
```

and inside the row loop, after the queue-position stamp:

```go
		// Queue position (queued rows only; 0 for non-queued).
		if pos, ok := posMap[s.ID]; ok {
			row.QueuePosition = pos
		}

		// Spawned-children count (0 => no expander).
		if n, ok := childCounts[s.ID]; ok {
			row.ChildCount = n
		}
```

- [ ] **Step 5: Wire it at the composition root**

In `cmd/arbitron/main.go`, immediately after
`instanceEnricher.SetQueuePositionProvider(queueMgr)` (~line 793), add:

```go
	// 12-count. Wire the link-addressed read surface as the enricher's child-count
	// source, so a page of the run list resolves every hierarchy expander in ONE
	// batched, post-authorization query.
	instanceEnricher.SetSpawnLinks(instanceRepo)
```

- [ ] **Step 6: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/ domain/instance/ cmd/arbitron/
go build ./... && go test ./domain/engine/ -run TestEnricherChildCount -v
```

Expected output:

```
--- PASS: TestEnricherChildCount_StampsCountsOnRows (0.00s)
--- PASS: TestEnricherChildCount_OneBatchedCallPerPage (0.00s)
--- PASS: TestEnricherChildCount_PassesTheCallersServiceSetVisibility (0.00s)
--- PASS: TestEnricherChildCount_AdminVisibilityIsUnconstrained (0.00s)
--- PASS: TestEnricherChildCount_InvisibleChildrenReportZero (0.00s)
--- PASS: TestEnricherChildCount_QueryFailureDegradesTheRowNotThePage (0.00s)
--- PASS: TestEnricherChildCount_NoRepositoryWiredReportsZero (0.00s)
--- PASS: TestEnricherChildCount_EmptyPageTouchesNoRepository (0.00s)
--- PASS: TestEnricherChildCount_NoIdentitySkipsCounting (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.7s
```

- [ ] **Step 7: Full Go gate**

```bash
go test ./...
```

Expected: `ok` for every package, no `FAIL` lines.

- [ ] **Step 8: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/instance/instance.go domain/engine/enricher.go domain/engine/enricher_child_count_test.go cmd/arbitron/main.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): batched post-authorization child counts on enriched list rows"
```
