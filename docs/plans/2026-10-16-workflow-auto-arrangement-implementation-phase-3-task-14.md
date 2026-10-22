# Workflow Auto-Arrangement — Phase 3 Task 14: Remove server-side manual layout writes + rebuild the embedded UI

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

**Alignment requirements:** Read contracts §6 (6.7) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Delete every server-side path that writes manual node positions: `Repository.SaveNodePositions`, `Service.SaveNodePositions`, `WorkflowRepo.SaveNodePositions`, the HTTP `SaveLayout` handler and the `PUT /workflows/{name}/layout` route. Keep manifest `position` parsing as the legacy fallback, and refresh the embedded dashboard build.

**Architecture:** This is a pure deletion task, guarded by regression tests written first:
- a router-level `PUT /api/v1/workflows/{name}/layout` must now return 405, because P3T9's `GET` on the same path stays;
- reflection checks prove the write methods no longer exist on the port, the service, the filesystem adapter or the HTTP handler;
- filesystem tests pin that `Load` still parses legacy `position` values.

Every test fake and test that implemented or called the removed methods is deleted, so `go vet ./...` is the compile-time proof of absence. `make ui` then re-embeds `adapter/dashboard/dist`, built from the dashboard after P3T1–P3T13 (drag/save code removed; shared renderer and render surface added).

**Tech Stack:** Go 1.25.5, chi v5.2.5, testify, `reflect`; Node/Vite dashboard build via `make ui`.

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` ("Replace manually adjusted node positions", Non-Goal "Preserving manual positions"); `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` §6 "Remove manual positioning and build" and §2 ("Remove `PUT /workflows/{name}/layout`"); contracts §0 row P3T14.

**Ordering:** run this task after P3T1–P3T13 and before the required P3T15 group migration, after P3T9 (adds `GET /{name}/layout` and `adapter/http/server_layout_test.go`) and after the dashboard tasks P3T1–P3T13. P3T12 removes `api.workflows.saveLayout` and both `WorkflowReview.tsx` callers. Line numbers are from the base checkout. P2T3 and P2T7 edit `service.go` and `workflow_repo.go` first, so locate each deletion by the quoted symbol and text.

---

## Interfaces

**Consumes (must already exist):**

```go
// adapter/http (P3T9) — package http_test helpers in adapter/http/server_layout_test.go / layout_handler_test.go
func layoutRouter(t *testing.T, layouts adapthttp.LayoutQueryService) (http.Handler, *mockInstanceRepo)
func routerDo(h http.Handler, method, path, body string) *httptest.ResponseRecorder
func newFakeLayoutQuery() *fakeLayoutQuery // records every call; (*fakeLayoutQuery).recorded() []string
// adapter/http (P3T9): r.Get("/{name}/layout", layoutHandler.GetWorkflowLayout) registered in NewRouterFromDeps
// adapter/filesystem test helpers (existing): testScope() workflow.Scope, scopeRootFor(t, base, scope) string, sampleWorkflow(name) *workflow.Workflow (workflow_repo_test.go:22, :45, :479)
// domain/workflow (existing, kept): type NodePosition struct{ X, Y float64 }; Node.Position *NodePosition `json:"position,omitempty"`
```

**Produces:** no new production API. **Removed** (anything left referencing these is a compile error):

```go
// domain/workflow/ports.go            Repository.SaveNodePositions(ctx, scope, name, positions) error
// domain/workflow/service.go          (*Service).SaveNodePositions(ctx, name, p) error
// adapter/filesystem/workflow_repo.go (*WorkflowRepo).SaveNodePositions(...)
// adapter/http/workflow_handler.go    type saveLayoutRequest; (*WorkflowHandler).SaveLayout(w, r)
// adapter/http/server.go              r.Put("/{name}/layout", wfHandler.SaveLayout)
```

---

### Task 14.1: Regression guards, then delete the write paths and every fake/test that used them

**Files:**
- Test (append): `adapter/http/server_layout_test.go` (created by P3T9)
- Test (create): `domain/workflow/layout_writes_removed_test.go`
- Test (create): `adapter/filesystem/workflow_repo_legacy_positions_test.go`
- Modify: `domain/workflow/ports.go:15`
- Modify: `domain/workflow/service.go:196` (comment), `:282-293` (`SaveNodePositions` method)
- Modify: `adapter/filesystem/workflow_repo.go:165-228` (`SaveNodePositions` method, including any lock acquisition P2T7 added inside it), `:374-377` (WriteFile manifest-protection comment)
- Modify: `adapter/http/workflow_handler.go:406-440` (`saveLayoutRequest`, `SaveLayout`)
- Modify: `adapter/http/server.go:281` (PUT route)
- Modify (fakes/tests): `adapter/mcp/tools_workflow_test.go:104-106`; `adapter/http/workflow_handler_test.go:75-77`, `:121-123`, `:805-841` (helper header + `capturingWorkflowRepo`), `:843-940` (`TestWorkflowHandler_SaveLayout`); `domain/engine/engine_test.go:58-60`; `domain/engine/engine_trigger_scope_test.go:63-65`; `domain/samples/seeder_test.go:81-83`; `domain/workflow/service_test.go:57-62`, `:706-762`; `domain/workflow/resolve_test.go:216-218`; `domain/workflow/rename_test.go:259-261`; `domain/workflow/edit_shared_test.go:86-96`; `adapter/filesystem/workflow_repo_test.go:1103-1210`
- Delete: `adapter/filesystem/workflow_repo_positions_test.go` (every test in it exercises only `SaveNodePositions`; its helpers `posScope`/`newTempWorkflow`/`posDir` are used nowhere else)

- [x] **Step 1: Write the failing regression tests**

Append to `adapter/http/server_layout_test.go`. Add `"reflect"` to its import block:

```go
// Manual layout writes are gone: PUT on the layout path is 405 (the GET status
// route on the same path still exists), and nothing reaches the layout service
// or the workflow repository.
func TestRouter_PutWorkflowLayout_MethodNotAllowed(t *testing.T) {
	q := newFakeLayoutQuery()
	router, _ := layoutRouter(t, q)

	w := routerDo(router, http.MethodPut, "/api/v1/workflows/my-wf/layout", `{"positions":{"A":{"x":1,"y":2}}}`)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Contains(t, w.Header().Get("Allow"), http.MethodGet)
	assert.Empty(t, q.recorded())
}

