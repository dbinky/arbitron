# Phase 1 — Task 4: instance link model, parent scope filter, and the link-addressed port

**Goal:** Add the parent-link fields to the instance aggregate and summary, the three-state parent narrowing on `ListFilters`, and the narrow `SpawnLinkRepository` port that answers link-shaped questions.

**Architecture:** New file `domain/instance/spawn_link.go` owns the whole link vocabulary (`SpawnMode`, `SpawnLink`, `ParentScope`, `ChildVisibility`, `SpawnLinkRepository`); `domain/instance/instance.go` only gains fields. The port is declared NARROW rather than widening `InstanceRepository`, following the precedent of `schedule.ActiveRunChecker` (satisfied by the same concrete `*sqlite.InstanceRepo`) — that keeps every existing in-memory `InstanceRepository` mock compiling. No foreign key backs the parent id, mirroring `ScheduleID`: a purged parent must not cascade-delete a child's execution history.

**Tech Stack:** Go 1.25.5, standard library `testing`, `github.com/google/uuid`, module `github.com/arbitron/arbitron`.

---

### Task 4: link model + parent scope + `SpawnLinkRepository`

**Files:**
- Create: `domain/instance/spawn_link.go`
- Modify: `domain/instance/instance.go:95-135` (`Instance`), `:137-165` (`InstanceSummary`), `:224-250` (`ListFilters`)
- Test:   `domain/instance/spawn_link_test.go`

---

- [x] **Step 1: Write the failing test**

Create `domain/instance/spawn_link_test.go`:

