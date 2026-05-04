package postgres

import (
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
)

// Compile-time interface satisfaction checks.
var _ instance.InstanceRepository = (*InstanceRepo)(nil)
var _ instance.NodeExecutionRepository = (*NodeExecutionRepo)(nil)
var _ instance.VariableSnapshotRepository = (*VariableSnapshotRepo)(nil)
var _ instance.UserRepository = (*UserRepo)(nil)

func TestInterfaceSatisfaction(t *testing.T) {
	// These compile-time checks above verify that each adapter type
	// satisfies the corresponding domain port interface.
	// If this file compiles, the interfaces are satisfied.
	t.Run("InstanceRepo implements InstanceRepository", func(t *testing.T) {
		var _ instance.InstanceRepository = (*InstanceRepo)(nil)
	})
	t.Run("NodeExecutionRepo implements NodeExecutionRepository", func(t *testing.T) {
		var _ instance.NodeExecutionRepository = (*NodeExecutionRepo)(nil)
	})
	t.Run("VariableSnapshotRepo implements VariableSnapshotRepository", func(t *testing.T) {
		var _ instance.VariableSnapshotRepository = (*VariableSnapshotRepo)(nil)
	})
	t.Run("UserRepo implements UserRepository", func(t *testing.T) {
		var _ instance.UserRepository = (*UserRepo)(nil)
	})
}
