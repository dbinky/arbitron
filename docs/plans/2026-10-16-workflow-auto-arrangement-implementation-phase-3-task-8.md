# Workflow Auto-Arrangement — Phase 3 Task 8: Authorised layout query/retry seams

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Execution discipline:** Expected outputs below are predictions, never recorded test results.
Where an initial run cannot compile/import a new symbol, add only its declaration and a
minimal compiling non-passing body, then rerun and observe the named behavioral assertion
fail before implementing behavior. Keep each subtask green before committing; do not commit
empty success stubs or knowingly failing tests. Test count examples are illustrative: record
the actual command/output at execution. Each data path needs an assertion at its consumer
and an absent/nil/empty/wrong-input negative control; constructor/call-count checks alone
cannot establish that the graph, status, evidence or retry was fed correctly.

**Alignment requirements:** Read contracts §6 (6.3) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Give every adapter one authorised, revision-scoped way to read a workflow's or an instance's arrangement status and to retry it, built on the existing `Service.Load` / `Engine.GetInstance` / `Service.Update` / `Engine.authorizeIntervention` policies and wired at the composition root.

**Architecture:** `domain/workflow` gains three read-only seams on `Service` (`LoadForLayout`, `AuthorizeLayoutRetry`, `LayoutRetryTargets`) that reuse its visibility filter and Update's private/admin-shared write policy without writing. `domain/engine` gains `InstanceLayoutSnapshot` / `AuthorizeInstanceLayoutRetry` (view vs intervention policy) plus a pure `DecodeWorkflowSnapshot`. `domain/layout.QueryService` consumes both through consumer-owned ports (`WorkflowAccess`, `InstanceAccess`) and the Phase 2 `Reader`/`Requester`. Instance reads and retries use only the snapshot's revision. `cmd/arbitron/layout_wiring.go` builds it via `(*layoutStack).attachQuery(workflowSvc, eng)` inside `initDomain`. `domain/workflow` never imports `domain/layout`, so there is no package cycle.

**Tech Stack:** Go 1.25.5, module `github.com/arbitron/arbitron`, testify `assert`/`require`, `domain/layout/layouttest` (MemStore + Measurements), SQLite + filesystem adapters through `initDomain` for the e2e test.

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` §1 "Authorised domain query/retry seam" (resolves **PLANNED DEPENDENCY QUERY-AUTH**); contracts `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md` §3 "Go auth seams (P3T8)".

Line numbers below are from the base checkout (before Phase 2 edits). Phase 2 tasks (P2T3, P2T9) shift `service.go` and `main.go`, so find each edit point by the quoted anchor text, not only by the number.

---

## Interfaces

**Consumes (already defined by earlier tasks; restated so this file stands alone):**

```go
// domain/workflow (existing code)
func caller(ctx context.Context) (identity.Identity, error)                      // service.go:158
func (s *Service) auditingFilter(id identity.Identity) SharedVisibilityFilter   // service.go:321
func LoadVisibleFiltered(ctx context.Context, repo Repository, owner uuid.UUID, name string, filter SharedVisibilityFilter) (*Workflow, Scope, error) // resolve.go:33
func Private(ownerID uuid.UUID) Scope; func Shared() Scope                      // scope.go
var ErrNotFound, ErrSharedReadOnly, ErrUnauthorized                             // errors.go

// domain/workflow (P2T2)
type Presentation struct {
	Revision string; SavedAt time.Time; Reason SaveReason; Scope string; OwnerID uuid.UUID; Name string; Retained RetainedLayout
}
type RetainedLayout struct { Source RetainedSource; FromRevision string; FromGeneration int64; Positions map[string]NodePosition }
const ( RetainedFromLayout RetainedSource = "layout"; RetainedFromManifest RetainedSource = "manifest"; RetainedNone RetainedSource = "none" )
const ( SaveReasonCreate SaveReason = "create"; SaveReasonUpdate SaveReason = "update" /* … */ )
// Workflow gains: Presentation *Presentation `json:"presentation,omitempty"`
func (w *Workflow) SavedRevision() string          // "" when Presentation is nil
func ScopeFromPresentation(p *Presentation) Scope  // Shared() for "shared", Private(p.OwnerID) otherwise
type ArrangementRequest struct { Revision string; Scope Scope; Name string; WorkflowID uuid.UUID; Reason SaveReason; Manifest []byte; Retained RetainedLayout; RequestedAt time.Time }
type RevisionJournal interface { /* complete §6.2 receipt/quarantine/relocation port; produced P2T2/P2T7 */ }

// domain/engine (existing code)
func (e *Engine) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error)            // engine.go:658 (canView, else ErrNotFound)
func (e *Engine) authorizeIntervention(ctx context.Context, id uuid.UUID) (*instance.Instance, error) // engine.go:693 (no view ⇒ ErrNotFound; view but no intervene ⇒ ErrForbidden)
// instance.Instance.WorkflowSnapshot map[string]any — written by Engine.TriggerWithDepartment (engine.go:479-487: json.Marshal(wf) → map)

// domain/layout (P2T5; addendum 3 gives NewReader its third argument)
type State string        // StatePending, StateInProgress, StatePassed, StateNeedsAttention, StateFailed, StateSuperseded
type DisplayStatus string // DisplayPending … DisplayLegacy, DisplayUnavailable
type Destination struct { Scope workflow.Scope; Name string; WorkflowID uuid.UUID }
type Record struct { Revision string; Destination Destination; Reason workflow.SaveReason; Manifest []byte; Retained workflow.RetainedLayout; State State; Stage Stage; JobGeneration int64; /* … */ }
type NewRequest struct { Revision string; Destination Destination; Reason workflow.SaveReason; Manifest []byte; Retained workflow.RetainedLayout; AttemptLimit int; RequestedAt time.Time }
type Fence struct { Revision string; Generation int64 }
type Completion struct { State State; AttemptsUsed int; FailureStage Stage; FailureReason string; Result *StoredResult; ReplaceBest bool }
type StoredResult struct { Revision string; Generation int64; Geometry Geometry; Review *Review; Preflight []Finding; Verdict Verdict; Score Score; CreatedAt time.Time }
var ErrRecordNotFound, ErrRetryConflict, ErrStoreUnavailable error
type ManifestSource interface { CurrentRevision(ctx context.Context, scope workflow.Scope, name string) (string, bool, error) }
type CacheReader interface { ReadCache(ctx context.Context, scope workflow.Scope, name string) (CacheEntry, bool, error) }
func NewReader(store Store, journal workflow.RevisionJournal, cache CacheReader) *Reader // cache may be nil
func (r *Reader) View(ctx context.Context, wf *workflow.Workflow) View                     // never errors
type View struct { SavedRevision string; JobGeneration int64; State State; DisplayStatus DisplayStatus; Geometry *Geometry; Retained *workflow.RetainedLayout; /* … see contracts §2 */ }
func IsActive(s DisplayStatus) bool
func NewRequester(store Store, attemptLimit int, logger *slog.Logger) *Requester
func (r *Requester) RequestArrangement(ctx context.Context, req workflow.ArrangementRequest) error
func (r *Requester) Retry(ctx context.Context, revision string) (Record, error)
// Phase 1
func BuildGraph(wf *workflow.Workflow) (Graph, error)
func Arrange(g Graph, m Measurements, c RendererContract, o Options) (Geometry, error)
func DefaultContract() RendererContract; func DefaultOptions() Options
func GeometryDigest(g Geometry) string
func ScoreOf(attempt int, findings []Finding, crossings int) Score
// domain/layout/layouttest (addendum 2)
func NewMemStore() *MemStore            // implements layout.Store
func (m *MemStore) SetFailure(err error) // every call returns fmt.Errorf("%w: %v", layout.ErrStoreUnavailable, err) until SetFailure(nil)
func Measurements(g layout.Graph, c layout.RendererContract) layout.Measurements

// cmd/arbitron (P2T9)
type layoutStack struct { store *adaptsqlite.LayoutRepo; journal *filesystem.RevisionJournal; cache *filesystem.LayoutCache; requester *layout.Requester; stamper *workflow.RevisionStamper; reader *layout.Reader; worker *layout.Worker; renderer layout.Renderer; reviewer layout.Reviewer; mutations workflow.MutationCoordinator; query *layout.QueryService; closeFns []func(); cancel context.CancelFunc }
// domainStack gains `layout *layoutStack`; initDomain's local is `ls` (contracts §2).
```

**Produces (contracts §3, plus the extra helpers named below):**

```go
// domain/workflow/layout_access.go
func (s *Service) LoadForLayout(ctx context.Context, name string) (*Workflow, Scope, error)
func (s *Service) AuthorizeLayoutRetry(ctx context.Context, name string) (*Workflow, Scope, error)
func (s *Service) LayoutRetryTargets(ctx context.Context, name string) ([]MutationTarget, error)

// domain/engine/layout_access.go
func (e *Engine) InstanceLayoutSnapshot(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error)
func (e *Engine) AuthorizeInstanceLayoutRetry(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error)
func DecodeWorkflowSnapshot(snapshot map[string]any) (*workflow.Workflow, error)

// domain/layout/query.go
type WorkflowAccess interface {
	LoadForLayout(ctx context.Context, name string) (*workflow.Workflow, workflow.Scope, error)
	AuthorizeLayoutRetry(ctx context.Context, name string) (*workflow.Workflow, workflow.Scope, error)
	LayoutRetryTargets(ctx context.Context, name string) ([]workflow.MutationTarget, error)
}
type InstanceAccess interface {
	InstanceLayoutSnapshot(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error)
	AuthorizeInstanceLayoutRetry(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error)
}
type RetryAck struct {
	SavedRevision string        `json:"savedRevision"`
	JobGeneration int64         `json:"jobGeneration"`
	State         State         `json:"state"`
	DisplayStatus DisplayStatus `json:"displayStatus"`
}
var (
	ErrRevisionConflict = errors.New("layout: expected revision is not current")
	ErrLegacyRevision   = errors.New("layout: revision has no arrangement request")
)
type QueryService struct{ /* reader, requester, workflows, instances, now */ }
func NewQueryService(reader *Reader, requester *Requester, workflows WorkflowAccess, instances InstanceAccess, mutations workflow.MutationCoordinator) *QueryService
func (q *QueryService) WorkflowLayout(ctx context.Context, name string) (View, error)
func (q *QueryService) InstanceLayout(ctx context.Context, id uuid.UUID) (View, error)
func (q *QueryService) RetryWorkflow(ctx context.Context, name, expectedRevision string) (RetryAck, error)
func (q *QueryService) RetryInstance(ctx context.Context, id uuid.UUID, expectedRevision string) (RetryAck, error)

