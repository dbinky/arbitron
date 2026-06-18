package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// MaxStatsWindow caps the from/to span a single workflow-stats query may scan.
// It is defense-in-depth against accidental or abusive unbounded aggregations
// (an inverted or enormous window otherwise produces a silent full-table scan).
const MaxStatsWindow = 366 * 24 * time.Hour

// StatsReader reads execution statistics for a single workflow over a time
// range. It is the narrow read port the WorkflowService delegates to AFTER the
// visibility gate has approved the caller; the reader itself performs no
// authorization (the service layer is the authorization boundary, not the repo).
type StatsReader interface {
	Stats(ctx context.Context, workflowName string, from time.Time, to time.Time) (*instance.WorkflowStats, error)
}

// ActiveInstanceCounter reports how many non-terminal instances carry a given
// stable workflow id. It is the narrow read port the admin Delete guard consults
// to refuse deletion of a workflow with live runs. "Non-terminal" is any state
// other than completed/failed/cancelled (i.e. queued/running/waiting/paused). A
// nil counter disables the guard (local/unconfigured mode, where the registry —
// and thus the workflow id — is also absent).
type ActiveInstanceCounter interface {
	CountActiveByWorkflowID(ctx context.Context, workflowID uuid.UUID) (int, error)
}

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
	// SetWorkflowID links a publication record to the stable workflow identity
	// (published_workflows.workflow_id). It is invoked after a successful publish
	// once the shared name's registry id is known. A Nil id is a no-op.
	SetWorkflowID(ctx context.Context, name string, id uuid.UUID) error
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
	// stats is the optional execution-statistics read port. When unset (nil),
	// Stats reports the feature is unavailable rather than panicking.
	stats StatsReader
	// registry is the optional workflow identity/uniqueness port. When unset
	// (nil), the service behaves exactly as before: purely name-addressed, no id
	// allocation, no global-uniqueness enforcement (local/unconfigured mode).
	registry WorkflowRegistry
	// refBackfiller is the optional port BackfillReferences uses to stamp stable
	// workflow ids onto the name-keyed reference tables. When unset (nil),
	// BackfillReferences is a no-op.
	refBackfiller ReferenceBackfiller
	// activeInstances is the optional read port the admin Delete guard consults to
	// refuse deletion while non-terminal instances carry the workflow id. When
	// unset (nil), the guard is skipped (local/unconfigured mode has no registry
	// id to count by).
	activeInstances ActiveInstanceCounter
}

// SetActiveInstanceCounter wires the non-terminal-instance counter consulted by
// the admin Delete guard. When unset (nil), Delete does not guard against running
// instances (local/unconfigured mode, which also has no workflow id to count by).
func (s *WorkflowService) SetActiveInstanceCounter(c ActiveInstanceCounter) {
	s.activeInstances = c
}

// SetRegistry wires the workflow identity registry (id allocation + global name
// uniqueness + name↔id resolution). When unset (nil), create/publish/load behave
// as before — name-addressed only, with no id assignment or uniqueness checks.
func (s *WorkflowService) SetRegistry(r WorkflowRegistry) {
	s.registry = r
}

// SetReferenceBackfiller wires the reference-table backfill port used by
// BackfillReferences. When unset (nil), BackfillReferences is a no-op.
func (s *WorkflowService) SetReferenceBackfiller(b ReferenceBackfiller) {
	s.refBackfiller = b
}

// SetAudit wires the best-effort audit emitter. When unset (nil), no audit
// events are recorded (local/unconfigured deployments behave as today).
func (s *WorkflowService) SetAudit(a *audit.Service) {
	s.audit = a
}

// SetStatsReader wires the execution-statistics read port consulted by Stats.
// When unset (nil), Stats returns ErrStatsUnavailable.
func (s *WorkflowService) SetStatsReader(r StatsReader) {
	s.stats = r
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
//
// When a registry is wired, identity is allocated and uniqueness enforced FIRST:
// a stable id is assigned to the definition and the registry row is inserted
// before the folder is written, so a duplicate name is rejected (ErrAlreadyExists)
// WITHOUT writing any folder. If the folder write then fails, the registry row is
// rolled back so the two stores do not diverge. With no registry (local mode),
// this is the prior pure name-addressed create.
func (s *WorkflowService) Create(ctx context.Context, name string, wf *Workflow) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if s.registry == nil {
		return s.repo.Create(ctx, Private(id.UserID), name, wf)
	}

	// Allocate identity, then claim the name in the registry FIRST. The registry's
	// UNIQUE(name) is the global-uniqueness authority; on a duplicate this returns
	// ErrAlreadyExists and we abort before touching the folder.
	if wf.ID == uuid.Nil {
		wf.ID = uuid.New()
	}
	if err := s.registry.Register(ctx, RegistryEntry{
		ID:      wf.ID,
		Name:    name,
		Scope:   ScopePrivate,
		OwnerID: id.UserID,
	}); err != nil {
		return err
	}
	if err := s.repo.Create(ctx, Private(id.UserID), name, wf); err != nil {
		// Folder write failed after the registry claim; roll the row back so a
		// future create of the same name is not blocked by an orphan row.
		_ = s.registry.Delete(ctx, wf.ID)
		return err
	}
	return nil
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
		if AdminOverrode(id, pub) {
			s.audit.Record(ctx, audit.ActionAdminOverride, name, auditDetail(map[string]string{
				"visibility": string(pub.Visibility),
				"department": pub.Department,
			}))
		}
		return true, nil
	}
}

