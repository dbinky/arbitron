package sqlite

import (
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
)

var _ instance.InstanceRepository = (*InstanceRepo)(nil)
var _ instance.NodeExecutionRepository = (*NodeExecutionRepo)(nil)
var _ instance.VariableSnapshotRepository = (*VariableSnapshotRepo)(nil)
var _ instance.UserRepository = (*UserRepo)(nil)
var _ instance.QueueRepository = (*QueueRepo)(nil)
var _ notification.Repository = (*NotificationRepo)(nil)