// cmd/arbitron/layout_wiring.go (additions)
func (ls *layoutStack) attachQuery(workflows layout.WorkflowAccess, instances layout.InstanceAccess)
func (ds *domainStack) layoutQuery() *layout.QueryService // nil when ds.layout is nil (lets callers avoid typed-nil interfaces)

// cmd/arbitron/layout_query_e2e_test.go (test helpers, reused by P3T9/P3T10 e2e files in package main)
func bootQueryStack(t *testing.T) *domainStack
func queryE2EWorkflow(name, label string) *workflow.Workflow
func manifestRevisionOnDisk(t *testing.T, ds *domainStack, name string) string
func snapshotRevision(t *testing.T, ds *domainStack, id uuid.UUID) string
func waitTerminalWorkflowView(t *testing.T, ds *domainStack, ctx context.Context, name, revision string) layout.View
```

Retry semantics that adapters rely on:

- **Workflow retry:** call `AuthorizeLayoutRetry` (never `Update`). No presentation ⇒ `ErrLegacyRevision`. `expected != wf.SavedRevision()` ⇒ `ErrRevisionConflict`. Obtain LayoutRetryTargets, acquire both caller-private and shared candidates in sorted order, authorize/load again inside them, and compare BOTH resolved scope and expected revision there immediately before `Requester.Retry(rev)`:
  - success ⇒ ack `{rev, rec.JobGeneration, rec.State, DisplayPending}`;
  - `ErrRecordNotFound` ⇒ re-request with the *authorised* scope, the requested name, `wf.ID`, `Presentation.Reason`/`Retained` and `json.Marshal(wf)`, then ack from `Reader.View(wf)`;
  - `ErrRetryConflict` / `ErrStoreUnavailable` propagate.
- **Instance retry:** call `AuthorizeInstanceLayoutRetry`. Apply the same checks against the **snapshot** revision. A missing row is re-requested with `ScopeFromPresentation(wf.Presentation)` and `wf.Presentation.Name`. Never touch `WorkflowAccess`.

---

### Task 8.1: `workflow.Service.LoadForLayout` + `AuthorizeLayoutRetry`

**Files:**
- Create: `domain/workflow/layout_access.go`
- Test:   `domain/workflow/layout_access_test.go`
- Reference (no edit): `domain/workflow/service.go:158-164` (`caller`), `:200-224` (`adminSharedWrite`, the policy mirrored), `:271-280` (`Update`), `:321-344` (`auditingFilter`), `:356-373` (`Load`); `domain/workflow/resolve.go:33-52`; test fakes `domain/workflow/service_test.go:17-131` (`fakeRepo`), `:133-201` (`fakePublishers`), `:203-223` (`ctxWith`, `ctxWithDepts`, `wf`), `domain/workflow/service_registry_test.go:19-38` (`fakeRegistry`).

- [x] **Step 1: Write the failing test**

Create `domain/workflow/layout_access_test.go`:

```go
package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCountingRepo wraps the in-memory fakeRepo and counts every MUTATING
// repository call, so the layout seams can be proven side-effect free. Seeding
// through repo.bucket(...) bypasses the counter on purpose. loadErr, when set,
// is returned by Load for the PRIVATE scope only (an infrastructure failure).
type writeCountingRepo struct {
	*fakeRepo
	writes  int
	loadErr error
}

func newWriteCountingRepo() *writeCountingRepo {
	return &writeCountingRepo{fakeRepo: newFakeRepo()}
}

func (r *writeCountingRepo) Load(ctx context.Context, s workflow.Scope, name string) (*workflow.Workflow, error) {
	if r.loadErr != nil && s.Kind == workflow.ScopePrivate {
		return nil, r.loadErr
	}
	return r.fakeRepo.Load(ctx, s, name)
}

func (r *writeCountingRepo) Create(ctx context.Context, s workflow.Scope, name string, w *workflow.Workflow) error {
	r.writes++
	return r.fakeRepo.Create(ctx, s, name, w)
}

func (r *writeCountingRepo) Update(ctx context.Context, s workflow.Scope, name string, w *workflow.Workflow) error {
	r.writes++
	return r.fakeRepo.Update(ctx, s, name, w)
}

func (r *writeCountingRepo) Delete(ctx context.Context, s workflow.Scope, name string) error {
	r.writes++
	return r.fakeRepo.Delete(ctx, s, name)
}

func (r *writeCountingRepo) WriteFile(ctx context.Context, s workflow.Scope, name, rel string, content []byte) error {
	r.writes++
	return r.fakeRepo.WriteFile(ctx, s, name, rel, content)
}

func (r *writeCountingRepo) DeleteFile(ctx context.Context, s workflow.Scope, name, rel string) error {
	r.writes++
	return r.fakeRepo.DeleteFile(ctx, s, name, rel)
}

func (r *writeCountingRepo) CopyTree(ctx context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool) error {
	r.writes++
	return r.fakeRepo.CopyTree(ctx, src, srcName, dst, dstName, overwrite)
}

var _ workflow.Repository = (*writeCountingRepo)(nil)

// layoutWF builds a stamped workflow carrying a saved revision.
func layoutWF(name, rev string) *workflow.Workflow {
	w := wf(name)
	w.Presentation = &workflow.Presentation{
		Revision: rev,
		Reason:   workflow.SaveReasonUpdate,
		Scope:    workflow.ScopeLabelPrivate,
		Name:     name,
		Retained: workflow.RetainedLayout{Source: workflow.RetainedNone},
	}
	return w
}

// ---------------------------------------------------------------------------
// LoadForLayout
// ---------------------------------------------------------------------------

// Happy path: an owner's private workflow resolves with the private scope.
func TestLoadForLayout_OwnerPrivate_ReturnsPrivateScope(t *testing.T) {
	repo := newWriteCountingRepo()
	owner := uuid.New()
	repo.bucket(workflow.Private(owner))["mine"] = layoutWF("mine", "rev-p")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	got, scope, err := svc.LoadForLayout(ctxWith(owner, false), "mine")
	require.NoError(t, err)
	assert.Equal(t, "rev-p", got.SavedRevision())
	assert.Equal(t, workflow.Private(owner), scope)
}

// Success: a visible (public) shared workflow resolves with the shared scope.
func TestLoadForLayout_VisibleShared_ReturnsSharedScope(t *testing.T) {
	repo := newWriteCountingRepo()
	pubs := newFakePublishers()
	repo.bucket(workflow.Shared())["pub-wf"] = layoutWF("pub-wf", "rev-s")
	require.NoError(t, pubs.Upsert(context.Background(), "pub-wf", uuid.New(), "src", workflow.VisibilityPublic, ""))
	svc := workflow.NewWorkflowService(repo, pubs)

	got, scope, err := svc.LoadForLayout(ctxWith(uuid.New(), false), "pub-wf")
	require.NoError(t, err)
	assert.Equal(t, "rev-s", got.SavedRevision())
	assert.Equal(t, workflow.Shared(), scope)
}

// Error: a department workflow hidden from an outsider is ErrNotFound — the
// same concealment Load applies, never ErrSharedReadOnly or a revision.
func TestLoadForLayout_DepartmentHiddenShared_NotFound(t *testing.T) {
	repo := newWriteCountingRepo()
	pubs := newFakePublishers()
	repo.bucket(workflow.Shared())["eng-wf"] = layoutWF("eng-wf", "rev-eng")
	require.NoError(t, pubs.Upsert(context.Background(), "eng-wf", uuid.New(), "src", workflow.VisibilityDepartment, "engineering"))
	svc := workflow.NewWorkflowService(repo, pubs)

	got, _, err := svc.LoadForLayout(ctxWithDepts(uuid.New(), false, "sales"), "eng-wf")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
	assert.NotErrorIs(t, err, workflow.ErrSharedReadOnly)
	assert.Nil(t, got, "no definition (and so no revision) may leak to an outsider")

	// A department member sees the same workflow.
	got, scope, err := svc.LoadForLayout(ctxWithDepts(uuid.New(), false, "engineering"), "eng-wf")
	require.NoError(t, err)
	assert.Equal(t, "rev-eng", got.SavedRevision())
	assert.Equal(t, workflow.Shared(), scope)
}

// Edge: unlike Load, LoadForLayout never resolves/allocates a registry id for an
// id-less legacy definition — a polled layout read has no id side effects.
func TestLoadForLayout_DoesNotAllocateID(t *testing.T) {
	repo := newWriteCountingRepo()
	reg := newFakeRegistry()
	owner := uuid.New()
	repo.bucket(workflow.Private(owner))["legacy"] = layoutWF("legacy", "rev-l")
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)

	loaded, err := svc.Load(ctxWith(owner, false), "legacy")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, loaded.ID, "precondition: Load allocates an in-memory id when a registry is wired")
	repo.bucket(workflow.Private(owner))["legacy"].ID = uuid.Nil // undo the in-memory mutation of the shared pointer

	got, _, err := svc.LoadForLayout(ctxWith(owner, false), "legacy")
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, got.ID, "LoadForLayout must not allocate an id")
	assert.Empty(t, reg.byName, "no registry row may be written by a layout read")
}

