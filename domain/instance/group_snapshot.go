package instance

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrUnsupportedSnapshotContent is the identity behind
// engine.ErrUnsupportedSnapshotContent (domain/engine/group_workspace.go
// re-exports this exact value, so errors.Is matches either name).
//
// It is declared HERE, in the group-run domain, rather than only in
// domain/engine because the adapters that produce it cannot import
// domain/engine: domain/engine's own internal tests import
// adapter/filesystem (template_include_samples_test.go), so an
// adapter/filesystem -> domain/engine import would be an import cycle.
// domain/instance is the lowest package both sides already depend on, and it
// owns GroupRun.WorkspaceSnapshot, the handle a snapshot produces.
var ErrUnsupportedSnapshotContent = errors.New("unsupported content in working area snapshot")

// GroupSnapshotInfo identifies a published snapshot or interrupted build.
// Handle is opaque; GroupRunID names the owner without parsing adapter paths.
type GroupSnapshotInfo struct {
	GroupRunID uuid.UUID
	Handle     string
	ModTime    time.Time
}
