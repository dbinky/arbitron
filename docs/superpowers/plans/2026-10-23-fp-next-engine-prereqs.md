# fp-next Engine Prerequisites Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `spawn_workflow` an explicit typed `inputs` map, with LLM preparation narrowed to unmapped required inputs that have no default. Add an engine-level `@lib` shared library: private-then-shared, live-read, containment-checked, reserved `_lib` name, managed through MCP, and seeded from `samples/_lib`.

**Architecture:**
- Spawn inputs: one domain parser (`ParseSpawnConfig`) and one pure planner (`PlanChildInputs`) are shared by validation and the spawn executor. Values are resolved with `variable.ResolveTypedMap`.
- `@lib`: a domain-owned path parser. `FileReaderAdapter` routes `@lib/` paths, so every file consumer gets it with no port change. Management goes through a new `LibraryStore` port behind `workflow.Service` methods, which are the authorization and audit boundary. MCP calls only the service.

**Tech Stack:** Go 1.25.5 (testify, chi, mcp-go); React 19 / Vite 8 / Vitest / CSS modules; bats + shellcheck for installer and release tests.

**Spec:** `docs/superpowers/specs/2026-10-23-fp-next-engine-prereqs-design.md`. Read it alongside this plan. Section references (§) point there.

## Global Constraints

- Hexagonal: `domain/` imports no `adapter/` package. Adapters import domain. `cmd/arbitron` wires them.
- Strict TDD. Write each test red first and watch it fail for the stated reason. Cover the five categories: happy path, edge, error/failure, boundary, integration.
- Run `gofmt -w` on every touched Go file before running tests.
- Each task ends with a commit, using a conventional-commit subject and the attribution trailer `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`. Do not commit work outside the task's files.
- `workspace` is the only reserved input name (`variable.ReservedInputNames`).
- Reserved library folder: `_lib`. Reference name: `@lib`. Path prefix: `@lib/`.
  - The prefix is case-sensitive.
  - The reserved-name check is case-insensitive and covers both `_lib` and `@lib`.
- `inputSources` values are exactly `"mapped"`, `"default"` and `"prepared"`. It holds names only, never values.
- New validation codes:
  - `SPAWN_RESERVED_INPUT`, `SPAWN_UNKNOWN_CHILD_INPUT`, `SPAWN_INPUT_TYPE_MISMATCH`, `SPAWN_PREPARER_PROFILE_MISSING` (errors)
  - `SPAWN_PREPARED_INPUTS`, `SPAWN_CHILD_CONTRACT_UNAVAILABLE` (severity `"warning"`)
  - `INVALID_LIB_PATH` (error)
- New audit action: `lib.edit_shared`, detail `{"path":..., "op":"write"|"delete"}`.
- Spawn findings never gate writes. `IsGroupValidationCode` must return false for every `SPAWN_*` and `INVALID_LIB_PATH` code.
- The MCP write limit stays 10 MiB (`maxWriteFileContentBytes`). The REST file surface is unchanged.
- Dashboard: rebuild with `cd dashboard && npm run build`, then `rm -rf adapter/dashboard/dist && cp -r dashboard/dist adapter/dashboard/dist`. Never use `make ui` (npm ci lock drift).
- Never use emojis in code, comments or docs.

## Review Focus

1. **A cleaned workflow-scoped path that becomes `@lib/x`** (for example `./@lib/x`, or `a/../@lib/x`) must be rejected on write just as `@lib/x` is. Otherwise a file is written that no read can ever reach. The test is in Task 12.
2. **A very large mapped value** (an array of 10,000 strings) must reach the child intact. The preparer's parent-context truncation (`maxContextValue`) must never apply to mapped values. The test is in Task 3.
3. **A lib file rewritten while a run reads it** must give the reader either the old bytes or the new ones, never a partial file. The test is in Task 13.
4. **An `@LIB` or `_LIB` name passed to the MCP file tools** must be rejected with the "reserved; use @lib" message. It must not fall through as an ordinary workflow name. The test is in Task 15.
5. **The local-mode default identity writing `scope=shared`** must succeed, because local mode's identity is admin. A real user role is refused. The test is in Task 15.

---

### Task 1: `SpawnConfig.Inputs` and the reserved-input check

**Files:**
- Modify: `domain/workflow/workflow.go` (the `SpawnConfig` struct, the error vars, `ParseSpawnConfig`, and `NodeConfig`, which gains `Inputs`)
- Reuse (no change): `cloneGroupValue` in `domain/workflow/group.go` for the deep copy
- Test: `domain/workflow/spawn_config_test.go`

**Interfaces:**
- Produces:
  - `NodeConfig.Inputs map[string]any \`json:"inputs,omitempty"\``
  - `SpawnConfig{ChildWorkflow, Mode string; Inputs map[string]any}`
  - `var ErrSpawnReservedInput = errors.New("spawn input name is reserved")`

- [ ] **Step 1: Write failing tests** in `spawn_config_test.go`:
  - `TestParseSpawnConfig_CarriesInputsAsDeepCopy`: config inputs are `{"branch":"{{intake.branch}}","paths":[]any{"a"},"opts":map[string]any{"x":1.0}}`. Mutating the returned `cfg.Inputs["paths"].([]any)[0]` and `cfg.Inputs["opts"]` leaves `node.Config.Inputs` unchanged, and the reverse also holds.
  - `TestParseSpawnConfig_NilInputsStayNil`: `cfg.Inputs == nil` when the config has none.
  - `TestParseSpawnConfig_ReservedWorkspaceInput`: `inputs{"workspace":"/x"}` gives `errors.Is(err, ErrSpawnReservedInput)` and the zero `SpawnConfig`.
  - `TestParseSpawnConfig_ReservedInputJoinedWithOtherProblems`: an empty childWorkflow, mode `"bogus"` and `inputs.workspace` together make all three sentinels satisfy `errors.Is`.
  - `TestNodeConfig_InputsJSONRoundTrip`: unmarshalling `{"childWorkflow":"c","spawnMode":"fire_and_forget","inputs":{"n":3,"b":true}}` gives `Inputs["n"]==3.0` and `Inputs["b"]==true`. Marshalling an empty config omits `inputs`.
- [ ] **Step 2: Run them to see them fail.** Run `go test ./domain/workflow -run 'TestParseSpawnConfig|TestNodeConfig_Inputs' -v`. Expected: compile errors or FAIL on the missing field and sentinel.
- [ ] **Step 3: Implement.** Add the field and the sentinel. `ParseSpawnConfig` appends `fmt.Errorf("%w: %q is seeded by the engine for every run; remove it from inputs", ErrSpawnReservedInput, k)` for each key in `cfg.Inputs` that is in the package's existing `reservedInputNames` set (`validation.go`), and deep-copies `Inputs` with `cloneGroupValue` on success.
- [ ] **Step 4: Run `gofmt -w domain/workflow && go test ./domain/workflow`.** Expected: PASS. This includes the existing group clone tests.
- [ ] **Step 5: Commit** with the message `feat(workflow): spawn_workflow inputs map in SpawnConfig`.

### Task 2: `PlanChildInputs` planner

**Files:**
- Create: `domain/workflow/child_input_plan.go`
- Test: `domain/workflow/child_input_plan_test.go`

**Interfaces:**
- Consumes: `InputField` and `cloneGroupValue`.
- Produces:
  ```go
  type ChildInputPlan struct {
      Unknown  []string        // mapped keys the child does not declare, sorted
      Mapped   []string        // declared mapped keys, sorted
      Defaults map[string]any  // unmapped Required inputs with non-nil Default -> deep copy
      Prepare  []InputField    // unmapped Required inputs with nil Default, author order
  }
  func PlanChildInputs(fields []InputField, mapped map[string]any) ChildInputPlan
  ```
  The key `workspace` is skipped entirely, because `ParseSpawnConfig` owns that error. `Defaults` is a non-nil map even when it is empty.

