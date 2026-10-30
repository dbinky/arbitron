package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// OwnerFileChecker is the scope-aware file-existence capability group
// validation needs for resolverPromptFile. It is satisfied by
// adapter/filesystem.FileReaderAdapter and engine.FileReader. The Service binds
// the CALLER's user id as owner, so a private override file is resolved in the
// author's own workspace first, then shared — the same order the engine reads it.
type OwnerFileChecker interface {
	FileExists(owner uuid.UUID, workflowName, relativePath string) bool
}

// ErrGroupInvalid is the errors.Is target for every write rejected because of
// parallel_group findings. The concrete error is *GroupValidationError.
var ErrGroupInvalid = errors.New("parallel group definition is invalid")

// GroupValidationError carries the structured group findings that blocked a
// write. Errors holds EVERY group finding for the workflow (hard errors and
// advisories), so an adapter can render the same {"valid":false,"errors":[...]}
// shape workflow_validate uses.
type GroupValidationError struct {
	Errors []ValidationError
}

// Error lists every finding as "CODE (node N, task K[, warning])".
func (e *GroupValidationError) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return ErrGroupInvalid.Error()
	}
	parts := make([]string, 0, len(e.Errors))
	for _, ve := range e.Errors {
		var where []string
		if ve.NodeID != "" {
			where = append(where, "node "+ve.NodeID)
		}
		if ve.TaskKey != "" {
			where = append(where, "task "+ve.TaskKey)
		}
		if ve.Severity == groupAdvisorySeverity {
			where = append(where, "warning")
		}
		part := ve.Code
		if len(where) > 0 {
			part += " (" + strings.Join(where, ", ") + ")"
		}
		parts = append(parts, part)
	}
	return ErrGroupInvalid.Error() + ": " + strings.Join(parts, "; ")
}

// Is makes errors.Is(err, ErrGroupInvalid) true for a *GroupValidationError.
func (e *GroupValidationError) Is(target error) bool { return target == ErrGroupInvalid }

// SetGroupCheckers wires the checkers parallel_group writes REQUIRE: the
// resolver-profile kind lookup and the owner-aware prompt-file check. When unset
// (or set to nil), every write of a workflow containing a parallel_group node is
// rejected with GROUP_CHECKER_UNAVAILABLE — the check is never silently skipped.
// Workflows without parallel_group nodes never consult these checkers.
func (s *Service) SetGroupCheckers(profiles ProfileChecker, files OwnerFileChecker) {
	s.groupProfiles = profiles
	s.groupFiles = files
}

// ChildContracts returns a ChildContractReader bound to the identity on ctx.
//
// Each lookup resolves the child private-then-shared through LoadVisibleFiltered
// with the caller's auditingFilter — EXACTLY the path Service.Load uses — so a
// workflow the author cannot load is reported as absent (wrapping ErrNotFound)
// and existence is never leaked. A missing identity or a storage failure is a
// lookup failure (NOT ErrNotFound). Results, including failures, are cached for
// the lifetime of the returned reader only (one validation pass).
func (s *Service) ChildContracts(ctx context.Context) ChildContractReader {
	return &callerChildContracts{ctx: ctx, svc: s, cache: map[string]cachedChildContract{}}
}

type cachedChildContract struct {
	inputs []InputField
	err    error
}

type callerChildContracts struct {
	ctx   context.Context
	svc   *Service
	mu    sync.Mutex
	cache map[string]cachedChildContract
}

// ChildInputs implements ChildContractReader.
func (r *callerChildContracts) ChildInputs(name string) ([]InputField, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cache[name]
	if !ok {
		c = r.load(name)
		r.cache[name] = c
	}
	if c.err != nil {
		return nil, c.err
	}
	return append([]InputField(nil), c.inputs...), nil
}

func (r *callerChildContracts) load(name string) cachedChildContract {
	id, err := caller(r.ctx)
	if err != nil {
		return cachedChildContract{err: fmt.Errorf("child workflow %q contract: %w", name, err)}
	}
	wf, _, err := LoadVisibleFiltered(r.ctx, r.svc.repo, id.UserID, name, r.svc.auditingFilter(id))
	if err != nil {
		if errors.Is(err, ErrInvalidName) && !errors.Is(err, ErrNotFound) {
			return cachedChildContract{err: fmt.Errorf("child workflow %q: %w", name, ErrNotFound)}
		}
		return cachedChildContract{err: fmt.Errorf("child workflow %q contract: %w", name, err)}
	}
	if wf == nil {
		return cachedChildContract{err: fmt.Errorf("child workflow %q: %w", name, ErrNotFound)}
	}
	return cachedChildContract{inputs: append([]InputField(nil), wf.Inputs...)}
}

