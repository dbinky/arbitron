package engine

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

// ErrChildContractUnsatisfied is returned when the values produced for a child
// workflow do not satisfy its declared input contract. It is the spec's
// "required startup values cannot be produced" condition, made
// errors.Is-checkable so the executor and its tests can attribute the failure
// precisely rather than string-matching.
var ErrChildContractUnsatisfied = errors.New("engine: prepared startup values do not satisfy the child workflow's declared inputs")

// schemaTypeForInput maps a declared workflow input type onto its JSON-schema
// type. The child's declaration is the authority; an unrecognized or empty
// declared type yields ("", false) and the property is emitted WITHOUT a type
// rather than with a fabricated one, so no second source of truth about valid
// input types is created here.
func schemaTypeForInput(t string) (string, bool) {
	switch t {
	case "string", "number", "boolean", "object", "array":
		return t, true
	default:
		return "", false
	}
}

// childInputSchema synthesizes the output contract for the preparation call
// from the CHILD's declared input fields. Each declared input contributes its
// name, declared type, description, and required flag. The parent never names
// the fields — the child's declaration is the contract.
//
// `required` is sorted so the assembled prompt is stable across runs (an
// unstable prompt defeats provider-side caching and makes trace diffs noisy).
func childInputSchema(fields []workflow.InputField) map[string]any {
	props := make(map[string]any, len(fields))
	var required []string
	for _, f := range fields {
		p := map[string]any{}
		if t, ok := schemaTypeForInput(f.Type); ok {
			p["type"] = t
		}
		if f.Description != "" {
			p["description"] = f.Description
		}
		props[f.Name] = p
		if f.Required {
			required = append(required, f.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}

// validateChildInputs decides whether produced actually satisfies the child's
// declared contract. It is the AUTHORITY for the spec's failure requirement.
//
// It rejects exactly three things:
//   - a nil produced map (the response was not an object at all),
//   - a missing required input,
//   - a required input present but null.
//
// It deliberately ACCEPTS and passes through:
//   - absent OPTIONAL inputs — the child's own trigger-time defaulting handles
//     them, exactly as Engine.TriggerWithDepartment already does;
//   - extra keys not in the contract — the trigger path already tolerates and
//     seeds extra variables, and its SetInput reserved-name guard prevents a
//     produced value from overwriting an engine-seeded variable such as
//     "workspace". Re-implementing that rejection here would duplicate a guard
//     that already exists one layer down.
//
// Type coercion is NOT attempted. The declared types travel into the prompt as
// guidance and produced values pass through as parsed; the trigger path is the
// single place input semantics are enforced.
func validateChildInputs(childWorkflow string, fields []workflow.InputField, produced map[string]any) error {
	if produced == nil {
		return fmt.Errorf("%w: child %q: the model did not produce a JSON object",
			ErrChildContractUnsatisfied, childWorkflow)
	}

	var missing []string
	for _, f := range fields {
		if !f.Required {
			continue
		}
		v, present := produced[f.Name]
		if !present || v == nil {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	// The message is written to be actionable by a workflow author reading the
	// failed node's trace entry: the fix is almost always in the guidance prose
	// or in the parent's upstream nodes.
	return fmt.Errorf("%w: child %q is missing required input(s): %s",
		ErrChildContractUnsatisfied, childWorkflow, strings.Join(missing, ", "))
}