- [ ] **Step 1: Write failing tests:**
  - **Happy path:** `TestPlanChildInputs_Classifies`. The fields are `a`(req), `b`(req, Default `"main"`), `c`(opt), `d`(req). With `mapped={a:"{{x}}"}` the result is Mapped=`[a]`, Defaults=`{b:"main"}`, Prepare names=`[d]`, Unknown empty, and `c` appears nowhere.
  - **Happy path:** `TestPlanChildInputs_FullyMapped`: Prepare and Defaults are both empty.
  - **Edge:** `TestPlanChildInputs_NilAndEmptyMapAreEquivalent`.
  - **Edge:** `TestPlanChildInputs_ZeroDeclaredInputsReportsAllMappedAsUnknown`, giving Unknown=`["x","y"]`, sorted.
  - **Edge:** `TestPlanChildInputs_PrepareKeepsAuthorOrder`, with fields `z`, `a`, `m` (all required, unmapped) giving the order `z,a,m`.
  - **Boundary:** `TestPlanChildInputs_FalsyDefaultsCountAsDefaults`, where `false`, `0.0` and `""` all land in Defaults.
  - **Boundary:** `TestPlanChildInputs_NullDefaultIsNoDefault`, where `Default: nil` lands in Prepare.
  - **Boundary:** `TestPlanChildInputs_DefaultsAreDeepCopies`. Mutating `plan.Defaults["list"].([]any)[0]` leaves the field's Default unchanged.
  - **Error/failure:** `TestPlanChildInputs_WorkspaceKeyIgnored`. A mapped `workspace` appears in neither Unknown nor Mapped.
