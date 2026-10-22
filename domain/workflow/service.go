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
// range. It is the narrow read port the Service delegates to AFTER the
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
// publication records, used by Publish/SetVisibility. A Service
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

// Service resolves storage scope and authorization from the context
// identity, per the Phase 1 decision that authz lives in domain services.
// Create/Update/Delete always target the caller's private scope; reads try the
// caller's private space first then shared; shared workflows are read-only
// except via publish.
type Service struct {
	repo       Repository
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
	registry Registry
	// refBackfiller is the optional port BackfillReferences uses to stamp stable
	// workflow ids onto the name-keyed reference tables. When unset (nil),
	// BackfillReferences is a no-op.
	refBackfiller ReferenceBackfiller
	// activeInstances is the optional read port the admin Delete guard consults to
	// refuse deletion while non-terminal instances carry the workflow id. When
	// unset (nil), the guard is skipped (local/unconfigured mode has no registry
	// id to count by).
	activeInstances ActiveInstanceCounter
	stamper         *RevisionStamper
	mutations       MutationCoordinator
	// groupProfiles / groupFiles are the scoped checkers parallel_group writes
	// require. When unset, group writes fail with GROUP_CHECKER_UNAVAILABLE.
	groupProfiles ProfileChecker
	groupFiles    OwnerFileChecker
}

// SetRevisionStamper enables the authoring-save protocol. A stamper requires
// the same MutationCoordinator used by the repository and recovery source.
func (s *Service) SetRevisionStamper(st *RevisionStamper) { s.stamper = st }

// SetMutationCoordinator wires operation-wide source/destination locking.
func (s *Service) SetMutationCoordinator(m MutationCoordinator) { s.mutations = m }

// SetActiveInstanceCounter wires the non-terminal-instance counter consulted by
// the admin Delete guard. When unset (nil), Delete does not guard against running
// instances (local/unconfigured mode, which also has no workflow id to count by).
func (s *Service) SetActiveInstanceCounter(c ActiveInstanceCounter) {
	s.activeInstances = c
}

// SetRegistry wires the workflow identity registry (id allocation + global name
// uniqueness + name↔id resolution). When unset (nil), create/publish/load behave
// as before — name-addressed only, with no id assignment or uniqueness checks.
func (s *Service) SetRegistry(r Registry) {
	s.registry = r
}

// SetReferenceBackfiller wires the reference-table backfill port used by
// BackfillReferences. When unset (nil), BackfillReferences is a no-op.
func (s *Service) SetReferenceBackfiller(b ReferenceBackfiller) {
	s.refBackfiller = b
}

// SetAudit wires the best-effort audit emitter. When unset (nil), no audit
// events are recorded (local/unconfigured deployments behave as today).
func (s *Service) SetAudit(a *audit.Service) {
	s.audit = a
}

