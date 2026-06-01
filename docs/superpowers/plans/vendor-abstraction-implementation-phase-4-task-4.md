# Phase 4, Task 4: Acceptance Harness (Spec §8 Criteria #1–#7)

## Goal

Build the **acceptance harness** for the whole vendor-abstraction layer: one
scenario per acceptance criterion in `docs/specs/feature-pipeline-replacement-mvp.md`
§8 (#1–#7), tying Phases 1–3 together in an end-to-end run of the Task 1 worked
example. This is the named acceptance test artifact (design §8). Two assertions are
called out as load-bearing and **must** be explicit: the **configuration-diff**
assertion (#4) and the **log-capture secrecy** assertion (#7).

This task is **last** in design §7 "Sequencing" (item 4) because every scenario
depends on capabilities delivered across Phases 1–3 and on the Task 1 worked example
(and optionally Task 3's lint). The per-scenario dependency list below is what orders
it last.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-4.md` §4
(acceptance harness, the 7 mapped scenarios) and §6 (Acceptance harness bullet:
config-diff for #4, log-capture secrecy for #7); spec §8.

## Dependencies (why this task is ordered last)

- **Task 1** — the committed `native-ralph-loop` workflow folder + both alias
  bindings. The harness runs this exact folder.
- **Phase 1** — port split (`ChatProvider`/`AgentProvider`), `agent_call`,
  tool-suppressed claude-cli `ChatAdapter`, tools-enabled claude-cli `AgentAdapter`,
  the profile registry + `StaticProfileResolver`, capability validation (#6 surface).
- **Phase 2** — connection store (AES-GCM at rest), widened `Profile.ConnectionRef`,
  the resolution chain → `ResolvedBinding` with lazy `RevealKey`, the connection-
  existence checker in validation, the secrecy/log-redaction plumbing.
- **Phase 3** — native `ChatProvider` adapters (Azure AI Foundry, OpenRouter), the
  **opencode** `AgentProvider`, and the **env-injection** path that puts the
  connection key + base-url into the opencode subprocess (`opencodeEnv` helper).
- **Task 3** (optional) — only if the lint scenario is included.

Each scenario below names the specific Phase 1–3 capabilities it exercises.

## Background (current state, grounded in real code)

- E2E/integration tests in this repo live alongside the package they exercise (e.g.
  `adapter/http/interactive_integration_test.go`, `adapter/sqlite/integration_test.go`)
  and drive the real `Engine` over in-memory or sqlite repositories. The harness
  follows that established style — a dedicated acceptance test package (e.g.
  `test/acceptance/` or `cmd/arbitron/acceptance_test.go`) that wires the real engine
  + executors + resolver + (faked or real) providers and triggers the worked-example
  workflow.
- The engine walk, counter persistence, and trace surface are as cited in Task 2
  (`engine.go:308`, `:521`, `:344`; `VariableSnapshot` per pass). Acceptance #5 reuses
  those assertions end-to-end.
- Real external calls (a live Claude subscription, real Azure/OpenRouter endpoints,
  real opencode) are **not** run in CI. Scenarios that would hit a vendor use the
  **fake `CommandRunner`** (claude-cli/opencode) and **fake HTTP transports**
  (Azure/OpenRouter) established in Phases 1–3, asserting the *delivery* (args,
  injected env, resolved endpoint) rather than live model output. Where a true
  live-run is required for sign-off (#1 local Claude), mark it as a manually-run /
  build-tagged scenario, not a CI gate, and document the manual run in the PR.

## Files to Create

### Create: acceptance harness test package (e.g. `test/acceptance/harness_test.go`)

A shared setup that, given a **binding** (Claude or open-weight profile/connection
set), wires: connection store (with a sentinel-keyed connection for the open-weight
binding), `StaticProfileResolver`, the chat/agent adapters (real adapters over **fake
runners/transports**), the executors, validation with both the file checker and the
connection/profile checker, and the engine over in-memory repos with a
**buffer-backed `*slog.Logger`** installed everywhere (for the #7 log-capture
assertion). Provide a `runWorkedExample(t, binding) → (instance, trace, logBuf)`
helper reused by the scenarios.

## Scenarios — one per spec §8 criterion (strict TDD: each is a category-spanning E2E test)

### #1 — Local Claude run, no API keys
- **Requires:** Phase 1 tool-suppressed claude-cli `ChatAdapter` (`assessor`) +
  tools-enabled `AgentAdapter` (`editor`); the no-connection / ambient-login path
  (Phase 2 empty `ConnectionRef` → no key).
- **Scenario:** run the worked example under the **Claude binding**. Assert the
  pipeline runs end-to-end to completion; `editor` invoked `claude -p
  --dangerously-skip-permissions` (tools) and `assessor` invoked tool-suppressed
  (`--allowedTools ""`, no skip-permissions) — asserted via the fake `CommandRunner`'s
  recorded args. Assert **zero** connections were required and **no** key was read.
  (Live sign-off variant: build-tagged manual run against a real subscription.)

### #2 — All three chat providers functional
- **Requires:** Phase 1 claude-cli chat; Phase 3 Azure AI Foundry + OpenRouter native
  `ChatProvider`; Phase 2 resolution + `RevealKey`.
- **Scenario:** run three times, flipping only the `assessor` profile across
  Claude / Azure / OpenRouter (single profile-reference change each time — **no graph
  edit**). Assert each run drives the correct adapter (claude-cli chat vs. Azure HTTP
  vs. OpenRouter HTTP, via the fake transports) and the loop completes. Assert the
  three runs differ **only** in the `assessor` profile reference.

### #3 — Both agent backends functional
- **Requires:** Phase 1 claude-cli `AgentAdapter`; Phase 3 opencode `AgentProvider`
  + env-injection; Phase 2 connection-backed open-weight binding.
- **Scenario:** run `editor` via `claude -p` (Claude binding) and via `opencode run
  --model <provider/model>` on a non-Claude/open-weight model (open-weight binding).
  Assert via the fake runner that the opencode invocation sets `cmd.Dir = {{workspace}}`,
  threads `--model provider/model`, and that the env-injection path appended the
  provider-keyed key + base-url vars (delivery half of #7; full secrecy below).

### #4 — Configuration interchangeability (EXPLICIT config-diff assertion)
- **Requires:** the same Task 1 `workflow.json` + both bindings; Phase 1 alias
  indirection; Phase 2 resolution.
- **Scenario:** run the worked example under the Claude binding and the open-weight
  binding. **Explicit diff assertion:** load the workflow definition used in each run
  (the instance `WorkflowSnapshot` / the on-disk `workflow.json`) and assert the two
  are **structurally identical** — same set of node IDs and `type`s, same edges
  (including the back-edge), same `templateFile` references — i.e. the JSON diff
  across the two runs, **after removing the profile/connection registry**, is empty.
  The only difference between configs is the external profile→connection binding, not
  anything in the graph. Both runs reach the same loop structure and complete. This is
  the load-bearing #4 assertion.

### #5 — Native loop (counter persisted + in trace)
- **Requires:** existing engine cyclic-edge behavior (Task 2); no Phase 2/3 specifics.
- **Scenario:** run the worked example with a scripted `assessor` that flips `done`
  after N passes (or relies on `max_iterations`). Assert the loop iterates multiple
  passes and exits on its `conditional`; assert `instance.Variables["iteration"]`
  holds the final count **and** the `VariableSnapshot` trace shows the counter
  progressing across passes. This re-asserts Task 2's engine-level proof at the
  end-to-end level (acceptance #5).

### #6 — Capability validation (negative test)
- **Requires:** Phase 1 capability validation (`PROFILE_KIND_MISMATCH`); Task 1's
  negative binding variant.
- **Scenario:** bind `editor` (an `agent_call`) to a **chat-only** profile and run
  `workflow_validate`. Assert it **fails** with a clear `PROFILE_KIND_MISMATCH` error
  on the `editor` node **before any execution** (no instance is created / no node
  runs). The positive control (both nodes correctly bound) validates with zero errors.

### #7 — Connection safety & delivery (EXPLICIT log-capture secrecy + multi-region)
- **Requires:** Phase 2 AES-GCM store + lazy `RevealKey` + log-redaction; Phase 3
  opencode env-injection; Azure two-connection resolution.
- **Scenario A (env-injection + secrecy):** create a connection with a **sentinel
  key** (e.g. `"sk-SENTINEL-DO-NOT-LOG"`), run `editor` via opencode under the open-
  weight binding. Assert the sentinel key is present in the **child process env**
  (recorded by the fake runner) but:
  - **EXPLICIT log-capture assertion:** the buffer-backed log output captured across
    the entire resolve-and-run sequence contains **no** substring of the sentinel key;
    log lines referencing the connection use its **name** only.
  - the serialized `workflow.json` / instance `WorkflowSnapshot` contains **no** key
    and **no** connection secret (nodes hold only the `model_profile` alias).
- **Scenario B (multi-region):** define **two Azure connections** with different
  endpoints + keys (e.g. two regions / two serverless deployments). Bind two profiles,
  one per connection, used by two nodes (or two runs). Assert each node resolves to the
  **correct** endpoint + key (right `base-url`, right `RevealKey` value) — no
  cross-contamination. Combined with Scenario A, this is the full #7 proof.

### (Optional) Interactive-in-loop lint scenario
- Only if **Task 3** is built: a variant graph placing an `interactive` node on the
  loop emits the `INTERACTIVE_IN_LOOP` **advisory** yet still **passes**
  `workflow_validate` (advisory, non-gating). Omit this scenario if Task 3 is descoped.

## Strict-TDD coverage note

The harness **is** the test artifact (design §6). Across the seven scenarios the five
categories are covered: happy/success (#1/#2/#3/#5 completing runs), failure/error
(#6 validation failure; #7 a missing-connection / wrong-key negative within Scenario
B), and edge (#4 same-graph-different-binding boundary; #7 sentinel-in-env-but-not-in-
logs boundary). Write each scenario test-first against the wired harness.

## Constraints

- No new engine primitive; the harness only **exercises** existing capabilities.
- Strict hexagonal: real adapters over fake runners/transports; the worked example is
  data (Task 1). No live external calls in CI; live #1 sign-off is build-tagged/manual.
- The #4 config-diff and #7 log-capture-secrecy assertions are **mandatory and
  explicit**, per design §6.

## Acceptance Criteria

- Seven scenarios, one per spec §8 criterion #1–#7, run against the Task 1 worked
  example and pass.
- #4 includes an explicit structural-diff assertion proving the **same** graph runs
  under both bindings (empty diff after removing the profile/connection registry).
- #7 includes an explicit log-capture assertion proving the sentinel key never appears
  in any log (name-only references) and never in `workflow.json`, plus the multi-region
  two-Azure-connection correct-endpoint resolution.
- #6 fails `workflow_validate` with `PROFILE_KIND_MISMATCH` before any run.
- Each scenario's required Phase 1–3 capabilities are documented (above), justifying
  this task's last position in the sequence.
- `go test ./test/acceptance/...` (or the chosen package) passes; existing tests still
  pass.
