package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// PublisherLookup resolves the publication record for a shared workflow name,
// including its visibility level. Backed by the published_workflows repo. A nil
// lookup yields no publications (local/read-only mode treats shared as public).
type PublisherLookup interface {
	// PublisherOf returns just the publisher id; retained for callers that only
	// need attribution. (false, nil) when the name is not published.
	PublisherOf(ctx context.Context, sharedName string) (uuid.UUID, bool, error)
	// PublicationOf returns the full publication (publisher + visibility +
	// department). (nil-value, false, nil) when the name is not published.
	PublicationOf(ctx context.Context, sharedName string) (Publication, bool, error)
}

// PublishedStore is the read+write authority for the shared workspace's
// publication records, used by Publish/SetVisibility. A WorkflowService
// constructed with only a PublisherLookup (no write side) supports List/Load
// but rejects Publish.
type PublishedStore interface {
	PublisherLookup
	Upsert(ctx context.Context, name string, publisher uuid.UUID, sourceName string, visibility Visibility, department string) error
	Delete(ctx context.Context, name string) error
}

// WorkflowService resolves storage scope and authorization from the context
// identity, per the Phase 1 decision that authz lives in domain services.
// Create/Update/Delete always target the caller's private scope; reads try the
// caller's private space first then shared; shared workflows are read-only
// except via publish.
type WorkflowService struct {
	repo       WorkflowRepository
	publishers PublisherLookup // may be nil
	// published is the write side, set when the supplied lookup also implements
	// PublishedStore; nil disables Publish.
	published PublishedStore
	audit     *audit.Service // optional; nil-safe best-effort audit emitter
}

// SetAudit wires the best-effort audit emitter. When unset (nil), no audit
// events are recorded (local/unconfigured deployments behave as today).
func (s *WorkflowService) SetAudit(a *audit.Service) {
	s.audit = a
}

// auditDetail marshals a small map to compact JSON for an audit event's detail,
// falling back to "{}" on the (practically impossible) marshal error so audit
// recording is never blocked.
func auditDetail(m map[string]string) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// NewWorkflowService builds a WorkflowService over the given repository and an
// optional publisher lookup (nil is allowed — shared list entries then carry no
// publisher). When the lookup also satisfies PublishedStore, Publish is enabled.
func NewWorkflowService(repo WorkflowRepository, publishers PublisherLookup) *WorkflowService {
	s := &WorkflowService{repo: repo, publishers: publishers}
	if store, ok := publishers.(PublishedStore); ok {
		s.published = store
	}
	return s
}

// caller extracts the authenticated identity from ctx, or the canonical
// identity.ErrUnauthenticated when none is present, so a missing identity maps to
// 401 (not 400) at the adapter seam.
func caller(ctx context.Context) (identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Identity{}, fmt.Errorf("%w: no identity in context", identity.ErrUnauthenticated)
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

// auditingFilter returns a SharedVisibilityFilter bound to id, or nil when no
// publisher lookup is configured (local/read-only ⇒ all shared visible). When a
// shared read is granted ONLY because the caller is admin, a best-effort
// admin.override audit event is recorded against the shared name.
func (s *WorkflowService) auditingFilter(id identity.Identity) SharedVisibilityFilter {
	if s.publishers == nil {
		return nil
	}
	return func(ctx context.Context, name string) (bool, error) {
		pub, ok, err := s.publishers.PublicationOf(ctx, name)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil // unpublished shared folder ⇒ open, nothing to audit
		}
		if !CanSeePublication(id, pub) {
			return false, nil
		}
		if adminOverrode(id, pub) {
			s.audit.Record(ctx, audit.ActionAdminOverride, name, auditDetail(map[string]string{
				"visibility": string(pub.Visibility),
				"department": pub.Department,
			}))
		}
		return true, nil
	}
}

