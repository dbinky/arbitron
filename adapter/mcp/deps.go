package mcp

import (
	"context"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/workflow"
)

// EngineService defines the subset of engine.Engine that the MCP adapter
// needs for workflow lifecycle operations. This interface keeps the adapter
// decoupled from the concrete Engine implementation.
type EngineService interface {
	Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)
	Pause(instanceID uuid.UUID) error
	Resume(instanceID uuid.UUID) error
	Cancel(instanceID uuid.UUID) error
	Retry(instanceID uuid.UUID, variableEdits map[string]any) error
	Rerun(instanceID uuid.UUID) (uuid.UUID, error)
	SetPriority(id uuid.UUID, priority string) error
}

// QueueManagerPort defines the subset of queue.Manager that the MCP adapter
// needs for queue configuration and evaluation.
type QueueManagerPort interface {
	UpdateConfig(globalCap, perUserCap int)
	Evaluate(ctx context.Context) error
}

// Deps holds the domain dependencies injected into MCP tool handlers.
// Each field corresponds to a port from the hexagonal architecture.
type Deps struct {
	Engine        EngineService
	Workflows     workflow.WorkflowRepository
	Instances     instance.InstanceRepository
	NodeExecs     instance.NodeExecutionRepository
	Snapshots     instance.VariableSnapshotRepository
	FileReader    engine.FileReader
	EventBus      engine.EventBus
	Config        *config.Config
	QueueManager  QueueManagerPort
	QueueRepo     instance.QueueRepository
	Notifications notification.Repository
}