// Load returns a workflow visible to the caller (private first, then shared).
//
// Lazy backfill: when a registry is wired and the loaded definition has no id, an
// id is allocated, persisted into the (writable) folder and registered, so legacy
// id-less workflows migrate transparently on first load and keep a stable id
// across reloads. Only the caller's own private workflows are persisted in place
// (shared workflows are read-only and are reconciled by the startup backfill).
func (s *WorkflowService) Load(ctx context.Context, name string) (*Workflow, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	wf, scope, err := LoadVisibleFiltered(ctx, s.repo, id.UserID, name, s.auditingFilter(id))
	if err != nil {
		return nil, err
	}
	if s.registry != nil && wf.ID == uuid.Nil {
		if err := s.ensureID(ctx, scope, name, wf); err != nil {
			return nil, err
		}
	}
	return wf, nil
}

// ensureID backfills a missing id on an already-loaded definition: it adopts an
// existing registry id for the name if one is present, otherwise allocates and
// registers a new one. The id is mutated onto wf and, for the caller's own
// private scope, persisted back into the folder so it is stable across reloads.
// A nil registry makes this a no-op (handled by the caller).
func (s *WorkflowService) ensureID(ctx context.Context, scope Scope, name string, wf *Workflow) error {
	// Adopt an existing registry id for this name, if any (e.g. registered by the
	// startup backfill while the folder stayed id-less).
	if existing, err := s.registry.ResolveID(ctx, name); err == nil {
		wf.ID = existing
	} else if errors.Is(err, ErrNotFound) {
		// No row yet: allocate and register one.
		wf.ID = uuid.New()
		if regErr := s.registry.Register(ctx, RegistryEntry{
			ID:      wf.ID,
			Name:    name,
			Scope:   scope.Kind,
			OwnerID: scope.OwnerID,
		}); regErr != nil {
			// A concurrent backfill may have just inserted the row; adopt it.
			if errors.Is(regErr, ErrAlreadyExists) {
				if adopted, rerr := s.registry.ResolveID(ctx, name); rerr == nil {
					wf.ID = adopted
				} else {
					return rerr
				}
			} else {
				return regErr
			}
		}
	} else {
		return err
	}
	// Persist the id back into the folder, but only for the caller's own private
	// workflow (shared is read-only here; startup backfill persists shared ids).
	if scope.Kind == ScopePrivate {
		if err := s.repo.Update(ctx, scope, name, wf); err != nil {
			return err
		}
	}
	return nil
}

// resolveID returns the stable id registered for name (name→id). It is the
// internal name↔id seam later phases use; ErrNotFound when no row matches.
func (s *WorkflowService) resolveID(ctx context.Context, name string) (uuid.UUID, error) {
	return s.registry.ResolveID(ctx, name)
}

