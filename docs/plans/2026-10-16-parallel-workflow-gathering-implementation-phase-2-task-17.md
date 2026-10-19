# Parallel Workflow Gathering — Phase 2 Task 17: Results-only worktree metadata isolation

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Run results-only groups from ordinary Git clones, nested repositories and linked
worktrees without leaving `.git` pointers to a parent's mutable repository. The bundled
feature-pipeline workspace uses linked worktrees; results-only mode must remain usable
there after the feature ships, without forcing authors to change gather mode.

**Prerequisites:** P2-T5. Execute BEFORE P2-T8 and P2-T14. P3-T2 consumes the metadata
copy helper produced here; it continues to own Git content plumbing and mergeable refs.

## Interfaces and production inputs

Add `adapter/filesystem/group_git_metadata.go` with
`CopyGroupGitMetadata(ctx context.Context, sourceRoot, destinationRoot string) error`.
Also export `var ErrGroupRepositoryLocked = errors.New("author git repository is locked")`;
wrap it when a source index lock exists. Both roots are canonical absolute directories. The destination's ordinary file content
has already been copied. The helper finds `.git` directories/files under the source and
materializes independent metadata in the corresponding destination directories. It
returns errors wrapping `engine.ErrUnsupportedSnapshotContent` for malformed or unsafe
metadata and names the path. It never invokes Git or alters the source.

`GroupWorkspaceAdapter.SnapshotParent` calls it after copying content into the temporary
snapshot and BEFORE atomically publishing that snapshot; `ForkTask` does the same from
snapshot to task before returning. P2-T5's ordinary copier must treat `.git` entries as
metadata handled by this helper and avoid rejecting pointer files as ordinary content.
The immutable snapshot holds all external metadata needed for later forks; forks never
read the original parent again. Check cancellation between files, and clean incomplete
destinations exactly as P2-T5 does for copy errors.

Production producers: `.git` file bytes (`gitdir:`), `commondir`, `HEAD`, worktree index,
config and object/ref files come from the actual parent workspace prepared by existing
`WorkspaceManager` and written by workflow scripts/agent nodes. Paths are resolved against
the metadata file's directory. A linked worktree may refer outside the area; copy its
common metadata into a private standalone `.git`, overlay its HEAD and index, exclude
other worktrees' registration, remove `commondir`, normalize `core.bare/core.worktree`,
and materialize alternates rather than retaining external writable dependencies.

For in-area repositories/worktrees, re-root pointers/back-pointers into the copy. For
standalone copies, keep staged-versus-unstaged state by copying the worktree index and
object content. Remove obsolete external worktree registrations. Handle relative metadata
paths and symlinks by materializing targets into the copy or rejecting unsupported content
before enqueue; never accept a task that can update its parent's refs/index/config.

P3-T2 imports this filesystem helper for metadata reproduction rather than implementing
a second inconsistent worktree copier. Its Git content snapshot still excludes `.git`
and retains its own manifests, immutable refs and staging/rollback responsibilities.
Keep P3-T2's linked-worktree and nested-repository tests, using the shared helper.

Composition root: the `filesystem.NewGroupWorkspace` passed to the real launcher in
P2-T14 now includes this behavior without a new DI option. The helper is required within
both production copy methods; absence is not accepted as a no-op. P3-T11 retains the
results/file-mode split: plain results-only groups still run without a Git executable.

## Strict TDD implementation steps

- [x] Step 1: Write/run failing tests in `adapter/filesystem/group_git_metadata_test.go`: create
   temporary real repositories and the sample-shaped `src` plus `work` linked worktree;
   call the production snapshot/fork methods and run Git in the fork. Assert inherited
   bytes, staged state and branch status. Mutate/commit in one fork and prove parent's
   files, refs, index and the sibling fork remain unchanged (the sink is all three areas).
