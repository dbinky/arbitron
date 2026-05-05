package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ConditionalExecutor evaluates conditions against the variable store and
// returns the output port of the first matching condition.
type ConditionalExecutor struct{}

// NewConditionalExecutor creates a new ConditionalExecutor.
func NewConditionalExecutor() *ConditionalExecutor {
	return &ConditionalExecutor{}
}

// Execute evaluates each condition in order and returns the output port of
// the first match. If no condition matches, the default output is returned.
func (e *ConditionalExecutor) Execute(_ context.Context, node workflow.Node, vars *variable.VariableStore, _ string) (*NodeResult, error) {
	for _, cond := range node.Config.Conditions {
		if evaluateCondition(cond, vars) {
			return &NodeResult{OutputPort: cond.Output}, nil
		}
	}
	return &NodeResult{OutputPort: node.Config.DefaultOutput}, nil
}

// evaluateCondition checks whether a single condition is satisfied.
func evaluateCondition(cond workflow.Condition, vars *variable.VariableStore) bool {
	fieldVal, found := vars.Get(cond.Field)

	switch cond.Operator {
	case "exists":
		return found && fieldVal != nil
	case "eq":
		if !found {
			return false
		}
		return fmt.Sprintf("%v", fieldVal) == fmt.Sprintf("%v", cond.Value)
	case "neq":
		if !found {
			return true
		}
		return fmt.Sprintf("%v", fieldVal) != fmt.Sprintf("%v", cond.Value)
	case "gt":
		return compareNumeric(fieldVal, cond.Value, func(a, b float64) bool { return a > b })
	case "gte":
		return compareNumeric(fieldVal, cond.Value, func(a, b float64) bool { return a >= b })
	case "lt":
		return compareNumeric(fieldVal, cond.Value, func(a, b float64) bool { return a < b })
	case "lte":
		return compareNumeric(fieldVal, cond.Value, func(a, b float64) bool { return a <= b })
	case "contains":
		if !found {
			return false
		}
		return evaluateContains(fieldVal, cond.Value)
	default:
		return false
	}
}

// toFloat64 attempts to convert a value to float64. Returns false if not numeric.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	default:
		return 0, false
	}
}

// compareNumeric converts both sides to float64 and applies the comparator.
// Returns false (skips) if either value is non-numeric.
func compareNumeric(fieldVal, condVal any, cmp func(a, b float64) bool) bool {
	a, okA := toFloat64(fieldVal)
	b, okB := toFloat64(condVal)
	if !okA || !okB {
		return false
	}
	return cmp(a, b)
}

// evaluateContains checks substring membership for strings and element
// membership for arrays.
func evaluateContains(fieldVal, condVal any) bool {
	switch v := fieldVal.(type) {
	case string:
		needle, ok := condVal.(string)
		if !ok {
			needle = fmt.Sprintf("%v", condVal)
		}
		return strings.Contains(v, needle)
	case []any:
		condStr := fmt.Sprintf("%v", condVal)
		for _, elem := range v {
			if fmt.Sprintf("%v", elem) == condStr {
				return true
			}
		}
		return false
	default:
		return false
	}
}