// SetStatsReader wires the execution-statistics read port consulted by Stats.
// When unset (nil), Stats returns ErrStatsUnavailable.
func (s *Service) SetStatsReader(r StatsReader) {
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

// NewWorkflowService builds a Service over the given repository and an
// optional publisher lookup (nil is allowed — shared list entries then carry no
// publisher). When the lookup also satisfies PublishedStore, Publish is enabled.
//
// The registry field is UNCONDITIONALLY populated: it defaults to the local-mode
// LocalRegistry Null-Object so there is a SINGLE Delete path (no nil-branch). A
// server/admin deployment overrides it with the real identity registry via
// SetRegistry; the registry-coordinated ops detect the Null-Object via
// localMode() and preserve their prior local behavior.
func NewWorkflowService(repo Repository, publishers PublisherLookup) *Service {
	s := &Service{repo: repo, publishers: publishers, registry: NewLocalRegistry(repo)}
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
func (s *Service) translateWrite(ctx context.Context, name string, err error) error {
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

// adminSharedWrite is the admin-only escape hatch from the shared-read-only rule.
// It runs the caller's PRIVATE-scope write first (privateErr is its result). When
// that write failed only because the name has no private copy but DOES exist in
// the shared scope, the resolution depends on the caller's role:
//
//   - admin: the write is re-applied to the SHARED scope (edit-in-place), so an
//     admin can fix a published workflow without first copying it private.
//   - non-admin: the prior ErrSharedReadOnly is returned unchanged — the shared
//     scope stays read-only for everyone else.
//
// Any private write that succeeded, or failed for a reason other than
// not-found-but-shared-exists, is returned verbatim (translateWrite's mapping is
// preserved for the non-admin/no-shared cases). sharedWrite performs the actual
// shared-scope mutation (Update/WriteFile against Shared()).
//
// An in-flight instance is unaffected: it executes from its own snapshot, so a
// shared edit only changes what FUTURE triggers load.
func (s *Service) adminSharedWrite(ctx context.Context, id identity.Identity, name string, privateErr error, sharedWrite func(context.Context, Scope) error) error {
	if privateErr == nil {
		return nil // the private write already succeeded
	}
	if !errors.Is(privateErr, ErrNotFound) {
		return privateErr
	}
	sharedExists, existsErr := s.repo.Exists(ctx, Shared(), name)
	if existsErr != nil || !sharedExists {
		return privateErr // no shared copy: a genuine not-found
	}
	if !id.IsAdmin() {
		// Shared copy exists but the caller is not an admin: read-only, as before.
		return fmt.Errorf("%w: %s", ErrSharedReadOnly, name)
	}
	// Admin edit-in-place against the shared scope.
	if err := sharedWrite(ctx, Shared()); err != nil {
		return err
	}
	// Best-effort audit of the admin in-place shared edit (nil-safe).
	s.audit.Record(ctx, audit.ActionWorkflowEditShared, name, auditDetail(map[string]string{
		"scope": ScopeLabelShared,
	}))
	return nil
}

// Create writes a new workflow in the caller's private scope.
//
// When a registry is wired, identity is allocated and uniqueness enforced FIRST:
// a stable id is assigned to the definition and the registry row is inserted
// before the folder is written, so a duplicate name is rejected (ErrAlreadyExists)
// WITHOUT writing any folder. If the folder write then fails, the registry row is
// rolled back so the two stores do not diverge. With no registry (local mode),
// this is the prior pure name-addressed create.
// A workflow containing parallel_group nodes is first checked by ValidateGroups;
// any hard finding returns *GroupValidationError before anything is written.
func (s *Service) Create(ctx context.Context, name string, wf *Workflow) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	// Reject invalid groups before allocating identity or claiming the name, on
	// both the stamped revision path and the legacy one. Non-group workflows
	// retain their write path without validation.
	if err := s.rejectInvalidGroups(ctx, wf, id.UserID); err != nil {
		return err
	}
	if s.stamper != nil {
		return s.createRevision(ctx, name, wf)
	}
	if s.localMode() {
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

// Update replaces an existing workflow. It targets the caller's private scope;
// when there is no private copy but a SHARED copy exists, a non-admin gets
// ErrSharedReadOnly while an ADMIN edits the shared copy in place (see
// adminSharedWrite). An in-flight instance is unaffected — it runs from its
// snapshot — so a shared edit only changes what future triggers load.
func (s *Service) Update(ctx context.Context, name string, wf *Workflow) error {
	_, err := s.updateWithGroupFindings(ctx, name, wf)
	return err
}

// updateWithGroupFindings is the shared Update path, retaining the accepted
// pre-write findings for callers that return a group view with the receipt.
// The group gate runs before either write protocol, so the stamped revision
// path (production) is gated exactly like the legacy one.
func (s *Service) updateWithGroupFindings(ctx context.Context, name string, wf *Workflow) ([]ValidationError, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	// Gate both private writes and admin shared-scope edits, checking files in
	// the scope this write lands in.
	var findings []ValidationError
	if HasGroups(wf) {
		fileOwner, err := s.groupWriteFileOwner(ctx, id, name)
		if err != nil {
			return nil, err
		}
		if findings, err = s.validateGroupsForWrite(ctx, wf, fileOwner); err != nil {
			return nil, err
		}
	}
	if s.stamper != nil {
		if err := s.updateRevision(ctx, name, wf); err != nil {
			return nil, err
		}
		return findings, nil
	}
	privateErr := s.repo.Update(ctx, Private(id.UserID), name, wf)
	if err := s.adminSharedWrite(ctx, id, name, privateErr, func(ctx context.Context, scope Scope) error {
		return s.repo.Update(ctx, scope, name, wf)
	}); err != nil {
		return nil, err
	}
	return findings, nil
}

// WriteFile writes a file into the caller's private workflow directory (an admin
// may target a shared-only workflow in place; see adminSharedWrite).
func (s *Service) WriteFile(ctx context.Context, name, rel string, content []byte) error {
	return s.withSupportingWrite(ctx, name, func(ctx context.Context) error { return s.writeFile(ctx, name, rel, content) })
}

func (s *Service) writeFile(ctx context.Context, name, rel string, content []byte) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	privateErr := s.repo.WriteFile(ctx, Private(id.UserID), name, rel, content)
	return s.adminSharedWrite(ctx, id, name, privateErr, func(ctx context.Context, scope Scope) error {
		return s.repo.WriteFile(ctx, scope, name, rel, content)
	})
}

// DeleteFile removes a file from the caller's private workflow directory.
func (s *Service) DeleteFile(ctx context.Context, name, rel string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	target := MutationTarget{Scope: Private(id.UserID), Name: name}
	return s.withMutations(ctx, []MutationTarget{target}, func(ctx context.Context) error {
		if err := s.settle(ctx, target); err != nil {
			return err
		}
		return s.translateWrite(ctx, name, s.repo.DeleteFile(ctx, target.Scope, name, rel))
	})
}

// auditingFilter returns a SharedVisibilityFilter bound to id, or nil when no
// publisher lookup is configured (local/read-only ⇒ all shared visible). When a
// shared read is granted ONLY because the caller is admin, a best-effort
// admin.override audit event is recorded against the shared name.
func (s *Service) auditingFilter(id identity.Identity) SharedVisibilityFilter {
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
// Load is contractually a QUERY: when a registry is wired and the loaded
// definition has no id, an id is resolved-or-allocated purely IN MEMORY and
// returned — Load never INSERTs a registry row or rewrites the folder as a side
// effect of a read. That keeps a reader-role caller (or a read-only DB) from
// getting a write error on a pure read, and keeps a read storm on a legacy
// id-less workflow from funnelling N concurrent Loads into folder+registry writes
// (a SQLite write-contention hotspot). The DURABLE write is owned by the startup
// BackfillRegistry, which registers and persists the id once.
func (s *Service) Load(ctx context.Context, name string) (*Workflow, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	wf, _, err := LoadVisibleFiltered(ctx, s.repo, id.UserID, name, s.auditingFilter(id))
	if err != nil {
		return nil, err
	}
	if !s.localMode() && wf.ID == uuid.Nil {
		resolvedID, err := s.resolveOrAllocateID(ctx, name)
		if err != nil {
			return nil, err
		}
		wf.ID = resolvedID
	}
	return wf, nil
}

// resolveOrAllocateID returns a stable id for an already-loaded id-less
// definition WITHOUT persisting anything: it adopts the registry id for the name
// when one already exists (e.g. registered by the startup BackfillRegistry),
// otherwise allocates a fresh id in memory and returns it. It does NOT Register
// the row or rewrite the folder — a read must not write. The durable write is the
// startup backfill's job; a transient pre-backfill load simply surfaces an
// allocated id that becomes stable once the backfill runs.
func (s *Service) resolveOrAllocateID(ctx context.Context, name string) (uuid.UUID, error) {
	existing, err := s.registry.ResolveID(ctx, name)
	if err == nil {
		return existing, nil
	}
	if errors.Is(err, ErrNotFound) {
		return uuid.New(), nil
	}
	return uuid.Nil, err
}

// resolveID returns the stable id registered for name (name→id). It is the
// internal name↔id seam later phases use; ErrNotFound when no row matches.
func (s *Service) resolveID(ctx context.Context, name string) (uuid.UUID, error) {
	return s.registry.ResolveID(ctx, name)
}

// resolveName returns the current name for id (id→name); ErrNotFound when unknown.
func (s *Service) resolveName(ctx context.Context, id uuid.UUID) (string, error) {
	return s.registry.ResolveName(ctx, id)
}

// LoadAs loads a specific user's private workflow. A caller may always load
// their own; loading another user's private workflow requires admin.
func (s *Service) LoadAs(ctx context.Context, owner uuid.UUID, name string) (*Workflow, error) {
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
func (s *Service) ListFiles(ctx context.Context, name string) ([]string, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	files, _, err := ListFilesVisibleFiltered(ctx, s.repo, id.UserID, name, s.auditingFilter(id))
	return files, err
}

// NameSetChecker is a WorkflowChecker over a fixed set of names. It is a set
// rather than a per-name lookup so validating a workflow with many spawn nodes
// costs ONE listing, not one query per node.
type NameSetChecker map[string]bool

// WorkflowExists implements WorkflowChecker.
func (c NameSetChecker) WorkflowExists(name string) bool { return c[name] }

// VisibleNames returns the set of workflow names the caller can resolve, for
// author-time validation of spawn_workflow references.
//
// It is built from List, which applies the SAME private-then-shared resolution
// and publication gate the spawn path's ChildInputs uses — so a name this
// reports as unknown is a name the spawn would fail to load, and a workflow the
// caller cannot see is reported as absent rather than as forbidden (existence
// is never leaked).
func (s *Service) VisibleNames(ctx context.Context) (NameSetChecker, error) {
	sums, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(NameSetChecker, len(sums))
	for _, sum := range sums {
		out[sum.Name] = true
	}
	return out, nil
}

// List returns the caller's private workflows (scope=private) concatenated with
// all shared workflows (scope=shared, publisher filled when a lookup is set).
// A private and shared workflow sharing a name both appear, distinguished by scope.
func (s *Service) List(ctx context.Context) ([]WorkflowSummary, error) {
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
func (s *Service) Stats(ctx context.Context, name string, from, to time.Time) (*instance.WorkflowStats, error) {
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
// Stamped saves stage the final identity/revision before visibility and retain a
// reversible tree backup until exact publication proof commits. The following
// describes only the isolated legacy path used without a stamper.
//
// LEGACY ORDERING: the shared tree is copied FIRST (repo.CopyTree), then the
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
func (s *Service) Publish(ctx context.Context, privateName, publishedName string, visibility Visibility, department string) error {
	if s.stamper != nil {
		return s.publishRevision(ctx, privateName, publishedName, visibility, department)
	}
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

	// Global uniqueness: if the registry already holds publishedName but no
	// publication record exists for it, the name is registered as a private
	// workflow in another scope — reject the cross-scope collision before writing
	// anything.
	if !s.localMode() {
		if _, rerr := s.registry.ResolveID(ctx, publishedName); rerr == nil {
			_, isPub, perr := s.published.PublisherOf(ctx, publishedName)
			if perr != nil {
				return perr
			}
			if !isPub {
				return fmt.Errorf("%w: %s", ErrAlreadyExists, publishedName)
			}
			// isPub=true: WriteAndRecord will handle same/different publisher.
		} else if !errors.Is(rerr, ErrNotFound) {
			return rerr
		}
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
			if err := s.rejectCopiedGroups(ctx, Private(id.UserID), privateName, publishedName); err != nil {
				return err
			}
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
	if !s.localMode() {
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
func (s *Service) Copy(ctx context.Context, sharedName, newPrivateName string) error {
	if s.stamper != nil {
		return s.copyRevision(ctx, sharedName, newPrivateName)
	}
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
	if err := s.rejectCopiedGroups(ctx, Shared(), sharedName, newPrivateName); err != nil {
		return err
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
func (s *Service) SetVisibility(ctx context.Context, publishedName string, visibility Visibility, department string) error {
	target := MutationTarget{Scope: Shared(), Name: publishedName}
	return s.withMutations(ctx, []MutationTarget{target}, func(ctx context.Context) error {
		if _, err := caller(ctx); err != nil {
			return err
		}
		if err := s.settle(ctx, target); err != nil {
			return err
		}
		return s.setVisibility(ctx, publishedName, visibility, department)
	})
}

func (s *Service) setVisibility(ctx context.Context, publishedName string, visibility Visibility, department string) error {
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