// Load returns a workflow visible to the caller (private first, then shared).
func (s *WorkflowService) Load(ctx context.Context, name string) (*Workflow, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	wf, _, err := LoadVisibleFiltered(ctx, s.repo, id.UserID, name, s.auditingFilter(id))
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
	files, _, err := ListFilesVisibleFiltered(ctx, s.repo, id.UserID, name, s.auditingFilter(id))
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
		// Resolve the publication once; use it for both gating and tagging.
		var pub Publication
		published := false
		if s.publishers != nil {
			if p, ok, perr := s.publishers.PublicationOf(ctx, sum.Name); perr == nil && ok {
				pub, published = p, true
			}
		}
		if published {
			if !CanSeePublication(id, pub) {
				continue // hide department workflows from non-members
			}
			if adminOverrode(id, pub) {
				s.audit.Record(ctx, audit.ActionAdminOverride, sum.Name, auditDetail(map[string]string{
					"visibility": string(pub.Visibility),
					"department": pub.Department,
				}))
			}
			sum.Publisher = pub.Publisher.String()
			sum.Visibility = string(pub.Visibility)
			sum.Department = pub.Department
		} else {
			// Unpublished shared folder (pre-model) or local mode: treat as public.
			sum.Visibility = string(VisibilityPublic)
		}
		out = append(out, sum)
	}

	return out, nil
}

// Publish copies the caller's private workflow into the shared scope under
// publishedName and records the caller as publisher. Only the original
// publisher may overwrite an existing published name.
//
// ORDERING INVARIANT: the shared tree is copied FIRST (repo.CopyTree), then the
// publication is recorded (published.Upsert). These are two separate stores with
// no shared transaction, so the failure handling is path-dependent:
//
//   - FRESH publish (!overwrite): if Upsert fails after a successful copy, the
//     copied tree is compensated with repo.Delete — there was nothing at the
//     destination before, so deleting it restores the prior state cleanly and
//     avoids an orphaned, unattributed shared copy.
//   - OVERWRITE (re-publish by the same publisher): a CopyTree(overwrite=true)
//     has already replaced a previously-good shared copy. If Upsert then fails we
//     must NOT delete — that would destroy the shared copy entirely (data loss).
//     The error is surfaced loudly so the operator can retry the record.
func (s *WorkflowService) Publish(ctx context.Context, privateName, publishedName string, visibility Visibility, department string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if s.published == nil {
		return fmt.Errorf("%w: publishing is not configured", ErrUnauthorized)
	}
	if err := authorizePublish(id, visibility, department); err != nil {
		return err
	}

	// Source must exist privately.
	if ok, err := s.repo.Exists(ctx, Private(id.UserID), privateName); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, privateName)
	}

	// Publisher-only-overwrite check.
	pubID, published, err := s.published.PublisherOf(ctx, publishedName)
	if err != nil {
		return err
	}
	overwrite := false
	if published {
		if pubID != id.UserID {
			return fmt.Errorf("%w: published name %q owned by another user", ErrUnauthorized, publishedName)
		}
		overwrite = true // re-publish by the same publisher
	}

	if err := s.repo.CopyTree(ctx, Private(id.UserID), privateName, Shared(), publishedName, overwrite); err != nil {
		return err
	}
	// Record the publication after the folder is in place (copy-then-record).
	if err := s.published.Upsert(ctx, publishedName, id.UserID, privateName, visibility, department); err != nil {
		if !overwrite {
			// FRESH path: compensate by deleting the just-copied tree. The
			// destination was empty before, so this restores the prior state. The
			// rollback is best-effort; if it also fails, surface the original Upsert
			// error joined with the rollback error so neither is lost.
			if delErr := s.repo.Delete(ctx, Shared(), publishedName); delErr != nil {
				return errors.Join(
					fmt.Errorf("recording publication for %q: %w", publishedName, err),
					fmt.Errorf("rolling back copied shared tree: %w", delErr),
				)
			}
		}
		// OVERWRITE path (or after a successful fresh rollback): surface the error
		// loudly; on overwrite we deliberately do NOT delete the shared copy.
		return fmt.Errorf("recording publication for %q: %w", publishedName, err)
	}
	// Best-effort audit of the successful publish (nil-safe per audit.Service).
	s.audit.Record(ctx, audit.ActionWorkflowPublish, publishedName, auditDetail(map[string]string{
		"to":         string(visibility),
		"department": department,
	}))
	return nil
}