// Error: no identity is rejected as unauthenticated.
func TestLoadForLayout_NoIdentity_Unauthenticated(t *testing.T) {
	svc := workflow.NewWorkflowService(newWriteCountingRepo(), nil)
	_, _, err := svc.LoadForLayout(context.Background(), "x")
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
}

// Edge: a missing workflow is ErrNotFound and nothing is written.
func TestLoadForLayout_Missing_NotFoundWithoutWrites(t *testing.T) {
	repo := newWriteCountingRepo()
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	_, _, err := svc.LoadForLayout(ctxWith(uuid.New(), false), "ghost")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
	assert.Zero(t, repo.writes)
}

// ---------------------------------------------------------------------------
// AuthorizeLayoutRetry
// ---------------------------------------------------------------------------

// Happy path: the owner of a private workflow may retry; scope is private.
func TestAuthorizeLayoutRetry_OwnerPrivate(t *testing.T) {
	repo := newWriteCountingRepo()
	owner := uuid.New()
	repo.bucket(workflow.Private(owner))["mine"] = layoutWF("mine", "rev-p")
	svc := workflow.NewWorkflowService(repo, nil)

	got, scope, err := svc.AuthorizeLayoutRetry(ctxWith(owner, false), "mine")
	require.NoError(t, err)
	assert.Equal(t, "rev-p", got.SavedRevision())
	assert.Equal(t, workflow.Private(owner), scope)
	assert.Zero(t, repo.writes, "authorizing a retry must not write the manifest")
}

// Success: an admin with no private copy may retry a shared-only workflow in
// place (Update's admin edit-in-place policy) and gets the shared scope.
func TestAuthorizeLayoutRetry_AdminSharedOnly_SharedScope(t *testing.T) {
	repo := newWriteCountingRepo()
	repo.bucket(workflow.Shared())["shared-wf"] = layoutWF("shared-wf", "rev-s")
	svc := workflow.NewWorkflowService(repo, nil)

	got, scope, err := svc.AuthorizeLayoutRetry(ctxWith(uuid.New(), true), "shared-wf")
	require.NoError(t, err)
	assert.Equal(t, "rev-s", got.SavedRevision())
	assert.Equal(t, workflow.Shared(), scope)
	assert.Zero(t, repo.writes)
}

// Edge: an admin who owns a private copy of the same name targets the private
// copy (the shared branch applies only when no private copy exists).
func TestAuthorizeLayoutRetry_AdminPrefersOwnPrivateCopy(t *testing.T) {
	repo := newWriteCountingRepo()
	admin := uuid.New()
	repo.bucket(workflow.Shared())["dup"] = layoutWF("dup", "rev-shared")
	repo.bucket(workflow.Private(admin))["dup"] = layoutWF("dup", "rev-private")
	svc := workflow.NewWorkflowService(repo, nil)

	got, scope, err := svc.AuthorizeLayoutRetry(ctxWith(admin, true), "dup")
	require.NoError(t, err)
	assert.Equal(t, "rev-private", got.SavedRevision())
	assert.Equal(t, workflow.Private(admin), scope)
}

// Failure: a non-admin cannot retry a shared-only workflow (read-only).
func TestAuthorizeLayoutRetry_NonAdminSharedOnly_SharedReadOnly(t *testing.T) {
	repo := newWriteCountingRepo()
	repo.bucket(workflow.Shared())["shared-wf"] = layoutWF("shared-wf", "rev-s")
	svc := workflow.NewWorkflowService(repo, nil)

	got, _, err := svc.AuthorizeLayoutRetry(ctxWith(uuid.New(), false), "shared-wf")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrSharedReadOnly)
	assert.Nil(t, got)
	assert.Zero(t, repo.writes)
}