- [ ] **Step 2: Run** `go test ./domain/workflow -run TestPlanChildInputs -v`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement `PlanChildInputs`** in `child_input_plan.go`.
- [ ] **Step 4: Run** `gofmt -w domain/workflow && go test ./domain/workflow`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(workflow): PlanChildInputs classifies mapped/default/prepared child inputs`.

### Task 3: Spawn executor resolves, plans, prepares and merges

**Files:**
- Modify: `domain/engine/executor_spawn.go` (`Execute` passes the parsed `cfg` to `executeFireAndForget`/`executeWait`, then to `prepareAndSpawn`; `spawnOutput` gains an `inputSources` parameter)
- Modify: `domain/engine/child_input_preparer.go` (the missing-profile message wording only)
- Modify: `domain/engine/executor_spawn_test.go`: in `TestSpawnExecutor_PassesChildContractAndParentContextToPreparer`, change `require.Len(t, got.Inputs, 2, ...)` to `require.Len(t, got.Inputs, 1, "only unmapped required inputs without a default are prepared")` and assert `got.Inputs[0].Name == "repo"`
- Create: `domain/engine/executor_spawn_inputs_test.go`

**Interfaces:**
- Consumes: `workflow.ParseSpawnConfig` / `SpawnConfig.Inputs` (Task 1), `workflow.PlanChildInputs` (Task 2), `variable.ResolveTypedMap`, `variable.ErrUnresolvedReference`, and `ErrChildContractUnsatisfied`.
- Produces:
  - `var ErrSpawnUnknownChildInput = errors.New("engine: spawn maps an input the child workflow does not declare")`
  - Spawn node output key `inputSources map[string]string`, present on first-execution results and on the wait-path node-execution record. `resumeFromLink` does not add it.

- [ ] **Step 1: Write failing tests** in `executor_spawn_inputs_test.go`. Reuse `newSpawnExecFixture`. Add a helper `mappedNode(child, mode string, inputs map[string]any) workflow.Node`, and extend the fixture's child contracts with a `"stage"` child: `repo_url`(req, string), `branch`(req, string), `paths`(req, array), `in_place`(opt, boolean), `base`(req, Default `"main"`), `topic`(req, string).

  **Happy path**
  - `TestSpawnInputs_FullyMappedFireAndForget_ZeroPreparerCalls`. Map every required no-default input (`repo_url`, `branch`, `paths`, `topic`) from the vars: `paths` is `[]any{"a","b"}` from node output `prior.paths`, plus the literal `in_place: true`. Assert:
    - `f.preparer.calls == 0`
    - the spawner received `Variables` equal to `{repo_url, branch, paths:[]any{"a","b"}, topic, in_place:true, base:"main"}`
    - `res.Output["inputSources"] == map[string]string{"repo_url":"mapped","branch":"mapped","paths":"mapped","topic":"mapped","in_place":"mapped","base":"default"}`
  - `TestSpawnInputs_FullyMappedWaitForChild_RecordCarriesInputSources`: the wait mode is set and the node-execution record's `Output["inputSources"]` matches.
  - `TestSpawnInputs_PartialMapping_PreparerAskedOnlyForUnmapped`. Map everything except `topic`. The fake preparer returns `{topic:"t"}`. Assert:
    - `calls == 1`
    - `gotReq.Inputs` names are exactly `[topic]`
    - the merged variables hold `topic:"t"`
    - `inputSources["topic"]=="prepared"`
  - `TestSpawnInputs_FullyMappedNeedsNoModelProfile`: a node with an empty `ModelProfile` that is fully mapped returns no error.

  **Edge**
  - `TestSpawnInputs_DefaultIsDeepCopied`. The child's `base` Default is `[]any{"x"}`. Mutating the spawned value leaves the fixture's Default unchanged.
  - `TestSpawnInputs_PreparerExtrasAndCollisionsDropped`. The preparer returns `{topic:"t", branch:"EVIL", extra:1}`. The spawned `branch` is the mapped value and `extra` is absent.
  - `TestSpawnInputs_FireAndForgetReResolvesEachExecution`. Run twice with a different `intake.branch` each time; the two spawns carry the two branches.

  **Error/failure**
  - `TestSpawnInputs_UnresolvedReference_FailsClosed`. The map has `branch:"{{nope.branch}}"`. Assert:
    - `errors.Is(err, variable.ErrUnresolvedReference)`
    - the message contains `nope.branch`
    - `preparer.calls==0`
    - the spawner has no calls
    - the parent state is still `running`
  - `TestSpawnInputs_UnknownMappedKey_FailsBeforeResolve`. Map `{brnch:"{{nope}}"}`. The result is `errors.Is(err, engine.ErrSpawnUnknownChildInput)`, not ErrUnresolvedReference, and the message lists the declared names.
  - `TestSpawnInputs_MappedRequiredResolvesToNull_Fails`. A var holds `nil` and `branch:"{{v}}"`. The result is `errors.Is(err, engine.ErrChildContractUnsatisfied)` with `calls==0`.
  - `TestSpawnInputs_PrepareNeededWithoutProfile_Fails`. The message contains `unmapped required input`.
  - `TestSpawnInputs_PreparerOmitsRequested_Fails`: `ErrChildContractUnsatisfied`.
  - `TestSpawnInputs_SnapshotWithReservedInput_Fails`: `errors.Is(err, workflow.ErrSpawnReservedInput)`.

  **Boundary**
  - `TestSpawnInputs_WholeReferenceWhitespaceRules`. `"{{ prior.paths }}"` keeps the `[]any` type. `" {{prior.paths}}"`, with a leading space outside the braces, interpolates to a string.
  - `TestSpawnInputs_EmptyStringLiteralPassedThrough`. A literal `branch:""` is spawned as `""`, with no error.
  - `TestSpawnInputs_LargeMappedArrayIntact` (Review Focus 2). A 10,000-element `[]any` is spawned with length 10000.
  - `TestSpawnInputs_AwaitedReentryDoesNotResolveAgain`. After the first wait, change a var and re-enter through a link that is still running. There is no new spawn and no preparer call.
- [ ] **Step 2: Run** `go test ./domain/engine -run 'TestSpawnInputs|TestSpawnExecutor' -v`. Expected: the new tests FAIL, and the edited `PassesChildContract...` test FAILs at `Len 1`.
- [ ] **Step 3: Implement** the 10-step flow in spec §3.3 inside `prepareAndSpawn(ctx, node, cfg workflow.SpawnConfig, vars, workflowName, instanceID, mode)`. Filter the preparer result to `plan.Prepare` names before calling `validateChildInputs(child, plan.Prepare, filtered)`. Log the dropped keys with `slog.Debug` (names only). The missing-profile message becomes `"spawn_workflow %q: child workflow %q has %d unmapped required input(s) without a default, so this node must set a model_profile to prepare them"`.
- [ ] **Step 4: Run** `gofmt -w domain/engine && go test ./domain/engine`. Expected: PASS, including every existing spawn test.
- [ ] **Step 5: Commit** with the message `feat(engine): spawn_workflow resolves explicit inputs; LLM prepares only unmapped required inputs`.

### Task 4: Spawn inputs end to end

**Files:**
- Create: `cmd/arbitron/spawn_inputs_e2e_test.go`. Reuse the harness helpers in `cmd/arbitron/spawn_wait_e2e_test.go`.

**Interfaces:**
- Consumes: Task 3 behaviour, and the existing e2e harness's chat selector fake. If the harness has no call counter, add a `calls int` counter to the harness's fake chat provider in that test file's helpers.

- [ ] **Step 1: Write failing tests:**
  - `TestSpawnInputsE2E_FullyMappedAwaitedSpawn_NoChatCalls`. The parent has input `branch`, and a `wait_for_child` node maps `{branch:"{{branch}}", n: 3}` to a child that declares required `branch` and `n:number` and has a script or transform that echoes them. Assert:
    - the parent completes through `success`
    - the child's variables have `branch` and `n==3.0`
    - the chat call count is 0
    - the parent's `{{spawn.childOutput.branch}}` matches
  - `TestSpawnInputsE2E_UnresolvedMapping_NoChildCreated`. The map has `branch:"{{missing}}"`. The parent fails (default error behaviour), and an instance listing shows no child linked to the parent.
- [ ] **Step 2: Run** `go test ./cmd/arbitron -run TestSpawnInputsE2E -v`. Expected: FAIL before Task 3 is merged; PASS after. If it already passes because Task 3 has landed, confirm that by temporarily reverting the merge step locally, then restore it.
- [ ] **Step 3: Run** `gofmt -w cmd/arbitron && go test ./cmd/arbitron -run 'TestSpawn'`. Expected: PASS.
- [ ] **Step 4: Commit** with the message `test(spawn): e2e fully mapped spawn makes no chat calls; unresolved mapping fails closed`.

### Task 5: Author-time spawn input validation

**Files:**
- Modify: `domain/workflow/validation_group_children.go`. Extract `checkChildInputMap(fields []InputField, inputs map[string]any) childInputFindings`:
  ```go
  type childInputFindings struct {
      unknown    []string           // sorted
      mismatches []childTypeProblem // {key, field InputField, problem string}, sorted by key
      unmappedRequired []InputField // author order
  }
  ```
  `checkTaskInputs` becomes a wrapper that emits the existing GROUP_* findings, with byte-identical messages.
- Modify: `domain/workflow/validation.go`. `validateSpawnWorkflowNode` emits `SPAWN_RESERVED_INPUT` when `errors.Is(cfgErr, ErrSpawnReservedInput)`. `Validate` calls `validateSpawnChildInputs(wf, o)` when `o.children != nil`.
- Create: `domain/workflow/validation_spawn_inputs.go` (`validateSpawnChildInputs`)
- Test: `domain/workflow/validation_spawn_inputs_test.go`. Integration tests go in `adapter/mcp/tools_workflow_test.go` (MCP `workflow_validate`) and `domain/workflow/service_group_test.go` (no write gating).

**Interfaces:**
- Consumes: `PlanChildInputs` (Task 2), `ChildContractReader`, `ErrNotFound`.
- Produces: the codes listed in Global Constraints. Each finding's `NodeID` is the spawn node's id.

- [ ] **Step 1: Write failing tests.** Use a fake `ChildContractReader` that counts calls and returns `ErrNotFound` for unknown names.

  **Happy path**
  - `TestValidateSpawnInputs_FullyMappedValid_NoFindings`.
  - `TestValidateSpawnInputs_PreparedInputsAdvisory`. Exactly one `SPAWN_PREPARED_INPUTS` with Severity `"warning"`, whose message lists the unmapped required names.

  **Error/failure**
  - `TestValidateSpawnInputs_ReservedInput` gives `SPAWN_RESERVED_INPUT`. It needs no reader.
  - `TestValidateSpawnInputs_UnknownKey` gives `SPAWN_UNKNOWN_CHILD_INPUT`, with a message containing `declared: `.
  - `TestValidateSpawnInputs_LiteralTypeMismatch`: `"yes"` for a boolean input gives `SPAWN_INPUT_TYPE_MISMATCH`.
  - `TestValidateSpawnInputs_NullLiteralForRequired` gives `SPAWN_INPUT_TYPE_MISMATCH`.
  - `TestValidateSpawnInputs_PreparerProfileMissing`: Prepare is non-empty and there is no `model_profile`, giving `SPAWN_PREPARER_PROFILE_MISSING`.
  - `TestValidateSpawnInputs_ContractUnavailableIsWarning`. A reader error that is not not-found gives `SPAWN_CHILD_CONTRACT_UNAVAILABLE` (warning).

  **Boundary**
  - `TestValidateSpawnInputs_WholeTemplateNotTypeChecked`.
  - `TestValidateSpawnInputs_InterpolatedStringIsStringLiteral`: `"x-{{a}}"` for a number input gives a mismatch.
  - `TestValidateSpawnInputs_RequiredWithDefaultNoAdvisory`.
  - `TestValidateSpawnInputs_UnknownChildReportedOnce_WithAndWithoutNameChecker`: exactly one `UNKNOWN_CHILD_WORKFLOW` in both configurations.

  **Edge**
  - `TestValidateSpawnInputs_ChildLoadedOncePerCall`: two spawn nodes on the same child mean the reader is called once.
  - `TestValidateSpawnInputs_NoReaderSkipsContractChecks`. Only `SPAWN_RESERVED_INPUT` can appear.

  **Integration**
  - `TestIsGroupValidationCode_FalseForSpawnCodes`: every new code is false.
  - In `service_group_test.go`, `TestService_UpdateNotGatedBySpawnInputFindings`: `Update` of a workflow with `SPAWN_UNKNOWN_CHILD_INPUT` succeeds.
  - In `tools_workflow_test.go`, `TestWorkflowValidateTool_ReportsSpawnInputFindings`: MCP `workflow_validate` output contains `SPAWN_UNKNOWN_CHILD_INPUT`.
  - **Regression:** the existing `validation_group_children_test.go` passes unchanged.
- [ ] **Step 2: Run** `go test ./domain/workflow -run 'TestValidateSpawnInputs|TestIsGroupValidationCode|TestService_UpdateNotGated' -v && go test ./adapter/mcp -run TestWorkflowValidateTool_ReportsSpawnInputFindings -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the extraction and `validateSpawnChildInputs`. Memoize per call as `validateGroupChildren` does. Report `UNKNOWN_CHILD_WORKFLOW` from this pass only when `o.workflows == nil`.
- [ ] **Step 4: Run** `gofmt -w domain/workflow && go test ./domain/workflow ./adapter/mcp ./adapter/http`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(validation): spawn input contract checks against the child's declared inputs`.

### Task 6: Dashboard `SpawnDetails`

**Files:**
- Create: `dashboard/src/components/SpawnDetails.tsx`
- Create: `dashboard/src/components/SpawnDetails.test.tsx`
- Modify: `dashboard/src/routes/WorkflowReview.tsx` (render `<SpawnDetails config={selectedNode.config} />` before the Config block when `selectedNode.type === 'spawn_workflow'`)
- Modify: `dashboard/src/routes/WorkflowReview.module.css` (`.spawnInputTable`, `.spawnBadge`, and the three badge modifiers)
- Modify: `dashboard/src/routes/WorkflowReview.test.tsx` (integration)
- Modify: `adapter/dashboard/dist/**` (rebuilt artifact)

