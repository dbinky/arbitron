// Package engine provides the workflow execution engine interfaces and implementations.
package engine

import (
	"context"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// NodeExecutor defines the contract for executing a single workflow node.
type NodeExecutor interface {
	Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error)
}