func TestWorkflowHandler_HasNoSaveLayout(t *testing.T) {
	_, ok := reflect.TypeOf(&adapthttp.WorkflowHandler{}).MethodByName("SaveLayout")
	assert.False(t, ok, "the manual layout write handler must be removed")
}
```

Create `domain/workflow/layout_writes_removed_test.go`:

```go
package workflow_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/workflow"
)

// Automatic arrangement replaces manual positioning: the repository port and
// the service expose no node-position write.
func TestRepository_HasNoSaveNodePositions(t *testing.T) {
	_, ok := reflect.TypeOf((*workflow.Repository)(nil)).Elem().MethodByName("SaveNodePositions")
	assert.False(t, ok, "workflow.Repository must not declare SaveNodePositions")
}

func TestService_HasNoSaveNodePositions(t *testing.T) {
	_, ok := reflect.TypeOf(&workflow.Service{}).MethodByName("SaveNodePositions")
	assert.False(t, ok, "workflow.Service must not expose SaveNodePositions")
}
```

Create `adapter/filesystem/workflow_repo_legacy_positions_test.go`:

```go
package filesystem_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
)

func TestWorkflowRepo_HasNoSaveNodePositions(t *testing.T) {
	_, ok := reflect.TypeOf(&filesystem.WorkflowRepo{}).MethodByName("SaveNodePositions")
	assert.False(t, ok, "the filesystem repo must not keep a position-patching write path")
}

// Legacy fallback: a manifest that carries node positions (pre-feature saves,
// hand-edited files, copied manifests) stays readable; the stamper (P2T2) uses
// them as the retained fallback.
func TestWorkflowRepo_Load_ParsesLegacyNodePositions(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	wfDir := filepath.Join(scopeRootFor(t, dir, testScope()), "legacy-pos")
	require.NoError(t, os.MkdirAll(wfDir, 0o700))
	manifest := `{
  "name": "legacy-pos",
  "nodes": [
    {"id": "A", "type": "transform", "entrypoint": true, "config": {}, "position": {"x": 120.5, "y": -40}},
    {"id": "B", "type": "transform", "config": {}}
  ],
  "edges": [{"source": "A", "target": "B"}]
}`
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "workflow.json"), []byte(manifest), 0o644))

	loaded, err := repo.Load(context.Background(), testScope(), "legacy-pos")
	require.NoError(t, err)
	require.Len(t, loaded.Nodes, 2)
	require.NotNil(t, loaded.Nodes[0].Position)
	assert.Equal(t, 120.5, loaded.Nodes[0].Position.X)
	assert.Equal(t, -40.0, loaded.Nodes[0].Position.Y)
	assert.Nil(t, loaded.Nodes[1].Position, "a node without a stored position stays nil")
}

// A manifest position supplied through a normal authoring save is persisted
// verbatim (it is presentation data the fallback can use, not a layout write).
func TestWorkflowRepo_Update_PreservesManifestPositions(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()
	wf := sampleWorkflow("pos-roundtrip")
	require.NoError(t, repo.Create(ctx, testScope(), "pos-roundtrip", wf))

	wf.Nodes[0].Position = &workflow.NodePosition{X: 7, Y: 9}
	require.NoError(t, repo.Update(ctx, testScope(), "pos-roundtrip", wf))

	loaded, err := repo.Load(ctx, testScope(), "pos-roundtrip")
	require.NoError(t, err)
	require.NotNil(t, loaded.Nodes[0].Position)
	assert.Equal(t, workflow.NodePosition{X: 7, Y: 9}, *loaded.Nodes[0].Position)
}
```

- [x] **Step 2: Run them, confirm the guards fail (and the legacy-parse tests already pass)**

```bash
go test ./adapter/http/ -run 'TestRouter_PutWorkflowLayout_MethodNotAllowed|TestWorkflowHandler_HasNoSaveLayout' -v
go test ./domain/workflow/ -run 'TestRepository_HasNoSaveNodePositions|TestService_HasNoSaveNodePositions' -v
go test ./adapter/filesystem/ -run 'TestWorkflowRepo_HasNoSaveNodePositions|TestWorkflowRepo_Load_ParsesLegacyNodePositions|TestWorkflowRepo_Update_PreservesManifestPositions' -v
```

Expected:

```
--- FAIL: TestRouter_PutWorkflowLayout_MethodNotAllowed
    server_layout_test.go:...: Error: Not equal: expected: 405 actual: 200