**Interfaces:**
- Produces:
  - `export function SpawnDetails({ config }: { config: Record<string, unknown> }): JSX.Element`
  - `export type MappingKind = 'reference' | 'template' | 'literal'`
  - `export function mappingKind(v: unknown): MappingKind`. A whole `{{path}}` string is `reference`, any other string containing `{{` is `template`, and everything else is `literal`. The whole-reference test mirrors Go's `wholeReference`: trimmed inner text, non-empty, no nested braces, and no whitespace outside the braces.

- [ ] **Step 1: Write failing tests** in `SpawnDetails.test.tsx`:

  **Happy path**
  - It renders a link with href `/workflows/stage` and the text "stage".
  - The mode text is "waits for child" or "fire and forget".
  - Rows are sorted by key, with badges `reference` for `{{a.b}}`, `template` for `x-{{a}}`, and `literal` for `true`.

  **Edge**
  - No inputs shows the exact empty-state copy from spec §3.5.
  - A nested object literal renders as compact JSON (`{"x":1}`).

  **Error/failure**
  - When `inputs` is a string or `childWorkflow` is missing, the component renders without throwing and shows an em dash for the child.

  **Boundary**
  - A 121-character literal renders truncated with `…`, with the full value in `title`. A 120-character literal is not truncated.
  - Keys containing `.` and `-` render verbatim.
  - `mappingKind(' {{a}}') === 'template'` and `mappingKind('{{ a }}') === 'reference'`.

  **Integration** (`WorkflowReview.test.tsx`)
  - Selecting a `spawn_workflow` node in `fixtureWorkflow` (extend the fixture with one spawn node) shows `data-testid="review-spawn-details"` and the raw Config block together.
  - A non-spawn node does not show it.
- [ ] **Step 2: Run** `cd dashboard && npx vitest run src/components/SpawnDetails.test.tsx src/routes/WorkflowReview.test.tsx`. Expected: FAIL.
- [ ] **Step 3: Implement** the component and the CSS, reusing the `nodeDetail*`, `inputName` and `inputType` classes.
- [ ] **Step 4: Run** `cd dashboard && npx tsc --noEmit && npx vitest run && npm run build`, then `rm -rf adapter/dashboard/dist && cp -r dashboard/dist adapter/dashboard/dist && go test ./adapter/dashboard/...`. Expected: all PASS.
- [ ] **Step 5: Commit** with the message `feat(dashboard): spawn node inspector shows the inputs mapping`.

### Task 7: Docs for spawn inputs

**Files:**
- Modify: `AGENTS.md`, spawn_workflow section. Add a paragraph covering:
  - the `inputs` map semantics
  - that the LLM prepares only unmapped required inputs without a default
  - that defaults are passed explicitly
  - that optional inputs are never LLM-filled
  - fail-closed unresolved references
  - zero LLM calls and no `model_profile` needed when fully mapped
  - `inputSources`
  - that parent `{{workspace}}` paths in mappings are discouraged
- Modify: `docs/specs/2026-07-27-workflow-spawn-node-spec.md`. Append an `## Amendment 2026-10-23` section that links the new spec and states the narrowed semantics in 3–5 lines.
- Modify: `adapter/mcp/authoring_guidance.go`. Append a `SpawnInputsGuidance` const to `WorkflowManifestHint` covering `inputs`, mapped-wins, fail-closed, and that no `model_profile` is needed when fully mapped.
- Test: `adapter/mcp/authoring_guidance_test.go` (create it if absent).

- [ ] **Step 1: Write a failing test,** `TestWorkflowManifestHint_DocumentsSpawnInputs`. `WorkflowManifestHint` contains `"inputs"`, `"spawn_workflow"` and `"model_profile"`. The existing guidance-example validation test still passes.
- [ ] **Step 2: Run** `go test ./adapter/mcp -run TestWorkflowManifestHint -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the const and the doc edits.
- [ ] **Step 4: Run** `gofmt -w adapter/mcp && go test ./adapter/mcp`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `docs(spawn): explicit inputs map and narrowed preparer semantics`.

### Task 8: `@lib` vocabulary — `ParseLibPath` and `IsReservedWorkflowName`

**Files:**
- Create: `domain/workflow/library.go`
- Test: `domain/workflow/library_test.go`

**Interfaces:**
- Produces:
  ```go
  const LibraryFolder = "_lib"; const LibraryRefName = "@lib"; const LibraryPathPrefix = "@lib/"
  var ErrInvalidLibPath = errors.New("invalid library path")
  func IsReservedWorkflowName(name string) bool            // EqualFold with "_lib" or "@lib"
  func ParseLibPath(p string) (rel string, isLib bool, err error)
  func ValidLibRel(rel string) error                       // the remainder rules alone; reused by LibraryStore
  ```
  `ValidLibRel` rejects:
  - an empty string
  - a leading `/`
  - `\` or NUL
  - any component that is `""`, `.` or `..`
  - `rel != path.Clean(rel)`
  - a first component that matches `workflow.json` or `layout.json` case-insensitively

  `ParseLibPath`: if `p == "@lib"` or `strings.HasPrefix(p, "@lib/")`, it returns `isLib=true` and runs `ValidLibRel` on the remainder, wrapping `ErrInvalidLibPath` with the reason. Otherwise it returns `("", false, nil)`.

- [ ] **Step 1: Write failing tests:**
  - **Happy path:** `TestParseLibPath_Valid`. The table is `@lib/a.md` to `a.md`, `@lib/a/b/c.sh` to `a/b/c.sh`, and `@lib/dir/.hidden` to `dir/.hidden`.
  - **Happy path:** `TestParseLibPath_NonLib`. `scripts/x.sh`, `@LIB/x`, `./@lib/x` and `lib/x` all give `isLib=false` and a nil error.
  - **Error/failure:** `TestParseLibPath_Rejected`. Each of these gives `isLib=true` and `errors.Is(err, ErrInvalidLibPath)`:
    - `@lib`, `@lib/`, `@lib/../x`, `@lib/a/../../x`, `@lib/./x`, `@lib//x`, `@lib//etc/passwd`, `@lib/a\b`, `@lib/a\x00b`, `@lib/a/`, `@lib/workflow.json`, `@lib/Layout.JSON`
  - **Boundary:** `TestParseLibPath_RejectsBeforeClean`. `@lib/../x` is rejected even though `path.Clean` would give `x`.
  - **Boundary:** `TestIsReservedWorkflowName`. `_lib`, `_LIB`, `_Lib`, `@lib` and `@LIB` are true. `lib`, `_lib2`, `my_lib` and `` are false.
- [ ] **Step 2: Run** `go test ./domain/workflow -run 'TestParseLibPath|TestIsReservedWorkflowName' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `library.go`.
- [ ] **Step 4: Run** `gofmt -w domain/workflow && go test ./domain/workflow`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(workflow): @lib path parser and reserved library name`.

### Task 9: `FileReaderAdapter` resolves `@lib/` paths

**Files:**
- Modify: `adapter/filesystem/file_reader.go` (`withExisting` branches on `workflow.ParseLibPath`; new `withExistingLib`)
- Test: `adapter/filesystem/file_reader_lib_test.go`
- Test: `adapter/process/executor_script_lib_test.go` (integration)

**Interfaces:**
- Consumes: `workflow.ParseLibPath`, `workflow.LibraryFolder`, `workflow.ErrInvalidLibPath` (Task 8).
- Produces: `ReadFile`, `ResolvePath` and `FileExists` resolve `@lib/` paths per spec §4.2. `ResolvePath` returns the EvalSymlinks real path. A symlinked `_lib` root, an escaping link or a dangling link gives an error wrapping `workflow.ErrInvalidLibPath`, with no fallthrough.

