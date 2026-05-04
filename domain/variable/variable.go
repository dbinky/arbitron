// Package variable provides the VariableStore for workflow instance state
// and a substitution engine for resolving {{dot.path}} templates.
package variable

import (
	"strconv"
	"strings"
)

// VariableStore holds workflow inputs and node outputs, providing dot-path
// access to arbitrarily nested values.
type VariableStore struct {
	inputs      map[string]any
	nodeOutputs map[string]any
}

// NewStore creates an empty VariableStore.
func NewStore() *VariableStore {
	return &VariableStore{
		inputs:      make(map[string]any),
		nodeOutputs: make(map[string]any),
	}
}

// Get resolves a dot-separated path. The first segment is looked up in inputs
// first, then nodeOutputs. Remaining segments walk nested maps and arrays.
// Integer segments index into slices.
func (s *VariableStore) Get(dotPath string) (any, bool) {
	segments := strings.Split(dotPath, ".")
	if len(segments) == 0 {
		return nil, false
	}

	first := segments[0]
	rest := segments[1:]

	// Check inputs first.
	if val, ok := s.inputs[first]; ok {
		return walk(val, rest)
	}

	// Fall back to nodeOutputs.
	if val, ok := s.nodeOutputs[first]; ok {
		return walk(val, rest)
	}

	return nil, false
}

// walk traverses a value through the remaining path segments.
func walk(current any, segments []string) (any, bool) {
	for _, seg := range segments {
		if current == nil {
			return nil, false
		}
		switch v := current.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, false
			}
			current = v[idx]
		default:
			// Cannot traverse into a scalar value.
			return nil, false
		}
	}
	return current, true
}

// SetInput stores a value in the inputs namespace.
func (s *VariableStore) SetInput(key string, value any) {
	s.inputs[key] = value
}

// SetNodeOutput stores a node's output in the nodeOutputs namespace.
func (s *VariableStore) SetNodeOutput(nodeID string, output any) {
	s.nodeOutputs[nodeID] = output
}

// DeleteInput removes a key from the inputs namespace.
func (s *VariableStore) DeleteInput(key string) {
	delete(s.inputs, key)
}

// Snapshot returns a merged view with inputs as the base and nodeOutputs
// layered on top (nodeOutputs overwrite inputs for the same key).
func (s *VariableStore) Snapshot() map[string]any {
	merged := make(map[string]any, len(s.inputs)+len(s.nodeOutputs))
	for k, v := range s.inputs {
		merged[k] = v
	}
	for k, v := range s.nodeOutputs {
		merged[k] = v
	}
	return merged
}

// FromSnapshot reconstructs a VariableStore from a snapshot map.
// Map values are placed in nodeOutputs; all other values go to inputs.
func FromSnapshot(data map[string]any) *VariableStore {
	store := NewStore()
	if data == nil {
		return store
	}
	for k, v := range data {
		if _, isMap := v.(map[string]any); isMap {
			store.nodeOutputs[k] = v
		} else {
			store.inputs[k] = v
		}
	}
	return store
}
