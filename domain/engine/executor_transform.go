package engine

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// TransformExecutor mutates the variable store by applying a sequence of
// operations (set, increment, decrement, append, delete).
type TransformExecutor struct{}

// NewTransformExecutor creates a new TransformExecutor.
func NewTransformExecutor() *TransformExecutor {
	return &TransformExecutor{}
}

// Execute applies each operation in order, mutating the variable store.
// Returns a nil-output NodeResult on success.
func (e *TransformExecutor) Execute(_ context.Context, node workflow.Node, vars *variable.VariableStore, _ string) (*NodeResult, error) {
	for _, op := range node.Config.Operations {
		if err := applyOperation(op, vars); err != nil {
			return nil, fmt.Errorf("transform %q op %q on %q: %w", node.ID, op.Operation, op.Variable, err)
		}
	}
	return &NodeResult{}, nil
}

// applyOperation dispatches a single transform operation.
func applyOperation(op workflow.Operation, vars *variable.VariableStore) error {
	switch op.Operation {
	case "set":
		return applySet(op, vars)
	case "increment":
		return applyIncrement(op, vars, 1)
	case "decrement":
		return applyIncrement(op, vars, -1)
	case "append":
		return applyAppend(op, vars)
	case "delete":
		vars.DeleteInput(op.Variable)
		return nil
	default:
		return fmt.Errorf("unknown operation %q", op.Operation)
	}
}

// applySet stores a value in the variable store, resolving substitution
// templates in string values.
func applySet(op workflow.Operation, vars *variable.VariableStore) error {
	val := op.Value
	if s, ok := val.(string); ok {
		val = variable.Resolve(s, vars)
	}
	vars.SetInput(op.Variable, val)
	return nil
}

// applyIncrement adds (direction=1) or subtracts (direction=-1) a numeric
// delta to/from the current value in the store.
func applyIncrement(op workflow.Operation, vars *variable.VariableStore, direction float64) error {
	current, found := vars.Get(op.Variable)
	if !found {
		return fmt.Errorf("variable %q not found", op.Variable)
	}

	currentNum, ok := toFloat64(current)
	if !ok {
		return fmt.Errorf("variable %q is non-numeric", op.Variable)
	}

	delta, ok := toFloat64(op.Value)
	if !ok {
		return fmt.Errorf("increment value is non-numeric")
	}

	vars.SetInput(op.Variable, currentNum+direction*delta)
	return nil
}

// applyAppend adds a value to an existing array, or creates a new array if
// the variable does not exist. Returns an error if the variable exists but
// is not an array.
func applyAppend(op workflow.Operation, vars *variable.VariableStore) error {
	current, found := vars.Get(op.Variable)
	if !found {
		vars.SetInput(op.Variable, []any{op.Value})
		return nil
	}

	arr, ok := current.([]any)
	if !ok {
		return fmt.Errorf("variable %q is not an array", op.Variable)
	}

	vars.SetInput(op.Variable, append(arr, op.Value))
	return nil
}