--- FAIL: TestWorkflowHandler_HasNoSaveLayout
    ... the manual layout write handler must be removed
FAIL	github.com/arbitron/arbitron/adapter/http
--- FAIL: TestRepository_HasNoSaveNodePositions
--- FAIL: TestService_HasNoSaveNodePositions
FAIL	github.com/arbitron/arbitron/domain/workflow
--- FAIL: TestWorkflowRepo_HasNoSaveNodePositions
--- PASS: TestWorkflowRepo_Load_ParsesLegacyNodePositions
--- PASS: TestWorkflowRepo_Update_PreservesManifestPositions
FAIL	github.com/arbitron/arbitron/adapter/filesystem
```

The two legacy-position tests pass on purpose: they are characterization guards that must keep passing after the deletion.

- [x] **Step 3: Delete the production write paths**

1. `domain/workflow/ports.go`, inside `Repository`, delete the line:

```go
	SaveNodePositions(ctx context.Context, scope Scope, name string, positions map[string]NodePosition) error
```

2. `domain/workflow/service.go`, in the `adminSharedWrite` doc comment, change

```go
// shared-scope mutation (Update/WriteFile/SaveNodePositions against Shared()).
```

to

```go
// shared-scope mutation (Update/WriteFile against Shared()).
```

and delete the whole method together with its doc comment:

```go
// SaveNodePositions persists node layout in the caller's private scope (an admin
// may target a shared-only workflow in place; see adminSharedWrite).
func (s *Service) SaveNodePositions(ctx context.Context, name string, p map[string]NodePosition) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	privateErr := s.repo.SaveNodePositions(ctx, Private(id.UserID), name, p)
	return s.adminSharedWrite(ctx, id, name, privateErr, func(ctx context.Context, scope Scope) error {
		return s.repo.SaveNodePositions(ctx, scope, name, p)
	})
}
```

If P2T3 added revision stamping to this method, delete that too; the whole method goes.

3. `adapter/filesystem/workflow_repo.go`: delete the whole `SaveNodePositions` method and its three-line doc comment, from `// SaveNodePositions updates the position field of each node in the workflow's` through the method's closing `}` (base lines 165-228). This includes any `r.lock(...)`/unlock lines P2T7 added inside it. In `WriteFile`, change the manifest-protection comment

```go
	// Protect the manifest: workflow.json is only ever written through Create,
	// Update, or SaveNodePositions, which use type-safe structs and atomic writes.
```

to

```go
	// Protect the manifest: workflow.json is only ever written through Create
	// and Update, which use type-safe structs and atomic writes.
```

Keep the remaining lines of that comment and the `workflow.json` and `layout.json` reservation checks (P2T7) unchanged. If P2T7's lock doc comment lists `SaveNodePositions` among the lock takers, remove that word from the list.

4. `adapter/http/workflow_handler.go`: delete `saveLayoutRequest` and `SaveLayout` (base lines 406-440), from `// saveLayoutRequest is the JSON body for saving node positions.` through the closing `}` of `SaveLayout`. `writeSharedReadOnlyOrNotFound` and `maxTriggerBodyBytes` stay; other handlers still use them.

5. `adapter/http/server.go`, in the `/workflows` route block, delete:

```go
			r.Put("/{name}/layout", wfHandler.SaveLayout)
```

The P3T9 lines `r.Get("/{name}/layout", …)` and `r.Post("/{name}/layout/retry", …)` stay.

- [x] **Step 4: Delete every fake implementation and every test that used the removed methods**

Delete these methods (each one is exactly the shown block):

- `adapter/mcp/tools_workflow_test.go` (base 104-106):

```go
func (s *stubWorkflowRepo) SaveNodePositions(_ context.Context, _ workflow.Scope, _ string, _ map[string]workflow.NodePosition) error {
	return nil
}
```

- `adapter/http/workflow_handler_test.go` (base 75-77 and 121-123):

```go
func (r *mockWorkflowRepo) SaveNodePositions(_ context.Context, _ workflow.Scope, _ string, _ map[string]workflow.NodePosition) error {
	return nil
}
```

```go
func (r *errWorkflowRepo) SaveNodePositions(_ context.Context, _ workflow.Scope, _ string, _ map[string]workflow.NodePosition) error {
	return r.err
}
```

- `domain/engine/engine_test.go` (base 58-60): `func (r *mockWorkflowRepo) SaveNodePositions(...) error { return nil }`
- `domain/engine/engine_trigger_scope_test.go` (base 63-65): `func (r *scopeAwareRepo) SaveNodePositions(...) error { return nil }`
- `domain/samples/seeder_test.go` (base 81-83): `func (r *fakeRepo) SaveNodePositions(context.Context, workflow.Scope, string, map[string]workflow.NodePosition) error { return nil }`
- `domain/workflow/service_test.go` (base 57-62):

