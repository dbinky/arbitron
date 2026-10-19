# Parallel Workflow Gathering — Phase 2 Task 6: Deep copy + typed recursive input resolver

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Give the group launcher a deep-copy primitive and a type-preserving, recursive, missing-reference-rejecting template resolver in `domain/variable`.
**Architecture:** Two pure files in `domain/variable`: `deepcopy.go` (`DeepCopy`/`DeepCopyMap`, fast path for JSON shapes, reflection fallback for other map/slice types) and `typed_resolve.go` (`ResolveTyped`/`ResolveTypedMap`: a whole-value `{{path}}` returns a deep copy of the typed value read via `VariableStore.Get`; other strings are checked for missing references and then interpolated with the existing `Resolve`; maps/arrays recurse). No new dependencies; no existing behaviour changes.
**Tech Stack:** Go 1.25.5, standard library (`reflect`, `sort`, `strconv`, `strings`), testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` (Parallel groups: "Each task starts with its own copy of the group's starting variables"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §2 "Task launcher and starting copies" (deep copies + typed recursive input resolver), Data Provenance row "Deep copies, typed recursive resolved map" (PLANNED DEPENDENCY P2-B, resolved here for the variable half).

**Interfaces**
- Consumes (existing code only):
  - `domain/variable/variable.go:48` `func (s *VariableStore) Get(dotPath string) (any, bool)` — typed path read (inputs first, then node outputs; integer segments index `[]any`).
  - `domain/variable/variable.go:38` `func NewStore() *VariableStore`.
  - `domain/variable/substitution.go:13` `func Resolve(input string, store *VariableStore) string` — string interpolation; leaves unresolved markers in place (so this task checks references BEFORE calling it).
  - `domain/variable/substitution.go:68` `func formatValue(val any) string` (unexported, same package) — renders a non-string value the way `Resolve` does.
- Produces (exact signatures):
  - `func DeepCopy(v any) any`
  - `func DeepCopyMap(m map[string]any) map[string]any`
  - `var ErrUnresolvedReference = errors.New("unresolved variable reference")`
  - `func ResolveTyped(v any, store *VariableStore) (any, error)`
  - `func ResolveTypedMap(m map[string]any, store *VariableStore) (map[string]any, error)`
  - Semantics later tasks rely on: `DeepCopyMap(nil) == nil`; `ResolveTypedMap(nil, s) == (nil, nil)`; the returned value never shares a map/slice with the input or the store; a missing reference anywhere fails the whole call with `errors.Is(err, ErrUnresolvedReference)` and a message containing `{{<path>}}` and, when nested, `(at "<key.path>")`; a nil store means every reference is missing.

**Wiring & data provenance**
- Composition root: none of its own — pure library functions. They become live through `GroupLauncher` (P2-T8), which is wired into `cmd/arbitron/main.go` by P2-T14.
- Producers:
  - `store` (the group-start variables): reconstructed by `domain/engine/engine.go:Engine.run` (`variable.FromSnapshotWithNodeIDs(inst.Variables, nodeIDs)`, engine.go:868) and handed to the parallel_group executor (P2-T11) → `GroupLaunchRequest.Vars` → `GroupLauncher.Launch` (P2-T8), which builds the task-local store (deep copy of the start snapshot + `SetReserved("workspace", <task dir>)`).
  - `v` / `m` (task input templates): `workflow.GroupTask.Inputs` parsed by `workflow.ParseGroupConfig` (P1-T1) from the parent's `Instance.WorkflowSnapshot`.
  - `DeepCopy` inputs: `VariableStore.Snapshot()` (variable.go:127) in the launcher (P2-T8), the parent's `WorkflowSnapshot` in `Engine.StartGroupTask` (P2-T7).
- Provenance probe: `TestResolveTyped_NilStoreTreatsEveryReferenceAsMissing` fails if a caller's store input is absent (nil) and the resolver silently succeeds.

### Task 6: Deep copy + typed recursive input resolver
**Files:**
- Create: `domain/variable/deepcopy.go`
- Create: `domain/variable/typed_resolve.go`
- Test:   `domain/variable/deepcopy_test.go`
- Test:   `domain/variable/typed_resolve_test.go`

(No existing file is modified. Test files use `package variable` like the neighbouring `variable_test.go` / `substitution_test.go`, with testify `assert`/`require`.)

- [x] Step 1: Write the failing deep-copy tests. Create `domain/variable/deepcopy_test.go`:

```go
package variable

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleNested builds a fresh value covering every container shape a group
// start snapshot can hold, nested at least three levels deep.
func sampleNested() map[string]any {
	return map[string]any{
		"name":  "alice",
		"count": float64(3),
		"ok":    true,
		"none":  nil,
		"cfg": map[string]any{
			"level1": map[string]any{
				"level2": map[string]any{"leaf": "v"},
				"list":   []any{map[string]any{"deep": []any{"x"}}},
			},
		},
		"rows":   []map[string]any{{"id": float64(1), "tags": []string{"a"}}},
		"labels": []string{"p", "q"},
		"env":    map[string]string{"K": "V"},
		"grid":   [][]any{{"r0c0"}},
	}
}

