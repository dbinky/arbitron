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