- [x] Step 2: Success: ordinary clone, multiple nested repositories, in-area and external linked
   worktrees, relative and absolute gitdir pointers, staged/untracked/dirty changes.
   Delete or modify original external metadata after SnapshotParent; forks must still
   work from the immutable copy. Add a no-Git-PATH test for the copy operation itself.
- [x] Step 3: Failure/error: broken gitdir/commondir, escaping metadata symlink or alternates that
   cannot be materialized, lock files, read/copy failure and cancellation. Assert errors
   before enqueue, source metadata unchanged, no half-published snapshot and fork cleanup.
- [x] Step 4: Edge: detached HEAD, empty repo, worktree index distinct from common repo, two forks
   of the same snapshot, no `.git` present, and existing safe relative content symlinks.
   Keep explicit rejection of general content symlinks escaping the working area.
- [x] Step 5: Implement the helper, wire both methods, and replace P2-T5's tests that EXPECT a `.git`
   pointer rejection with success/isolation tests. This task supersedes only that staged
   limitation; devices/sockets/unsafe content remain actionable pre-launch errors.
- [x] Step 6: P2-T14 boots `initDomain` with a sample-shaped linked parent worktree, launches both
   results-only forms and asserts each downstream result read the inherited file while
   its edits/commits reached neither parent nor sibling. The test fails if the helper is
   bypassed or the metadata path argument is ignored. Run red/green for each boundary,
   then filesystem/engine suites and focused race checks.

> RESOLVED (Step 6 completion pass): P2-T14 now supplies the real composition root and group stack. `TestGroupE2E_ResultsLinkedWorktreeMetadataIsolation` in `cmd/arbitron/group_e2e_test.go` boots `initDomain` for both results-only forms. Workflow scripts create the actual sample-shaped `src` repository and linked `work` worktree, including distinct staged and dirty content. Historical blocker: P2-T14 had not created `cmd/arbitron/group_e2e_test.go`, its `bootGroupExecStack` helpers, or the production group repository/coordinator/launcher/executor wiring in `cmd/arbitron/main.go:initDomain`. Consequently the real root still refuses results groups, and neither group form can exercise metadata isolation there. Steps 1–5 supply T14's missing metadata prerequisite; leave this root proof unchecked until that task wires real execution. Do not enable group support or build a substitute test-only root in this task.

Selection: skipped the previously recorded P2-T11/T13/T14/T15/T16 completion blockers rather than re-failing them. Atomic receipts now exist, but their remaining real-root proofs still depend on T14. T17's metadata producer was the next independent unblocked slice.

TDD evidence: the real sample-shaped `src`/`work` snapshot/fork test first failed on the old `.git` pointer rejection. Expanded success/failure/edge tests then failed to compile on the missing shared helper/sentinel. The minimal helper and mandatory snapshot/fork calls made them green. A worktree-config replay test exposed accumulating config sections (red); normalization is now idempotent (green). Replaced only the obsolete blanket pointer-rejection expectation; malformed pointers and unsafe ordinary content still fail.

Production-path tests run Git in both real forks, check inherited staged/dirty/untracked bytes and branch status, commit/configure one fork, and compare parent/common metadata, sibling and immutable snapshot trees. External originals are deleted after snapshot; shared-clone alternates remain usable without them. Copy operations also run with no Git on PATH. Cancellation tests wait until metadata HEAD has actually been written, then assert complete snapshot/fork rollback, including preservation of existing empty task directories.