- [ ] **Step 1: Write failing tests.** Use a `t.TempDir()` base with `users/<owner>/_lib` and `shared/_lib`, and `os.Symlink` for the link cases.

  **Happy path**
  - `TestFileReaderLib_PrivateWins`
  - `TestFileReaderLib_FallsThroughToShared`
  - `TestFileReaderLib_NilOwnerSharedOnly`
  - `TestFileReaderLib_ResolvePathIsRealPathInsideLib`
  - `TestFileReaderLib_FileExistsSharedOnly`

  **Edge**
  - `TestFileReaderLib_MissingPrivateLibDirFallsThrough`
  - `TestFileReaderLib_WorkflowNameIrrelevant`: the same bytes come back for `"a"`, `"b"` and `"does-not-exist"`.
  - `TestFileReaderLib_PrivateOnlyWithNilOwnerNotFound`, giving `errors.Is(err, os.ErrNotExist)`.
  - `TestFileReaderLib_SymlinkInsideLibAllowed`. In `shared/_lib`, `alias.md` points to `real/target.md`. Reading `@lib/alias.md` returns the target's bytes. This test is required by the orchestrator.

  **Error/failure** (each gives `errors.Is(err, workflow.ErrInvalidLibPath)`)
  - `TestFileReaderLib_MalformedPathsRejected`: a table over the Task 8 rejected set.
  - `TestFileReaderLib_EscapingSymlinkErrorsNoFallthrough`. Private `_lib/x.md` points to `../../../shared/wf/secret` while shared `_lib/x.md` also exists. The result is an error, not the shared bytes.
  - `TestFileReaderLib_AbsoluteSymlinkOutsideErrors`: the link points to `/etc/hosts`.
  - `TestFileReaderLib_SymlinkedLibRootErrors`: `users/<o>/_lib` is a symlink to another directory.
  - `TestFileReaderLib_DanglingSymlinkErrors`.

  **Boundary**
  - `TestFileReaderLib_DeepNesting`: `@lib/a/b/c/d.md`.
  - `TestFileReaderLib_CaseVariantPrefixIsWorkflowPath`. `@LIB/x` resolves inside the workflow folder, not the lib.
  - `TestFileReaderLib_NonLibPathsUnchanged`. A regression check: an existing workflow-relative read still works and `../other-wf/x` is still rejected.

  **Integration** (`adapter/process/executor_script_lib_test.go`)
  - `TestScriptExecutor_RunsLibScript`. `shared/_lib/echo.sh` prints `{"cwd":"'$(pwd)'"}`, and `scriptFile: "@lib/echo.sh"` runs it. The output `cwd` equals EvalSymlinks(`shared/_lib`).
- [ ] **Step 2: Run** `go test ./adapter/filesystem -run TestFileReaderLib -v && go test ./adapter/process -run TestScriptExecutor_RunsLibScript -v`. Expected: FAIL.
- [ ] **Step 3: Implement `withExistingLib(owner uuid.UUID, rel string, read func(string) error) (string, error)`** per spec §4.2 steps 1–5.
- [ ] **Step 4: Run** `gofmt -w adapter/filesystem && go test ./adapter/filesystem ./adapter/process`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(filesystem): resolve @lib/ paths private-then-shared with symlink containment`.

### Task 10: Lib-aware includes

**Files:**
- Modify: `domain/engine/template_include.go`. `expandIncludesDepth` gains a `libBase bool` parameter. `loadTemplate` sets `libBase` from `workflow.ParseLibPath(templateFile)`.
- Test: `domain/engine/template_include_lib_test.go`

**Interfaces:**
- Consumes: `workflow.ParseLibPath` (Task 8). The existing `FileReader` port is unchanged.
- Produces: the include semantics in spec §4.3. The cycle key is `"@lib/"+rel` for lib files and `path.Clean(p)` otherwise.

- [ ] **Step 1: Write failing tests.** Use the existing `mockFileReader`, keyed by the literal relativePath passed, so it records `@lib/...` keys.

  **Happy path**
  - `TestIncludeLib_WorkflowTemplateIncludesLib`
  - `TestIncludeLib_LibFileRelativeIncludeStaysInLib`: the reader is asked for `@lib/part.md`.
  - `TestIncludeLib_LibTemplateFileExpandsLibRelative`: `loadTemplate(..., "@lib/x.md")`.

  **Edge**
  - `TestIncludeLib_LibRelativeBeatsSameNamedWorkflowFile`
  - `TestIncludeLib_ExplicitLibPrefixInsideLibFile`
  - `TestIncludeLib_SameNameInBothBasesBothExpand`: no cycle error.

  **Error/failure**
  - `TestIncludeLib_LibCannotReachWorkflowFile`. The name exists only workflow-side, giving `errors.Is(err, ErrTemplateInclude)`.
  - `TestIncludeLib_CycleAcrossLibFilesDetected`. The message contains `cycle` and `@lib/a.md`.
  - `TestIncludeLib_MalformedLibIncludeFails`: `{{include:@lib/../x}}`.

  **Boundary**
  - `TestIncludeLib_BudgetsSharedAcrossBases`: `maxIncludeCount` (64) includes split across both bases. The 64th succeeds and the 65th fails.

  **Integration**
  - `TestIncludeLib_AgentPromptContainsExpandedLib`. A fake agent provider receives a prompt containing the lib text. Use the `executor_agent_test.go` fixture pattern.
  - `TestInteractive_OrchestrationFileFromLib`. In `executor_interactive_test.go`'s fixture, a node with `orchestrationFile: "@lib/orch.md"` reads that exact path from the mock reader, and its text reaches the session.
  - `TestGroupResolverPrompt_FromLib`. `LoadGroupResolverPrompt` with `resolverPromptFile: "@lib/resolve.md"` reads that path and expands its lib-relative includes.
  - **Regression:** the existing `template_include_test.go` and `template_include_samples_test.go` pass.
- [ ] **Step 2: Run** `go test ./domain/engine -run 'TestIncludeLib|TestExpandIncludes|TestTemplateInclude' -v`. Expected: the new tests FAIL.
- [ ] **Step 3: Implement.** For each directive: if `ParseLibPath(incPath)` gives `isLib`, read the path as written, and the child base is lib. Else if `libBase`, read `"@lib/"+incPath` (re-parsed for validation), and the child base is lib. Else read the path as written, and the child base is the workflow.
- [ ] **Step 4: Run** `gofmt -w domain/engine && go test ./domain/engine`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(engine): lib-aware template includes`.

### Task 11: Validation of `@lib` references

**Files:**
- Modify: `domain/workflow/validation.go` (`validateFileReferences`)
- Modify: `domain/workflow/validation_group.go` (the `resolverPromptFile` check)
- Modify: `domain/workflow/service_group.go` (`importFileSet.FileExists` treats a valid `isLib` path as present)
- Modify: `domain/samples/seeder.go` (`seedFiles.FileExists` gets the same treatment)
- Test: `domain/workflow/validation_lib_refs_test.go`; `adapter/http/workflow_handler_test.go` (one integration case)

**Interfaces:**
- Consumes: `ParseLibPath` (Task 8). The adapter-backed checkers resolve lib paths through Task 9.
- Produces: code `INVALID_LIB_PATH`, with message `"node <id> references invalid library path <field>: <p> (<reason>)"`.

- [ ] **Step 1: Write failing tests:**
  - **Happy path:** `TestValidateFileRefs_LibPathPresent_NoFinding`, using a checker that answers for `@lib/x.md`.
  - **Error/failure:** `TestValidateFileRefs_LibPathMissing_MissingFile`.
  - **Error/failure:** `TestValidateFileRefs_MalformedLibPath_InvalidLibPath`, once for each of `templateFile`, `scriptFile` and `orchestrationFile`. The result is `INVALID_LIB_PATH`, not `MISSING_FILE`.
  - **Error/failure:** `TestValidateGroup_ResolverPromptMalformedLibPath`.
  - **Edge:** `TestImportFileSet_LibPathTreatedAsPresent`.
  - **Edge:** `TestSeedFiles_LibPathTreatedAsPresent` (in `domain/samples`).
  - **Boundary:** `TestValidateFileRefs_CaseVariantPrefixIsWorkflowFile`. `@LIB/x` is checked as an ordinary workflow file.
  - **Integration:** `TestValidateEndpoint_LibRefResolvesThroughFileReader`. REST `POST /workflows/{name}/validate` against a real `FileReaderAdapter` with `shared/_lib/x.md` reports no `MISSING_FILE`.
  - **Integration:** `TestIsGroupValidationCode_FalseForInvalidLibPath`.