// resolveName returns the current name for id (id→name); ErrNotFound when unknown.
func (s *WorkflowService) resolveName(ctx context.Context, id uuid.UUID) (string, error) {
	return s.registry.ResolveName(ctx, id)
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
			if AdminOverrode(id, pub) {
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

// Stats returns execution statistics for a workflow the caller is allowed to
// see. It is the authorization boundary for the stats read path: the caller is
// resolved from the context, the name is gated through the SAME visibility
// predicate as Load/List (private scope first, then shared via CanSeePublication
// in auditingFilter), and only an authorized caller's request reaches the stats
// reader. A name the caller cannot see returns ErrNotFound — NOT ErrUnauthorized
// — so the endpoint is not an existence oracle across user/department boundaries.
//
// The from/to window is validated (rejecting an inverted range or one wider than
// MaxStatsWindow) before any aggregation runs, as defense-in-depth against
// unbounded scans.
func (s *WorkflowService) Stats(ctx context.Context, name string, from, to time.Time) (*instance.WorkflowStats, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if s.stats == nil {
		return nil, ErrStatsUnavailable
	}
	if from.After(to) {
		return nil, fmt.Errorf("%w: from %s is after to %s", ErrInvalidStatsRange, from, to)
	}
	if to.Sub(from) > MaxStatsWindow {
		return nil, fmt.Errorf("%w: window exceeds the %s maximum", ErrInvalidStatsRange, MaxStatsWindow)
	}

	// Gate: resolve the name through the caller's visible scopes (private first,
	// then shared subject to CanSeePublication). A name the caller cannot see
	// yields ErrNotFound and never reaches the stats reader.
	if _, _, err := LoadVisibleFiltered(ctx, s.repo, id.UserID, name, s.auditingFilter(id)); err != nil {
		return nil, err
	}

	return s.stats.Stats(ctx, name, from, to)
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
	// Fresh publish: the source is the caller's own private content, which belongs
	// to no department yet, so ownerDept is "".
	if err := authorizePublish(id, visibility, department, ""); err != nil {
		return err
	}

	// Source must exist privately.
	if ok, err := s.repo.Exists(ctx, Private(id.UserID), privateName); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, privateName)
	}

	// The write-tree + record-publication + rollback + idempotency invariant lives
	// in WriteAndRecord; Publish supplies the private-source copy as its write step.
	err = WriteAndRecord(ctx, s.published, PublishRequest{
		PublishedName: publishedName,
		Publisher:     id.UserID,
		SourceName:    privateName,
		Visibility:    visibility,
		Department:    department,
		WriteTree: func(ctx context.Context, overwrite bool) error {
			return s.repo.CopyTree(ctx, Private(id.UserID), privateName, Shared(), publishedName, overwrite)
		},
		Rollback: func(ctx context.Context) error {
			return s.repo.Delete(ctx, Shared(), publishedName)
		},
	})
	if err != nil {
		return err
	}
	// Register the shared name's identity if it is not already registered. A
	// re-publish (overwrite) by the same publisher keeps its existing shared row,
	// so an ErrAlreadyExists here is the reconciled state, not a failure. The
	// published_workflows store remains the publisher-overwrite authority. The
	// resolved/allocated id is then linked onto the publication record so the
	// published_workflows row references the workflow by its stable identity.
	if s.registry != nil {
		publishedID, rerr := s.registry.ResolveID(ctx, publishedName)
		if errors.Is(rerr, ErrNotFound) {
			publishedID = uuid.New()
			if regErr := s.registry.Register(ctx, RegistryEntry{
				ID:    publishedID,
				Name:  publishedName,
				Scope: ScopeShared,
			}); regErr != nil {
				if !errors.Is(regErr, ErrAlreadyExists) {
					return regErr
				}
				// A concurrent publish just registered it; adopt the existing id.
				if adopted, aerr := s.registry.ResolveID(ctx, publishedName); aerr == nil {
					publishedID = adopted
				}
			}
		} else if rerr != nil {
			return rerr
		}
		// Link the publication record to the stable id (best-effort within the
		// publish transaction's success path: the tree + record are already in
		// place, so a stamp failure surfaces loudly rather than corrupting state).
		if publishedID != uuid.Nil {
			if setErr := s.published.SetWorkflowID(ctx, publishedName, publishedID); setErr != nil {
				return fmt.Errorf("linking publication %q to workflow id: %w", publishedName, setErr)
			}
		}
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
			if AdminOverrode(id, pub) {
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
//   - public: ADMIN, or a LEAD of the workflow's OWN department (ownerDept). When
//     ownerDept is "" (a fresh publish of the caller's own private content, which
//     belongs to no department yet), any lead may make their own content public —
//     there is no foreign department being escalated.
//   - private: not a storable publication level → ErrInvalidVisibility.
//
// ownerDept is the department the workflow currently belongs to. It scopes the
// public lead-check so a lead of department A cannot push a department-B
// publication org-wide (PR #21, cross-department visibility tampering).
func authorizePublish(id identity.Identity, visibility Visibility, department, ownerDept string) error {
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
		if id.IsAdmin() {
			return nil
		}
		// A lead may take a workflow public only if they lead the department it
		// currently belongs to. ownerDept == "" means fresh public publish of the
		// caller's own content (no existing department), so any lead qualifies.
		if ownerDept == "" {
			if len(id.LedDepartments) > 0 {
				return nil
			}
		} else if id.LeadsDepartment(ownerDept) {
			return nil
		}
		return fmt.Errorf("%w: publishing to public requires admin or a lead of the workflow's department", ErrUnauthorized)
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

	// Only the publisher, a lead of THIS publication's department, or an admin may
	// mutate it. Scoping to LeadsDepartment(cur.Department) — not "leads SOMETHING"
	// — stops a lead of department A from releveling a department-B publication
	// (PR #21, cross-department visibility tampering).
	if cur.Publisher != id.UserID && !id.IsAdmin() && !id.LeadsDepartment(cur.Department) {
		return fmt.Errorf("%w: cannot change visibility of %q", ErrUnauthorized, publishedName)
	}
	// Target level role gate (same as Publish). The workflow's own department
	// (cur.Department) scopes the public lead-check so only a lead of THAT
	// department (or an admin) may take it org-wide.
	if err := authorizePublish(id, visibility, department, cur.Department); err != nil {
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