Self-review, in the required order:
1. Spec compliance: only the shared filesystem metadata helper/sentinel and its mandatory snapshot/fork calls were added; no Git process dependency, config field, mode enablement, or Phase 3 content plumbing. Copies flatten worktree/submodule metadata, retain worktree-local index/HEAD, normalize location settings and materialize local alternates; unsafe symlinks, external config includes and unmaterializable HTTP alternates fail explicitly. Conditional-include whitespace and HTTP-alternate regressions were run red, fixed and run green.
2. Anti-green-theater: temporarily bypassed the production metadata call; the real sample-layout test failed at `git status` in the fork with “not a git repository”. Restored it and ran green. Tests assert actual parent/common metadata and sibling trees after fork commits/config changes, not just helper invocation. These filesystem producer/sink proofs do not substitute for the explicitly blocked composition-root test.
3. Code quality: reused existing byte-copy, containment, directory-mode and rollback helpers; checked neighboring workspace patterns, kept config normalization idempotent and errors path-addressed/wrapped. Formatted, vetted and reran the task gates after review.

Final green verifications:
- `go test ./adapter/filesystem/ ./domain/engine/ -count=1`
- `go test -race ./adapter/filesystem/ ./domain/engine/ -run 'TestGroupGitMetadata|TestGroupWorkspace|TestGroupLauncher|TestParallelGroupExecutor|TestNewParallelGroupExecutor' -count=1`
- `go vet ./adapter/filesystem/ ./domain/engine/`
- `git diff --check`

Steps 1–5 were committed as partial progress with Step 6 unchecked. Step 6 is now complete after P2-T14 landed; no additional production behavior or mode enablement was necessary.

Step 6 TDD / boundary evidence:
- Wrote the real-root test first. Temporarily bypassing metadata copying only at `SnapshotParent` made both forms fail at the first task's terminal sink (`failed`, not `completed`). Restoring the mandatory copy made both pass.
- Repeated red/green with only `ForkTask` bypassed: both forms failed, then passed after restoration.
- Replaced the helper's source argument with the destination: both forms failed before task admission. Restored the real source path and confirmed green. No mutation remains in production code.
- Process gates hold the sibling idle during task a's commit/config writes and hold the parent before terminal workspace cleanup. Full tree hashes compare parent files and all Git metadata (including common refs, objects, config and worktree index), plus the sibling's metadata/content. Downstream transforms persist each task's inherited dirty bytes, staged index content, own bytes and distinct commit IDs; real ref advancement and exactly-once continuation are asserted.

Completion self-review, in the required order:
1. Spec compliance: completed only the outstanding composition-root isolation proof, using both branches and repeated child workflows. Existing production snapshot/fork wiring remains unchanged; no new options, Git dependency for copying, or Phase 3 work.
2. Anti-green-theater: reads/commits run in real task workspaces, output flows through the actual group executor into downstream persisted variables, and unchanged parent/sibling trees are asserted. The separate snapshot/fork bypasses and incorrect metadata source each turn the test red; missing wiring or empty inputs cannot satisfy the downstream sinks.
3. Code quality: reused the neighboring real-root boot/roster/output helpers and bounded process-gate pattern. Tree checks hash bytes/modes/symlink targets without invoking Git and refreshing the index under test. Formatted, vetted, and reran focused tests after review.

Completion verifications (all green):
- `go test ./cmd/arbitron/ -count=1`
- `go test ./adapter/filesystem/ ./domain/engine/ -count=1`
- `go test -race ./cmd/arbitron/ ./adapter/filesystem/ ./domain/engine/ -run 'TestGroupE2E_ResultsLinkedWorktreeMetadataIsolation|TestGroupGitMetadata|TestGroupWorkspace|TestGroupLauncher|TestParallelGroupExecutor|TestNewParallelGroupExecutor' -count=1`
- `go vet ./cmd/arbitron/ ./adapter/filesystem/ ./domain/engine/`
- `gofmt -l cmd/arbitron/group_e2e_test.go` and `git diff --check`

**Files:** create the helper/test files above; modify
`adapter/filesystem/group_workspace.go`, `adapter/filesystem/group_workspace_test.go`;
P2-T14 extends `cmd/arbitron/group_e2e_test.go`; P3-T2 reuses the helper in
`adapter/git/metadata.go` with its existing real-repository tests. No public config field
or Git process dependency is added to results-only execution.
