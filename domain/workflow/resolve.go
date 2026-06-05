package workflow

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ReadScopes returns the ordered scopes a read should try for the given owner:
// the owner's private space first, then shared.
func ReadScopes(owner uuid.UUID) []Scope {
	return []Scope{Private(owner), Shared()}
}

// SharedVisibilityFilter reports whether the caller may see the given shared
// workflow name. A nil filter disables shared-scope gating (every shared name
// is visible) for callers that have no publication lookup.
type SharedVisibilityFilter func(ctx context.Context, name string) (bool, error)

// LoadVisible loads a workflow by name from the caller's visible scopes
// (private first, then shared), returning the first hit and the scope it
// came from. ErrNotFound if neither has it. A non-ErrNotFound error from any
// probe is returned immediately (not masked by the shared fallback).
func LoadVisible(ctx context.Context, repo WorkflowRepository, owner uuid.UUID, name string) (*Workflow, Scope, error) {
	return LoadVisibleFiltered(ctx, repo, owner, name, nil)
}

// LoadVisibleFiltered behaves like LoadVisible but, when the resolution falls
// through to the shared scope, requires the filter to approve the name. A
// disapproved shared workflow returns ErrNotFound — never revealing that a
// department workflow exists to an outsider.
func LoadVisibleFiltered(ctx context.Context, repo WorkflowRepository, owner uuid.UUID, name string, filter SharedVisibilityFilter) (*Workflow, Scope, error) {
	for _, s := range ReadScopes(owner) {
		if s.Kind == ScopeShared && filter != nil {
			ok, err := filter(ctx, name)
			if err != nil {
				return nil, Scope{}, err
			}
			if !ok {
				continue // treat as absent: do not leak existence
			}
		}
		wf, err := repo.Load(ctx, s, name)
		if err == nil {
			return wf, s, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, Scope{}, err
		}
	}
	return nil, Scope{}, ErrNotFound
}

// ListFilesVisible lists files for a workflow from the caller's visible scopes
// (private first, then shared), returning the first scope that has the
// workflow. ErrNotFound if neither has it.
func ListFilesVisible(ctx context.Context, repo WorkflowRepository, owner uuid.UUID, name string) ([]string, Scope, error) {
	return ListFilesVisibleFiltered(ctx, repo, owner, name, nil)
}

// ListFilesVisibleFiltered behaves like ListFilesVisible but applies the shared
// visibility filter when falling through to the shared scope; a disapproved
// shared workflow returns ErrNotFound (no existence leak).
func ListFilesVisibleFiltered(ctx context.Context, repo WorkflowRepository, owner uuid.UUID, name string, filter SharedVisibilityFilter) ([]string, Scope, error) {
	for _, s := range ReadScopes(owner) {
		if s.Kind == ScopeShared && filter != nil {
			ok, err := filter(ctx, name)
			if err != nil {
				return nil, Scope{}, err
			}
			if !ok {
				continue
			}
		}
		files, err := repo.ListFiles(ctx, s, name)
		if err == nil {
			return files, s, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, Scope{}, err
		}
	}
	return nil, Scope{}, ErrNotFound
}