func TestDeepCopyMap_EqualButIndependentAtDepthThree(t *testing.T) {
	orig := sampleNested()
	cp := DeepCopyMap(orig)
	require.Equal(t, orig, cp)

	level1 := cp["cfg"].(map[string]any)["level1"].(map[string]any)
	level1["level2"].(map[string]any)["leaf"] = "mutated"
	level1["list"].([]any)[0].(map[string]any)["deep"].([]any)[0] = "mutated"
	level1["added"] = "new"
	cp["rows"].([]map[string]any)[0]["tags"].([]string)[0] = "mutated"
	cp["rows"].([]map[string]any)[0]["id"] = float64(99)
	cp["labels"].([]string)[0] = "mutated"
	cp["env"].(map[string]string)["K"] = "mutated"
	cp["grid"].([][]any)[0][0] = "mutated"
	cp["name"] = "bob"

	assert.Equal(t, sampleNested(), orig, "mutating the copy at any depth must never reach the original")
}

func TestDeepCopyMap_MutatingOriginalDoesNotReachCopy(t *testing.T) {
	orig := sampleNested()
	cp := DeepCopyMap(orig)

	orig["cfg"].(map[string]any)["level1"].(map[string]any)["level2"].(map[string]any)["leaf"] = "mutated"
	orig["rows"].([]map[string]any)[0]["tags"].([]string)[0] = "mutated"
	delete(orig, "name")

	assert.Equal(t, sampleNested(), cp)
}

func TestDeepCopyMap_TwoCopiesAreIndependentOfEachOther(t *testing.T) {
	base := sampleNested()
	a := DeepCopyMap(base)
	b := DeepCopyMap(base)

	a["cfg"].(map[string]any)["level1"].(map[string]any)["level2"].(map[string]any)["leaf"] = "task-a"

	leaf := b["cfg"].(map[string]any)["level1"].(map[string]any)["level2"].(map[string]any)["leaf"]
	assert.Equal(t, "v", leaf, "one task's copy must not see another task's write")
}

func TestDeepCopy_SliceBackingArraysAreDistinct(t *testing.T) {
	in := map[string]any{"list": []any{"a", "b"}}
	out := DeepCopyMap(in)

	// Appending into the copy's spare capacity would overwrite a shared
	// backing array in place.
	trimmed := out["list"].([]any)[:1]
	out["list"] = append(trimmed, "c")

	assert.Equal(t, []any{"a", "b"}, in["list"])
}

func TestDeepCopy_ScalarsReturnedByValue(t *testing.T) {
	for _, v := range []any{"s", float64(1.5), 7, int64(8), true, false, nil} {
		assert.Equal(t, v, DeepCopy(v))
	}
}