// Failure: a workflow in neither scope is ErrNotFound.
func TestAuthorizeLayoutRetry_Missing_NotFound(t *testing.T) {
	repo := newWriteCountingRepo()
	svc := workflow.NewWorkflowService(repo, nil)

	_, _, err := svc.AuthorizeLayoutRetry(ctxWith(uuid.New(), true), "ghost")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Edge: another user's private workflow is never reachable (not found).
func TestAuthorizeLayoutRetry_OtherUsersPrivate_NotFound(t *testing.T) {
	repo := newWriteCountingRepo()
	a, b := uuid.New(), uuid.New()
	repo.bucket(workflow.Private(a))["a-wf"] = layoutWF("a-wf", "rev-a")
	svc := workflow.NewWorkflowService(repo, nil)

	_, _, err := svc.AuthorizeLayoutRetry(ctxWith(b, false), "a-wf")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Error: a private-scope infrastructure failure propagates verbatim (it is not
// collapsed into not-found and never falls through to the shared copy).
func TestAuthorizeLayoutRetry_PrivateLoadError_Propagates(t *testing.T) {
	repo := newWriteCountingRepo()
	repo.loadErr = errors.New("disk unreadable")
	repo.bucket(workflow.Shared())["shared-wf"] = layoutWF("shared-wf", "rev-s")
	svc := workflow.NewWorkflowService(repo, nil)

	_, _, err := svc.AuthorizeLayoutRetry(ctxWith(uuid.New(), true), "shared-wf")
	require.Error(t, err)
	assert.EqualError(t, err, "disk unreadable")
	assert.NotErrorIs(t, err, workflow.ErrNotFound)
}

// Error: no identity is rejected as unauthenticated.
func TestAuthorizeLayoutRetry_NoIdentity_Unauthenticated(t *testing.T) {
	svc := workflow.NewWorkflowService(newWriteCountingRepo(), nil)
	_, _, err := svc.AuthorizeLayoutRetry(context.Background(), "x")
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
}

// Policy parity: for every scenario AuthorizeLayoutRetry reaches the same
// allow/deny decision as Update (run against an independent repo), so the
// retry seam cannot drift from the write policy it mirrors.
func TestAuthorizeLayoutRetry_MatchesUpdatePolicy(t *testing.T) {
	owner := uuid.New()
	cases := []struct {
		name    string
		seed    func(r *writeCountingRepo)
		ctx     context.Context
		target  string
		wantErr error // nil = allowed
	}{
		{"owner private", func(r *writeCountingRepo) { r.bucket(workflow.Private(owner))["w"] = layoutWF("w", "r") }, ctxWith(owner, false), "w", nil},
		{"admin shared-only", func(r *writeCountingRepo) { r.bucket(workflow.Shared())["w"] = layoutWF("w", "r") }, ctxWith(owner, true), "w", nil},
		{"non-admin shared-only", func(r *writeCountingRepo) { r.bucket(workflow.Shared())["w"] = layoutWF("w", "r") }, ctxWith(owner, false), "w", workflow.ErrSharedReadOnly},
		{"missing", func(r *writeCountingRepo) {}, ctxWith(owner, true), "w", workflow.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retryRepo := newWriteCountingRepo()
			tc.seed(retryRepo)
			_, _, retryErr := workflow.NewWorkflowService(retryRepo, nil).AuthorizeLayoutRetry(tc.ctx, tc.target)

			updateRepo := newWriteCountingRepo()
			tc.seed(updateRepo)
			updateErr := workflow.NewWorkflowService(updateRepo, nil).Update(tc.ctx, tc.target, layoutWF(tc.target, "r2"))

			if tc.wantErr == nil {
				assert.NoError(t, retryErr)
				assert.NoError(t, updateErr)
			} else {
				assert.ErrorIs(t, retryErr, tc.wantErr)
				assert.ErrorIs(t, updateErr, tc.wantErr)
			}
			assert.Zero(t, retryRepo.writes, "AuthorizeLayoutRetry must never write")
		})
	}
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
go test ./domain/workflow/ -run 'TestLoadForLayout|TestAuthorizeLayoutRetry' -v
```

Expected output (build failure):

```
# github.com/arbitron/arbitron/domain/workflow_test [github.com/arbitron/arbitron/domain/workflow.test]
./layout_access_test.go:...: svc.LoadForLayout undefined (type *workflow.Service has no field or method LoadForLayout)
./layout_access_test.go:...: svc.AuthorizeLayoutRetry undefined (type *workflow.Service has no field or method AuthorizeLayoutRetry)
FAIL	github.com/arbitron/arbitron/domain/workflow [build failed]
```

- [x] **Step 3: Minimal implementation**

Create `domain/workflow/layout_access.go`:

```go
package workflow

import (
	"context"
	"errors"
	"fmt"
)

// LoadForLayout resolves the workflow the caller may READ for arrangement
// status, using exactly Load's visibility rules: the caller's private scope
// first, then the shared scope gated by the publication filter, so a
// department-hidden shared workflow is ErrNotFound and its existence (and saved
// revision) never leaks. It also returns the scope the definition resolved
// from, which is the arrangement destination.
//
// Unlike Load it never resolves or allocates a registry id: arrangement status
// is keyed by the saved revision, not the workflow id, and a polled read must
// not do id bookkeeping. It performs no repository writes.
func (s *Service) LoadForLayout(ctx context.Context, name string) (*Workflow, Scope, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, Scope{}, err
	}
	return LoadVisibleFiltered(ctx, s.repo, id.UserID, name, s.auditingFilter(id))
}

// AuthorizeLayoutRetry applies Update's write policy (see Update and
// adminSharedWrite) WITHOUT writing, and returns the definition and the scope a
// retry may target:
//
//   - the caller's own private workflow ⇒ (wf, Private(caller));
//   - no private copy, a shared copy exists, caller is admin ⇒ (shared wf, Shared())
//     — the admin edit-in-place escape hatch;
//   - no private copy, a shared copy exists, caller is not admin ⇒ ErrSharedReadOnly;
//   - neither (or the shared existence probe fails, exactly as adminSharedWrite
//     treats it) ⇒ ErrNotFound.
//
// A private-scope failure other than not-found is returned verbatim. Retrying an
// arrangement never rewrites the manifest, so authorizing it must not call
// Update merely to check permission.
func (s *Service) AuthorizeLayoutRetry(ctx context.Context, name string) (*Workflow, Scope, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, Scope{}, err
	}
	private := Private(id.UserID)
	wf, privateErr := s.repo.Load(ctx, private, name)
	if privateErr == nil {
		return wf, private, nil
	}
	if !errors.Is(privateErr, ErrNotFound) {
		return nil, Scope{}, privateErr
	}
	sharedExists, existsErr := s.repo.Exists(ctx, Shared(), name)
	if existsErr != nil || !sharedExists {
		return nil, Scope{}, privateErr // no shared copy: a genuine not-found
	}
	if !id.IsAdmin() {
		return nil, Scope{}, fmt.Errorf("%w: %s", ErrSharedReadOnly, name)
	}
	shared, err := s.repo.Load(ctx, Shared(), name)
	if err != nil {
		return nil, Scope{}, err
	}
	return shared, Shared(), nil
}
```

- [x] **Step 4: Run it, confirm it passes**

```bash
go test ./domain/workflow/ -run 'TestLoadForLayout|TestAuthorizeLayoutRetry' -v
```

Expected: every `TestLoadForLayout_*` and `TestAuthorizeLayoutRetry_*` (including the four `MatchesUpdatePolicy` subtests) reports `--- PASS`, ending with `ok  	github.com/arbitron/arbitron/domain/workflow`.

Then the whole package:

```bash
go test ./domain/workflow/
```

Expected: `ok  	github.com/arbitron/arbitron/domain/workflow`

- [x] **Step 5: Commit**

```bash
git add domain/workflow/layout_access.go domain/workflow/layout_access_test.go && git commit -m "feat(workflow): add side-effect-free layout read and retry authorization seams" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8.2: Engine instance-layout seams + `DecodeWorkflowSnapshot`

**Files:**
- Create: `domain/engine/layout_access.go`
- Test:   `domain/engine/layout_access_test.go`
- Reference (no edit): `domain/engine/engine.go:655-672` (`GetInstance`), `:683-708` (`authorizeIntervention`), `:479-487` (snapshot written as `json.Marshal(wf)` → map), `:1289-1297` (`parseWorkflowGraph` decode idiom); `domain/engine/authz.go:14-38`; test helpers `domain/engine/engine_read_test.go:17-58` (`readGetRepo`), `:60-93` (counting repos), `:107-114` (`ownedInstance`); `domain/engine/engine_list_test.go:83-89` (`adminCtx`, `userCtx`); `domain/engine/engine_intervention_split_test.go:14-24` (`memberCtx`, `leadCtx`).

- [x] **Step 1: Write the failing test**

Create `domain/engine/layout_access_test.go`:

```go
package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// layoutInstanceRepo honours the instance id: Get returns ErrNotFound for any id
// it was not seeded with, so a seam that loads the wrong id cannot pass. Every
// other InstanceRepository method is the panicking readGetRepo stub.
type layoutInstanceRepo struct {
	readGetRepo
	byID map[uuid.UUID]*instance.Instance
}

func (r *layoutInstanceRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	if err := ctx.Err(); err != nil { return nil, err }
	inst, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return inst, nil
}

var _ instance.InstanceRepository = (*layoutInstanceRepo)(nil)

func newLayoutEngine(insts ...*instance.Instance) *engine.Engine {
	repo := &layoutInstanceRepo{byID: map[uuid.UUID]*instance.Instance{}}
	for _, inst := range insts {
		repo.byID[inst.ID] = inst
	}
	return engine.NewEngine(
		newScopeAwareRepo(), repo, &countingNodeExecRepo{}, &countingSnapshotRepo{}, newMockUserRepo(),
		map[workflow.NodeType]engine.NodeExecutor{}, newMockEventBus(), newMockQueueManager(), &mockQueueRepo{},
		"",
	)
}

// snapshotOf reproduces exactly how TriggerWithDepartment captures a snapshot:
// json.Marshal of the loaded workflow, decoded into a generic map.
func layoutSnapshotMap(t *testing.T, wf *workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func stampedSnapshotWorkflow(owner uuid.UUID, rev string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: "snap-wf",
		Nodes: []workflow.Node{
			{ID: "a", Type: workflow.NodeTypeTransform, Entrypoint: true, Label: "Start"},
			{ID: "b", Type: workflow.NodeTypeTransform, Label: "Finish"},
		},
		Edges: []workflow.Edge{{Source: "a", Target: "b"}},
		Presentation: &workflow.Presentation{
			Revision: rev,
			SavedAt:  time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
			Reason:   workflow.SaveReasonCreate,
			Scope:    workflow.ScopeLabelPrivate,
			OwnerID:  owner,
			Name:     "snap-wf",
			Retained: workflow.RetainedLayout{
				Source:    workflow.RetainedFromManifest,
				Positions: map[string]workflow.NodePosition{"a": {X: 1, Y: 2}},
			},
		},
	}
}

func layoutInstance(t *testing.T, owner uuid.UUID, vis instance.Visibility, dept string, rev string) *instance.Instance {
	t.Helper()
	inst := ownedInstance(owner)
	inst.Visibility = vis
	inst.Department = dept
	inst.WorkflowSnapshot = layoutSnapshotMap(t, stampedSnapshotWorkflow(owner, rev))
	return inst
}

// --- InstanceLayoutSnapshot (view policy) ---

func TestInstanceLayoutSnapshot_Owner_DecodesSnapshotRevision(t *testing.T) {
	owner := uuid.New()
	inst := layoutInstance(t, owner, instance.VisibilityPrivate, "", "rev-snap-1")
	eng := newLayoutEngine(inst)

	wf, err := eng.InstanceLayoutSnapshot(userCtx(owner), inst.ID)
	require.NoError(t, err)
	require.NotNil(t, wf.Presentation)
	assert.Equal(t, "rev-snap-1", wf.SavedRevision())
	assert.Equal(t, workflow.RetainedFromManifest, wf.Presentation.Retained.Source)
	assert.Equal(t, workflow.NodePosition{X: 1, Y: 2}, wf.Presentation.Retained.Positions["a"])
	require.Len(t, wf.Nodes, 2)
	assert.Equal(t, "Finish", wf.Nodes[1].Label)
}

func TestInstanceLayoutSnapshot_AdminAndPublicViewer_Allowed(t *testing.T) {
	owner := uuid.New()
	private := layoutInstance(t, owner, instance.VisibilityPrivate, "", "rev-priv")
	public := layoutInstance(t, owner, instance.VisibilityPublic, "", "rev-pub")
	eng := newLayoutEngine(private, public)

	wf, err := eng.InstanceLayoutSnapshot(adminCtx(uuid.New()), private.ID)
	require.NoError(t, err)
	assert.Equal(t, "rev-priv", wf.SavedRevision())

	wf, err = eng.InstanceLayoutSnapshot(userCtx(uuid.New()), public.ID)
	require.NoError(t, err)
	assert.Equal(t, "rev-pub", wf.SavedRevision())
}

func TestInstanceLayoutSnapshot_NonViewer_NotFound(t *testing.T) {
	owner := uuid.New()
	inst := layoutInstance(t, owner, instance.VisibilityPrivate, "", "rev-secret")
	eng := newLayoutEngine(inst)

	wf, err := eng.InstanceLayoutSnapshot(userCtx(uuid.New()), inst.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
	assert.Nil(t, wf, "a non-viewer must not receive the snapshot revision")
}

func TestInstanceLayoutSnapshot_NoIdentity_NotFound(t *testing.T) {
	inst := layoutInstance(t, uuid.New(), instance.VisibilityPublic, "", "rev")
	eng := newLayoutEngine(inst)

	_, err := eng.InstanceLayoutSnapshot(context.Background(), inst.ID)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

func TestInstanceLayoutSnapshot_UnknownID_NotFound(t *testing.T) {
	owner := uuid.New()
	inst := layoutInstance(t, owner, instance.VisibilityPrivate, "", "rev")
	eng := newLayoutEngine(inst)

	_, err := eng.InstanceLayoutSnapshot(userCtx(owner), uuid.New())
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

func TestInstanceLayoutSnapshot_DepartmentPeer_CanRead(t *testing.T) {
	inst := layoutInstance(t, uuid.New(), instance.VisibilityDepartment, "engineering", "rev-dept")
	eng := newLayoutEngine(inst)

	wf, err := eng.InstanceLayoutSnapshot(memberCtx(uuid.New(), "engineering"), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "rev-dept", wf.SavedRevision())
}

// Edge: a legacy snapshot (pre-feature manifest) decodes with a nil
// Presentation and an empty saved revision — the caller renders its own
// positions/dagre fallback.
func TestInstanceLayoutSnapshot_LegacySnapshot_NilPresentation(t *testing.T) {
	owner := uuid.New()
	inst := ownedInstance(owner)
	legacy := &workflow.Workflow{
		Name:  "old",
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeTransform, Entrypoint: true, Position: &workflow.NodePosition{X: 5, Y: 6}}},
	}
	inst.WorkflowSnapshot = layoutSnapshotMap(t, legacy)
	eng := newLayoutEngine(inst)

	wf, err := eng.InstanceLayoutSnapshot(userCtx(owner), inst.ID)
	require.NoError(t, err)
	assert.Nil(t, wf.Presentation)
	assert.Equal(t, "", wf.SavedRevision())
	require.NotNil(t, wf.Nodes[0].Position)
	assert.Equal(t, 5.0, wf.Nodes[0].Position.X)
}

func TestInstanceLayoutSnapshot_MissingSnapshot_Errors(t *testing.T) {
	owner := uuid.New()
	inst := ownedInstance(owner) // WorkflowSnapshot nil
	eng := newLayoutEngine(inst)

	_, err := eng.InstanceLayoutSnapshot(userCtx(owner), inst.ID)
	require.Error(t, err)
	assert.ErrorContains(t, err, "instance has no workflow snapshot")
}

// --- AuthorizeInstanceLayoutRetry (intervention policy) ---

func TestAuthorizeInstanceLayoutRetry_OwnerAdminLead_Allowed(t *testing.T) {
	owner := uuid.New()
	inst := layoutInstance(t, owner, instance.VisibilityDepartment, "engineering", "rev-r")
	eng := newLayoutEngine(inst)

	for name, ctx := range map[string]context.Context{
		"owner": userCtx(owner),
		"admin": adminCtx(uuid.New()),
		"lead":  leadCtx(uuid.New(), "engineering"),
	} {
		t.Run(name, func(t *testing.T) {
			wf, err := eng.AuthorizeInstanceLayoutRetry(ctx, inst.ID)
			require.NoError(t, err)
			assert.Equal(t, "rev-r", wf.SavedRevision())
		})
	}
}

// A department peer can READ the run but cannot retry it: 403, not 404.
func TestAuthorizeInstanceLayoutRetry_DepartmentPeer_Forbidden(t *testing.T) {
	inst := layoutInstance(t, uuid.New(), instance.VisibilityDepartment, "engineering", "rev-r")
	eng := newLayoutEngine(inst)

	wf, err := eng.AuthorizeInstanceLayoutRetry(memberCtx(uuid.New(), "engineering"), inst.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrForbidden)
	assert.NotErrorIs(t, err, instance.ErrNotFound)
	assert.Nil(t, wf)
}

func TestAuthorizeInstanceLayoutRetry_NonViewer_NotFound(t *testing.T) {
	inst := layoutInstance(t, uuid.New(), instance.VisibilityPrivate, "", "rev-r")
	eng := newLayoutEngine(inst)

	_, err := eng.AuthorizeInstanceLayoutRetry(userCtx(uuid.New()), inst.ID)
	assert.ErrorIs(t, err, instance.ErrNotFound)
	assert.NotErrorIs(t, err, instance.ErrForbidden)
}

func TestAuthorizeInstanceLayoutRetry_UnknownID_NotFound(t *testing.T) {
	inst := layoutInstance(t, uuid.New(), instance.VisibilityPrivate, "", "rev-r")
	eng := newLayoutEngine(inst)

	_, err := eng.AuthorizeInstanceLayoutRetry(adminCtx(uuid.New()), uuid.New())
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

// --- DecodeWorkflowSnapshot ---

func TestDecodeWorkflowSnapshot_RoundTripsPresentation(t *testing.T) {
	src := stampedSnapshotWorkflow(uuid.New(), "rev-rt")
	wf, err := engine.DecodeWorkflowSnapshot(layoutSnapshotMap(t, src))
	require.NoError(t, err)
	assert.Equal(t, src.Presentation.Revision, wf.Presentation.Revision)
	assert.Equal(t, src.Presentation.OwnerID, wf.Presentation.OwnerID)
	assert.True(t, src.Presentation.SavedAt.Equal(wf.Presentation.SavedAt))
	assert.Equal(t, src.Edges, wf.Edges)
}

func TestDecodeWorkflowSnapshot_NilMap_Errors(t *testing.T) {
	_, err := engine.DecodeWorkflowSnapshot(nil)
	assert.ErrorContains(t, err, "instance has no workflow snapshot")
}

func TestDecodeWorkflowSnapshot_WrongShape_Errors(t *testing.T) {
	_, err := engine.DecodeWorkflowSnapshot(map[string]any{"nodes": "not-a-list"})
	require.Error(t, err)
	assert.ErrorContains(t, err, "parsing workflow snapshot")
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
go test ./domain/engine/ -run 'TestInstanceLayoutSnapshot|TestAuthorizeInstanceLayoutRetry|TestDecodeWorkflowSnapshot' -v
```

Expected output:

```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
./layout_access_test.go:...: eng.InstanceLayoutSnapshot undefined (type *engine.Engine has no field or method InstanceLayoutSnapshot)
./layout_access_test.go:...: eng.AuthorizeInstanceLayoutRetry undefined (type *engine.Engine has no field or method AuthorizeInstanceLayoutRetry)
./layout_access_test.go:...: undefined: engine.DecodeWorkflowSnapshot
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] **Step 3: Minimal implementation**

Create `domain/engine/layout_access.go`:

```go
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/workflow"
)

