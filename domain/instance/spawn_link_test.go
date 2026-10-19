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
		{SpawnModeGroupTask, true},
		{"", false},
		{"Group_Task", false},
		{"Wait_For_Child", false},
		{"async", false},
	}
	for _, c := range cases {
		if got := c.mode.Valid(); got != c.want {
			t.Errorf("SpawnMode(%q).Valid() = %v, want %v", c.mode, got, c.want)
		}
	}
}

// TestSpawnModeTotality guards against a fourth mode landing without the
// consumers (validation, executor short-circuit, wake match, group dispatch,
// SQLite link persistence) being extended. group_task is internal: it is
// never an author-visible spawn_workflow mode.
func TestSpawnModeTotality(t *testing.T) {
	all := []SpawnMode{SpawnModeFireAndForget, SpawnModeWaitForChild, SpawnModeGroupTask}
	if len(all) != 3 {
		t.Fatalf("spawn mode vocabulary changed to %d values: extend SpawnMode.Valid, "+
			"the executor short-circuit, the child-terminal wake match, the group "+
			"terminal dispatch and the SQLite link read/write", len(all))
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
		"nil parent": {ParentNodeID: "n", Mode: SpawnModeWaitForChild},
		"empty node": {ParentInstanceID: p, Mode: SpawnModeWaitForChild},
		"empty mode": {ParentInstanceID: p, ParentNodeID: "n"},
		"bogus mode": {ParentInstanceID: p, ParentNodeID: "n", Mode: "async"},
		"zero value": {},
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