func TestDeepCopy_NilContainersStayNilWithTheirType(t *testing.T) {
	assert.Nil(t, DeepCopyMap(nil))

	var nm map[string]any
	gm, ok := DeepCopy(nm).(map[string]any)
	require.True(t, ok, "a typed nil map keeps its type")
	assert.Nil(t, gm)

	var ns []any
	gs, ok := DeepCopy(ns).([]any)
	require.True(t, ok, "a typed nil slice keeps its type")
	assert.Nil(t, gs)

	var nstr []string
	gstr, ok := DeepCopy(nstr).([]string)
	require.True(t, ok)
	assert.Nil(t, gstr)
}

func TestDeepCopy_NilElementsInsideContainersSurvive(t *testing.T) {
	in := []any{nil, map[string]any{"k": nil}, []map[string]any{nil}}
	out := DeepCopy(in).([]any)
	assert.Equal(t, in, out)
}

func TestDeepCopyMap_EmptyMapIsNewEmptyMap(t *testing.T) {
	in := map[string]any{}
	out := DeepCopyMap(in)
	require.NotNil(t, out)
	out["x"] = 1
	assert.Empty(t, in)
}
```

- [x] Step 2: Run it, confirm it fails.

```bash
go test ./domain/variable/ -run 'TestDeepCopy' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/variable [github.com/arbitron/arbitron/domain/variable.test]
domain/variable/deepcopy_test.go:33:8: undefined: DeepCopyMap
domain/variable/deepcopy_test.go:52:8: undefined: DeepCopyMap
...
FAIL	github.com/arbitron/arbitron/domain/variable [build failed]
```

- [x] Step 3: Minimal implementation. Create `domain/variable/deepcopy.go`:

```go
package variable

import "reflect"

// DeepCopy returns a structurally independent copy of v: every map and slice
// reachable from v is freshly allocated, so mutating the copy at any depth can
// never reach v (and vice versa).
//
// VariableStore.Snapshot is deliberately a SHALLOW map copy; a parallel group
// that handed every task the same nested maps would let one task's transform
// node rewrite another task's (and the stored group-start) state. DeepCopy is
// the primitive that makes "each task starts with its own copy" true.
//
// Scalars (string, bool, numbers, nil) are returned by value. map[string]any
// and []any — the shapes JSON decoding produces — take a fast path; any other
// map or slice type (e.g. []map[string]any, []string, map[string]string,
// [][]any) is copied generically via reflection with its static type
// preserved. A nil map or slice stays nil with its type preserved. Pointers,
// structs and channels are returned as-is: the variable store only ever holds
// JSON-shaped values, and those kinds never appear in it.
func DeepCopy(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case map[string]any:
		return DeepCopyMap(t)
	case []any:
		if t == nil {
			return []any(nil)
		}
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = DeepCopy(e)
		}
		return out
	case string, bool, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return v
	default:
		return deepCopyReflect(v)
	}
}

// DeepCopyMap returns a deep copy of m (see DeepCopy). A nil map yields nil.
func DeepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = DeepCopy(v)
	}
	return out
}

// deepCopyReflect copies any other map or slice type element by element,
// recursing through DeepCopy so nested containers are copied too.
func deepCopyReflect(v any) any {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice:
		if rv.IsNil() {
			return v
		}
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(copyElem(rv.Index(i), rv.Type().Elem()))
		}
		return out.Interface()
	case reflect.Map:
		if rv.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), copyElem(iter.Value(), rv.Type().Elem()))
		}
		return out.Interface()
	default:
		return v
	}
}

// copyElem deep-copies one container element and returns a value assignable
// to elemType. A nil interface element stays the zero value of elemType.
func copyElem(ev reflect.Value, elemType reflect.Type) reflect.Value {
	if ev.Kind() == reflect.Interface && ev.IsNil() {
		return reflect.Zero(elemType)
	}
	c := DeepCopy(ev.Interface())
	if c == nil {
		return reflect.Zero(elemType)
	}
	return reflect.ValueOf(c)
}
```

- [x] Step 4: Run it, confirm it passes.

```bash
go test ./domain/variable/ -run 'TestDeepCopy' -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/variable	0.0XXs
```

- [x] Step 5: Write the failing typed-resolver tests. Create `domain/variable/typed_resolve_test.go`:

```go
package variable

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// typedStore seeds a store the way Engine.run reconstructs one: inputs,
// a reserved workspace, and a node output.
func typedStore(t *testing.T) *VariableStore {
	t.Helper()
	s := NewStore()
	require.NoError(t, s.SetInput("repo", "acme/widgets"))
	require.NoError(t, s.SetInput("count", float64(3)))
	require.NoError(t, s.SetInput("enabled", true))
	require.NoError(t, s.SetInput("maybe", nil))
	require.NoError(t, s.SetInput("cfg", map[string]any{
		"depth": map[string]any{"leaf": "v"},
		"list":  []any{"a", map[string]any{"k": "deep"}},
	}))
	require.NoError(t, s.SetInput("tags", []any{"x", "y"}))
	s.SetReserved("workspace", "/ws/task-1")
	s.SetNodeOutput("plan", map[string]any{
		"steps": []any{map[string]any{"title": "one"}},
		"total": float64(2),
	})
	return s
}

