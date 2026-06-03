package engine

import (
	"encoding/json"
	"fmt"
)

// validateAgainstSchema strictly validates a parsed JSON value against a
// declared OutputSchema and returns an error describing the first
// non-conformance.
//
// SECURITY (PR #18 HIGH, comment 4614959041): a node whose OutputSchema gates
// control flow (e.g. assessor.done) is untrusted-by-default — attacker-
// influenced variable content can steer the model to emit a forged verdict, and
// the salvage parser will lift the first balanced object out of arbitrary prose.
// Validating the recovered object against the declared schema and failing closed
// on non-conformance is what stops a forged {"done":true} from being accepted as
// the authoritative verdict.
//
// It implements the JSON-Schema subset the workflow OutputSchemas actually use:
// object type with properties + required, primitive types (string, number,
// integer, boolean), arrays with an items schema, nested objects, and enum
// membership. An unrecognised or absent constraint is treated permissively (it
// imposes no additional restriction) — the goal is to reject clearly
// non-conforming verdicts, not to be a complete JSON-Schema implementation.
func validateAgainstSchema(value any, schema any) error {
	sm, ok := schema.(map[string]any)
	if !ok {
		// Schema is not an object description we understand; impose no extra
		// constraint beyond "something was parsed".
		return nil
	}

	if enum, ok := sm["enum"].([]any); ok {
		if !enumContains(enum, value) {
			return fmt.Errorf("value %v is not one of the allowed enum values", value)
		}
	}

	declaredType, _ := sm["type"].(string)
	switch declaredType {
	case "object", "":
		obj, ok := value.(map[string]any)
		if !ok {
			if declaredType == "object" {
				return fmt.Errorf("expected an object, got %T", value)
			}
			return nil
		}
		return validateObject(obj, sm)
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("expected an array, got %T", value)
		}
		if items, ok := sm["items"].(map[string]any); ok {
			for i, el := range arr {
				if err := validateAgainstSchema(el, items); err != nil {
					return fmt.Errorf("array index %d: %w", i, err)
				}
			}
		}
		return nil
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected a string, got %T", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected a boolean, got %T", value)
		}
	case "number", "integer":
		// encoding/json decodes all JSON numbers into float64 (and json.Number
		// when configured); accept either.
		switch value.(type) {
		case float64, json.Number:
		default:
			return fmt.Errorf("expected a %s, got %T", declaredType, value)
		}
	}
	return nil
}

// validateObject checks an object's required fields and each declared
// property's type. Properties not declared in the schema are left untouched
// (the schemas in use do not set additionalProperties:false).
func validateObject(obj map[string]any, schema map[string]any) error {
	if required, ok := schema["required"].([]any); ok {
		for _, r := range required {
			name, ok := r.(string)
			if !ok {
				continue
			}
			if _, present := obj[name]; !present {
				return fmt.Errorf("missing required field %q", name)
			}
		}
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	for name, propSchema := range props {
		v, present := obj[name]
		if !present {
			continue // presence is governed by required, above
		}
		if err := validateAgainstSchema(v, propSchema); err != nil {
			return fmt.Errorf("field %q: %w", name, err)
		}
	}
	return nil
}

// enumContains reports whether value equals one of the allowed enum entries,
// compared structurally via JSON marshalling so numbers/strings/bools match
// regardless of concrete Go type.
func enumContains(allowed []any, value any) bool {
	want, err := json.Marshal(value)
	if err != nil {
		return false
	}
	for _, a := range allowed {
		got, err := json.Marshal(a)
		if err != nil {
			continue
		}
		if string(got) == string(want) {
			return true
		}
	}
	return false
}
