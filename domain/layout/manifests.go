package layout

import (
	"context"
	"errors"

	"github.com/arbitron/arbitron/domain/workflow"
)

// ManifestSource supplies the current revision only, never replacement job input.
type ManifestSource interface {
	CurrentRevision(context.Context, workflow.Scope, string) (string, bool, error)
}

// RepositoryManifests reads tokens without enumerating or modifying workflows.
type RepositoryManifests struct{ repo workflow.Repository }

func NewRepositoryManifests(repo workflow.Repository) *RepositoryManifests {
	return &RepositoryManifests{repo: repo}
}
func (m *RepositoryManifests) CurrentRevision(ctx context.Context, scope workflow.Scope, name string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	wf, err := m.repo.Load(ctx, scope, name)
	if errors.Is(err, workflow.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	revision := wf.SavedRevision()
	return revision, revision != "", nil
}

var _ ManifestSource = (*RepositoryManifests)(nil)