// errNoWorkflowSnapshot reports an instance row that carries no snapshot. Every
// triggered instance has one; this is a corrupt/foreign row, not a caller error.
var errNoWorkflowSnapshot = errors.New("instance has no workflow snapshot")

// InstanceLayoutSnapshot returns the exact workflow definition an instance ran
// from — including its saved revision and retained presentation — for a caller
// that may VIEW the instance (GetInstance's canView policy: no view ⇒
// ErrNotFound, existence not leaked). It never consults the current workflow:
// a historical run keeps its own revision even after the workflow is updated,
// renamed or deleted.
func (e *Engine) InstanceLayoutSnapshot(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error) {
	inst, err := e.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	wf, err := DecodeWorkflowSnapshot(inst.WorkflowSnapshot)
	if err != nil {
		return nil, fmt.Errorf("instance %s: %w", id, err)
	}
	return wf, nil
}

// AuthorizeInstanceLayoutRetry applies the intervention policy
// (authorizeIntervention: owner, a lead of the run's department, or an admin;
// a viewer who may not intervene gets ErrForbidden; a non-viewer ErrNotFound)
// and returns the snapshot definition whose revision a retry may target. It
// performs no instance mutation.
func (e *Engine) AuthorizeInstanceLayoutRetry(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error) {
	inst, err := e.authorizeIntervention(ctx, id)
	if err != nil {
		return nil, err
	}
	wf, err := DecodeWorkflowSnapshot(inst.WorkflowSnapshot)
	if err != nil {
		return nil, fmt.Errorf("instance %s: %w", id, err)
	}
	return wf, nil
}

// DecodeWorkflowSnapshot converts an instance's generic WorkflowSnapshot map
// (written by TriggerWithDepartment as json.Marshal of the loaded workflow) back
// into a typed workflow, preserving presentation metadata. A legacy snapshot with a valid graph decodes with a nil Presentation. Empty maps are corrupt/missing snapshots and must fail.
func DecodeWorkflowSnapshot(snapshot map[string]any) (*workflow.Workflow, error) {
	if len(snapshot) == 0 {
		return nil, errNoWorkflowSnapshot
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshalling workflow snapshot: %w", err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(b, &wf); err != nil {
		return nil, fmt.Errorf("parsing workflow snapshot: %w", err)
	}
	if len(wf.Nodes) == 0 { return nil, fmt.Errorf("parsing workflow snapshot: no nodes") }
	return &wf, nil
}
```

- [x] **Step 4: Run it, confirm it passes**

```bash
go test ./domain/engine/ -run 'TestInstanceLayoutSnapshot|TestAuthorizeInstanceLayoutRetry|TestDecodeWorkflowSnapshot' -v
```

Expected: all listed tests (and the `owner`/`admin`/`lead` subtests) `--- PASS`; `ok  	github.com/arbitron/arbitron/domain/engine`.

```bash
go test ./domain/engine/
```

Expected: `ok  	github.com/arbitron/arbitron/domain/engine`

- [x] **Step 5: Commit**

```bash
git add domain/engine/layout_access.go domain/engine/layout_access_test.go && git commit -m "feat(engine): add instance layout snapshot read and retry authorization seams" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8.3: `layout.QueryService`

**Files:**
- Create: `domain/layout/query.go`
- Test:   `domain/layout/query_test.go` (package `layout_test`, per addendum 2)
- Reference (no edit; produced by P2T5): `domain/layout/store.go`, `requester.go`, `reader.go`, `manifests.go`, `domain/layout/layouttest/memstore.go`, `measure.go`.

- [x] **Step 1: Write the failing test**

Create `domain/layout/query_test.go`:

Use the existing `domain/layout/query_test.go` support rather than recreating a second
query harness: `queryMutationGate`, `fakeWorkflowAccess`, `fakeInstanceAccess`,
`newQueryHarness`, `queryStamped`, `querySnapshotCopy` and `completePassed`. The source
file is the producer of these test-only helpers; they are not production collaborators.
Extend its three-method fake WorkflowAccess with explicit complete lock target sets.
The gate must compare the whole requested set and the second authorization must reject a
missing owned callback context. Missing/malformed targets have dedicated failures, not
a fallback success. Instances resolve the exact requested UUID/snapshot and may never
fall through to the workflow read seam.

Required assertion matrix (existing named tests can be strengthened):

| Test / input | Exact sink assertions |
|---|---|
| WorkflowLayout_ReturnsViewOfAuthorisedRevision | saved revision/geometry/evidence come from the accessible manifest's Store result; read-only |
| InstanceLayout_UsesSnapshotRevision_NeverLatest | historical snapshot token and geometry even when the current workflow differs or is deleted |
| SameRevision_WorkflowAndInstanceViewsIdentical | complete View including geometry/digest/result generation matches |
| RetryWorkflow_FailedRevision_BumpsGeneration | StatePending, previous generation + 1, unchanged immutable input and no authoring write |
| RetryWorkflow_LocksEveryResolutionCandidate | full private+shared set, owned-context second authorization, actual Store generation increment |
| RetryWorkflow_PrivateShadowSameToken_Conflict | ErrRevisionConflict, unchanged old/new generation, no request to either destination |
| RetryWorkflow_StaleOrEmptyExpected_Conflict / PassedRevision_RetryConflict | exact sentinel and unchanged record |
| RetryInstance_RetriesSnapshotRevisionOnly | exact authorized historical token, current workflow untouched, other active history preserved |
| Missing row for saved revision | enqueue only trusted saved input and current committed envelope; actual pending generation |
| Legacy, missing definition, missing identity, forbidden, unavailable | correct error/concealment, no panic, no mutation or empty success |
| Corrupt/missing passed result | Reader attention/fallback, Requester fenced invalidation then new pending generation; outages never invalidate good evidence |