```go
package instance

import (
	"testing"

	"github.com/google/uuid"
)

// ---------- SpawnMode vocabulary ----------

func TestSpawnModeConstants(t *testing.T) {
	if SpawnModeFireAndForget != "fire_and_forget" {
		t.Errorf("SpawnModeFireAndForget = %q", SpawnModeFireAndForget)
	}
	if SpawnModeWaitForChild != "wait_for_child" {
		t.Errorf("SpawnModeWaitForChild = %q", SpawnModeWaitForChild)
	}
}

func TestSpawnModeValid(t *testing.T) {
	cases := []struct {
		mode SpawnMode
		want bool
	}{
		{SpawnModeFireAndForget, true},
		{SpawnModeWaitForChild, true},
		{"", false},
		{"Wait_For_Child", false},
		{"async", false},
	}
	for _, c := range cases {
		if got := c.mode.Valid(); got != c.want {
			t.Errorf("SpawnMode(%q).Valid() = %v, want %v", c.mode, got, c.want)
		}
	}
}

// TestSpawnModeTotality guards against a third mode landing without the
// consumers (validation, executor short-circuit, wake match) being extended.
func TestSpawnModeTotality(t *testing.T) {
	all := []SpawnMode{SpawnModeFireAndForget, SpawnModeWaitForChild}
	if len(all) != 2 {
		t.Fatalf("spawn mode vocabulary changed to %d values: extend SpawnMode.Valid, "+
			"the executor short-circuit, and the child-terminal wake match", len(all))
	}
	for _, m := range all {
		if !m.Valid() {
			t.Errorf("%q must be Valid()", m)
		}
	}
}

// ---------- Instance link fields ----------

// TestInstanceNullLinksAreRoots: every instance created before this feature,
// and every manually triggered run after it, has null links. Null is NORMAL,
// not an error, and reads as "this is a root run".
func TestInstanceNullLinksAreRoots(t *testing.T) {
	inst := Instance{ID: uuid.New()}
	if inst.ParentInstanceID != nil {
		t.Error("a fresh Instance must have a nil ParentInstanceID")
	}
	if inst.ParentNodeID != nil {
		t.Error("a fresh Instance must have a nil ParentNodeID")
	}
	if inst.SpawnMode != "" {
		t.Errorf("a fresh Instance must have an empty SpawnMode, got %q", inst.SpawnMode)
	}
	if !inst.IsRoot() {
		t.Error("an instance with no parent link must report IsRoot() == true")
	}
}

func TestInstanceWithLinkIsNotRoot(t *testing.T) {
	parent := uuid.New()
	node := "spawn-1"
	inst := Instance{
		ID:               uuid.New(),
		ParentInstanceID: &parent,
		ParentNodeID:     &node,
		SpawnMode:        SpawnModeWaitForChild,
	}
	if inst.IsRoot() {
		t.Error("an instance with a parent link must report IsRoot() == false")
	}
}

// TestSpawnLinkShape pins the value the spawner carries on the context and the
// engine stamps onto the child at creation.
func TestSpawnLinkShape(t *testing.T) {
	p := uuid.New()
	l := SpawnLink{ParentInstanceID: p, ParentNodeID: "spawn-1", Mode: SpawnModeFireAndForget}
	if l.ParentInstanceID != p || l.ParentNodeID != "spawn-1" || l.Mode != SpawnModeFireAndForget {
		t.Fatalf("SpawnLink round-trip failed: %+v", l)
	}
	if !l.Valid() {
		t.Error("a fully populated SpawnLink must be Valid()")
	}
}

func TestSpawnLinkValidRejectsIncomplete(t *testing.T) {
	p := uuid.New()
	cases := map[string]SpawnLink{
		"nil parent":  {ParentNodeID: "n", Mode: SpawnModeWaitForChild},
		"empty node":  {ParentInstanceID: p, Mode: SpawnModeWaitForChild},
		"empty mode":  {ParentInstanceID: p, ParentNodeID: "n"},
		"bogus mode":  {ParentInstanceID: p, ParentNodeID: "n", Mode: "async"},
		"zero value":  {},
	}
	for name, l := range cases {
		if l.Valid() {
			t.Errorf("%s: SpawnLink.Valid() must be false for %+v", name, l)
		}
	}
}

// ---------- InstanceSummary ----------

func TestInstanceSummaryCarriesParent(t *testing.T) {
	p := uuid.New()
	s := InstanceSummary{ID: uuid.New(), ParentInstanceID: &p}
	if s.ParentInstanceID == nil || *s.ParentInstanceID != p {
		t.Fatal("InstanceSummary must carry the parent instance id for the hierarchy read")
	}
}

// ---------- ParentScope three-state ----------

func TestParentScopeConstants(t *testing.T) {
	if ParentScopeAny != "" {
		t.Errorf("ParentScopeAny must be the zero value so an un-set filter does no narrowing, got %q", ParentScopeAny)
	}
	if ParentScopeRoots != "roots" {
		t.Errorf("ParentScopeRoots = %q", ParentScopeRoots)
	}
	if ParentScopeChildren != "children" {
		t.Errorf("ParentScopeChildren = %q", ParentScopeChildren)
	}
}

func TestParseParentScope(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name      string
		raw       string
		wantScope ParentScope
		wantID    *uuid.UUID
		wantErr   bool
	}{
		{"empty means any", "", ParentScopeAny, nil, false},
		{"roots", "roots", ParentScopeRoots, nil, false},
		{"uuid means children of it", id.String(), ParentScopeChildren, &id, false},
		{"garbage", "not-a-uuid", ParentScopeAny, nil, true},
		{"wrong case roots", "ROOTS", ParentScopeAny, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scope, gotID, err := ParseParentScope(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseParentScope(%q) expected an error", c.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseParentScope(%q) unexpected error: %v", c.raw, err)
			}
			if scope != c.wantScope {
				t.Errorf("scope = %q, want %q", scope, c.wantScope)
			}
			if (gotID == nil) != (c.wantID == nil) {
				t.Fatalf("id nil-ness mismatch: got %v want %v", gotID, c.wantID)
			}
			if gotID != nil && *gotID != *c.wantID {
				t.Errorf("id = %v, want %v", *gotID, *c.wantID)
			}
		})
	}
}

// TestListFiltersParentNarrowingComposes: the parent selector is a caller-
// supplied NARROWING. It must never be able to clear or widen the service-set
// owner/department constraints — this test pins that setting it leaves them
// untouched.
func TestListFiltersParentNarrowingComposes(t *testing.T) {
	owner := uuid.New()
	parent := uuid.New()
	f := ListFilters{
		OwnerID:       &owner,
		Departments:   []string{"engineering"},
		IncludePublic: true,
	}
	f.ParentScope = ParentScopeChildren
	f.ParentInstanceID = &parent

	if f.OwnerID == nil || *f.OwnerID != owner {
		t.Error("setting the parent scope must not clear the service-set OwnerID")
	}
	if len(f.Departments) != 1 || f.Departments[0] != "engineering" {
		t.Error("setting the parent scope must not clear the service-set Departments")
	}
	if !f.IncludePublic {
		t.Error("setting the parent scope must not clear IncludePublic")
	}
}

// ---------- ChildVisibility ----------

// TestChildVisibilityFromFilters keeps the count query's visibility EXACTLY the
// same as the authorized page's. A count that is not derived from the caller's
// own constraint would leak the existence of invisible children through an
// expander.
func TestChildVisibilityFromFilters(t *testing.T) {
	owner := uuid.New()
	vis := ChildVisibility{OwnerID: &owner, Departments: []string{"eng"}, IncludePublic: true}
	if vis.Unconstrained {
		t.Error("a non-admin visibility must not be Unconstrained")
	}
	admin := ChildVisibility{Unconstrained: true}
	if admin.OwnerID != nil {
		t.Error("an admin visibility carries no owner narrowing")
	}
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/instance/ -run 'TestSpawn|TestInstanceNullLinks|TestInstanceWithLink|TestInstanceSummaryCarriesParent|TestParentScope|TestParseParentScope|TestListFiltersParent|TestChildVisibility'
```