- [ ] **Step 2: Run** `go test ./domain/workflow ./domain/samples ./adapter/http -run 'LibPath|LibRef|ImportFileSet_Lib|SeedFiles_Lib|InvalidLibPath' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `gofmt -w domain adapter/http && go test ./domain/workflow ./domain/samples ./adapter/http`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(validation): validate @lib file references`.

### Task 12: Reserved `_lib` name — repository, service and every folder enumerator

The enumerators below were found with `rg -n 'os\.ReadDir\(' --type go adapter domain cmd` and filtered to scope-root and workspace-root workflow folder scans. Re-run that `rg` before starting, and add any new scope-root enumerator to this task.

| Enumerator | File | Change |
|---|---|---|
| `WorkflowRepo.List` (feeds `Service.List`, `VisibleNames`, `BackfillRegistry`) | `adapter/filesystem/workflow_repo.go` (List, ~L135) | skip `workflow.IsReservedWorkflowName(entry.Name())` |
| `WorkflowRepo.LoadManifests` (feeds `adapter/provider/path_inventory.go`) | `adapter/filesystem/workflow_repo.go` (~L236) | the same skip |
| `migrateFlatStore` | `cmd/arbitron/flatstore_migrate.go` (`reservedRoots`) | add `"_lib": true` |
| `SampleSource.List` | `adapter/filesystem/sample_source.go` | the same skip (Task 16 relies on it) |
| `WorkflowRepo.ListScopes` | `adapter/filesystem/workflow_repo.go` (~L182) | **no change**. It enumerates `users/<uuid>`, not workflow folders. Add a test proving a `shared/_lib` does not add a scope. |

**Files:**
- Modify: `adapter/filesystem/workflow_repo.go`:
  - `resolveWorkflowDir` returns `ErrInvalidName` for reserved names. It is the single choke point for Load, Exists, Create, Update, Delete, WriteFile, DeleteFile, ListFiles and lockKey.
  - List and LoadManifests skip reserved names.
- Modify: `adapter/filesystem/file_reader.go`. `safeJoin` rejects a reserved `workflowName` for **non-lib** paths.
- Modify: `adapter/filesystem/workflow_repo.go` `writeFile`. Reject with `ErrInvalidFilePath` when `workflow.ParseLibPath(filepath.ToSlash(filepath.Clean(relativePath)))` reports `isLib`, or when the raw path does.
- Modify: `domain/workflow/service.go` (`Create`, `Publish` publishedName, `Copy` newPrivateName), `domain/workflow/rename.go` (newName), `domain/workflow/admin_ops.go` (`Duplicate` newName), and `domain/workflow/revision_stamper.go` (`validateStampDestination`).
  - Each one starts with `if IsReservedWorkflowName(n) { return fmt.Errorf("%w: %q is reserved for the shared library (use @lib)", ErrInvalidName, n) }`. `Publish`/`Copy` return the same error.
- Modify: `cmd/arbitron/flatstore_migrate.go`, `adapter/filesystem/sample_source.go`.
- Tests:
  - `adapter/filesystem/workflow_repo_reserved_test.go`
  - `domain/workflow/reserved_name_test.go`
  - `cmd/arbitron/flatstore_migrate_test.go` (one case)
  - `adapter/filesystem/sample_source_test.go` (one case)
  - `adapter/filesystem/workflow_repo_scopes_test.go` (one case)
  - `adapter/filesystem/workflow_repo_manifests_test.go` (one case)

**Interfaces:**
- Consumes: `IsReservedWorkflowName`, `ParseLibPath` (Task 8).
- Produces: the guarantee that no workflow-repository method touches `_lib`. Task 13's `LibraryStore` therefore must not call `resolveWorkflowDir`.

- [ ] **Step 1: Write failing tests:**

  **Happy path**
  - `TestWorkflowRepo_ListHidesLibEvenWithManifest`: plant `shared/_lib/workflow.json` with a valid manifest; List excludes it.
  - `TestWorkflowRepo_LoadManifestsSkipsLib`.

  **Error/failure**
  - `TestWorkflowRepo_ReservedNameRejectedEverywhere`. A table over Load (gives ErrNotFound), Exists, Create, Update, Delete, WriteFile, DeleteFile and ListFiles with `_lib` and `_LIB` (gives ErrInvalidName).
  - `TestService_ReservedNameRejected`. A table over `Create("_lib")`, `Create("_Lib")`, `Create("@lib")`, `Rename("a","_lib")`, `Publish("a","_lib",...)`, `Copy("s","_lib")` and `Duplicate("a","_lib")`. Each gives `errors.Is(err, ErrInvalidName)` and a message containing `use @lib`. The repo is never written; assert this with the fake repo's call log.
  - `TestRevisionStamper_ReservedDestinationRejected`.
  - `TestWorkflowRepo_WriteFileRejectsLibPrefixedPath`. Each of `@lib/x`, `./@lib/x` and `a/../@lib/x` gives `ErrInvalidFilePath` (Review Focus 1).
  - `TestFileReader_ReservedWorkflowNameRejectedForNonLibPath`.

  **Edge**
  - `TestFlatStoreMigrate_LeavesLibFolder`: a top-level `_lib/` holding a `workflow.json` is not moved.
  - `TestSampleSourceList_SkipsLib`.

  **Boundary**
  - `TestWorkflowRepo_NearMissNamesAllowed`: `lib`, `_lib2` and `my_lib` can be created.
  - `TestListScopes_SharedLibAddsNoScope`.

  **Integration**
  - `TestEngineTrigger_ReservedNameNotFound`. Triggering `_lib` through the engine trigger path fails with a not-found error. Use the `engine_test.go` fixture.
- [ ] **Step 2: Run** `go test ./adapter/filesystem ./domain/workflow ./domain/engine ./cmd/arbitron -run 'Reserved|LibFolder|SkipsLib|NearMiss|SharedLibAddsNoScope|LibPrefixedPath' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the changes in the table and the file list.
- [ ] **Step 4: Run** `gofmt -w adapter cmd domain && go test ./adapter/filesystem ./domain/... ./cmd/arbitron`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(workflow): reserve _lib; hide it from every workflow enumerator`.

### Task 13: `LibraryStore` port and filesystem implementation

**Files:**
- Create: `domain/workflow/library_store.go` (the port)
- Create: `adapter/filesystem/library_store.go`
- Test: `adapter/filesystem/library_store_test.go`

**Interfaces:**
- Consumes: `ValidLibRel`, `LibraryFolder` (Task 8), and the existing `durableFiles.write` (atomic temp+rename) and `durableMkdir` helpers in `adapter/filesystem`.
- Produces:
  ```go
  // domain/workflow
  type LibraryStore interface {
      ListFiles(ctx context.Context, scope Scope) ([]string, error)
      ReadFile(ctx context.Context, scope Scope, rel string) ([]byte, error)
      WriteFile(ctx context.Context, scope Scope, rel string, content []byte) error
      DeleteFile(ctx context.Context, scope Scope, rel string) error
  }
  // adapter/filesystem
  func NewLibraryStore(basePath string) *LibraryStore
  ```
  - Errors are `ErrInvalidLibPath` (from `ValidLibRel` or a link escape) and `ErrFileNotFound` (a missing read or delete target).
  - `ListFiles` returns a sorted, slash-separated list, or an empty slice when `_lib` is absent. It skips internal-artifact directories and symlinks that escape.
  - `ReadFile` uses the same root and link rules as Task 9. Share one unexported helper, `libRealPath(libDir, rel string) (string, error)`, between `file_reader.go` and `library_store.go`.