```go
func (r *fakeRepo) SaveNodePositions(_ context.Context, s workflow.Scope, name string, _ map[string]workflow.NodePosition) error {
	if _, ok := r.bucket(s)[name]; !ok {
		return workflow.ErrNotFound
	}
	return nil
}
```

- `domain/workflow/resolve_test.go` (base 216-218): `func (baseRepo) SaveNodePositions(...) error { return workflow.ErrNotFound }`
- `domain/workflow/rename_test.go` (base 259-261): `func (r *errOnDeleteNameRepo) SaveNodePositions(...) error { return r.inner.SaveNodePositions(...) }`

Delete these tests:

- `domain/workflow/service_test.go` (base 706-762): the whole `// SaveNodePositions (service-level)` section, i.e. its banner comment and `TestService_SaveNodePositions_TargetsPrivate`, `_SharedOnly_ReturnsSharedReadOnly`, `_NoIdentity_Rejected`, `_Nowhere_ReturnsNotFound`, `_OnlyAffectsCallerPrivate`. Keep the `// ListFiles (service-level)` banner that follows.
- `domain/workflow/edit_shared_test.go` (base 86-96): `TestService_SaveNodePositionsShared_Admin_Succeeds` and its two-line doc comment. The admin shared-write path stays covered by `TestService_EditShared_Admin_WritesSharedScope` and `TestService_WriteFileShared_Admin_Succeeds`.
- `adapter/http/workflow_handler_test.go` (base 805-940): replace the helper banner

```go
// ---------------------------------------------------------------------------
// Helpers: configurable mocks for SaveLayout / Stats
// ---------------------------------------------------------------------------
```

with

```go
// ---------------------------------------------------------------------------
// Helpers: configurable mocks for Stats
// ---------------------------------------------------------------------------
```

  then delete `capturingWorkflowRepo`, `newCapturingWorkflowRepo` and `(*capturingWorkflowRepo).SaveNodePositions` (keep `statsInstanceRepo`). Delete the `// Tests: SaveLayout` banner and the whole `TestWorkflowHandler_SaveLayout` function. Nothing else references `capturingWorkflowRepo`.
- `adapter/filesystem/workflow_repo_test.go` (base 1103-1210): the `// Tests: SaveNodePositions` banner and `TestWorkflowRepo_SaveNodePositions_HappyPath`, `_NotFound`, `_PathTraversalRejected`, `_EmptyPositionsMap`, `_UnknownNodeIDsIgnored`. After this deletion `strings.` has no other use in the base file; run `grep -n 'strings\.' adapter/filesystem/workflow_repo_test.go`, and if it prints nothing, remove `"strings"` from that file's import block.
- Delete the file `adapter/filesystem/workflow_repo_positions_test.go`:

```bash
git rm adapter/filesystem/workflow_repo_positions_test.go
```

Then sweep for fakes and tests that Phase 2 tasks added after this plan was written. P2T2's `revision_stamper_test.go`, P2T5's `manifests` tests and P2T7's lock tests may implement or call the method:

```bash
grep -rn "SaveNodePositions\|SaveLayout\|saveLayoutRequest\|capturingWorkflowRepo" --include=*.go .
```

Expected: production declarations/calls are absent. Reflective HasNoSaveNodePositions/HasNoSaveLayout regression names/string literals above are intentional matches and must remain. For each other remaining hit:
- a fake method: delete the method;
- a test whose only purpose is `SaveNodePositions`: delete the test;
- a concurrency/lock test that used `SaveNodePositions` as "one of the writers": change that call to `repo.Update(ctx, scope, name, wf)` on a loaded copy and keep the test.

Re-run the search and inspect every match; only the regression guard names/string literals remain. Do not delete those guards to make a grep green.

- [x] **Step 5: Run everything, confirm it passes**

> Resolved (2026-10-19): build/vet, all listed deletion/route/legacy guards and the exact `go test ./...` prerequisite of `make test-all` pass. The old sample/profile and package-timeout failures below are historical, not current blockers.

> Historical full-gate failure (rerun required after current corrections): Build, vet, all deletion/route/legacy guards and full affected HTTP/filesystem/MCP packages pass. `go test ./...` reproduces the previously recorded unrelated `adapter/config:TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` UNKNOWN_PROFILE failure; the unbounded run also exceeded 600s before cmd/arbitron completed. No sample/profile policy or verification assertion was changed to hide that failure. Local deletion completion is green; the whole-module gate remains unchecked.

```bash
go build ./... && go vet ./...
```

Expected: no output. `go vet` type-checks every test package, so this proves no fake or caller of the removed methods remains.

```bash
go test ./adapter/http/ -run 'TestRouter_PutWorkflowLayout_MethodNotAllowed|TestWorkflowHandler_HasNoSaveLayout|TestRouter_LayoutRoutes' -v
go test ./domain/workflow/ -run 'TestRepository_HasNoSaveNodePositions|TestService_HasNoSaveNodePositions|TestService_EditShared' -v
go test ./adapter/filesystem/ -run 'TestWorkflowRepo_HasNoSaveNodePositions|TestWorkflowRepo_Load_ParsesLegacyNodePositions|TestWorkflowRepo_Update_PreservesManifestPositions' -v
```

Expected: every listed test `--- PASS`, including `TestRouter_PutWorkflowLayout_MethodNotAllowed` (405, `Allow: GET`).

