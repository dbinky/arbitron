# fp-next engine prerequisites — explicit spawn `inputs` and the `@lib` shared library

**Status:** design approved 2026-10-23 (orchestrator on the user's behalf); ready for implementation planning.
**Parent idea:** [docs/ideas/fp-next.md](../../ideas/fp-next.md) §4g, §4h, §5, §6, §9 steps 1–2.
**Classification:** architectural. This changes the `spawn_workflow` manifest interface, file resolution across every file-consuming node kind, the reserved-name rules for workflows, the MCP file tools, and sample seeding.

---

## 1. Purpose and success criteria

`feature-pipeline-next` chains standalone stage workflows with awaited `spawn_workflow` nodes. Two engine gaps block it.

1. **Spawn inputs are LLM-only.** Every child input is produced by a chat call over the parent's variables (`domain/engine/child_input_preparer.go`, called from `domain/engine/executor_spawn.go` `prepareAndSpawn`). Structured output is not enforced on the claude-cli path, so every exact-string handoff (repo URL, branch, slug, path) is a coin flip.
2. **Shared processes have no shared home.** `{{include:}}` and script resolution are confined to the workflow's own folder (`adapter/filesystem/file_reader.go` `safeJoin`). So `notify-teams.sh`, `checkpoint-push.sh`, init helpers, and the coming tamper gate get copy-pasted into each workflow.

**Success means:**

- A spawn node with a full `inputs` map starts its child with exactly the mapped, typed values, and makes **zero** LLM calls.
- A mapped reference that does not resolve **fails the node**. It is never handed to an LLM to guess.
- The LLM preparer runs only for required child inputs that are unmapped and have no default. It is asked only for those inputs, and it can never override a mapped or default value.
- A path starting `@lib/` works in `{{include:}}`, `templateFile`, `scriptFile`, `orchestrationFile` (and, through the same port, `resolverPromptFile`). It resolves from the run owner's private `_lib` first, then the shared `_lib`, and it cannot escape `_lib` by traversal or symlink.
- `_lib` never behaves as a workflow. It can't be created, renamed to, published, copied, listed, loaded, triggered, or spawned.
- `samples/_lib` ships in the release samples tarball, the Docker image, and `make install-samples`, and the boot seeder seeds it.
- Library files are read live, the same way templates are today.

## 2. Decisions (locked)

| # | Decision |
|---|---|
| A-spawn | Approach A: `ParseSpawnConfig` carries a deep-copied `Inputs` map. One pure domain planner (`PlanChildInputs`) is shared by validation and the executor. |
| A-lib | Approach A: a domain-owned `@lib` path parser. `FileReaderAdapter` routes `@lib/` paths, so no engine port changes. Management goes through a new `LibraryStore` port behind workflow-service methods. |
| Q1 | The narrowing applies to **every** spawn node, with or without an `inputs` map. Optional inputs are never LLM-filled. No sample or live workflow uses `spawn_workflow` today, which was checked. |
| Q2 | An unmapped required input that declares a `default` is passed to the child as a deep copy of that default. The LLM is not involved. This is needed because the trigger (`domain/engine/engine.go` input loop) rejects a missing required input even when it has a default. |
| Q3 | Preparer output is filtered to the requested keys only. Extra keys are dropped. Mapped and default values cannot be overridden. |
| Q4 | Mapped values that point into the parent's `{{workspace}}` are **not** refused (unlike group tasks). This is documented as discouraged. |
| Q5 | Spawn input findings come from `Validate` when a `ChildContractReader` is supplied (the validate endpoints). Writes are **not** gated, which matches `UNKNOWN_CHILD_WORKFLOW`. Spawn time always re-checks. There is no runtime type coercion. |
| Q6 | Spawn node output gains `inputSources: {name: "mapped" \| "default" \| "prepared"}`. It holds names only, never values. |
| Q7 | Dashboard: a `SpawnDetails` section in the WorkflowReview side panel, with no client-side contract fetch. The SPA is rebuilt with `npm run build` and synced to `adapter/dashboard/dist`. |
| Q8 | A relative `{{include:x}}` inside a file loaded from `@lib` means `@lib/x`. Lib files can never include workflow-local files. |
| Q9 | Shadowing is per file: the run owner's private copy wins over the shared one. This matches how workflow files already resolve. |
| Q10 | Malformed `@lib` paths are rejected before cleaning. The `_lib` root must be a real directory. Resolved realpaths must stay inside realpath(`_lib`). An escaping symlink fails closed and does **not** fall through to shared. A symlink that resolves **inside** `_lib` is allowed. |
| Q11 | The MCP `workflow_*_file` tools accept the name `@lib` with an optional `scope`. Shared writes require admin and are audited (`lib.edit_shared`). The REST file surface is unchanged and rejects both reserved names. |
| Q12 | Seeding is per file, create-if-absent, and recorded per file in the ledger. It never overwrites. The release samples tarball tests (installer and release bats) must stay green with `_lib` included. |
| Q13 | There is no check for published workflows whose `@lib` refs resolve only from the publisher's private lib. This is documented as a gotcha (§6). |

## 3. Prerequisite 1 — explicit `inputs` on `spawn_workflow`

### 3.1 Manifest shape

```json
{ "id": "spec", "type": "spawn_workflow",
  "config": {
    "childWorkflow": "spec",
    "spawnMode": "wait_for_child",
    "inputs": {
      "repo_url": "{{repo_url}}",
      "branch": "{{intake.branch}}",
      "idea_path": "{{ideate.childOutput.idea_path}}",
      "design_paths": "{{design.childOutput.design_paths}}",
      "in_place": true
    }
  } }
```

- `NodeConfig` gains `Inputs map[string]any \`json:"inputs,omitempty"\``. It is only meaningful on `spawn_workflow`. The parallel_group task inputs live under `config.group.tasks[].inputs` and are a separate field.
- Values are typed JSON literals and/or `{{templates}}`, with the same semantics as parallel_group task inputs:
  - A string that is exactly one `{{path}}` keeps the referenced value's type (array, object, number, bool, string, null), as a deep copy.
  - Any other templated string is interpolated.
  - Nested maps and arrays are resolved element by element.
  - The resolver is `variable.ResolveTypedMap` (`domain/variable/typed_resolve.go`), run against the parent's live `VariableStore`. The store is read-only to the resolver.
- `workspace` is reserved. A key named `workspace` is a config error.
- Mapped keys must be inputs the child declares. Extra keys are not allowed. This is stricter than the trigger path's extra-variable tolerance, on purpose: a typo'd key would otherwise silently become an LLM-prepared input.

### 3.2 Domain units

**`workflow.SpawnConfig` / `ParseSpawnConfig`** (`domain/workflow/workflow.go`):

- `SpawnConfig` gains `Inputs map[string]any`. It is a deep copy made with the existing `cloneGroupValue` helper (moved or renamed as needed so both can share it), so neither the caller nor the snapshot can mutate the other.
- There is a new sentinel, `ErrSpawnReservedInput`. It is reported joined with the other problems, so every problem appears in one pass, and the result is the zero `SpawnConfig`.
- The executor already re-parses through `ParseSpawnConfig`. That remains the one parser for both validation and execution.

**`workflow.PlanChildInputs(fields []InputField, mapped map[string]any) ChildInputPlan`** is a new pure function in `domain/workflow`:

```go
type ChildInputPlan struct {
    Unknown  []string          // mapped keys the child does not declare (sorted)
    Mapped   []string          // mapped keys that ARE declared (sorted)
    Defaults map[string]any    // unmapped REQUIRED inputs with a non-nil Default -> deep copy of the default
    Prepare  []InputField      // unmapped REQUIRED inputs with no Default (author order preserved)
}
```

- Unmapped **optional** inputs appear nowhere. The child's trigger-time defaulting handles them.
- Mapped keys are classified by key only. Resolution happens later, in the executor.
- The `workspace` key is excluded here because `ParseSpawnConfig` owns that error.
- Validation (§3.4) and the executor (§3.3) both consume this plan. That shared use is what keeps them from drifting.

**Shared literal checks.** The literal-type and unknown-key logic in `domain/workflow/validation_group_children.go` (`checkTaskInputs`, `groupLiteralTypeProblem`, `declaredNames`, `isWholeTemplate`) is extracted into an unexported helper, `checkChildInputMap(fields, inputs) childInputFindings`. Groups and spawn each wrap it with their own codes and messages. Group behaviour, codes and messages are unchanged, and the existing group tests are the regression guard.

### 3.3 Executor flow (`SpawnWorkflowExecutor.prepareAndSpawn`)

This applies to both modes. The `resumeFromLink` re-entry path is unchanged: inputs are resolved only when the child is first spawned.

1. Load the parent, reconstruct the owner identity, and load the child's declared inputs. This is unchanged and still fails closed.
2. `cfg := ParseSpawnConfig(node.Config)`. This already happens in `Execute`, and the parsed `cfg` is passed down instead of reading `node.Config` again.
3. `plan := workflow.PlanChildInputs(fields, cfg.Inputs)`.
4. **Unknown keys:** if `plan.Unknown` is non-empty, fail the node with a new `ErrSpawnUnknownChildInput` (in `domain/engine`). The message names the node, the child, the offending keys, and the declared names. Nothing is resolved, no LLM is called, and no child is created.
5. **Resolve:** `resolved, err := variable.ResolveTypedMap(cfg.Inputs, vars)`.
   - On error, fail the node with `spawn_workflow %q: resolve inputs: %w`, wrapping `variable.ErrUnresolvedReference`. The error names the missing `{{path}}` and its key path.
   - **The LLM is never consulted.**
6. **Null for required:** if a mapped required input resolved to `nil`, fail with `ErrChildContractUnsatisfied`, naming the input. Again, no LLM fallback.
7. **Prepare (only if `len(plan.Prepare) > 0`):** call `preparer.Prepare` with `Inputs: plan.Prepare`. Guidance, parent context, profile and workdir are passed as they are today.
   - The preparer's existing "no model_profile" error now counts `len(plan.Prepare)`, and its message says these are the *unmapped required* inputs.
   - The result is filtered to the requested names. Extra keys are dropped and logged at DEBUG with names only.
   - The existing `validateChildInputs` still enforces presence and non-null for the requested set.
8. **Merge:** `values = prepared ∪ plan.Defaults ∪ resolved`. Precedence is mapped over default over prepared. The sets are disjoint by construction, and the precedence is asserted as defence in depth.
9. **Spawn** with `values`. `inputSources` records the origin of each key: `mapped` for every resolved key, `default` for `plan.Defaults`, and `prepared` for every requested input the preparer returned.
10. **Output:** `spawnOutput` gains `inputSources` (a map of name to source, names only). The awaited path writes it into the node-execution record's output alongside `childInstanceId`. `resumeFromLink` rebuilds its output from the link and does not carry `inputSources`; the trace shows it on the first-execution record.

Every failure above is a plain error, so the node's resolved `ErrorBehavior` applies (retry, skip, route), as it does today. Resolution is deterministic, so a retry re-fails the same way, which is harmless.

**A fully mapped spawn** (`plan.Prepare` is empty) never calls `Prepare`. It needs no `model_profile` and no `ChatProviderSelector`.

**No runtime type coercion.** Resolved values are passed as they are. The trigger path stays the single place where input semantics are enforced, the same as for groups.

### 3.4 Author-time validation (`domain/workflow/validation.go`)

`validateSpawnWorkflowNode` reports `SPAWN_RESERVED_INPUT` from `ParseSpawnConfig`. This needs no checker, so it is always on.

`validateSpawnChildInputs(wf, o.children)` is a new function. It runs when `WithChildContractReader` is supplied, which is what `GroupValidateOptions` does for MCP `workflow_validate` and REST `POST /workflows/{name}/validate`. Each distinct child is loaded once per call, using the same memoization `validateGroupChildren` uses.

| Code | Severity | When |
|---|---|---|
| `SPAWN_RESERVED_INPUT` | error | `inputs` contains `workspace` |
| `SPAWN_UNKNOWN_CHILD_INPUT` | error | a mapped key is not declared by the child (the message lists the declared names) |
| `SPAWN_INPUT_TYPE_MISMATCH` | error | a **literal** value contradicts the declared type, or is a null literal for a required input. Whole-value templates are never type-checked. |
| `SPAWN_PREPARER_PROFILE_MISSING` | error | `plan.Prepare` is non-empty and `model_profile` is empty. Before this change it could only be found at run time. |
| `SPAWN_PREPARED_INPUTS` | warning | `plan.Prepare` is non-empty. The message lists the inputs that an LLM will prepare at run time. |
| `SPAWN_CHILD_CONTRACT_UNAVAILABLE` | warning | the reader returned an error other than not-found. Spawn time re-checks. |

- Reader not-found produces no spawn-input finding. `UNKNOWN_CHILD_WORKFLOW` is already reported by the name checker, and when only the reader is supplied, this pass reports `UNKNOWN_CHILD_WORKFLOW` once itself.
- The existing optional-`model_profile` rules in `validateModelProfiles` and `validateConnectionReferences` are unchanged. A spawn alias is still allowed to be empty. `SPAWN_PREPARER_PROFILE_MISSING` is the new contract-aware rule.
- **Writes are not gated** by any of these findings. `IsGroupValidationCode` must return false for every `SPAWN_*` code, so `groupFindings` never picks them up.

### 3.5 Dashboard

- `dashboard/src/api/types.ts`: the workflow node config type gains `childWorkflow?: string`, `spawnMode?: 'fire_and_forget' | 'wait_for_child'`, and `inputs?: Record<string, unknown>`.
- There is a new `SpawnDetails` component in `dashboard/src/routes/WorkflowReview.tsx`. It renders alongside `GroupDetails` when the selected node's type is `spawn_workflow`, and it shows:
  - the child workflow as a `Link` to `/workflows/<child>`
  - the mode, in human text ("waits for child" or "fire and forget")
  - a mapping table with one row per key, sorted. Each value is badged `reference` (a whole `{{path}}`), `template` (an interpolated string), or `literal`. Literal values are rendered as compact JSON and truncated with an ellipsis past 120 characters.
  - an empty state when the node has no inputs: "No inputs mapped — every required input without a default will be prepared by the LLM at run time."
- CSS goes in `WorkflowReview.module.css`, reusing the existing `nodeDetail*` and `inputName`/`inputType` classes.
- There is no client-side fetch of the child's contract.
- The raw Config `<pre>` block stays.
- The run view is unchanged. `inputSources` is visible through the existing node-output rendering.
- Build: run `cd dashboard && npx tsc --noEmit && npx vitest run && npm run build`, then sync the build output into `adapter/dashboard/dist`. `make ui` is avoided because of npm ci lock drift.

### 3.6 Docs

- **`AGENTS.md`**, spawn_workflow section: document the explicit `inputs` map and the narrowed preparer semantics:
  - the LLM runs only for unmapped required inputs with no default
  - defaults are passed explicitly
  - optional inputs are never LLM-filled
  - unresolved mapped references fail closed
  - zero LLM calls when fully mapped
  - `inputSources`
- **`docs/specs/2026-07-27-workflow-spawn-node-spec.md`**: add a dated "Amendment 2026-10-23" note that points here and states the narrowed semantics.
- **MCP authoring guidance** (`adapter/mcp/authoring_guidance.go`): add a spawn sentence covering the `inputs` map, mapped-wins, fail-closed, and that a fully mapped spawn needs no `model_profile`. Also add an `@lib` sentence (§4.7). The unit test that validates the guidance example must still pass.

## 4. Prerequisite 2 — the `@lib` shared library

### 4.1 Layout and vocabulary

- Library roots:
  - private: `<workspace>/users/<owner>/_lib/`
  - shared: `<workspace>/shared/_lib/`
- Domain constants in `domain/workflow` (a new file `library.go`):
  - `LibraryFolder = "_lib"`
  - `LibraryRefName = "@lib"`
  - `LibraryPathPrefix = "@lib/"`
- `IsReservedWorkflowName(name string) bool` is case-insensitive (`strings.EqualFold(name, LibraryFolder)`) because APFS folds case. The literal `@lib` is also reserved, since it can never be a workflow folder name.
- `ParseLibPath(p string) (rel string, isLib bool, err error)`:
  - `isLib` is true iff `p` starts with `@lib/`, or `p` is exactly `@lib`.
  - It rejects, with `ErrInvalidLibPath`, **before** any cleaning:
    - an empty remainder (`@lib`, `@lib/`)
    - a leading `/` in the remainder
    - any backslash or NUL
    - any `..` component
    - any `.` component
    - an empty component (`@lib/a//b`)
    - a remainder that differs from `path.Clean(remainder)`
    - a first component equal (case-insensitively) to `workflow.json` or `layout.json`. The adapter additionally rejects its internal-artifact names (`reservedWorkflowFile`) on every lib read and write, so the domain parser never needs the adapter's list.
  - It returns the clean, slash-separated `rel`.
  - Because the check runs before cleaning, `@lib/../x` cannot slip through as `x`.

### 4.2 Resolution (`adapter/filesystem/file_reader.go`)

`FileReaderAdapter.withExisting` (behind `ReadFile`, `ResolvePath`, `FileExists`) checks `workflow.ParseLibPath(relativePath)` first.

- **Non-lib paths** keep the current behaviour exactly.
- **Lib paths with a parse error** return an error wrapping `workflow.ErrInvalidLibPath`.
- **Valid lib paths** go through a new `withExistingLib(owner, rel, read)`. For each candidate root, private (when the owner is non-nil) and then shared:
  1. `libDir := <scopeRoot>/_lib`. `Lstat(libDir)`:
     - if it does not exist, try the next root
     - if it is a symlink or not a directory, **error** (fail closed, no fallthrough)
  2. `full := libDir/rel`, with the same prefix containment `safeJoin` applies.
  3. `Lstat(full)`: if it does not exist, try the next root.
  4. `real, err := filepath.EvalSymlinks(full)` and `realLib := EvalSymlinks(libDir)`. `real` must equal `realLib` or sit under `realLib + separator`. Otherwise **error** `ErrInvalidLibPath` ("escapes the library"), with no fallthrough to shared. A dangling symlink is also an error.
  5. Read or return `real`. Scripts execute the resolved real path, so `cwd` is `dirname(real)`, inside `_lib`.
- If no root has the file, the result wraps `os.ErrNotExist`, as today.
- Lib reads do not need the reserved-artifact settlement, because `_lib` has no manifest. They take no manifest lock. Writes are atomic (§4.5), so a reader never sees a torn file.
- The `workflowName` argument is ignored for lib paths. A lib path resolves the same way from any workflow, which is the point.
- **Owner:** this is the run owner, already passed by every engine caller through `ownerFromContext` / `OwnerFromContext`. A user's private `_lib/x` therefore shadows shared `x` in that user's runs of shared workflows (Q9).
- **Consumers covered with no code change:**
  - `loadTemplate`: agent, llm, interactive, the spawn guidance `templateFile`, and the interactive `orchestrationFile`
  - `ScriptExecutor` `ResolvePath`
  - the group resolver prompt (`resolverPromptFile`)
  - validation `FileChecker` (`ownerBoundChecker`, `ownerScopedFiles`)
  - MCP `workflow_read_file` with a workflow name. This one is deliberately harmless: it returns what a run would read.
- **Not covered:** the symlink hardening for ordinary workflow files. `safeJoin` is not changed, and that is out of scope.

### 4.3 Includes (`domain/engine/template_include.go`)

`expandIncludesDepth` gains a notion of the **current base**: either the workflow, or the library.

- **The top-level template's base** is the library when its own path is `@lib/...` (for example `templateFile: "@lib/x.md"`), and the workflow otherwise. `loadTemplate` passes the base.
- **For each directive:**
  - `@lib/...` resolves from the library, regardless of the base.
  - A relative path while the base is the library means `@lib/<path>`.
  - A relative path while the base is the workflow keeps today's workflow-root-relative behaviour.
- The included file's base becomes the base under which it was resolved. So a lib file's nested relative includes stay in the lib, and **a lib file can never include a workflow-local file** (Q8).
- **The cycle key** is canonical: `"@lib/" + rel` for library files, and `path.Clean(p)` for workflow files. `a.md` in the workflow and `@lib/a.md` are therefore different files, while `@lib/a.md` and a lib-relative `a.md` included from a lib file are the same file.
- **Unchanged:** depth, byte and count budgets are shared across bases, the behaviour is fail-closed, and there is still no escape syntax.

### 4.4 Validation of references

In `validateFileReferences` (`domain/workflow/validation.go`), for each of `templateFile`, `scriptFile` and `orchestrationFile`:

- If `ParseLibPath` reports `isLib` with an error, report `INVALID_LIB_PATH` (error) with the reason, instead of `MISSING_FILE`.
- Otherwise use the `FileChecker` as today. The adapter resolves valid lib paths, so a missing lib file is `MISSING_FILE`, and the message names the path.

The same treatment applies to the group `resolverPromptFile` check (`validation_group.go`), using the existing `GROUP_*` code family's missing-file finding and `INVALID_LIB_PATH` for malformed paths.

The import and seed file sets (`importFileSet`, `seedFiles`) only describe the incoming workflow's files. For `@lib/...` paths they must answer through the real lib, not report the path missing. These checkers gain an optional fallback `OwnerFileChecker` for lib paths. When none is wired, a lib path is treated as present: lib refs are always re-checked at run time, and the seeder seeds `_lib` before workflow samples (§4.6).

### 4.5 Reserved name and management

**Repository and service guards (`_lib` is never a workflow):**

- `WorkflowRepo.List` skips `_lib` explicitly, compared case-insensitively. Today it is skipped only by accident, because it has no `workflow.json`.
- `WorkflowRepo.Load`, `Exists`, `Create`, `Update`, `Delete` and the workflow-scoped `WriteFile`, `DeleteFile` and `ListFiles` return `ErrInvalidName` (or `ErrNotFound` for `Load`, matching its historical invalid-name mapping) for a reserved name. `resolveWorkflowDir` is the single choke point. Lib access uses its own adapter (below), never these methods.
- `Service.Create`, `Rename` (new name), `Publish` (published name), `Copy` (new private name), `Duplicate` (new name), and `validateStampDestination` reject reserved names with `ErrInvalidName`, plus a message saying `_lib` is the reserved library folder. Spawn and trigger of `_lib` fail through `Load` as not-found.
- `BackfillRegistry`, `BackfillReferences`/`SweepOrphans`, the flat-layout migration, and any other scope-root folder enumerator skip `_lib`. The plan must enumerate each enumerator with `rg`. The rule is "every enumerator of scope-root folders skips `_lib`."
- Workflow-scoped file writes whose path parses as `isLib` are rejected with `ErrInvalidFilePath`. A file at `<wf>/@lib/x` could never be read back.

**`LibraryStore` port (`domain/workflow`), implemented by `adapter/filesystem` (`library_store.go`):**

```go
type LibraryStore interface {
    ListFiles(ctx context.Context, scope Scope) ([]string, error)         // sorted, slash-separated; missing _lib -> empty
    ReadFile(ctx context.Context, scope Scope, rel string) ([]byte, error) // exact scope, same symlink rules as §4.2
    WriteFile(ctx context.Context, scope Scope, rel string, content []byte) error // creates _lib on demand; atomic temp+rename
    DeleteFile(ctx context.Context, scope Scope, rel string) error         // ErrFileNotFound when absent
}
```

- `rel` is validated with the same rules as `ParseLibPath`'s remainder.
- Private `_lib` is created under the owner's partition with the partition's `ensureScopeRoot` permissions.
- `ListFiles` skips internal-artifact directories, and does not descend into or report symlinks that escape the lib.

**Service methods (`domain/workflow/library.go`)** form the authorization boundary. MCP never calls the store or `FileReader` directly for `@lib`.

- `LibraryList(ctx) (LibraryListing{Private, Shared []string}, error)`: any authenticated caller. The private listing is the caller's own.
- `LibraryRead(ctx, scope *Scope, rel) ([]byte, error)`:
  - with no scope, the **effective** resolution: the caller's private copy, then shared, exactly as a run by the caller would read it
  - with a scope, that exact scope
- `LibraryWrite(ctx, scope, rel, content)` and `LibraryDelete(ctx, scope, rel)`:
  - private scope means the caller's own partition
  - shared scope requires `identity.IsAdmin()`, else `ErrUnauthorized`
  - shared writes and deletes record a best-effort audit event, `audit.ActionLibEditShared = "lib.edit_shared"`, with detail `{path, op: "write"|"delete"}`
- Lib writes do **not** take the workflow execution gate (`s.mutate`). Lib content is never snapshotted, so there is nothing to serialize against. The atomic rename is the consistency guarantee.

**MCP tools (`adapter/mcp/tools_workflow.go`):** `workflow_list_files`, `workflow_read_file`, `workflow_write_file` and `workflow_delete_file` behave as follows.

- They gain an optional `scope` string (`private` | `shared`). It is valid only when `name == "@lib"`. Anywhere else it is a tool error.
- `name == "@lib"` routes to the service library methods:
  - list returns `{"private":[...],"shared":[...]}`
  - read with no scope is the effective read
  - write and delete default to `private`
  - error mapping: `ErrUnauthorized` → "shared library requires admin", `ErrInvalidLibPath` / `ErrInvalidFilePath` → "invalid library path", `ErrFileNotFound` → not found
  - the 10 MiB write limit is unchanged
- `name` equal to `_lib`, case-insensitively, is rejected: "`_lib` is reserved; use `@lib`".
- The tool descriptions mention `@lib` and `scope`.

**REST:** unchanged. `/workflows/_lib/...` and `/workflows/@lib/...` fail through the reserved-name guards as not-found or bad-request. No new routes.

### 4.6 Shipping and seeding

- `samples/_lib/` is a directory of library files, with no `workflow.json`. Its `*_test.sh` and `*_test.go` siblings are excluded from seeding by the existing `isTestFile` rule.
  - This sub-project ships **one** placeholder file, `samples/_lib/README.md`, explaining the folder, its precedence, and the live-read caveat. This keeps packaging and seeding testable end to end.
  - The real contents (`idea-doc-skeleton.md`, `tamper-gate.sh`, manifest helpers, `notify-teams.sh`, `checkpoint-push.sh`, the init helper) belong to fp-next §9 step 3 and are out of scope here.
- **Release tarball:** `make samples-tarball` (`git archive HEAD:samples`) includes `_lib` with no change. The installer's `verify_tar_entries_safe` is unaffected because `_lib` contains no links.
- **Docker:** `COPY samples/ /samples/` includes `_lib` with no change.
- **`make install-samples`:** add `samples/_lib` to the copy list.
- **Seeder (`domain/samples`):**
  - `SampleSource` gains `ReadLibrary(ctx) (map[string][]byte, error)`. It returns the files under `<sourcedir>/_lib`, excluding test files, or an empty map when the folder is absent. `List` keeps ignoring `_lib`, and also skips it explicitly.
  - `Seeder.Seed` runs a **library pass first**. For each file `rel`, sorted:
    - if the ledger has `@lib/<rel>`, skip it (deletions are honored)
    - otherwise, if `shared/_lib/<rel>` already exists, record it in the ledger and skip (an admin's edit or a pre-placed file is never clobbered)
    - otherwise write it through `LibraryStore.WriteFile(Shared(), rel, content)` and record it in the ledger
  - Changed files in later releases are **not** overwritten. They reach live installs through `workflow_write_file name=@lib scope=shared`.
  - An infrastructure error aborts seeding, as it does for workflows. A file that is invalid by `ParseLibPath` rules is logged, skipped, and not recorded.
  - The `seedSampleWorkflows` wiring in `cmd/arbitron/sample_seed.go` passes the `LibraryStore`.
- **Composition root (`cmd/arbitron`):** construct the filesystem `LibraryStore` on the workspace path, inject it into `workflow.Service` (`SetLibraryStore`) and the seeder. The MCP server reaches it only through the service.

### 4.7 Docs

- **`AGENTS.md`**: a short `@lib` section covering:
  - the two roots and their precedence
  - the `@lib/` prefix in all file fields and includes
  - the lib-relative include rule
  - live-read semantics, the "broken lib breaks running instances" caveat, and that lib scripts need tests and should change through PRs
  - that `_lib` is a reserved name
  - the MCP `@lib` and `scope` usage
- **MCP authoring guidance**: one sentence covering `@lib/...` in file fields and includes, and managing the lib with the file tools using `name: "@lib"`.

## 5. Units, boundaries and ownership

| Unit | Layer | Purpose | Depends on |
|---|---|---|---|
| `ParseSpawnConfig` (+`Inputs`) | domain/workflow | one checked spawn contract | — |
| `PlanChildInputs` | domain/workflow | classify mapped / default / prepare / unknown | `InputField` |
| `checkChildInputMap` | domain/workflow | shared unknown-key + literal-type checks | — |
| `validateSpawnChildInputs` | domain/workflow | author-time spawn findings | `ChildContractReader`, planner |
| `SpawnWorkflowExecutor` | domain/engine | resolve → plan → (prepare) → merge → spawn | planner, `variable.ResolveTypedMap`, `ChildInputPreparer` |
| `ParseLibPath`, `IsReservedWorkflowName` | domain/workflow | lib vocabulary + reserved name | — |
| `expandIncludes` (+base) | domain/engine | lib-aware includes | `FileReader` port |
| `FileReaderAdapter` lib routing | adapter/filesystem | private→shared lib resolution, containment | `ParseLibPath` |
| `LibraryStore` port / fs impl | domain/workflow / adapter/filesystem | lib CRUD per scope | — |
| `Service.Library*` | domain/workflow | authz + audit boundary; effective read = store private then shared | `LibraryStore`, `audit.Service` |
| MCP `@lib` routing | adapter/mcp | tool surface | `Service` only |
| Seeder lib pass | domain/samples | seed `samples/_lib` | `SampleSource.ReadLibrary`, `LibraryStore`, `Ledger` |
| `SpawnDetails` | dashboard | show the mapping | node config |

Hexagonal rule: domain packages import no adapters. `ParseLibPath` lives in domain, and the adapter imports it.

## 6. Error handling and gotchas

- **Fail closed everywhere:**
  - unknown mapped key, unresolved reference, or null for a required input: the node fails and no LLM is called
  - malformed lib path, escaping symlink, or a symlinked `_lib` root: the read errors, with no fallthrough
  - a missing include: fails, as it does today
- **Messages are actionable by an author reading the trace:**
  - spawn failures name the node, the child, and the key or `{{path}}`
  - lib failures name the `@lib/...` path and the reason
  - absolute filesystem paths are not exposed beyond what `ResolvePath` errors already include today
- **Live-read cuts both ways.** A broken shared-lib edit breaks every running instance that reads that file next. This is documented, and so are the audit trail and the PR-driven change process.
- **Publishing with private-only lib refs (Q13).** A workflow whose `@lib/x` exists only in the publisher's private lib validates for the publisher and fails for every other runner. There is no check for this now. It is documented in AGENTS.md as a publish step: put `x` in the shared lib first.
- **Shadowing (Q9).** A private `_lib/x` changes the behaviour of shared workflows for that user's own runs only. This is documented.
- **Workspace paths in mapped inputs (Q4)** are passed through. Children should receive repo URLs and branches, not parent paths. This is documented as discouraged.
- **TOCTOU.** A symlink swapped between the containment check and the script exec is not defended against. Both scopes are writable only by their owner or an admin, the same trust model as workflow scripts (see the `adapter/process/executor_script.go` security note).

## 7. Testing (strict TDD, five categories)

Tests sit next to the package they cover. Fakes follow the existing patterns: a fake `ChatProvider` that counts calls, the fake `ChildSpawner`, `t.TempDir()` workspaces, and `os.Symlink` for link cases. Every test is written red first.

### 7.1 Spawn inputs: domain (`domain/workflow`)

**Happy path**
- `ParseSpawnConfig` returns a deep copy of `inputs`. Mutating the result leaves the node config untouched, and the reverse holds too.
- `PlanChildInputs`, with a child declaring required `a`, required `b` with a default, optional `c`, and required `d`, and `mapped={a}`: Mapped is `[a]`, Defaults is `{b}`, Prepare is `[d]`, and `c` is absent.
- A fully mapped plan has empty Prepare and empty Defaults.

**Edge cases**
- A nil or empty `inputs` map plans exactly like today's no-map spawn under the narrowed rule.
- A child with zero declared inputs produces an empty plan, whatever the map holds; extra keys are reported as Unknown.
- Mapped keys come back sorted. Prepare preserves author order.
- A `Default` of `false`, `0` or `""` counts as a default (non-nil). A JSON `null` default counts as no default.

**Error/failure**
- `inputs.workspace` returns `ErrSpawnReservedInput`, joined with a missing `childWorkflow` and a bad mode. All three are reported.
- Validate with a reader reports:
  - `SPAWN_UNKNOWN_CHILD_INPUT`, with the declared names listed
  - `SPAWN_INPUT_TYPE_MISMATCH` for a string literal given to a `boolean` input
  - `SPAWN_INPUT_TYPE_MISMATCH` for a null literal given to a required input
  - `SPAWN_PREPARER_PROFILE_MISSING` when Prepare is non-empty and there is no `model_profile`
  - `SPAWN_CHILD_CONTRACT_UNAVAILABLE` (a warning) when the reader returns a non-not-found error

**Boundary**
- A whole-value template mapped to a `number` input is not type-checked.
- An interpolated string such as `"x-{{a}}"` mapped to a `number` input is reported as a string literal mismatch.
- A required input with a default that is unmapped produces no `SPAWN_PREPARED_INPUTS` warning.
- `SPAWN_PREPARED_INPUTS` lists exactly the Prepare names.
- A reader not-found produces exactly one `UNKNOWN_CHILD_WORKFLOW`, both when the name checker is also supplied and when it is not.

**Integration**
- `GroupValidateOptions` through MCP `workflow_validate` and REST validate surfaces the spawn findings.
- `Service.Create`/`Update` of a workflow carrying `SPAWN_*` errors is **not** rejected (no write gating).
- `IsGroupValidationCode` is false for every `SPAWN_*` code.
- The existing group child-input tests pass unchanged after the `checkChildInputMap` extraction.

### 7.2 Spawn inputs: executor (`domain/engine`)

**Happy path**
- A fully mapped `wait_for_child` spawn:
  - the preparer's call count is **0**
  - the child receives exactly the mapped typed values: an array stays `[]any`, an object stays a map, a bool stays a bool
  - `inputSources` reports all keys as `mapped`
  - the node record carries `inputSources`
- A fully mapped `fire_and_forget` spawn behaves the same.
- A partially mapped spawn: the preparer is called once, and its request `Inputs` equals only the Prepare fields. The merged values hold mapped, default and prepared keys, and `inputSources` labels each one correctly.

**Edge cases**
- A required-with-default input that is unmapped receives a deep copy of the default. Mutating the child's value does not affect the manifest.
- The preparer returns extra keys and a key that collides with a mapped key. Both are dropped, and the mapped value wins.
- A fire-and-forget spawn inside a loop re-resolves on each revisit, picking up the new parent values.
- An awaited spawn's re-entry (`resumeFromLink`) does not resolve or prepare again.

**Error/failure**
- An unresolved `{{nope.x}}`:
  - the node fails, wrapping `ErrUnresolvedReference` and naming the path
  - the preparer's call count is 0
  - no child is spawned
  - no parent `waiting` transition happens
- An unknown mapped key gives `ErrSpawnUnknownChildInput` before any resolution, with no spawn.
- A mapped required input that resolves to null gives `ErrChildContractUnsatisfied`, with no LLM call.
- Prepare is non-empty and there is no profile: an error naming the unmapped required count.
- The preparer returns without a requested key: `ErrChildContractUnsatisfied`.
- A snapshot with `inputs.workspace` that predates validation fails through `ParseSpawnConfig`.

**Boundary**
- A mapping that is exactly `"{{a}}"` versus `" {{ a }} "`, with whitespace inside and outside the braces, follows the `wholeReference` rules. Only the whitespace-inside form keeps its type.
- An empty-string literal for a required string input is passed through, not nulled.
- A mapping to a node output path that exists with value `null` behaves as null.

**Integration**
- `cmd/arbitron` e2e, using the existing `spawn_wait_e2e_test.go` harness:
  - a parent with a fully mapped awaited spawn runs to completion
  - the child's variables equal the mapped values
  - a counting fake chat selector records zero calls
  - `childOutput` routes on `success`
- A second e2e test: an unresolved reference routes the parent through the node's error behaviour, and no child instance exists.

### 7.3 `@lib` resolution (`adapter/filesystem`, `domain/workflow`)

**Happy path**
- `ParseLibPath("@lib/a/b.sh")` returns `("a/b.sh", true, nil)`. `"scripts/x.sh"` gives `isLib=false`.
- `ReadFile(owner, "any-wf", "@lib/x.md")`:
  - reads private when it exists there
  - otherwise reads shared
  - with owner `uuid.Nil`, reads shared only
- `ResolvePath` returns the real path inside `_lib`.
- `FileExists` is true for a shared-only lib file.

**Edge cases**
- The private `_lib` is missing entirely: falls through to shared.
- Both scopes have `x`: private wins.
- Only private has `x` and the owner is nil: not found.
- The `workflowName` argument is irrelevant: the same result for two different workflow names, and for a workflow that does not exist.
- **A symlink inside `_lib` that resolves to another file inside the same `_lib` is allowed** (orchestrator-required test).

**Error/failure**
- These are all rejected with `ErrInvalidLibPath`, and nothing is read:
  - `@lib/../x`, `@lib/a/../../x`, `@lib/./x`, `@lib//x`, `@lib/`, `@lib`, `@lib/\x`, `@lib/a\0b`, `@lib//etc/passwd`
- A symlink in private `_lib` pointing outside `_lib` (for example `../../shared/<wf>/secret` or `/etc/hosts`) **errors**, and a shared file of the same name is **not** returned (no fallthrough).
- A private `_lib` that is itself a symlink to elsewhere errors.
- A dangling symlink errors.

**Boundary**
- A deeply nested `@lib/a/b/c/d.md` works.
- A file named with a leading dot inside a subdirectory works.
- `@lib/workflow.json` and `@lib/layout.json` are rejected.
- The case-variant `@LIB/x` is **not** a lib path; it is an ordinary relative path. The prefix is case-sensitive by design, and this is documented.

**Integration**
- A `ScriptExecutor` node with `scriptFile: "@lib/echo.sh"` against a temp workspace runs, sees cwd = `_lib`, and returns its JSON.
- An `interactive_agent` `orchestrationFile` under `@lib` loads, using the fake provider.
- The group `resolverPromptFile` loads from `@lib`.
- Validation `FileChecker` reports an existing lib file as present and a missing one as `MISSING_FILE`, and a malformed path gives `INVALID_LIB_PATH`.

### 7.4 Includes (`domain/engine`)

**Happy path**
- A workflow template with `{{include:@lib/skel.md}}` expands it.
- A lib file containing `{{include:part.md}}` resolves `@lib/part.md`.
- `templateFile: "@lib/x.md"` expands lib-relative includes.

**Edge cases**
- A workflow file named `part.md` exists and a lib file includes `part.md`: the **lib** file is used.
- A lib file includes `@lib/part.md` explicitly: same result.
- A workflow file and a lib file with the same name are both included from one workflow template, and both expand: different cycle keys.

**Error/failure**
- A lib file includes a name that exists only in the workflow folder: fails closed with `ErrTemplateInclude` (lib cannot reach workflow files).
- A cycle `@lib/a.md` → `b.md` (lib) → `a.md` (lib) is reported as a cycle.
- A malformed `{{include:@lib/../x}}` fails.

**Boundary**
- The byte, count and depth budgets are charged across both bases: hitting `maxIncludeCount` across mixed bases fails exactly at the limit.

**Integration**
- The existing `template_include_samples_test.go` still passes.
- An agent-node test with a fake provider receives a prompt containing the expanded lib content.

### 7.5 Reserved name, store, service and MCP

**Happy path**
- `Service.LibraryWrite(private)` and then `LibraryRead(nil scope)` return the content.
- An admin `LibraryWrite(shared)` records a `lib.edit_shared` audit event with `{path, op}`.
- `LibraryList` returns both scopes.
- MCP `workflow_write_file name=@lib path=x.md` writes to private. `scope=shared` as admin writes shared. `workflow_list_files name=@lib` returns the `{private, shared}` JSON.

**Edge cases**
- The first write creates `_lib` with partition permissions.
- Listing when neither `_lib` exists returns empty arrays, not an error.
- An effective read prefers private.
- An exact-scope read of shared ignores a private shadow.
- Deleting the private shadow re-exposes the shared copy in effective reads.

**Error/failure**
- A non-admin `scope=shared` write or delete returns `ErrUnauthorized`, and the MCP tool error says the shared library requires admin.
- `scope` with a non-`@lib` name is a tool error.
- MCP name `_lib` or `_LIB` is rejected with a pointer to `@lib`.
- Workflow-scoped `write_file name=wf path=@lib/x` returns `ErrInvalidFilePath`.
- These reject a reserved name with `ErrInvalidName`:
  - `Service.Create("_lib")`, `Create("_Lib")`
  - `Rename(x, "_lib")`
  - `Publish(x, "_lib")`
  - `Copy(s, "_lib")`
  - `Duplicate(x, "_lib")`
  - the stamper destination
- Trigger and spawn of `_lib` fail as not-found.
- Deleting a missing lib file returns `ErrFileNotFound`.

**Boundary**
- A 10 MiB write succeeds and 10 MiB + 1 is rejected (the existing limit).
- `WorkflowRepo.List` hides `_lib` even when a `workflow.json` was planted inside it.
- REST `GET /workflows/_lib/files` and `/workflows/@lib/files` are not-found. The REST surface is unchanged.

**Integration**
- MCP round trip on a real filesystem store: write shared as admin, then a run by a second user reads it through `{{include:@lib/x.md}}`. Then a private write by the second user shadows it for that user's run only.
- Every scope-root enumerator test (registry backfill, ref sweep, flat migration) has a `_lib` folder present and asserts it is ignored.

### 7.6 Seeding and packaging

**Happy path**
- A sample source with `_lib/README.md` and one workflow: the lib file lands in `shared/_lib/README.md` **before** the workflow is seeded, and the ledger records `@lib/README.md`.

**Edge cases**
- A second boot is a no-op.
- A new file added to the source in a later version seeds on the next boot.
- A source file changed in a later version does **not** overwrite.
- A file already present in `shared/_lib` without a ledger entry is recorded and not overwritten.
- `_lib/*_test.sh` is not seeded.

**Error/failure**
- A deleted seeded lib file is not re-created (the ledger is honored).
- An unwritable shared root aborts seeding with an error.
- An invalid lib file name in the source is logged, skipped and not recorded.

**Boundary**
- A sample source with no `_lib` folder skips the lib pass silently.
- `SampleSource.List` never returns `_lib`.

**Integration**
- `samples/samples_test.go` asserts that `samples/_lib` exists, contains no `workflow.json`, contains no symlinks, and that every file passes `ParseLibPath`.
- `make samples-tarball` output contains `./_lib/README.md`. This is checked in the release bats or a Go test over the produced tarball.
- `tests/install/samples.bats` gains a fixture case where the tarball includes `_lib/` and `fetch_samples_to` extracts it.
- `tests/release/*.bats` stays green.
- `make install-samples DEST=<tmp>` copies `_lib`.

### 7.7 Dashboard (Vitest)

**Happy path**
- `SpawnDetails` renders the child link (`/workflows/<child>`), the mode text, and one sorted row per mapped key, with the correct `reference`, `template` or `literal` badge.

**Edge cases**
- No `inputs`: the empty-state copy appears.
- Nested object and array literals render as compact JSON.
- A non-spawn node does not render `SpawnDetails`.

**Error/failure**
- A malformed config (`inputs` is not an object, or `childWorkflow` is missing) renders without crashing: the row is omitted and an em dash is shown for the child.

**Boundary**
- A literal longer than 120 characters is truncated with an ellipsis, and the full value appears in `title`.
- A key containing dots or dashes renders verbatim.

**Integration**
- In `WorkflowReview.test.tsx`, clicking a spawn node in the review graph shows `SpawnDetails` and the raw Config block together.
- `npx tsc --noEmit` passes.
- After `npm run build`, the synced `adapter/dashboard/dist` is embedded and the existing dist-freshness check (`check-ui-fresh`) passes.

### 7.8 Gates

`gofmt`, then:

- `go test ./...`
- `cd dashboard && npx tsc --noEmit && npx vitest run && npm run build` (then sync dist)
- `make test-install` / `make test-release`, or the bats targets that cover `samples.bats` and `tests/release`
- `shellcheck` on any new `.sh` file

## 8. Out of scope

- The contents of `samples/_lib` beyond `README.md` (fp-next §9 step 3).
- The stage workflows, the parent pipeline, ralph-loop/pr-review changes, the tamper gate, and mutation testing.
- Symlink hardening for ordinary workflow files.
- REST endpoints for the library.
- A dedicated `lib_*` MCP tool family (fp-next open question 2).
- Overwrite-on-upgrade seeding for lib files.
- A publish-time check for private-only lib references.
- Client-side child-contract display in the dashboard.
- Runtime type coercion of spawn inputs.