Write/strengthen these assertions first. Test doubles must consume name/UUID, target set,
owned context, geometry/evidence and cancellation. Omit a load-bearing argument in a
mutation probe and observe the named sink fail. Fixture evidence is unit-only; actual
browser/user-route parity remains P3T14/P3T15.


- [x] **Step 2: Run it, confirm it fails**

```bash
go test ./domain/layout/ -run 'TestQueryService' -v
```

Expected output:

```
# github.com/arbitron/arbitron/domain/layout_test [github.com/arbitron/arbitron/domain/layout.test]
./query_test.go:...: undefined: layout.QueryService
./query_test.go:...: undefined: layout.NewQueryService
./query_test.go:...: undefined: layout.WorkflowAccess
./query_test.go:...: undefined: layout.InstanceAccess
./query_test.go:...: undefined: layout.ErrRevisionConflict
FAIL	github.com/arbitron/arbitron/domain/layout [build failed]
```

- [x] **Step 3: Minimal implementation**

Create `domain/layout/query.go`:

The public signatures are the Interfaces block above, including required
WorkflowAccess.LayoutRetryTargets. Implement in query.go against those exact ports:

1. Authorize the first workflow/scope and expected token; request the complete candidate
   targets from Service. Reject empty sets or sets omitting that destination.
2. Under the full sorted mutation lock set, authorize again and compare resolved scope
   AND expected saved revision before retry/missing-row enqueue. Pass the owned callback
   context through every read/store call. Shared-only must work without lock expansion.
3. Historical retry authorizes only the instance snapshot, resolves a committed relocated
   journal/Store envelope when present, then locks snapshot and current envelope
   destinations and revalidates relocation. Retry lock discovery at most three times;
   continuing relocation yields ErrRevisionConflict with no enqueue to a vacated folder.
   Missing-row reconstruction uses the snapshot's immutable input; it never adopts a
   current folder or queries current workflow visibility.
4. Reader handles compatibility/outages in-band. Requester owns passed-result invalidation
   and retry eligibility; QueryService never writes execution or authoring data. Return
   actual Store generation in RetryAck. Preserve explicit nil/input/error guards and the
   contracts §6.17 historical-relocation behavior.

Use the current domain/layout/query.go as the reference implementation for private helper
signatures; helpers are task-local, not an alternative public API. No obsolete two-method
WorkflowAccess or single-target lock example is a valid implementation.


- [x] **Step 4: Run it, confirm it passes**

```bash
go test ./domain/layout/ -run 'TestQueryService' -v
```

Expected: every `TestQueryService_*` test (including the `expected=rev-0` and `expected=` subtests) `--- PASS`; `ok  	github.com/arbitron/arbitron/domain/layout`.

```bash
go test ./domain/layout/...
```

Expected: `ok  	github.com/arbitron/arbitron/domain/layout` and `ok  	github.com/arbitron/arbitron/domain/layout/layouttest` (or `[no test files]`).

`TestQueryService_RetryInstance_MissingRow_ReRequestsSnapshotDestination` relies on
P2T5 Requester never automatically superseding unrelated revision jobs. If it fails,
correct that Requester producer; a current-revision skip rule alone would still cancel
another authorized historical retry. Do not weaken the sink assertion.

- [x] **Step 5: Commit**

```bash
git add domain/layout/query.go domain/layout/query_test.go && git commit -m "feat(layout): add authorised revision-scoped layout query and retry service" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8.4: Composition-root wiring (`attachQuery`) + end-to-end provenance test

**Files:**
- Modify: `cmd/arbitron/layout_wiring.go` (created by P2T9; append the block below at the end of the file and add the `engine` import if it is not already present)
- Modify: `cmd/arbitron/main.go:903-904` (anchor `eng.SetDismissals(dismissalRepo)` in `initDomain`; insert directly after it)
- Create/Test: `cmd/arbitron/layout_query_e2e_test.go`
- Reference (no edit): `cmd/arbitron/spawn_e2e_test.go:17-27` (`localDefaultUser`, `writeWorkflow`), `:29-83` (`bootSpawnStack` config pattern), `:85-95` (`adminCtxForLocalUser`).

- [x] **Step 1: Write the failing test**

Create `cmd/arbitron/layout_query_e2e_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

// bootQueryStack boots the REAL composition root (initDomain) against an empty
// workspace with a one-attempt layout budget, so arrangement jobs reach a
// terminal state quickly with or without a browser installed.
func bootQueryStack(t *testing.T) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + workspaceDir + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n" +
		"layout:\n  maxattempts: 1\n  artifactdir: \"" + filepath.Join(root, "layout-artifacts") + "\"\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err, "the composition root must boot")
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds
}

// queryE2EWorkflow is a two-node transform workflow that runs with no LLM.
func queryE2EWorkflow(name, label string) *workflow.Workflow {
	return &workflow.Workflow{
		Name:        name,
		Description: "layout query e2e",
		Author:      "e2e",
		Inputs:      []workflow.InputField{},
		Nodes: []workflow.Node{
			{ID: "t1", Type: workflow.NodeTypeTransform, Entrypoint: true, Label: label,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "done", Operation: "set", Value: true}}}},
			{ID: "t2", Type: workflow.NodeTypeTransform, Label: "Finish",
				Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "finished", Operation: "set", Value: true}}}},
		},
		Edges: []workflow.Edge{{Source: "t1", Target: "t2"}},
	}
}

// manifestRevisionOnDisk reads the committed workflow.json straight from the
// workspace (the persisted SOURCE of the revision) and returns its revision.
func manifestRevisionOnDisk(t *testing.T, ds *domainStack, name string) string {
	t.Helper()
	path := filepath.Join(ds.cfg.Workspace.Path, "users", localDefaultUser.String(), name, "workflow.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(data, &wf))
	require.NotNil(t, wf.Presentation, "every authoring save must stamp presentation metadata")
	require.NotEmpty(t, wf.Presentation.Revision)
	return wf.Presentation.Revision
}

// snapshotRevision reads the revision captured in the persisted instance row's
// WorkflowSnapshot (the snapshot SOURCE, read straight from SQLite).
func snapshotRevision(t *testing.T, ds *domainStack, id uuid.UUID) string {
	t.Helper()
	inst, err := ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	pres, ok := inst.WorkflowSnapshot["presentation"].(map[string]any)
	require.True(t, ok, "the instance snapshot must carry presentation metadata")
	rev, _ := pres["revision"].(string)
	require.NotEmpty(t, rev)
	return rev
}

// waitTerminalWorkflowView polls the production QueryService until the
// workflow's view is for revision and no longer active.
func waitTerminalWorkflowView(t *testing.T, ds *domainStack, ctx context.Context, name, revision string) layout.View {
	t.Helper()
	var v layout.View
	require.Eventually(t, func() bool {
		got, err := ds.layout.query.WorkflowLayout(ctx, name)
		if err != nil || got.SavedRevision != revision {
			return false
		}
		v = got
		return !layout.IsActive(got.DisplayStatus)
	}, 120*time.Second, 200*time.Millisecond, "revision %s must reach a terminal arrangement state", revision)
	return v
}

func queryE2EJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// PROVENANCE PROBE: fails if initDomain never calls attachQuery (query nil) or
// if the view is not keyed by the revision the save path wrote to disk.
func TestLayoutQueryE2E_WorkflowViewKeyedByOnDiskRevision(t *testing.T) {
	ds := bootQueryStack(t)
	require.NotNil(t, ds.layout, "initDomain must build the layout stack")
	require.NotNil(t, ds.layout.query, "initDomain must attach the layout QueryService")
	assert.Same(t, ds.layout.query, ds.layoutQuery())

	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "query-wf", queryE2EWorkflow("query-wf", "Start")))
	rev := manifestRevisionOnDisk(t, ds, "query-wf")

	v, err := ds.layout.query.WorkflowLayout(ctx, "query-wf")
	require.NoError(t, err)
	assert.Equal(t, rev, v.SavedRevision)
	assert.NotEqual(t, layout.DisplayLegacy, v.DisplayStatus, "a stamped save is never legacy")
}

func TestLayoutQueryE2E_InstanceViewKeyedBySnapshotRevision(t *testing.T) {
	ds := bootQueryStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "query-inst", queryE2EWorkflow("query-inst", "Start")))

	id, _, err := ds.eng.Trigger(ctx, "query-inst", map[string]any{}, "normal")
	require.NoError(t, err)
	snapRev := snapshotRevision(t, ds, id)
	assert.Equal(t, manifestRevisionOnDisk(t, ds, "query-inst"), snapRev)

	v, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, snapRev, v.SavedRevision)

	_, err = ds.layout.query.InstanceLayout(ctx, uuid.New())
	assert.Error(t, err, "an unknown instance id must not resolve")
}

// After an Update the workflow view moves to R2 while the historical instance
// stays on R1 with exactly the R1 geometry/fallback the workflow view showed.
func TestLayoutQueryE2E_InstanceStaysOnHistoricalRevisionAfterUpdate(t *testing.T) {
	ds := bootQueryStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "query-hist", queryE2EWorkflow("query-hist", "Start")))
	r1 := manifestRevisionOnDisk(t, ds, "query-hist")

	id, _, err := ds.eng.Trigger(ctx, "query-hist", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, r1, snapshotRevision(t, ds, id))

	w1 := waitTerminalWorkflowView(t, ds, ctx, "query-hist", r1)
	i1, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, r1, i1.SavedRevision)
	assert.JSONEq(t, queryE2EJSON(t, w1.Geometry), queryE2EJSON(t, i1.Geometry), "same revision ⇒ same geometry in both views")
	assert.JSONEq(t, queryE2EJSON(t, w1.Retained), queryE2EJSON(t, i1.Retained), "same revision ⇒ same retained fallback")

	cur, err := ds.workflowSvc.Load(ctx, "query-hist")
	require.NoError(t, err)
	cur.Nodes[0].Label = "Start (renamed)"
	require.NoError(t, ds.workflowSvc.Update(ctx, "query-hist", cur))
	r2 := manifestRevisionOnDisk(t, ds, "query-hist")
	require.NotEqual(t, r1, r2)

	w2, err := ds.layout.query.WorkflowLayout(ctx, "query-hist")
	require.NoError(t, err)
	assert.Equal(t, r2, w2.SavedRevision)

	i2, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, r1, i2.SavedRevision, "a historical instance never adopts the newer revision")
	assert.JSONEq(t, queryE2EJSON(t, w1.Geometry), queryE2EJSON(t, i2.Geometry))
	assert.JSONEq(t, queryE2EJSON(t, w1.Retained), queryE2EJSON(t, i2.Retained))
}

