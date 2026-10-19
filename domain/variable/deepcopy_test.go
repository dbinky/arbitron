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
