package postgres

import "github.com/arbitron/arbitron/domain/instance"

// Compile-time interface satisfaction checks. These are enforced by `go build`
// (not just `go test`) because they live in a non-test file.
var _ instance.InstanceRepository = (*InstanceRepo)(nil)
var _ instance.NodeExecutionRepository = (*NodeExecutionRepo)(nil)
var _ instance.VariableSnapshotRepository = (*VariableSnapshotRepo)(nil)
var _ instance.UserRepository = (*UserRepo)(nil)