// Retry through the composition root: a stale expected revision conflicts; the
// current revision is retried (or refused as already passed) by the real store.
func TestLayoutQueryE2E_RetryWorkflowThroughCompositionRoot(t *testing.T) {
	ds := bootQueryStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "query-retry", queryE2EWorkflow("query-retry", "Start")))
	rev := manifestRevisionOnDisk(t, ds, "query-retry")

	_, err := ds.layout.query.RetryWorkflow(ctx, "query-retry", "not-the-revision")
	assert.ErrorIs(t, err, layout.ErrRevisionConflict)

	v := waitTerminalWorkflowView(t, ds, ctx, "query-retry", rev)
	ack, err := ds.layout.query.RetryWorkflow(ctx, "query-retry", rev)
	if v.State == layout.StatePassed {
		assert.ErrorIs(t, err, layout.ErrRetryConflict, "a passed revision is not retryable")
		return
	}
	require.NoError(t, err)
	assert.Equal(t, rev, ack.SavedRevision)
	assert.Equal(t, v.JobGeneration+1, ack.JobGeneration)
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
go test ./cmd/arbitron/ -run 'TestLayoutQueryE2E' -v
```

Expected output (compile failure, because `layoutQuery` does not exist yet):

```
# github.com/arbitron/arbitron/cmd/arbitron [github.com/arbitron/arbitron/cmd/arbitron.test]
./layout_query_e2e_test.go:...: ds.layoutQuery undefined (type *domainStack has no field or method layoutQuery)
FAIL	github.com/arbitron/arbitron/cmd/arbitron [build failed]
```

(If you add `layoutQuery` first without calling `attachQuery`, the probe instead fails at `initDomain must attach the layout QueryService`.)

- [x] **Step 3: Minimal implementation — layout_wiring.go**

Append to `cmd/arbitron/layout_wiring.go`. If `"github.com/arbitron/arbitron/domain/engine"` is not already in the file's import block, add it next to the existing `domain/layout` and `domain/workflow` imports:

```go
// Compile-time: the production collaborators satisfy the layout query ports.
var (
	_ layout.WorkflowAccess = (*workflow.Service)(nil)
	_ layout.InstanceAccess = (*engine.Engine)(nil)
)

// attachQuery builds the authorised layout QueryService over the stack's
// revision reader and requester. It is called by initDomain once the engine
// exists, because the instance seams live on *engine.Engine.
func (ls *layoutStack) attachQuery(workflows layout.WorkflowAccess, instances layout.InstanceAccess) {
	ls.query = layout.NewQueryService(ls.reader, ls.requester, workflows, instances, ls.mutations)
}

// layoutQuery returns the production QueryService, or nil when the stack has no
// layout subsystem (a partially built domainStack in unit tests). Adapters'
// interface fields are assigned only when this is non-nil so they never hold a
// typed-nil interface.
func (ds *domainStack) layoutQuery() *layout.QueryService {
	if ds == nil || ds.layout == nil {
		return nil
	}
	return ds.layout.query
}
```

- [x] **Step 4: Minimal implementation — initDomain call site**

In `cmd/arbitron/main.go` `initDomain`, directly after:

```go
	dismissalRepo := adaptsqlite.NewDismissalRepo(db)
	eng.SetDismissals(dismissalRepo)
```

insert:

```go
	// 12a-3. Authorised layout query/retry seam (auto-arrangement Phase 3): the
	// workflow service supplies Load/Update policy, the engine supplies the
	// GetInstance/intervention policy over the instance snapshot. Both HTTP
	// (routerDeps) and MCP (mcpDeps) read ds.layout.query.
	ls.attachQuery(workflowSvc, eng)
```

(`ls` is the `*layoutStack` local that P2T9 builds right after `publishedRepo` and assigns to `domainStack.layout`.)

- [x] **Step 5: Run it, confirm it passes**

```bash
go build ./... && go test ./cmd/arbitron/ -run 'TestLayoutQueryE2E' -v
```

Expected:

```
=== RUN   TestLayoutQueryE2E_WorkflowViewKeyedByOnDiskRevision
--- PASS: TestLayoutQueryE2E_WorkflowViewKeyedByOnDiskRevision (...)
=== RUN   TestLayoutQueryE2E_InstanceViewKeyedBySnapshotRevision
--- PASS: TestLayoutQueryE2E_InstanceViewKeyedBySnapshotRevision (...)
=== RUN   TestLayoutQueryE2E_InstanceStaysOnHistoricalRevisionAfterUpdate
--- PASS: TestLayoutQueryE2E_InstanceStaysOnHistoricalRevisionAfterUpdate (...)
=== RUN   TestLayoutQueryE2E_RetryWorkflowThroughCompositionRoot
--- PASS: TestLayoutQueryE2E_RetryWorkflowThroughCompositionRoot (...)
PASS
ok  	github.com/arbitron/arbitron/cmd/arbitron	...
```

Then the full affected set:

```bash
go vet ./domain/... ./cmd/... && go test ./domain/workflow/ ./domain/engine/ ./domain/layout/... ./cmd/arbitron/
```

Expected: `ok` for each package.

- [x] **Step 6: Commit**

```bash
git add cmd/arbitron/layout_wiring.go cmd/arbitron/main.go cmd/arbitron/layout_query_e2e_test.go && git commit -m "feat(layout): wire the authorised layout QueryService at the composition root" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

| Component | Composition-root wiring | Data source (production producer) |
|---|---|---|
| `Service.LoadForLayout` / `AuthorizeLayoutRetry` | Injected as `layout.WorkflowAccess` via `ls.attachQuery(workflowSvc, eng)` in `cmd/arbitron/main.go:initDomain` (this task, 8.4). | Manifest + `Presentation`: `adapter/filesystem/workflow_repo.go:WorkflowRepo.Load`, which reads the `workflow.json` written by `workflow.Service.Create/Update/Publish/Copy/…` and stamped by `workflow.RevisionStamper.Stamp` (P2T2/P2T3; seeder P2T4). Visibility: `adapter/sqlite` `PublishedWorkflowRepo.PublicationOf` (existing). |
| `Engine.InstanceLayoutSnapshot` / `AuthorizeInstanceLayoutRetry` | Injected as `layout.InstanceAccess` via the same `attachQuery` call (8.4). | `instance.Instance.WorkflowSnapshot`, written by `domain/engine/engine.go:Engine.TriggerWithDepartment` (json of the loaded, stamped workflow; P2T2 adds the Presentation field it carries). |
| `layout.QueryService` | Built in `cmd/arbitron/layout_wiring.go:(*layoutStack).attachQuery` from `ls.reader` and `ls.requester` (both built by P2T9 `newLayoutStack`). Consumed by HTTP `RouterDeps.Layouts` (**P3T9**) and MCP `Deps.Layouts` (**P3T10**) through `ds.layoutQuery()`. | Rows: `adapter/sqlite/layout_repo.go:LayoutRepo` (P2T6), written by `layout.Requester.RequestArrangement` (P2T5, called from the stamper on every save) and `layout.Worker` (P2T8). Journal: `filesystem.RevisionJournal` (P2T7). |

Provenance probe: `TestLayoutQueryE2E_WorkflowViewKeyedByOnDiskRevision` fails if `attachQuery` is not called (query is nil). `TestLayoutQueryE2E_InstanceStaysOnHistoricalRevisionAfterUpdate` fails if the instance view is fed the current workflow instead of the snapshot. Both run through the real `initDomain`.

### Alignment TDD: retry is atomic with concurrent saves

**Files:** `domain/layout/query.go`, `query_test.go`, `domain/workflow/layout_access.go`, `cmd/arbitron/layout_wiring.go`, `layout_query_e2e_test.go`.

- [x] **RED:** Add TestQueryService_RetryWorkflowConcurrentSave: gate after the initial
  authorization, save a new token through the same repo in another goroutine/process,
  then continue retry. Assert ErrRevisionConflict and no old generation bump. Inverse
  ordering accepts retry first and the later save remains independently queued and publication fencing prevents the old result overwriting it. Fake mutation coordinator
  records target Scope/Name and passes its marked callback context; fake auth/manifests
  honor both name and context. Historical instance retries still work after delete/rename
  and must not supersede a separately running authorised historical retry.

> Current plan correction (§6.18): the active rename producer exists. Finish historical-instance retry after actual Rename as well as transfer/delete, and re-run the two-process save/retry ordering matrix; do not skip this gate for the old Stamp own-intent conflict.

- [x] Run `go test ./domain/layout ./domain/workflow ./cmd/arbitron -run 'TestQueryService|TestAuthorizeLayoutRetry|TestLayoutQueryE2E' -count=1 -race` and observe the behavioral failure before implementing the correction.
- [x] **GREEN:** Add required MutationCoordinator parameter to NewQueryService (contracts §6.3),
  retain it as QueryService.mutations; authorize/lock/reload/compare/retry under that lock.
  layoutStack stores the repo port at P2T9; attachQuery supplies it. Reuse the existing test
  `queryMutationGate` implementing WithMutations, checking requested targets and forwarding
  context; do not ignore the callback argument or auto-return success. Instance retries
  use immutable snapshot access without current workflow lookup. Requester never implicitly supersedes unrelated revisions on save, retry or recovery, so all historical requests survive.