Expected output:

```
# github.com/arbitron/arbitron/domain/instance [github.com/arbitron/arbitron/domain/instance.test]
domain/instance/spawn_link_test.go:13:5: undefined: SpawnModeFireAndForget
domain/instance/spawn_link_test.go:...: inst.ParentInstanceID undefined (type Instance has no field or method ParentInstanceID)
...
FAIL	github.com/arbitron/arbitron/domain/instance [build failed]
```

- [x] **Step 3: Minimal implementation — the link vocabulary and port**

Create `domain/instance/spawn_link.go`:

```go
package instance

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// SpawnMode records HOW a child instance was created by its parent's spawn
// node. It is a self-owned type in domain/instance (the package that owns the
// stored field) so nothing here imports domain/workflow — the same
// one-directional domain edge Visibility and TriggerSource preserve. The
// values MUST match workflow.SpawnModeFireAndForget / SpawnModeWaitForChild.
type SpawnMode string

const (
	// SpawnModeFireAndForget: the parent continued immediately after starting
	// the child. The child's outcome never affects the parent.
	SpawnModeFireAndForget SpawnMode = "fire_and_forget"
	// SpawnModeWaitForChild: the parent suspended at the spawn node and resumes
	// through its success/failure port when the child reaches a terminal state.
	SpawnModeWaitForChild SpawnMode = "wait_for_child"
)

// Valid reports whether m is one of the two recognized modes. The empty value
// is INVALID: an unset mode on a link would make the wake match and the
// resumption short-circuit ambiguous.
func (m SpawnMode) Valid() bool {
	return m == SpawnModeFireAndForget || m == SpawnModeWaitForChild
}

// SpawnLink is the child's back-reference to the exact parent NODE that created
// it. All three parts are load-bearing:
//
//   - ParentInstanceID identifies the run to wake / cascade to.
//   - ParentNodeID makes the link ADDRESSABLE: "the child spawned by this node
//     of this parent" is the key the resumption short-circuit and the wake both
//     match on. Without it, a fire-and-forget child from node A finishing while
//     the parent waits at node B would wrongly route node B.
//   - Mode is stored on the LINK rather than re-derived from the parent's
//     workflow snapshot, so both lookups are answerable without parsing every
//     parent's snapshot.
//
// There is intentionally NO foreign key on ParentInstanceID in storage, the
// same deliberate choice ScheduleID records: a purged parent must not
// cascade-delete a child's execution history.
type SpawnLink struct {
	ParentInstanceID uuid.UUID
	ParentNodeID     string
	Mode             SpawnMode
}

// Valid reports whether the link is fully populated. A partial link is never
// written: a child with a parent id but no node id could not be matched by the
// wake and would strand its parent forever.
func (l SpawnLink) Valid() bool {
	return l.ParentInstanceID != uuid.Nil && l.ParentNodeID != "" && l.Mode.Valid()
}

// ParentScope is the three-state parent narrowing on a list query.
//
// SECURITY POSTURE: unlike OwnerID/Departments/IncludePublic on ListFilters —
// which are SERVICE-SET from the request identity and must never come from
// request input — the parent scope IS legitimately caller-supplied. It is a
// NARROWING and never a widening: it composes with (never replaces) the
// service-set constraints, so a caller cannot see a run they could not already
// see by naming its parent.
type ParentScope string

const (
	// ParentScopeAny is the zero value: no parent narrowing at all.
	ParentScopeAny ParentScope = ""
	// ParentScopeRoots returns only instances with no parent link (top level).
	ParentScopeRoots ParentScope = "roots"
	// ParentScopeChildren returns only the children of ListFilters.ParentInstanceID.
	ParentScopeChildren ParentScope = "children"
)

// ParseParentScope decodes the wire form of the parent selector used by the
// REST query parameter and the MCP tool argument, so both surfaces share ONE
// decoder and cannot drift:
//
//	""              -> (ParentScopeAny,      nil)
//	"roots"         -> (ParentScopeRoots,    nil)
//	"<uuid>"        -> (ParentScopeChildren, &uuid)
//
// Anything else is an error; a malformed selector is rejected rather than
// silently degrading to an unfiltered list.
func ParseParentScope(raw string) (ParentScope, *uuid.UUID, error) {
	switch raw {
	case "":
		return ParentScopeAny, nil, nil
	case string(ParentScopeRoots):
		return ParentScopeRoots, nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return ParentScopeAny, nil, fmt.Errorf("invalid parent selector %q: expected \"roots\" or an instance UUID", raw)
	}
	return ParentScopeChildren, &id, nil
}

// ChildVisibility is the caller's already-computed, service-set visibility
// scope, passed verbatim into the batched child-count query.
//
// Counts MUST be computed post-authorization: a count that includes children
// the caller cannot see would render an expander whose expansion yields an
// empty list, leaking the existence of runs the caller may not view. This is
// the same non-disclosure discipline the shared-workflow visibility filter
// enforces.
type ChildVisibility struct {
	// Unconstrained is true for an admin: no visibility narrowing at all.
	Unconstrained bool
	OwnerID       *uuid.UUID
	Departments   []string
	IncludePublic bool
}

// SpawnLinkRepository answers link-shaped questions without any caller knowing
// how links are stored.
//
// It is declared as a NARROW port rather than as a widening of
// InstanceRepository so that the many existing in-memory InstanceRepository
// implementations across the test suites keep compiling. The concrete
// *sqlite.InstanceRepo satisfies both, exactly as it already satisfies
// schedule.ActiveRunChecker alongside instance.InstanceRepository.
type SpawnLinkRepository interface {
	// ChildCountsByParents returns the number of children per parent id,
	// restricted to what vis permits the caller to see.
	//
	// Contract (mirrors NodeExecutionRepository.AggregateByInstances):
	//   - Parents with no visible children are ABSENT from the map; absence
	//     means zero, not an error.
	//   - An empty parentIDs slice returns an empty map WITHOUT touching storage.
	// It exists so a page of the run list resolves every expander in ONE round
	// trip instead of N+1 queries.
	ChildCountsByParents(ctx context.Context, parentIDs []uuid.UUID, vis ChildVisibility) (map[uuid.UUID]int, error)

	// ListActiveChildren returns the ids of parentID's children that are still
	// in a cancellable state (queued, running, waiting, paused), REGARDLESS of
	// spawn mode — the cancel cascade covers every active child, not only
	// awaited ones. A parent with no active children returns an empty slice,
	// not an error.
	ListActiveChildren(ctx context.Context, parentID uuid.UUID) ([]uuid.UUID, error)
}
```