- [ ] **Step 1: Write failing tests:**

  **Happy path**
  - `TestLibraryStore_WriteReadListDelete_Shared`
  - `TestLibraryStore_WriteReadListDelete_Private`

  **Edge**
  - `TestLibraryStore_FirstWriteCreatesLibWithPartitionPerms`: the private `users/<o>` mode is `0700`.
  - `TestLibraryStore_ListEmptyWhenAbsent`, returning a non-nil empty slice.
  - `TestLibraryStore_ListSkipsEscapingSymlink`

  **Error/failure**
  - `TestLibraryStore_InvalidRelRejected`: a table over the Task 8 set.
  - `TestLibraryStore_DeleteMissing_ErrFileNotFound`
  - `TestLibraryStore_ReadEscapingSymlinkErrors`

  **Boundary**
  - `TestLibraryStore_NestedDirsCreated`: `a/b/c.sh`.
  - `TestLibraryStore_ReservedFirstComponentRejected`: `workflow.json` and `layout.json`.

  **Integration**
  - `TestLibraryStore_ConcurrentRewriteNeverTorn` (Review Focus 3). One goroutine alternates writing a 1 MiB file of all `A` and all `B` for 200 iterations. Another reads through `FileReaderAdapter.ReadFile(nil, "wf", "@lib/x")` in a loop. Every read is entirely `A` or entirely `B`.
- [ ] **Step 2: Run** `go test ./adapter/filesystem -run TestLibraryStore -v -race`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `gofmt -w adapter/filesystem domain/workflow && go test -race ./adapter/filesystem`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(filesystem): LibraryStore for per-scope _lib files`.

### Task 14: `Service` library methods, audit action and wiring

**Files:**
- Create: `domain/workflow/library_service.go`
- Modify: `domain/workflow/service.go` (a `library LibraryStore` field and `SetLibraryStore(LibraryStore)`)
- Modify: `domain/audit/audit.go` (`ActionLibEditShared = "lib.edit_shared"`)
- Modify: `cmd/arbitron/main.go`. Construct `filesystem.NewLibraryStore(cfg.Workspace.Path)` once, then call `workflowSvc.SetLibraryStore(lib)`. Task 16 also passes it to the seeder.
- Test: `domain/workflow/library_service_test.go`; `cmd/arbitron/library_wiring_test.go`

**Interfaces:**
- Consumes: `LibraryStore` (Task 13), `identity.Identity.IsAdmin`, `audit.Service.Record`, `caller(ctx)`.
- Produces:
  ```go
  type LibraryListing struct { Private []string `json:"private"`; Shared []string `json:"shared"` }
  func (s *Service) LibraryList(ctx context.Context) (LibraryListing, error)
  func (s *Service) LibraryRead(ctx context.Context, scope *Scope, rel string) ([]byte, error) // nil => effective
  func (s *Service) LibraryWrite(ctx context.Context, scope Scope, rel string, content []byte) error
  func (s *Service) LibraryDelete(ctx context.Context, scope Scope, rel string) error
  var ErrLibraryUnavailable = errors.New("shared library store is not configured")
  ```
  - Private scopes are always the caller's own: `Private(id.UserID)`. A `Private(other)` scope from a caller who is not that user gives `ErrUnauthorized`.
  - A `Shared()` write or delete requires `IsAdmin()`, else `ErrUnauthorized`. On success it records `ActionLibEditShared` with detail `{"path":rel,"op":"write"|"delete"}` against the name `"@lib"`.
  - Library methods do not call `s.mutate`.

- [ ] **Step 1: Write failing tests:**

  **Happy path**
  - `TestLibraryService_PrivateWriteThenEffectiveRead`
  - `TestLibraryService_AdminSharedWriteAudited`. The fake audit records `lib.edit_shared` with detail `{path, op:"write"}`.
  - `TestLibraryService_ListBothScopes`

  **Edge**
  - `TestLibraryService_EffectivePrefersPrivate`
  - `TestLibraryService_ExactSharedIgnoresPrivateShadow`
  - `TestLibraryService_DeletePrivateReexposesShared`

  **Error/failure**
  - `TestLibraryService_NonAdminSharedWriteUnauthorized`, with no audit event.
  - `TestLibraryService_NonAdminSharedDeleteUnauthorized`
  - `TestLibraryService_NoIdentityUnauthenticated`
  - `TestLibraryService_PrivateScopeOfAnotherUserUnauthorized`
  - `TestLibraryService_NoStoreConfigured`, giving `ErrLibraryUnavailable`.

  **Boundary**
  - `TestLibraryService_DoesNotTakeExecutionGate`. A fake gate that would block forever is never entered.

  **Integration**
  - `TestLibraryWiring_ServiceReachesFilesystemLib` in `cmd/arbitron`. The composition helper that builds the service has a library store wired, and a write lands at `<workspace>/shared/_lib/x`.
- [ ] **Step 2: Run** `go test ./domain/workflow -run TestLibraryService -v && go test ./cmd/arbitron -run TestLibraryWiring -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `gofmt -w domain cmd && go test ./domain/... ./cmd/arbitron`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(workflow): library service methods with admin-gated, audited shared writes`.

### Task 15: MCP `@lib` routing and `scope` param

**Files:**
- Modify: `adapter/mcp/tools_workflow.go`.
  - Add `gomcp.WithString("scope", gomcp.Description("Only with name \"@lib\": \"private\" (default) or \"shared\" (admin)"))` to the four `workflow_*_file` tools, and mention `@lib` in each tool's description.
  - Each handler begins with: if `name == "@lib"`, route to `libraryFileHandler`. Else if `workflow.IsReservedWorkflowName(name)`, return the tool error `"\"<name>\" is reserved; use @lib"`. Else if `scope != ""`, return the tool error `"scope is only valid with name @lib"`.
- Create: `adapter/mcp/tools_library.go` (the four `@lib` branches)
- Test: `adapter/mcp/tools_library_test.go`; `adapter/http/workflow_handler_test.go` (REST unchanged)

**Interfaces:**
- Consumes: the `Service.Library*` methods (Task 14).
- Produces the tool contract:
  - list returns `{"private":[...],"shared":[...]}`
  - read with no scope is the effective read
  - write and delete default to private
  - error texts:
    - `ErrUnauthorized` gives `"the shared library requires admin"`
    - `ErrInvalidLibPath` and `ErrInvalidFilePath` give `"invalid library path: <p>"`
    - `ErrFileNotFound` gives `"not found: <p>"`
    - an oversize write gives the existing size message
  - write success returns `{"status":"written","name":"@lib","scope":...,"path":...}`, and delete returns `{"status":"deleted",...}`

- [ ] **Step 1: Write failing tests** (the MCP handler test pattern in `tools_workflow_test.go`):

  **Happy path**
  - `TestLibTool_WriteDefaultsPrivate`
  - `TestLibTool_AdminWritesShared`
  - `TestLibTool_ListReturnsBothScopes`, checking the exact JSON shape.
  - `TestLibTool_ReadEffective`

  **Edge**
  - `TestLibTool_ReadExactSharedScope`
  - `TestLibTool_DeleteThenEffectiveFallsBack`

  **Error/failure**
  - `TestLibTool_NonAdminSharedWriteRefused`, with the message `the shared library requires admin`.
  - `TestLibTool_ScopeWithOtherNameRefused`
  - `TestLibTool_ReservedSpellingsRefused` (Review Focus 4). `_lib`, `_LIB`, `@LIB` and `_Lib` each give a message containing `use @lib`.
  - `TestLibTool_InvalidPathRefused`: `../x` and `workflow.json`.
  - `TestLibTool_UnknownScopeValueRefused`: `scope=public`.

  **Boundary**
  - `TestLibTool_WriteSizeLimit`: 10 MiB is accepted and 10 MiB+1 is refused.
  - `TestLibTool_LocalModeIdentityCanWriteShared` (Review Focus 5). The local-mode default identity, as built by the MCP server's local auth path, succeeds with `scope=shared`. A `RoleUser` identity is refused.

  **Integration**
  - `TestLibTool_SharedWriteVisibleToAnotherUsersRunInclude`. An admin writes `shared/_lib/x.md` through MCP. Then, for user B, `engine`'s `loadTemplate` on a template containing `{{include:@lib/x.md}}` expands it. B writes a private `x.md` through MCP, and B's expansion changes while user C's does not.
  - `TestRESTFiles_ReservedNamesUnchanged` (in `adapter/http`). `GET /workflows/_lib/files` and `GET /workflows/@lib/files` return 404, and `PUT /workflows/@lib/files/x` returns 404 or 400 with no file created.
- [ ] **Step 2: Run** `go test ./adapter/mcp -run TestLibTool -v && go test ./adapter/http -run TestRESTFiles_ReservedNamesUnchanged -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** Do not use `srv.deps.FileReader` for `@lib`; go through the service only.
- [ ] **Step 4: Run** `gofmt -w adapter && go test ./adapter/mcp ./adapter/http`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(mcp): manage the @lib library through the workflow file tools`.

### Task 16: Seeder library pass, `samples/_lib`, and packaging

**Files:**
- Create: `samples/_lib/README.md`. It explains the folder, private-then-shared precedence, the `@lib/` prefix, the lib-relative include rule, live-read semantics (a broken edit breaks running instances), and changing lib files through PRs with tests.
- Modify: `domain/samples/ports.go` (`SampleSource.ReadLibrary`)
- Modify: `domain/samples/seeder.go`:
  - `NewSeeder` is unchanged
  - add `SetLibraryStore(workflow.LibraryStore)`
  - `Seed` runs `seedLibrary(ctx, seeded)` first when a store is set
- Modify: `adapter/filesystem/sample_source.go` (`ReadLibrary`)
- Modify: `cmd/arbitron/sample_seed.go` (an extra `lib workflow.LibraryStore` parameter, then `seeder.SetLibraryStore(lib)`), and its caller in `cmd/arbitron/main.go`
- Modify: `Makefile`, the `install-samples` copy list: `samples/_lib`
- Modify: `samples/samples_test.go` (the lib folder invariants)
- Modify: `tests/install/samples.bats` (a fixture case with `_lib`)
- Test: `domain/samples/seeder_lib_test.go`, `adapter/filesystem/sample_source_test.go`, `cmd/arbitron/sample_seed_lib_test.go`

**Interfaces:**
- Consumes: `LibraryStore` (Task 13), `ValidLibRel` (Task 8), the existing `Ledger`, and `isTestFile`.
- Produces:
  - `ReadLibrary(ctx context.Context) (map[string][]byte, error)`. It returns an empty map when `<root>/_lib` is absent and excludes test files.
  - The ledger key is `"@lib/" + rel`.
  - The library pass runs before any workflow sample. The existence check is `LibraryStore.ReadFile(Shared(), rel)`, where `ErrFileNotFound` means absent.

- [ ] **Step 1: Write failing tests:**

  **Happy path**
  - `TestSeederLib_SeedsBeforeWorkflows`. A fake store and repo record a call order, and the lib write comes before the first workflow `Create`. The ledger has `@lib/README.md`.

  **Edge**
  - `TestSeederLib_SecondBootNoop`
  - `TestSeederLib_NewFileInLaterVersionSeeds`
  - `TestSeederLib_ChangedFileNotOverwritten`
  - `TestSeederLib_PreexistingSharedFileRecordedNotOverwritten`
  - `TestSampleSourceReadLibrary_ExcludesTestFiles`: `x_test.sh` is absent.

  **Error/failure**
  - `TestSeederLib_DeletedSeededFileNotRecreated`
  - `TestSeederLib_StoreWriteErrorAborts`
  - `TestSeederLib_InvalidNameSkippedNotRecorded`: a source file whose rel fails `ValidLibRel`, for example `workflow.json`.

  **Boundary**
  - `TestSeederLib_NoLibFolderSkipsSilently`
  - `TestSeederLib_NoStoreConfiguredSkipsLibPass`

  **Integration**
  - `TestSampleSeedLib_RealFilesystem` in `cmd/arbitron`. `seedSampleWorkflows` against a temp workspace and the repo's real `samples/` dir gives `<ws>/shared/_lib/README.md` with the same bytes as `samples/_lib/README.md`.
  - `TestSamplesLibFolderInvariants` in `samples/samples_test.go`. `samples/_lib` exists, has no `workflow.json`, has no symlinks (Lstat walk), and every file's rel passes `workflow.ValidLibRel`.
  - `tests/install/samples.bats`: `@test "fetch_samples_to extracts the _lib library folder"`. `_make_samples_fixture` gains a `_lib/README.md`, and the test asserts `[ -f "$destdir/_lib/README.md" ]`.
- [ ] **Step 2: Run** `go test ./domain/samples ./adapter/filesystem ./cmd/arbitron ./samples -run 'SeederLib|ReadLibrary|SampleSeedLib|SamplesLibFolder' -v && bats tests/install/samples.bats`. Expected: FAIL.
- [ ] **Step 3: Implement.** Add the README, the ports, the seeder pass, the wiring, the Makefile line and the bats fixture.
- [ ] **Step 4: Run:**
  ```bash
  gofmt -w domain adapter cmd samples && go test ./domain/samples ./adapter/filesystem ./cmd/arbitron ./samples
  bats tests/install/samples.bats
  make samples-tarball VERSION=0.0.0-test && tar -tzf dist/arbitron_samples_0.0.0-test.tar.gz | grep -q '^./_lib/README.md$'
  make test-release
  D="$(mktemp -d)" && make install-samples DEST="$D" && test -f "$D/_lib/README.md"
  ```
  Expected: all PASS. The `samples-tarball` check runs on the committed tree, so run it after the commit in Step 5. Note it in the commit if it was verified post-commit.
- [ ] **Step 5: Commit** with the message `feat(samples): ship and seed the _lib shared library`.

### Task 17: Docs for `@lib`

**Files:**
- Modify: `AGENTS.md`. Add a new `## Shared library (@lib)` section covering:
  - the two roots and their precedence, keyed by the run owner
  - `@lib/` in templateFile, scriptFile, orchestrationFile, resolverPromptFile and includes
  - the lib-relative include rule
  - the case-sensitive prefix and the reserved `_lib`/`@lib` names
  - live-read and the "broken lib breaks running instances" caveat, so lib changes need tests and PRs
  - MCP usage (`name: "@lib"`, `scope`, admin for shared)
  - the publish gotcha: put lib files in shared before publishing a workflow that uses them
  - that private lib files shadow shared ones for your own runs