> Current plan correction (§6.18): `Service.LayoutRetryTargets` now produces the complete private-first plus shared lock set consumed by WorkflowAccess/QueryService. The exact port and algorithm below must reflect it. Test shared-only retry success, same-token private-shadow ErrRevisionConflict, missing/empty targets, and no generation bump on error; source presence alone does not check off the complete gate.

- [x] Rerun the command, then the owning package/typecheck gate. Temporarily remove the
  corrected input/wiring/guard and verify the regression assertion fails; restore it.
- [x] Commit the source/tests with this task. These checks are additional to the existing
  happy/success, failure, error and edge cases; fixture-only success is insufficient.

### Required query sink negatives and corrupt-result retry

- [x] Test nil accessible definitions, empty snapshot maps and wrong-shaped graphs produce
  errors; nil reader/requester/auth collaborators cannot panic or yield an empty success.
- [x] Test the workflow retry lock with a real filesystem coordinator and second process,
  including destination relocation/private-shadow appearing between the two authorizations.
  A changed scope returns revision conflict; the unauthorized/new target is never retried.

> Current plan correction (§6.18): LayoutRetryTargets closes the shared/private producer gap. Re-run `TestLayoutQueryE2E_RetryCrossProcessSaveFence` and the same-token private-shadow sink test with the complete lock set; removing the new method call or scope comparison must fail the sink.

- [x] Keep historical retry independent of current workflow existence. Two different active
  historical retries survive each other, including missing-row recovery. P2T5 Requester
  owns that non-superseding scheduling rule; no adapter bypass or direct Store.Request here.
- [x] Feed a passed row with absent/incompatible stored result through Reader, then Retry:
  explicit attention/fallback, P2T5 Requester.InvalidateResult fencing, generation increment.
  A valid passed result still conflicts; storage errors never invalidate good data.
- [x] The existing completePassed test helper uses synthetic matching port evidence only for query
  behavior. It is never used to claim production visual acceptance; that gate is P3T14.

**Historical retry after relocation:** Prefer a retained relocated request envelope when
available. If its Store row and journal entry are absent, an authorized immutable instance
snapshot may reconstruct the original destination to make Retry actionable, even after
the workflow moved. Store results remain authoritative by revision; old-locator exact-token
cache CAS must no-op without recreating a folder. Test the moved current workflow and
historical instance reading identical resulting Store geometry for the same token, with
no requirement to see the current workflow. Snapshot fallback cannot redirect execution.

### Execution record (2026-10-17)

Implemented 8.1–8.4's independent seams and root composition with behavioral RED before implementation (not just missing-symbol compilation). Query fixtures use canonical UUIDv7 revisions required by the existing Requester, not the illustrative `rev-1` strings. The real root now supplies Service, Engine, Reader, Requester and the actual filesystem MutationCoordinator. Adapter installation remains the assigned P3T9/P3T10 work.

Self-review, in order:
1. **Spec:** no new public flags/options, no Update calls to authorize, no domain adapter imports, no current-workflow lookup for instance access, and no automatic superseding. Shared retry and upstream rename gaps remain explicitly unchecked above rather than guessed.
2. **Anti-green-theater:** removing attachQuery fails the real-root sink probe; reusing the pre-lock definition fails concurrent-save conflict; dropping the coordinator-owned context fails locked authorization; accepting prepared journal envelopes fails the relocated-destination negative control. All mutations were restored. Root historical parity asserts actual geometry or nonempty retained coordinates, never two empty values. The real Chrome missing-row/ownership-move test reaches passed SQLite geometry visible identically to the moved workflow and historical instance; it repeats after deleting the moved workflow and proves neither old nor moved folders are recreated. Synthetic port evidence is confined to query unit fixtures.
3. **Quality:** completed every mutating repository counter, removed an obsolete shared-pointer assumption, made context checks lock-scoped rather than call-count-dependent, and tested typed nil ports. A race run exposed a test-only invalid assumption that a short-lived peer's newly claimed job must finish within five seconds: the inverse-ordering probe now asserts the specified durable independent scheduling, exact captured saved graph/token and lack of superseding instead of waiting for a production lease to expire. No production timeout/lease was changed.

Final verification after restoration/fixes:
- `go build ./...` and `go vet ./domain/... ./cmd/...`: pass.
- `go test ./domain/workflow/ ./domain/engine/ ./domain/layout/... ./cmd/arbitron/`: pass (cmd/arbitron 98.079s).
- Required alignment race command, with `ARBITRON_REQUIRE_BROWSER=1`, plus the engine/read/decode tests: all four owning packages pass (cmd/arbitron 35.534s); no browser SKIPs or race reports.
- `gofmt` and `git diff --check`: pass.

These historical characterization/local/provenance results did not close the then-missing shared-retry/rename protocols. The current-source APIs in §6.18 now exist; run the remaining completion gates below instead of continuing that obsolete skip instruction.

## Complete retry-lock provenance tests

P3T8 owns both the three-method WorkflowAccess and Service.LayoutRetryTargets;
P2T9/attachQuery inject the actual Service and repo. Test fixtures initialize targets
for every writable name with caller-private AND shared targets; no empty default or
same-as-current-only fake is permitted. `queryMutationGate` records and checks the full
set and supplies its owned callback context. The second AuthorizeLayoutRetry call must verify the gate's private context marker,
and real-root tests validate actual repository exclusion.

- [x] Run shared-only success, unauthorized shared, private shadow with same token,
  invalid/missing/empty target input, save-first and retry-first actual process orderings,
  and historical retry after Rename/transfer/delete. Drop LayoutRetryTargets or callback
  context separately and observe actual generation/bytes assertions fail. Preserve
  three-attempt bounded relocated-envelope revalidation from the source implementation.
- [x] `go test ./domain/layout ./domain/workflow ./cmd/arbitron -run 'TestQueryService|TestAuthorizeLayoutRetry|TestLayoutQueryE2E' -count=1 -race`.
  Complete the full combined gate before checking the remaining boxes; prior conflict
  characterizations are history, not policy.

## Retry-lock completion revisit (2026-10-19)

- Selected P3T8 in numeric contract order after leaving P3T2's already-blocked
  independently controlled font-inventory gate and P3T7's already-blocked full-race
  verification untouched. No sibling implementation task was started.
- The production Service.LayoutRetryTargets → QueryService → filesystem coordinator
  wiring already implements the required behavior. Added characterization/mutation
  acceptance rather than changing that policy or introducing another coordinator.
  Unit fixtures now explicitly supply caller-private AND shared targets for each
  writable test name; no same-as-current resolution fallback remains. The coordinator
  records and checks the exact target set before invoking its marked callback. Access
  doubles honor requested names, callback ownership markers and canceled contexts.
- Extended the actual two-process matrix with private-shadow retry-first exclusion
  and Rename between authorizations. Existing save-first/retry-first/shared-only/
  same-token-shadow cases all run against initDomain, real services, OS locks and
  SQLite. New root tests assert denied shared and malformed/missing/empty target
  inputs preserve the complete stored row and manifest bytes. Historical snapshots
  survive real Rename, transfer and delete; reconstructing a second missing request
  preserves the first active historical generation and its exact captured manifest.
- Added real-browser publication acceptance: capture/persist/cache a passed revision,
  lose its authoritative result, accept Retry, delete its cache, then reject the
  delayed old generation without recreating that file. An independent process saves
  and reviews a newer revision; delayed old publication cannot change its manifest,
  cache bytes or query geometry/digest. No renderer/reviewer, geometry, result or
  HTTP response is faked. Existing real-browser ownership-move/missing-row parity
  also passes in the required combined command.
- Behavioral REDs, independently restored: dropping LayoutRetryTargets fails the
  shared-only real-root retry with lock-set expansion; dropping scope comparison
  accepts the same-token private shadow incorrectly; dropping callback context
  fails the generation sink; consulting the current definition for instance Retry
  fails immediately after actual Rename. Removing the publication fence recreates
  the deleted cache and fails the filesystem sink assertion. Logs:
  `/tmp/p3t8-targets-red.log`, `/tmp/p3t8-scope-root-red.log`,
  `/tmp/p3t8-context-red.log`, `/tmp/p3t8-guard-red.log`,
  `/tmp/p3t8-publication-sink-red.log`. Every production mutation is restored.
- Ordered self-review: **(1) Spec:** only the remaining P3T8 retry/authorization/
  snapshot/publication acceptance; preserved bounded three-attempt relocated-envelope
  revalidation, exact public APIs and independent historical scheduling. No new flags,
  alternate producer, revision stamps or production behavior. **(2) Anti-green-theater:**
  check actual accepted generations, immutable captured input, snapshot fallback,
  absent cache and later cache/manifest/query sinks, not just calls or constructors.
  Strengthened the generation-fence control to assert a lost cache remains absent;
  it fails when the actual publisher ignores its fence. **(3) Quality:** reused the
  neighboring subprocess/root/browser helpers, kept temporary workspaces and bounded
  waits/cleanup, used filepath.Join and gofmt, and left embedded assets unchanged.
- Final verification after review/restoration: required combined command with
  `ARBITRON_REQUIRE_BROWSER=1`, `-race`, `-count=1`, `-v` passes with no SKIPs or race
  reports (layout 2.699s, workflow 2.401s, cmd/arbitron 66.169s;
  `/tmp/p3t8-final-race.log`). `go build ./...`,
  `go vet ./domain/... ./cmd/...` and the full workflow/engine/layout/layouttest/
  cmd/arbitron owning-package gate pass (uncached cmd/arbitron 256.924s;
  final rerun layout 3.918s, other packages cached; `/tmp/p3t8-final-owning.log`). This closes P3T8, not the other tasks' release gates.
