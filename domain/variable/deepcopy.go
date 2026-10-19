package variable

import "reflect"

// DeepCopy returns a structurally independent copy of v: every map and slice
// reachable from v is freshly allocated, so mutating the copy at any depth can
// never reach v (and vice versa).
//
// VariableStore.Snapshot is deliberately a shallow map copy; a parallel group
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