// GroupValidateOptions is the option set validate endpoints (MCP
// workflow_validate, REST POST /workflows/{name}/validate) append to Validate so
// they check groups with the same scoped capabilities the write path uses:
//   - WithWorkflowChecker(VisibleNames): the existing spawn_workflow reference
//     check; omitted when the listing fails (the endpoints' prior behaviour).
//   - WithChildContractReader(ChildContracts(ctx)): caller-scoped child inputs.
//   - WithGroupCheckersRequired(): a nil profile/file checker on a group-bearing
//     workflow is reported as GROUP_CHECKER_UNAVAILABLE.
func (s *Service) GroupValidateOptions(ctx context.Context) []ValidateOption {
	opts := make([]ValidateOption, 0, 3)
	if names, err := s.VisibleNames(ctx); err == nil {
		opts = append(opts, WithWorkflowChecker(names))
	}
	return append(opts, WithChildContractReader(s.ChildContracts(ctx)), WithGroupCheckersRequired())
}

// ValidateGroups runs Validate with the Service's group checkers and returns
// only parallel_group findings (IsGroupValidationCode), advisories included. A
// workflow without parallel_group nodes returns nil without consulting any
// checker.
//
// It deliberately does NOT add the VisibleNames spawn checker: that pass only
// produces non-group codes (filtered out below) and costs a full listing that,
// for an admin, records admin.override audit events on every write.
func (s *Service) ValidateGroups(ctx context.Context, wf *Workflow) []ValidationError {
	owner := uuid.Nil
	if id, err := caller(ctx); err == nil {
		owner = id.UserID
	}
	return s.validateGroupsAs(ctx, wf, owner)
}

// validateGroupsAs is ValidateGroups with workflow files resolved for owner
// (private-then-shared; uuid.Nil means shared only).
func (s *Service) validateGroupsAs(ctx context.Context, wf *Workflow, owner uuid.UUID) []ValidationError {
	if wf == nil || !HasGroups(wf) {
		return nil
	}
	var files FileChecker
	if s.groupFiles != nil {
		files = ownerScopedFiles{files: s.groupFiles, owner: owner}
	}
	return s.groupFindings(ctx, wf, files)
}

// groupWriteFileOwner names the scope whose files a write of name is checked
// against, mirroring the scope Create/Update write to. A private write is run by
// its author, who resolves files private-then-shared. An admin's in-place shared
// edit (adminSharedWrite) is run by other users, whose resolver reads their own
// scope then shared (engine.LoadGroupResolverPrompt with the run owner), so only
// the shared scope can vouch for its files: a file in the admin's private
// partition would validate here and fail every other runner's resolution.
func (s *Service) groupWriteFileOwner(ctx context.Context, id identity.Identity, name string) (uuid.UUID, error) {
	if !id.IsAdmin() {
		return id.UserID, nil
	}
	private, err := s.repo.Exists(ctx, Private(id.UserID), name)
	if err != nil {
		return uuid.Nil, err
	}
	if private {
		return id.UserID, nil
	}
	shared, err := s.repo.Exists(ctx, Shared(), name)
	if err != nil {
		return uuid.Nil, err
	}
	if shared {
		return uuid.Nil, nil
	}
	return id.UserID, nil
}

func (s *Service) groupFindings(ctx context.Context, wf *Workflow, files FileChecker) []ValidationError {
	if !HasGroups(wf) {
		return nil
	}
	all := Validate(wf, files, s.groupProfiles, nil,
		WithChildContractReader(s.ChildContracts(ctx)),
		WithGroupCheckersRequired(),
	)
	var out []ValidationError
	for _, ve := range all {
		if IsGroupValidationCode(ve.Code) {
			out = append(out, ve)
		}
	}
	return out
}

// rejectInvalidGroups is the write gate Create/Update run BEFORE touching the
// registry or the folder store. Advisories (Severity "warning") never gate.
func (s *Service) rejectInvalidGroups(ctx context.Context, wf *Workflow, fileOwner uuid.UUID) error {
	_, err := s.validateGroupsForWrite(ctx, wf, fileOwner)
	return err
}

// validateGroupsForWrite returns the findings accepted by the write gate, so
// a write receipt can include advisories without consulting dependencies again
// after persistence. fileOwner is the write target's scope (groupWriteFileOwner).
func (s *Service) validateGroupsForWrite(ctx context.Context, wf *Workflow, fileOwner uuid.UUID) ([]ValidationError, error) {
	findings := s.validateGroupsAs(ctx, wf, fileOwner)
	for _, f := range findings {
		if f.Severity != groupAdvisorySeverity {
			return nil, &GroupValidationError{Errors: findings}
		}
	}
	return findings, nil
}

