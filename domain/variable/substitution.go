package variable

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Resolve scans the input string for {{dot.path}} patterns and replaces each
// with the corresponding value from the store. Unresolved paths are left as
// literal {{path}} text.
func Resolve(input string, store *VariableStore) string {
	if len(input) == 0 {
		return input
	}

	var b strings.Builder
	b.Grow(len(input))

	i := 0
	for i < len(input) {
		// Find the next closing "}}" first, then look backwards for "{{".
		closeRel := strings.Index(input[i:], "}}")
		if closeRel == -1 {
			// No more closing markers; append the rest.
			b.WriteString(input[i:])
			break
		}

		closeAbs := i + closeRel

		// Look backwards from the closing marker to find the last "{{".
		openAbs := strings.LastIndex(input[i:closeAbs], "{{")
		if openAbs == -1 {
			// No opening marker before this close; write up to and including "}}"
			b.WriteString(input[i : closeAbs+2])
			i = closeAbs + 2
			continue
		}

		openAbs += i // Convert to absolute position.

		// Write text before the opening marker.
		b.WriteString(input[i:openAbs])

		// Extract the path between {{ and }}, trimming any surrounding whitespace.
		path := strings.TrimSpace(input[openAbs+2 : closeAbs])

		// Resolve the path.
		val, ok := store.Get(path)
		if !ok {
			// Leave unresolved marker in place.
			b.WriteString("{{")
			b.WriteString(path)
			b.WriteString("}}")
		} else {
			b.WriteString(formatValue(val))
		}

		i = closeAbs + 2
	}

	return b.String()
}

// formatValue converts a resolved value to its string representation.
func formatValue(val any) string {
	if val == nil {
		return "null"
	}
	switch v := val.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case int:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case float64:
		if v == math.Trunc(v) && !math.IsInf(v, 0) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case float32:
		f := float64(v)
		if f == math.Trunc(f) && !math.IsInf(f, 0) {
			return fmt.Sprintf("%d", int64(f))
		}
		return fmt.Sprintf("%g", v)
	case map[string]any, []any:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(data)
	default:
		return fmt.Sprintf("%v", v)
	}
}