- Modify: `adapter/mcp/authoring_guidance.go`. Add a `LibraryGuidance` const appended to `WorkflowManifestHint`.
- Test: `adapter/mcp/authoring_guidance_test.go`

- [ ] **Step 1: Write a failing test,** `TestWorkflowManifestHint_DocumentsLibrary`. The hint contains `@lib/` and `workflow_write_file`.
- [ ] **Step 2: Run** `go test ./adapter/mcp -run TestWorkflowManifestHint_DocumentsLibrary -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the const and the AGENTS.md section.
- [ ] **Step 4: Run** `gofmt -w adapter/mcp && go test ./adapter/mcp`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `docs(lib): document the @lib shared library`.

### Task 18: Full gates

- [ ] **Step 1:** Run `test -z "$(gofmt -l domain adapter cmd samples)"`. Expected: exit 0.
- [ ] **Step 2:** Run `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 3:** Run `cd dashboard && npx tsc --noEmit && npx vitest run && npm run build`, then `rm -rf adapter/dashboard/dist && cp -r dashboard/dist adapter/dashboard/dist && git status --short adapter/dashboard/dist`. Expected: PASS, and the dist is unchanged from Task 6 (if it changed, commit the refresh).
- [ ] **Step 4:** Run `make check-ui-fresh`. Expected: PASS.
- [ ] **Step 5:** Run `make test-install && make test-release` (bats + shellcheck). Expected: PASS.
- [ ] **Step 6:** Run `rg -n 'TODO|FIXME' $(git diff --name-only main...HEAD -- '*.go' '*.ts' '*.tsx')`. Expected: no new placeholders.
- [ ] **Step 7: Commit** any gate fixes with the message `chore: fp-next engine prerequisites gates green`. If nothing changed, skip the commit and record "gates green, no changes" in the task report.