// ownerScopedFiles adapts an OwnerFileChecker to the owner-blind FileChecker
// Validate takes, binding the caller.
type ownerScopedFiles struct {
	files OwnerFileChecker
	owner uuid.UUID
}

func (c ownerScopedFiles) FileExists(workflowName, relativePath string) bool {
	return c.files.FileExists(c.owner, workflowName, relativePath)
}

// ValidateGroupImport checks an incoming definition against its source/staged
// files, before a destination tree exists. Child contracts remain caller-scoped.
// It performs no registry or repository writes; legacy definitions are unchanged.
func (s *Service) ValidateGroupImport(ctx context.Context, wf *Workflow, files FileChecker) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !HasGroups(wf) {
		return nil
	}
	if _, err := caller(ctx); err != nil {
		return err
	}
	findings := s.groupFindings(ctx, wf, files)
	for _, f := range findings {
		if f.Severity != groupAdvisorySeverity {
			return &GroupValidationError{Errors: findings}
		}
	}
	return nil
}

// rejectCopiedGroups checks the exact source scope rather than the caller's
// private shadow or a not-yet-created destination. Also check the destination
// name: a rename/copy must not introduce a direct self-run.
func (s *Service) rejectCopiedGroups(ctx context.Context, scope Scope, name, destName string) error {
	_, err := s.precheckCopiedGroupImport(ctx, scope, name, destName)
	return err
}

// checkedGroupImport binds pre-lock validation to the definition used under
// the final source lock. Compare the entire manifest, including legacy inputs
// with no saved revision and definitions that did not yet contain any groups.
type checkedGroupImport struct {
	scope    Scope
	manifest []byte
}

func (c checkedGroupImport) check(scope Scope, wf *Workflow) error {
	if wf == nil || scope != c.scope {
		return ErrMutationPending
	}
	manifest, err := json.Marshal(wf)
	if err != nil {
		return err
	}
	if !bytes.Equal(c.manifest, manifest) {
		return ErrMutationPending
	}
	return nil
}

func (s *Service) precheckCopiedGroupImport(ctx context.Context, scope Scope, name, destName string) (checkedGroupImport, error) {
	if err := ctx.Err(); err != nil {
		return checkedGroupImport{}, err
	}
	wf, err := s.repo.Load(ctx, scope, name)
	if err != nil {
		return checkedGroupImport{}, err
	}
	if wf == nil {
		return checkedGroupImport{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	manifest, err := json.Marshal(wf)
	if err != nil {
		return checkedGroupImport{}, err
	}
	if err := s.rejectStoredGroupImport(ctx, wf, scope, name, destName); err != nil {
		return checkedGroupImport{}, err
	}
	return checkedGroupImport{scope: scope, manifest: manifest}, nil
}

func (s *Service) rejectStoredGroupImport(ctx context.Context, wf *Workflow, scope Scope, name, destName string) error {
	if !HasGroups(wf) {
		return nil
	}
	paths, err := s.repo.ListFiles(ctx, scope, name)
	if err != nil {
		return fmt.Errorf("listing group import files for %q: %w", name, err)
	}
	owner := uuid.Nil
	if id, err := caller(ctx); err == nil {
		owner = id.UserID
	}
	files := newImportFileSet(paths, s.groupFiles, owner)
	if err := s.ValidateGroupImport(ctx, wf, files); err != nil {
		return err
	}
	if destName != wf.Name {
		renamed := *wf
		renamed.Name = destName
		return s.ValidateGroupImport(ctx, &renamed, files)
	}
	return nil
}

// importFileSet answers existence for the files of an incoming tree. Lib paths
// are not part of the tree: they go to the optional lib fallback, and are
// treated as present when none is wired.
type importFileSet struct {
	paths map[string]bool
	lib   OwnerFileChecker
	owner uuid.UUID
}

func newImportFileSet(paths []string, lib OwnerFileChecker, owner uuid.UUID) importFileSet {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	return importFileSet{paths: set, lib: lib, owner: owner}
}

func (f importFileSet) FileExists(workflowName, path string) bool {
	if _, isLib, err := ParseLibPath(path); isLib {
		if err != nil {
			return false
		}
		return f.lib == nil || f.lib.FileExists(f.owner, workflowName, path)
	}
	return f.paths[path]
}