```bash
go test ./...
```

Expected: `ok` for every package (no `FAIL` lines).

- [x] **Step 6: Commit**

```bash
git add -A domain/workflow adapter/filesystem adapter/http adapter/mcp domain/engine domain/samples && git commit -m "refactor(workflow): remove manual node-position writes and the PUT layout route" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 14.1 execution evidence

- RED: PUT returned 200 instead of 405; all four reflection guards failed. Both legacy characterization tests already passed.
- GREEN: removed public and private write implementations (including P2T7's `workflow_operations.go` wrapper), route, request type, fakes and write-only tests. Remaining Go matches are only absence guards. Kept lock coverage using Update on a loaded manifest.
- Ordered review: (1) spec — deletion only, GET/retry and manifest parsing unchanged; (2) anti-green-theater — router asserts real 405/Allow sink, reflection covers every removed API, temporarily discarding parsed positions fails BOTH filesystem guards; restored; (3) quality — retained neighboring conventions, gofmt and diff checks clean.
- After review: build/vet and all six affected packages pass (HTTP 5.709s, filesystem 9.912s, workflow 13.702s, engine 3.227s, samples 3.870s, MCP 3.513s). Whole-suite concern is recorded above, not hidden.

### Task 14.2: Rebuild and re-embed the dashboard (`make ui`)

**Files:**
- Modify (generated): `adapter/dashboard/dist/**`, produced by `Makefile:27-30` (`ui:` → `cd dashboard && npm ci && npm run build`, then copy `dashboard/dist` into `adapter/dashboard/dist`)
- Test (existing, no edit): `adapter/dashboard/embed_test.go`

- [x] **Step 1: Confirm the client-side manual-positioning removal (P3T12) is in place**

```bash
grep -rn "saveLayout\|onNodeDragStop\|needsAutoSave" dashboard/src --include=*.ts --include=*.tsx | grep -v '\.test\.'
```

Expected: **no output** in production sources (test files may still name these symbols in regression assertions). If anything prints, P3T12/P3T3 are not merged yet. Stop and complete them first; this task must embed the post-removal UI.

- [x] **Step 2: Verify the dashboard sources**

```bash
cd dashboard && npx tsc --noEmit && npx vitest run
```

Expected: `tsc` prints nothing. Vitest ends with `Test Files  N passed (N)` and `Tests  M passed (M)`, with no failures.

- [x] **Step 3: Build and embed**

```bash
make ui
```

Expected: `npm ci` and `vite build` complete. The build lists `dist/index.html` and `dist/render.html` (P3T4's render surface entry) plus hashed assets, and the copy step succeeds. Then:

```bash
test -f adapter/dashboard/dist/index.html && test -f adapter/dashboard/dist/render.html && ! rg -l "saveLayout" adapter/dashboard/dist
```

Expected: zero exit status with both files present and no manual-write symbol. A missing HTML file or rg error must not be converted into a success echo.

- [x] **Step 4: Verify the Go side embeds and serves it**

> Resolved (2026-10-19): the exact build/three-package command passes; the final unfiltered browser-required gate also passes (renderreview 210.064s, cmd/arbitron 946.471s). Historical failure: `go build ./...`, dashboard embed tests and the complete renderreview package pass (including real Chrome). The prescribed full cmd/arbitron package did not finish within a 5m verification timeout; the subsequent exact `make test-all` hit Go's default 10m timeout in the pre-existing `TestLayoutPreflight_RealBrowserSavedFindingsAndScoreReachView/ralph-loop`. The new surface/root/route/build tests pass independently under -race (final run 97.194s; no skips). No timeout, expensive sample algorithm or unrelated test was weakened to claim a complete package pass.


```bash
go build ./... && go test ./adapter/dashboard/... ./adapter/renderreview/... ./cmd/arbitron/
```

Expected: `ok` for each package. `adapter/dashboard` embed tests serve `index.html`. `adapter/renderreview` (P3T5) loads the embedded `render.html` via `dashboard.Assets()`; nonrequired local browser tests may skip; the required final gate below must run ARBITRON_REQUIRE_BROWSER=1 and cannot skip.

- [x] **Step 5: Final full verification**

> Resolved (2026-10-19): the exact `make test-all` passes after restored assets and ordered self-review: Go suite, TypeScript, 90 Vitest files / 1380 tests and both whole browser-required packages. Log: `/tmp/p3t14-test-all.log`.

> Historical full-gate failure (rerun required after current corrections): Actual `make test-all` rebuilt both entries, then failed in its Go prerequisite: the pre-existing sample/default-profile UNKNOWN_PROFILE assertion fails, and cmd/arbitron exceeds Go's default 10m test timeout in the pre-existing sample/preflight suite. It therefore does not reach its later UI/render prerequisites. Separately TypeScript and all 1,027 Vitest tests pass with `--maxWorkers=2`; the unrestricted run hit a 5s font-test timeout under concurrent host load. A later bounded full UI rerun hit the existing ArrangedGraph recenter spy timing assertion; that file and report.test.ts then passed in isolation (42 tests) with one worker. These separate greens do not replace the final full gate. Additional exploratory whole-package -race verification reproduces the pre-existing 15ms TestRun_StageTimeout flake and unsynchronised engine_spawn_stamp_test/mockInstanceRepo races; this task's focused producer and real surface -race suites pass.


```bash
make test-all
```

Expected: rebuilt embedded assets, dashboard typecheck/Vitest, Go tests and required real-browser layout/surface tests all pass without browser skips.

- [x] **Step 6: Commit the refreshed embedded build**

The restored `make ui` output is byte-identical to the already-current embedded distribution (including bundled fonts, index.html and render.html); there is no generated delta to fabricate. Rebuild/absence verification is recorded in this task's final commit alongside its production-surface tests.


```bash
git add -A adapter/dashboard/dist && git commit -m "build(dashboard): re-embed UI with shared arranged renderer and no manual layout saves" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

| Item | Wiring | Data source |
|---|---|---|
| Removal of `PUT /workflows/{name}/layout` | `adapter/http/server.go:NewRouterFromDeps`. The route is gone; `GET /{name}/layout` and `POST /{name}/layout/retry` stay, wired by **P3T9** through `RouterDeps.Layouts` → `ds.layoutQuery()` (**P3T8**). The 405 test drives the real router. | n/a (no producer remains). Node positions are now produced only by automatic arrangement: `layout.Worker` (**P2T8**) stores the geometry and `filesystem.LayoutCache` (**P2T7**) publishes it. |
| Legacy `Node.Position` fallback (kept) | Read by `adapter/filesystem/workflow_repo.go:WorkflowRepo.Load` and consumed by `workflow.RevisionStamper.Stamp` retained derivation (**P2T2**) and the dashboard fallback (**P3T3** `fallbackPositions`). | Pre-feature `workflow.json` files and manifests that authoring saves (`Service.Create/Update`) persist verbatim. Pinned by `TestWorkflowRepo_Load_ParsesLegacyNodePositions` and `TestWorkflowRepo_Update_PreservesManifestPositions`. |
| Embedded dashboard (`adapter/dashboard/dist`) | `adapter/dashboard/embed.go:Handler()` is mounted as `RouterDeps.DashboardHandler` in `runServe` via `ds.routerDeps(...)` (**P3T9**). `dashboard.Assets()` feeds `renderreview.BrowserRenderer` (**P3T5/P3T7**). | `make ui` output from the dashboard sources changed by **P3T1–P3T13**. |

### Task 14.3: Real-browser workflow/instance parity at the production sink

**Files:** Create `cmd/arbitron/layout_surfaces_e2e_test.go`; modify Makefile `test-render`
to include `TestLayoutSurfaces_` and CI `.github/workflows/layout.yml` (P3T7 creates it).
Consumes P3T7 real-browser test helpers, P3T9 `domainStack.routerDeps`, embedded dashboard,
P3T12 WorkflowReview and P3T13 ExecutionView. Use chromedp already added by P3T5.

- [x] **Step 1 — RED:** Add `TestLayoutSurfaces_WorkflowAndInstanceUseReviewedGeometry`:
  build real initDomain/router, save pr-review and feature-pipeline through the real
  authoring API, create a real instance snapshot using Engine.Trigger, await production
  rendered review, open workflow and instance routes in Chrome. For each route compare
  every `[data-arranged-node]` box/text and `[data-edge-path]` actual d/marker/label with
  the persisted generation's geometry, after accounting for viewport transform. Assert
  the routes share savedRevision/resultGeneration/digest and practical initial zoom.
  No mocked graph, layout query, renderer, reviewer or HTTP layout response is allowed.
  Add a completed-instance-before-layout-finishes case with the production renderer
  gated at its boundary, then release it and observe the live DOM refresh without a
  WebSocket event. Unmount/cleanup removes polling.

> Resolved (2026-10-19): both named sample tests now pass through both actual routes, including all persisted crossing gaps, dashes, markers and labels. The completed-before-review/unmount/no-WebSocket test passes and fails when the instance's polled geometry input is removed. Historical failure: The exact two-sample test was written and executed through initDomain, real HTTP authoring, Engine.Trigger's persisted snapshot (held before the real execution queue), SQLite, BrowserRenderer/ReadabilityReviewer and both embedded browser routes. pr-review passed every box/text/full gap-split route/marker/branch-label/zoom/revision/generation/digest sink. feature-pipeline again reached needs_attention after five attempts: branch ink missing on edge 49, indistinct labels 31/34 and ambiguous crossings [34,49], [34,53], [14,53], [78,61], [77,69], [78,77] (same P3T7 producer blocker). No review rule, threshold or passed assertion was relaxed. Per the prohibition on knowingly failing commits, only the independently green `TestLayoutSurfaces_PRReviewWorkflowAndInstanceUseReviewedGeometry` slice is retained, explicitly NOT the two-sample release gate. To reproduce the full failing gate, run that test body for feature-pipeline too with attempt limit 5 and the unchanged StatePassed assertion. Actual log: /tmp/layout14-surfaces-red.log. Completed transform/Unicode late-review, SPA unmount/poll cleanup and no-execution-WebSocket refresh probes are independently implemented and green.

- [x] **Step 2 — RED:** Add historical retry after a newer workflow save, cache deletion,
  restart, needs_attention and render-failure cases. Inspect graph **and** truthful panel;
  a missing renderer/status/geometry input must fail or show explicit unreviewed fallback,
  never silently pass. Recovery uses the real P2T7 Task 7.5 `CommitSource` (contracts §6.2);
  never inject a passing source fake into this acceptance gate.
- [x] **Step 3:** Build `make ui`, then run `ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron -run TestLayoutSurfaces_ -count=1 -v` and observe RED for any missing route wiring.

Actual REDs: crossing-free candidates reached the HTTP/UI sink with `crossings:null`, so completed instances falsely stayed on fallback with “geometry has no crossing list”; repairing a deleted historical result accepted Retry but Worker aborted on its missing prior Result lookup. Focused behavioral REDs reproduce both before their minimal producer fixes. GREEN: Arrange emits the required empty crossings array; Worker replaces an actually missing best while still rejecting storage outages. No route/status validation was weakened.

- [x] **Step 4 — GREEN:** Fix only the owning producer/route integration revealed by the
  failures, rebuild embedded assets, rerun to GREEN and then `make test-all`. Delete
  the instance view's polled layout prop temporarily; the parity/update tests must fail.
  Restore and commit tests, embedded build and any focused fixes. This is the final
  acceptance gate; earlier fixture/component tests do not replace it.

> Resolved completion gate (2026-10-19): full feature-pipeline and whole-suite acceptance pass; P3T7 already records the Docker/runtime verification. The new killed-writer browser matrix closes this task's private/shared rename and unavailable recovery checks. Historical completion note: full feature-pipeline, Docker/runtime and whole-suite acceptance remains unchecked. Active rename and settled raw FileReader producers now exist (contracts §6.18); finish the actual killed-writer/private-rename/unavailable route matrix using them, without an unlocked preflight or fabricated recovery receipt. Preserve the green transfer/history/late-layout sinks and their missing-polled-input mutation. Test-render already selects LayoutSurfaces; P3T15 adds required group/child-snapshot surface acceptance after its v2 migration.


**Acceptance semantics:** Do not execute real feature-pipeline/pr-review agent/script
missions merely to obtain an instance screenshot. Engine.Trigger captures the immutable
snapshot; hold its execution queue using the real lifecycle/control seam before executors
run, then inspect the authorized instance view. For the completed-before-layout case use
a deterministic transform-only workflow through the real engine (same renderer/query/UI),
plus the full sample visualization parity gate. A test boundary may delay the production
renderer and then call it unchanged; it may not replace it with passing fake evidence.
Record instance/revision/generation/digest from actual HTTP JSON and DOM. Historical retry
compares the instance to its own earlier saved revision, never to the newer workflow view.

**Peer-crash browser case:** After the P3T9 second-process unsettled read returns503,
keep the original graph/panel visible, show the safe availability message and back off.
After the real read settler/worker resolves the journal/registry commit, the browser
refreshes to only the committed revision. Source is P2T7 WorkflowRepo.SetReadSettler +
P2T9 injection; absent injection must fail this gate instead of showing uncommitted nodes.

**Historical retry after relocation:** Prefer a retained relocated request envelope when
available. If its Store row and journal entry are absent, an authorized immutable instance
snapshot may reconstruct the original destination to make Retry actionable, even after
the workflow moved. Store results remain authoritative by revision; old-locator exact-token
cache CAS must no-op without recreating a folder. Test the moved current workflow and
historical instance reading identical resulting Store geometry for the same token, with
no requirement to see the current workflow. Snapshot fallback cannot redirect execution.

## Final execution evidence and ordered self-review

1. **Spec compliance:** Deleted every public/private manual-write implementation, port, handler and route; preserved read-only GET/retry and manifest-position parsing. Adapted the older root pending-recovery test to assert 405 for the now-removed PUT rather than expecting a manual writer to return503. Rebuilt both embedded entries with no generated byte changes. The only additional production edits fix the two actual producer defects revealed by this task's real browser acceptance (empty crossing-array wire shape and missing-prior-result Retry). No configuration flags, authentication bypass, alternate graph or fake production recovery source was added. The full feature-pipeline/peer-crash release acceptance remains unchecked, not renamed as success.
2. **Anti-green-theater:** Root tests author over the real authorised HTTP router and read actual SQLite generation/results, hashed PNGs, immutable Engine snapshots and both SPA routes. The sample execution queue boundary delays/delegates the real queue; sample executors never run. Browser assertions cover every node box/text/type, edge ordinal/full gap-split path, visible marker shape/orientation/colour, label box/text, practical zoom and truthful panel. The completed transform/Unicode test starts with fallback, verifies active polling and SPA unmount cleanup, releases the actual renderer unchanged, asserts the live graph refreshes and observes no execution WebSocket event. Historical Retry repairs real SQL result loss after newer save/cache deletion/restart, and missing-row retry after ownership transfer uses the immutable snapshot without recreating the old folder. Deleting ExecutionView's polled geometry input and rebuilding makes the DOM test fail; restored. Dropping parsed positions fails both filesystem characterization guards; restored. Focused producer REDs precede both fixes; the result-store fake matches exact revision AND generation and distinguishes absence from outage.
3. **Code quality:** Kept the existing root/adapter helpers and conventions, joined boundary goroutines, bounded waits and cleanup, used actual browser computed bounds with viewport transforms, and waited for React Flow's asynchronous edge mount rather than accepting missing edges. No renderer, reviewer, query or HTTP layout response is faked. gofmt, diff checks, build and vet are clean.

After this ordered review: final browser-required root/build/removed-route suite passes under -race (97.194s, no skips); producer Arrange/Worker suite passes under -race (22.863s); all six affected Go packages pass; embedded dashboard checks pass. The complete renderreview package passed during make test-all (413.257s). TypeScript and isolated display/font tests pass; the earlier bounded whole-dashboard run passed all 1,027 tests. The exact make test-all failure and unresolved upstream release gates are documented above. Local logs: /tmp/layout14-final-root.log, /tmp/layout14-ui-isolated.log, /tmp/layout14-test-all.log. At that earlier execution Task 14 was **partially completed, not release-complete**; the completion revisit below supersedes that status.

## Completion revisit (2026-10-19)

Selected P3T14 after leaving the unchanged P3T2 two-browser inventory and P3T7 full-race blockers alone. Current source/history already supplies passing complex-sample geometry, Docker runtime and both sample route tests; those are no longer reasons to skip this task. P3T15 remains a separate, untouched task.

- Added `cmd/arbitron/layout_surfaces_recovery_test.go`. A subprocess renames an already mounted reviewed workflow through actual Service/SQLite/filesystem ports, pauses immediately before/after durable identity, and is killed. All four private/shared × identity-boundary cases assert actual HTTP 503 bodies, safe visible panel, retained full node/text/path/marker/label geometry, exponential polling, and immutable authorized instance geometry. The real CommitSource then settles by registry authority; a restarted initDomain/worker supplies reviewed persisted evidence for the committed winner. Aborted targets are removed and never queued. Cache is deleted before the crash, so it cannot supply retained geometry. Before-identity recovery refreshes the existing page through its scheduled backoff GET, without navigation/focus/WebSocket rescue.
- Behavioral RED: removing `repo.SetReadSettler(ls.commits)` prevents the browser's required durable-recovery status; `/tmp/p3t14-peer-red.log`. Restoring the existing production wiring passes all four cases (83.431s). No new production flag, fake geometry/review, settlement bypass or alternate root was needed.
- Required missing-polled-input control: replacing ExecutionView's graph input with `layoutView && null`, rebuilding, and running the completed-instance acceptance fails at the actual sink: `Mode:fallback Panel:Layout reviewed Nodes:[] Edges:[]` (`/tmp/p3t14-input-red.log`). Restored the source and rebuilt; the embedded distribution is byte-identical to HEAD. An initial literal-null mutation failed TypeScript's unused-parameter check and is NOT counted as behavioral RED.

Ordered self-review, applied before final verification:

1. **Spec compliance:** only closes P3T14's remaining actual-route/crash/release proof. All manual write APIs remain absent and legacy parsing remains intact. Both original acceptance samples and five-attempt limits remain unchanged; no group migration or sibling-task full-race work was started. Existing root feeds the actual renderer/reviewer, SQLite, authorized queries and embedded routes.
2. **Anti-green-theater:** both missing production inputs above fail real browser sinks. The new matrix validates complete geometry, not component calls or just `data-layout-mode`; it observes genuine unsettled bytes and HTTP errors, then asserts settled revision/generation/digest and hashed rendered evidence. Historical instances remain on their own frozen revision after rename/root replacement. No HTTP response, renderer, reviewer, registry decision or evidence is fabricated.
3. **Code quality:** reused the argument-honoring crash decorators and generalized only the peer-kill helper name for its HTTP/browser caller. The first broad surface-race run exposed a test collector TOCTOU (readiness saw all routes, a separate capture caught zero routes during remount). Changed readiness and DOM capture to one atomic browser task; every box/path/marker/text/zoom assertion and the original 30s deadline remain. No production timing, attempt budget or unrelated test was changed. gofmt/vet/diff checks pass.

Final verification AFTER review/fixes:

- Build/vet, all prescribed deletion/405/legacy guards, and `go test ./adapter/dashboard/... ./adapter/renderreview/... ./cmd/arbitron/`: PASS. The initial concurrent three-package run was stopped at its external 240s limit; it is not counted as a pass. The final exact command passes (`/tmp/p3t14-final-go-embed.log`).
- Rebuilt `index.html` and `render.html`, confirmed no embedded `saveLayout` symbol and no generated/source mutation delta.
- Exact `make test-all`: PASS (`/tmp/p3t14-test-all.log`). Go cmd package 307.909s; TypeScript and **90 files / 1380 Vitest tests**; unfiltered browser-required renderreview **210.064s**, cmd/arbitron **946.471s**. No browser skip is substituted for success.
- Full owning actual-surface suite: `ARBITRON_REQUIRE_BROWSER=1 go test -race ./cmd/arbitron -run '^TestLayoutSurfaces_' -count=1`: PASS **277.809s**, no races (`/tmp/p3t14-final-surfaces-race.log`). Focused restored crash/sample/completed tests also pass under race (126.853s).
- `git diff --check`: PASS. P3T14's remaining boxes are now complete. P3T2's independent font-inventory and P3T7's whole-suite race gates remain owned by those tasks, not claimed here.
