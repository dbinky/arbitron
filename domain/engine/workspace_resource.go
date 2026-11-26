package engine

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
)

// WorkspaceResource restores committed files under an exclusive resource claim.
// Commit publishes a mutation and releases the claim; Release also permits
// read-only users and unsuccessful mutations to relinquish ownership.
type WorkspaceResource interface {
	Acquire(context.Context, uuid.UUID, string, string) (ResourceFenceRef, error)
	Commit(context.Context, ResourceFenceRef, uuid.UUID, string) error
	Release(context.Context, ResourceFenceRef) error
}

var ErrWorkspaceBusy = errors.New("engine: a step of the run is using its working area")

// SetWorkspaceResource fences snapshots against concurrent workspace writers.
func (l *GroupLauncher) SetWorkspaceResource(r WorkspaceResource) { l.resource = r }

// SetWorkspaceResource restores the task's committed files before finalization.
func (f *GroupFileFinalizer) SetWorkspaceResource(r WorkspaceResource) { f.resource = r }

// SetWorkspaceResource publishes gathered files through the resource committer.
func (g *FileGatherer) SetWorkspaceResource(r WorkspaceResource) { g.resource = r }

func releaseWorkspaceResource(ctx context.Context, r WorkspaceResource, claim ResourceFenceRef) {
	if err := r.Release(context.WithoutCancel(ctx), claim); err != nil {
		slog.Error("releasing group workspace resource", "resource", claim.Key, "error", err)
	}
}
