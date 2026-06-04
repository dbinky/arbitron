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

// LoadVisible loads a workflow by name from the caller's visible scopes
// (private first, then shared), returning the first hit and the scope it
// came from. ErrNotFound if neither has it. A non-ErrNotFound error from any
// probe is returned immediately (not masked by the shared fallback).
func LoadVisible(ctx context.Context, repo WorkflowRepository, owner uuid.UUID, name string) (*Workflow, Scope, error) {
	for _, s := range ReadScopes(owner) {
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
	for _, s := range ReadScopes(owner) {
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