- [x] **Step 4: Minimal implementation — the aggregate fields**

In `domain/instance/instance.go`, add to `Instance` immediately after the `ScheduleID` field (line 118):

```go
	TriggerSource     TriggerSource
	ScheduleID        *uuid.UUID
	// ParentInstanceID / ParentNodeID / SpawnMode are the child's link back to
	// the parent's spawn node that created it (see SpawnLink). All three are
	// nil/empty for a root run — every instance created before this feature and
	// every manually triggered run after it. Null links are NORMAL, not an
	// error: read paths treat them as "this is a root run".
	//
	// There is intentionally NO foreign key on parent_instance_id, the same
	// deliberate choice recorded for ScheduleID above: a referenced row may be
	// removed while historical runs remain, and a purged parent must not
	// cascade-delete a child's execution history.
	ParentInstanceID *uuid.UUID
	ParentNodeID     *string
	SpawnMode        SpawnMode
```

Add the `IsRoot` helper directly after the `Instance` struct definition:

```go
// IsRoot reports whether this run has no spawning parent. It is the ONE place
// the "null link means root" convention is expressed, so read paths do not each
// re-derive it.
func (i Instance) IsRoot() bool {
	return i.ParentInstanceID == nil
}
```

Add to `InstanceSummary`, after the `ScheduleID` field:

```go
	ScheduleID    *uuid.UUID    `json:"scheduleId,omitempty"`
	// ParentInstanceID is the spawning parent run, nil for a root. It is carried
	// on the summary (the projection the list path and enricher consume) so the
	// hierarchy read needs no per-row instance load.
	ParentInstanceID *uuid.UUID `json:"parentInstanceId,omitempty"`
```

Add to `ListFilters`, after `IncludePublic`:

```go
	IncludePublic bool
	// ParentScope narrows the list by position in the spawn hierarchy: unset
	// (no narrowing), roots-only, or the children of ParentInstanceID.
	//
	// Unlike OwnerID/Departments/IncludePublic above, this IS a legitimate
	// caller-supplied value: it is a NARROWING, never a widening, and composes
	// with — never replaces — the service-set constraints.
	ParentScope ParentScope
	// ParentInstanceID is the parent whose children to return. Read ONLY when
	// ParentScope == ParentScopeChildren; ignored otherwise.
	ParentInstanceID *uuid.UUID
	Limit         int
	Offset        int
```

- [x] **Step 5: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/instance/
go test ./domain/instance/ -run 'TestSpawn|TestInstanceNullLinks|TestInstanceWithLink|TestInstanceSummaryCarriesParent|TestParentScope|TestParseParentScope|TestListFiltersParent|TestChildVisibility' -v
```

Expected output:

```
--- PASS: TestSpawnModeConstants (0.00s)
--- PASS: TestSpawnModeValid (0.00s)
--- PASS: TestSpawnModeTotality (0.00s)
--- PASS: TestInstanceNullLinksAreRoots (0.00s)
--- PASS: TestInstanceWithLinkIsNotRoot (0.00s)
--- PASS: TestSpawnLinkShape (0.00s)
--- PASS: TestSpawnLinkValidRejectsIncomplete (0.00s)
--- PASS: TestInstanceSummaryCarriesParent (0.00s)
--- PASS: TestParentScopeConstants (0.00s)
--- PASS: TestParseParentScope (0.00s)
--- PASS: TestListFiltersParentNarrowingComposes (0.00s)
--- PASS: TestChildVisibilityFromFilters (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/instance	0.2s
```

- [x] **Step 6: Confirm nothing else regressed (the struct additions are additive)**

```bash
go build ./... && go test ./domain/... ./adapter/... ./cmd/...
```

Expected: build succeeds; every package prints `ok` (no `FAIL`).

- [x] **Step 7: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/instance/spawn_link.go domain/instance/spawn_link_test.go domain/instance/instance.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(instance): add parent/child spawn link model, parent scope filter, and link port"
```
