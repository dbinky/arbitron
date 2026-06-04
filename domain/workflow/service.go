package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// PublisherLookup resolves the publisher id for a shared workflow name. Backed
// by the published_workflows repo (Task 4). A nil lookup yields empty publishers.
type PublisherLookup interface {
	PublisherOf(ctx context.Context, sharedName string) (uuid.UUID, bool, error)
}

// WorkflowService resolves storage scope and authorization from the context
// identity, per the Phase 1 decision that authz lives in domain services.
// Create/Update/Delete always target the caller's private scope; reads try the
// caller's private space first then shared; shared workflows are read-only
// except via publish.
type WorkflowService struct {
	repo       WorkflowRepository
	publishers PublisherLookup // may be nil
}

// NewWorkflowService builds a WorkflowService over the given repository and an
// optional publisher lookup (nil is allowed — shared list entries then carry no
// publisher).
func NewWorkflowService(repo WorkflowRepository, publishers PublisherLookup) *WorkflowService {
	return &WorkflowService{repo: repo, publishers: publishers}
}

// caller extracts the authenticated identity from ctx, or an ErrInvalidName-
// wrapped error when none is present.
func caller(ctx context.Context) (identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Identity{}, fmt.Errorf("%w: no identity in context", ErrInvalidName)
	}
	return id, nil
}

// translateWrite maps a private-scope ErrNotFound into ErrSharedReadOnly when
// the name exists in the shared scope, so an attempt to mutate a shared-only
// workflow is informative rather than a bare not-found.
func (s *WorkflowService) translateWrite(ctx context.Context, name string, err error) error {
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	sharedExists, existsErr := s.repo.Exists(ctx, Shared(), name)
	if existsErr != nil {
		return err
	}
	if sharedExists {
		return fmt.Errorf("%w: %s", ErrSharedReadOnly, name)
	}
	return err
}

// Create writes a new workflow in the caller's private scope.
func (s *WorkflowService) Create(ctx context.Context, name string, wf *Workflow) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	return s.repo.Create(ctx, Private(id.UserID), name, wf)
}

// Update replaces an existing workflow in the caller's private scope. An
// attempt to edit a shared-only workflow returns ErrSharedReadOnly.
func (s *WorkflowService) Update(ctx context.Context, name string, wf *Workflow) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	return s.translateWrite(ctx, name, s.repo.Update(ctx, Private(id.UserID), name, wf))
}

// Delete removes a workflow from the caller's private scope.
func (s *WorkflowService) Delete(ctx context.Context, name string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	return s.translateWrite(ctx, name, s.repo.Delete(ctx, Private(id.UserID), name))
}

// SaveNodePositions persists node layout in the caller's private scope.
func (s *WorkflowService) SaveNodePositions(ctx context.Context, name string, p map[string]NodePosition) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	return s.translateWrite(ctx, name, s.repo.SaveNodePositions(ctx, Private(id.UserID), name, p))
}

// WriteFile writes a file into the caller's private workflow directory.
func (s *WorkflowService) WriteFile(ctx context.Context, name, rel string, content []byte) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	return s.translateWrite(ctx, name, s.repo.WriteFile(ctx, Private(id.UserID), name, rel, content))
}

// DeleteFile removes a file from the caller's private workflow directory.
func (s *WorkflowService) DeleteFile(ctx context.Context, name, rel string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	return s.translateWrite(ctx, name, s.repo.DeleteFile(ctx, Private(id.UserID), name, rel))
}

// Load returns a workflow visible to the caller (private first, then shared).
func (s *WorkflowService) Load(ctx context.Context, name string) (*Workflow, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	wf, _, err := LoadVisible(ctx, s.repo, id.UserID, name)
	return wf, err
}

// LoadAs loads a specific user's private workflow. A caller may always load
// their own; loading another user's private workflow requires admin.
func (s *WorkflowService) LoadAs(ctx context.Context, owner uuid.UUID, name string) (*Workflow, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if owner != id.UserID && !id.IsAdmin() {
		return nil, fmt.Errorf("%w: cannot load another user's private workflow", ErrUnauthorized)
	}
	return s.repo.Load(ctx, Private(owner), name)
}

// ListFiles lists files for a workflow visible to the caller (private then shared).
func (s *WorkflowService) ListFiles(ctx context.Context, name string) ([]string, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	files, _, err := ListFilesVisible(ctx, s.repo, id.UserID, name)
	return files, err
}

// List returns the caller's private workflows (scope=private) concatenated with
// all shared workflows (scope=shared, publisher filled when a lookup is set).
// A private and shared workflow sharing a name both appear, distinguished by scope.
func (s *WorkflowService) List(ctx context.Context) ([]WorkflowSummary, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	priv, err := s.repo.List(ctx, Private(id.UserID))
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowSummary, 0, len(priv))
	for _, sum := range priv {
		sum.Scope = ScopeLabelPrivate
		out = append(out, sum)
	}

	shared, err := s.repo.List(ctx, Shared())
	if err != nil {
		return nil, err
	}
	for _, sum := range shared {
		sum.Scope = ScopeLabelShared
		if s.publishers != nil {
			if pubID, ok, perr := s.publishers.PublisherOf(ctx, sum.Name); perr == nil && ok {
				sum.Publisher = pubID.String()
			}
		}
		out = append(out, sum)
	}

	return out, nil
}