func TestResolveTyped_WholeReferencePreservesType(t *testing.T) {
	s := typedStore(t)
	cases := []struct {
		in   string
		want any
	}{
		{"{{count}}", float64(3)},
		{"{{enabled}}", true},
		{"{{repo}}", "acme/widgets"},
		{"{{tags}}", []any{"x", "y"}},
		{"{{cfg.depth}}", map[string]any{"leaf": "v"}},
		{"{{plan.steps.0.title}}", "one"},
		{"{{plan.total}}", float64(2)},
		{"{{maybe}}", nil},
	}
	for _, c := range cases {
		got, err := ResolveTyped(c.in, s)
		require.NoError(t, err, c.in)
		assert.Equal(t, c.want, got, c.in)
	}
}

func TestResolveTyped_WholeReferenceToleratesWhitespaceInsideBraces(t *testing.T) {
	s := typedStore(t)

	got, err := ResolveTyped("{{  count }}", s)
	require.NoError(t, err)
	assert.Equal(t, float64(3), got)

	got, err = ResolveTyped("{{\tcfg.depth.leaf\t}}", s)
	require.NoError(t, err)
	assert.Equal(t, "v", got)
}

func TestResolveTyped_WholeReferenceReturnsDeepCopy(t *testing.T) {
	s := typedStore(t)

	got, err := ResolveTyped("{{cfg}}", s)
	require.NoError(t, err)
	m := got.(map[string]any)
	m["depth"].(map[string]any)["leaf"] = "mutated"
	m["list"].([]any)[1].(map[string]any)["k"] = "mutated"

	leaf, _ := s.Get("cfg.depth.leaf")
	assert.Equal(t, "v", leaf, "mutating a resolved map must not reach the store")
	k, _ := s.Get("cfg.list.1.k")
	assert.Equal(t, "deep", k)
}

func TestResolveTyped_InterpolatedStringsUseResolve(t *testing.T) {
	s := typedStore(t)
	cases := []struct {
		in   string
		want string
	}{
		{"{{workspace}}/repo", "/ws/task-1/repo"},
		{"count={{count}} on={{ enabled }}", "count=3 on=true"},
		{"cfg: {{cfg.depth}}", `cfg: {"leaf":"v"}`},
		{"  {{count}}", "  3"},
		{"{{repo}}{{count}}", "acme/widgets3"},
	}
	for _, c := range cases {
		got, err := ResolveTyped(c.in, s)
		require.NoError(t, err, c.in)
		assert.Equal(t, c.want, got, c.in)
	}
}

func TestResolveTyped_LiteralsAreUnchanged(t *testing.T) {
	s := typedStore(t)
	for _, v := range []any{"plain text", float64(4), 7, true, false, nil, "{{}}", "single { brace }"} {
		got, err := ResolveTyped(v, s)
		require.NoError(t, err)
		assert.Equal(t, v, got)
	}
}