// Copy duplicates a shared workflow into the caller's private scope. The copy
// is fully independent — no back-link to the shared original.
func (s *WorkflowService) Copy(ctx context.Context, sharedName, newPrivateName string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if ok, err := s.repo.Exists(ctx, Private(id.UserID), newPrivateName); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, newPrivateName)
	}
	// A user may only copy a shared workflow they can see.
	if s.publishers != nil {
		pub, ok, err := s.publishers.PublicationOf(ctx, sharedName)
		if err != nil {
			return err
		}
		if ok {
			if !CanSeePublication(id, pub) {
				return fmt.Errorf("%w: %s", ErrNotFound, sharedName)
			}
			if adminOverrode(id, pub) {
				s.audit.Record(ctx, audit.ActionAdminOverride, sharedName, auditDetail(map[string]string{
					"visibility": string(pub.Visibility),
					"department": pub.Department,
				}))
			}
		}
	}
	return s.repo.CopyTree(ctx, Shared(), sharedName, Private(id.UserID), newPrivateName, false)
}

// authorizePublish validates the requested visibility/department against the
// caller's role per spec §Workflow Visibility:
//   - department: caller must be a MEMBER of department; department required.
//   - public: caller must be a LEAD (of any department) or an ADMIN.
//   - private: not a storable publication level → ErrInvalidVisibility.
func authorizePublish(id identity.Identity, visibility Visibility, department string) error {
	switch visibility {
	case VisibilityDepartment:
		if department == "" {
			return fmt.Errorf("%w: department required for department visibility", ErrInvalidVisibility)
		}
		if !id.IsAdmin() && !id.InDepartment(department) {
			return fmt.Errorf("%w: not a member of department %q", ErrUnauthorized, department)
		}
		return nil
	case VisibilityPublic:
		if id.IsAdmin() || len(id.LedDepartments) > 0 {
			return nil
		}
		return fmt.Errorf("%w: publishing to public requires a lead or admin", ErrUnauthorized)
	default:
		return fmt.Errorf("%w: %q is not a publishable visibility", ErrInvalidVisibility, visibility)
	}
}

// SetVisibility changes an existing publication's level without re-copying the
// shared tree (the metadata model makes this a cheap "column flip"). The caller
// must be the original publisher, a lead of the relevant department, or an
// admin; the target level is gated by the same rules as Publish (member→own
// department, lead/admin→public). An unknown publication is ErrNotFound.
func (s *WorkflowService) SetVisibility(ctx context.Context, publishedName string, visibility Visibility, department string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if s.published == nil {
		return fmt.Errorf("%w: publishing is not configured", ErrUnauthorized)
	}

	cur, ok, err := s.published.PublicationOf(ctx, publishedName)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, publishedName)
	}

	// Only the publisher, a lead, or an admin may mutate a publication.
	if cur.Publisher != id.UserID && !id.IsAdmin() && len(id.LedDepartments) == 0 {
		return fmt.Errorf("%w: cannot change visibility of %q", ErrUnauthorized, publishedName)
	}
	// Target level role gate (same as Publish).
	if err := authorizePublish(id, visibility, department); err != nil {
		return err
	}

	// Preserve the original publisher and source name (Publication carries
	// SourceName so the NOT NULL source_name column survives the flip); only the
	// visibility level changes.
	if err := s.published.Upsert(ctx, publishedName, cur.Publisher, cur.SourceName, visibility, department); err != nil {
		return fmt.Errorf("updating visibility for %q: %w", publishedName, err)
	}
	// Best-effort audit of the level change, carrying from/to + departments.
	s.audit.Record(ctx, audit.ActionWorkflowVisibilityChange, publishedName, auditDetail(map[string]string{
		"from":            string(cur.Visibility),
		"from_department": cur.Department,
		"to":              string(visibility),
		"to_department":   department,
	}))
	return nil
}