func TestResolveTypedMap_NestedMapsAndArraysRecurse(t *testing.T) {
	s := typedStore(t)
	in := map[string]any{
		"work_dir": "{{workspace}}/repo",
		"opts": map[string]any{
			"n":    "{{count}}",
			"list": []any{"{{repo}}", "lit", float64(1), map[string]any{"deep": "{{cfg.depth.leaf}}"}},
		},
		"rows":  []map[string]any{{"t": "{{plan.steps.0.title}}"}},
		"names": []string{"{{repo}}", "x-{{count}}", "{{count}}"},
		"flag":  false,
	}
	want := map[string]any{
		"work_dir": "/ws/task-1/repo",
		"opts": map[string]any{
			"n":    float64(3),
			"list": []any{"acme/widgets", "lit", float64(1), map[string]any{"deep": "v"}},
		},
		"rows":  []map[string]any{{"t": "one"}},
		"names": []string{"acme/widgets", "x-3", "3"},
		"flag":  false,
	}

	got, err := ResolveTypedMap(in, s)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, "{{workspace}}/repo", in["work_dir"], "the input map is never modified")
	assert.Equal(t, "{{count}}", in["opts"].(map[string]any)["n"])
}

func TestResolveTypedMap_ResultSharesNoStateWithLiteralInput(t *testing.T) {
	s := typedStore(t)
	in := map[string]any{"lit": map[string]any{"inner": []any{"a"}}}

	got, err := ResolveTypedMap(in, s)
	require.NoError(t, err)
	got["lit"].(map[string]any)["inner"].([]any)[0] = "mutated"

	assert.Equal(t, []any{"a"}, in["lit"].(map[string]any)["inner"])
}

func TestResolveTyped_MissingWholeReferenceIsAnError(t *testing.T) {
	s := typedStore(t)

	_, err := ResolveTyped("{{missing.path}}", s)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnresolvedReference)
	assert.Contains(t, err.Error(), "{{missing.path}}")
}

func TestResolveTyped_MissingReferenceInsideInterpolationIsAnError(t *testing.T) {
	s := typedStore(t)

	_, err := ResolveTyped("x-{{repo}}-{{ nope }}", s)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnresolvedReference)
	assert.Contains(t, err.Error(), "{{nope}}")
}

func TestResolveTyped_PathThroughScalarOrOutOfRangeIndexIsMissing(t *testing.T) {
	s := typedStore(t)
	for _, in := range []string{"{{tags.5}}", "{{repo.owner}}", "{{plan.steps.0.title.x}}"} {
		_, err := ResolveTyped(in, s)
		assert.ErrorIs(t, err, ErrUnresolvedReference, in)
	}
}

func TestResolveTypedMap_MissingReferenceDeepInsideNamesKeyPath(t *testing.T) {
	s := typedStore(t)
	in := map[string]any{
		"ok":   "{{repo}}",
		"opts": map[string]any{"list": []any{"fine", "{{gone}}"}},
	}

	got, err := ResolveTypedMap(in, s)
	require.Error(t, err)
	assert.Nil(t, got, "no partial result on failure")
	assert.ErrorIs(t, err, ErrUnresolvedReference)
	assert.Contains(t, err.Error(), "{{gone}}")
	assert.Contains(t, err.Error(), `"opts.list.1"`)
}

func TestResolveTypedMap_MissingReferenceInStringSliceAndMapSlice(t *testing.T) {
	s := typedStore(t)

	_, err := ResolveTypedMap(map[string]any{"names": []string{"ok", "{{absent}}"}}, s)
	assert.ErrorIs(t, err, ErrUnresolvedReference)
	assert.Contains(t, err.Error(), `"names.1"`)

	_, err = ResolveTypedMap(map[string]any{"rows": []map[string]any{{"t": "{{absent}}"}}}, s)
	assert.ErrorIs(t, err, ErrUnresolvedReference)
	assert.Contains(t, err.Error(), `"rows.0.t"`)
}

func TestResolveTypedMap_NilAndEmpty(t *testing.T) {
	s := typedStore(t)

	got, err := ResolveTypedMap(nil, s)
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = ResolveTypedMap(map[string]any{}, s)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// Provenance probe: the resolver's only producer of values is the store it is
// handed. With no store, every reference must fail rather than silently
// resolve to an empty value.
func TestResolveTyped_NilStoreTreatsEveryReferenceAsMissing(t *testing.T) {
	_, err := ResolveTyped("{{repo}}", nil)
	assert.ErrorIs(t, err, ErrUnresolvedReference)

	_, err = ResolveTypedMap(map[string]any{"w": "{{workspace}}/repo"}, nil)
	assert.ErrorIs(t, err, ErrUnresolvedReference)

	got, err := ResolveTyped("literal", nil)
	require.NoError(t, err)
	assert.Equal(t, "literal", got)
}
```

- [x] Step 6: Run it, confirm it fails.

```bash
go test ./domain/variable/ -run 'TestResolveTyped' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/variable [github.com/arbitron/arbitron/domain/variable.test]
domain/variable/typed_resolve_test.go:48:15: undefined: ResolveTyped
domain/variable/typed_resolve_test.go:57:14: undefined: ResolveTyped
...
FAIL	github.com/arbitron/arbitron/domain/variable [build failed]
```

- [x] Step 7: Minimal implementation. Create `domain/variable/typed_resolve.go`:

```go
package variable

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ErrUnresolvedReference is returned by ResolveTyped / ResolveTypedMap when a
// {{path}} template names a variable or node output that does not exist.
// Resolve (the string-only substitution) silently leaves such markers in
// place; the typed resolver refuses them because a parallel group task that
// started with a literal "{{typo}}" as an input would fail far from its cause.
var ErrUnresolvedReference = errors.New("unresolved variable reference")

// ResolveTyped resolves templates inside v against store, preserving types:
//
//   - a string that is EXACTLY one "{{path}}" reference (whitespace inside the
//     braces allowed) resolves to the referenced value with its type intact
//     (map, array, number, bool, string, null). The returned value is a DEEP
//     COPY, so mutating it never reaches the store;
//   - any other string containing references is interpolated with Resolve
//     (maps/arrays render as JSON, numbers without a trailing ".0");
//   - map[string]any, []any, []map[string]any and []string recurse element by
//     element ([]string elements stay strings: a whole reference to a
//     non-string value is rendered the way Resolve renders it);
//   - every other value is a literal and is returned as a deep copy, unchanged.
//
// A reference that does not resolve ANYWHERE in v fails the whole call with an
// error wrapping ErrUnresolvedReference that names the missing path and, for
// nested values, the key path where it was found (e.g. "opts.list.1"). "{{}}"
// (an empty path) is not a reference and is kept literally, as Resolve does.
// A nil store resolves nothing: every reference is missing.
func ResolveTyped(v any, store *VariableStore) (any, error) {
	if store == nil {
		store = NewStore()
	}
	return resolveTypedAt(v, store, "")
}

// ResolveTypedMap applies ResolveTyped to every value of m and returns a new
// map; m itself is never modified. A nil map yields (nil, nil). Keys are
// visited in sorted order so the reported missing reference is deterministic.
func ResolveTypedMap(m map[string]any, store *VariableStore) (map[string]any, error) {
	if m == nil {
		return nil, nil
	}
	if store == nil {
		store = NewStore()
	}
	return resolveTypedMapAt(m, store, "")
}

func resolveTypedAt(v any, store *VariableStore, at string) (any, error) {
	switch t := v.(type) {
	case string:
		return resolveTypedString(t, store, at)
	case map[string]any:
		if t == nil {
			return map[string]any(nil), nil
		}
		return resolveTypedMapAt(t, store, at)
	case []any:
		if t == nil {
			return []any(nil), nil
		}
		out := make([]any, len(t))
		for i, e := range t {
			r, err := resolveTypedAt(e, store, joinKeyPath(at, strconv.Itoa(i)))
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case []map[string]any:
		if t == nil {
			return []map[string]any(nil), nil
		}
		out := make([]map[string]any, len(t))
		for i, m := range t {
			if m == nil {
				continue
			}
			r, err := resolveTypedMapAt(m, store, joinKeyPath(at, strconv.Itoa(i)))
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case []string:
		if t == nil {
			return []string(nil), nil
		}
		out := make([]string, len(t))
		for i, s := range t {
			r, err := resolveTypedString(s, store, joinKeyPath(at, strconv.Itoa(i)))
			if err != nil {
				return nil, err
			}
			if rs, ok := r.(string); ok {
				out[i] = rs
			} else {
				out[i] = formatValue(r)
			}
		}
		return out, nil
	default:
		return DeepCopy(v), nil
	}
}

func resolveTypedMapAt(m map[string]any, store *VariableStore, at string) (map[string]any, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(m))
	for _, k := range keys {
		r, err := resolveTypedAt(m[k], store, joinKeyPath(at, k))
		if err != nil {
			return nil, err
		}
		out[k] = r
	}
	return out, nil
}

// resolveTypedString resolves one string value (see ResolveTyped).
func resolveTypedString(s string, store *VariableStore, at string) (any, error) {
	if path, ok := wholeReference(s); ok {
		val, found := store.Get(path)
		if !found {
			return nil, unresolvedReference(path, at)
		}
		return DeepCopy(val), nil
	}
	for _, path := range templatePaths(s) {
		if _, found := store.Get(path); !found {
			return nil, unresolvedReference(path, at)
		}
	}
	return Resolve(s, store), nil
}

// wholeReference reports whether s is exactly one "{{path}}" reference with
// nothing outside the braces, returning the trimmed path.
func wholeReference(s string) (string, bool) {
	if len(s) < 4 || !strings.HasPrefix(s, "{{") || !strings.HasSuffix(s, "}}") {
		return "", false
	}
	inner := s[2 : len(s)-2]
	if strings.Contains(inner, "{{") || strings.Contains(inner, "}}") {
		return "", false
	}
	path := strings.TrimSpace(inner)
	if path == "" {
		return "", false
	}
	return path, true
}

// templatePaths returns the trimmed, non-empty paths of every {{...}} marker
// in s, scanning exactly the way Resolve does (find the next "}}", then the
// last "{{" before it) so the two can never disagree about what a reference
// is.
func templatePaths(s string) []string {
	var paths []string
	i := 0
	for i < len(s) {
		closeRel := strings.Index(s[i:], "}}")
		if closeRel == -1 {
			break
		}
		closeAbs := i + closeRel
		openRel := strings.LastIndex(s[i:closeAbs], "{{")
		if openRel == -1 {
			i = closeAbs + 2
			continue
		}
		path := strings.TrimSpace(s[i+openRel+2 : closeAbs])
		if path != "" {
			paths = append(paths, path)
		}
		i = closeAbs + 2
	}
	return paths
}

func joinKeyPath(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

func unresolvedReference(path, at string) error {
	if at == "" {
		return fmt.Errorf("%w: {{%s}} does not name an existing variable or node output", ErrUnresolvedReference, path)
	}
	return fmt.Errorf("%w: {{%s}} (at %q) does not name an existing variable or node output", ErrUnresolvedReference, path, at)
}
```

Design notes the implementer must keep (they are what the tests pin):
- `templatePaths` scans exactly like `Resolve` (next `}}`, then the last `{{` before it), so "is this a reference?" can never disagree between the missing-reference check and the interpolation.
- The whole-value case returns `DeepCopy(val)` — a task that mutates a map it received by reference must not reach the group-start snapshot.
- `"{{}}"` is not a reference (empty path) and stays literal, matching `Resolve`.
- Map keys are visited in sorted order so the reported missing path is deterministic.

- [x] Step 8: Run the new tests, then the whole package, gofmt and vet.

```bash
go test ./domain/variable/ -run 'TestResolveTyped|TestDeepCopy' -count=1
go test ./domain/variable/ -count=1
gofmt -l domain/variable/deepcopy.go domain/variable/typed_resolve.go domain/variable/deepcopy_test.go domain/variable/typed_resolve_test.go
go vet ./domain/variable/
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/variable	0.0XXs
ok  	github.com/arbitron/arbitron/domain/variable	0.0XXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 9: Commit.

```bash
git add domain/variable/deepcopy.go domain/variable/deepcopy_test.go domain/variable/typed_resolve.go domain/variable/typed_resolve_test.go
git commit -m "feat(variable): deep copy and typed recursive input resolver for parallel groups" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
