# fp-next Workflow Content Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Ordering gate:** run this plan only **after** [2026-10-23-fp-next-engine-prereqs.md](2026-10-23-fp-next-engine-prereqs.md) is fully complete and green. Every workflow here relies on spawn `inputs` and on `@lib` resolution (`ParseLibPath`, lib-aware `FileReaderAdapter`, `isTestFile` lib seeding), and `TestSamplesValidate` needs the real `@lib` resolver. Deployment and live smoke tests are out of scope for this plan; the orchestrator handles them separately.

**Goal:** Build the shared `samples/_lib` script library, the `ralph-loop` changes, the six standalone stage workflows (`ideate`, `spec`, `design`, `plan`, `implement`, `harden`), the `pr-review` sample sync, and the `feature-pipeline-next` parent, all with TDD.

**Architecture:**
- Each stage is a standalone sample workflow that clones its branch, does its work, pushes, and records itself in `.arbitron/pipeline/<slug>.json` through `@lib/manifest-append.sh`. That call is the stage's `finish` node, and its output is the stage's return contract.
- The parent is an acyclic chain: intake, then detect, then `pre-<stage>` (`@lib/manifest-read.sh`), then an awaited spawn whose inputs are fully mapped.
- A deterministic `@lib/tamper-gate.sh` runs after every implement task, every ralph pass and every harden round. A judge from the other model family rules on its judgeable findings, and `@lib/tamper-verdict.sh` either voids the pass or lets it stand.

**Tech Stack:**
- POSIX `sh` with `git`, `jq`, `sed`, `awk` and `grep -E`
- `bats` and the existing `_test.sh` style for script tests
- `shellcheck`
- Go 1.25 `samples/` tests driving `workflow.Validate`, `LintWorkflow`, `engine.NewConditionalExecutor()` and `variable.ResolveTypedMap`
- JSON workflow manifests

**Spec:** [docs/superpowers/specs/2026-10-23-fp-next-workflows-design.md](../specs/2026-10-23-fp-next-workflows-design.md). It includes pre-flight rulings P1–P15, which are also carried into the affected tasks below. Read the spec section each task cites. The plan states decisions; the spec carries the full rules.

## Global Constraints

- Every lib script is `#!/bin/sh`, POSIX and shellcheck-clean.
  - It reads the snapshot JSON on stdin and writes exactly one JSON object to stdout, with diagnostics on stderr.
  - It never sources a sibling file.
- Every script node uses `"interpreter": "/bin/sh"`, an exact JSON `args` array, and an explicit `timeoutSeconds`.
- No jq-filter argv element may contain `{{` or `}}`; write nested objects with a space, as in `{a: {b: .c} }`.
- Array values never travel through argv. Scripts read them from stdin by key.
- Agents and interactive nodes only commit. Pushing happens only in `@lib/checkpoint-push.sh` and `@lib/manifest-append.sh`.
- Templates use `./work/...` paths. Scratch files live under `{{workspace}}/tamper/` and `{{workspace}}/harden/`, never inside `./work`.
- Conditionals are first-match, so deterministic evidence comes first. Routed fields come only from script outputs or input counters.
- No interactive node and no `wait_for_child` spawn may sit on a cycle. The parent is acyclic.
- Stage order: `ideate, spec, design, plan, implement, harden, refine, review`. Manifest statuses: `done | blocked`.
- Model family is the model vendor after stripping the provider prefix (spec §3.4), so `openai-codex/gpt-6.1-sol` is GPT. A judge's family must differ from its doer's.
- The tamper gate's exit code 2 means bad inputs only. A history rewrite is a non-judgeable void (`history_rewritten`). Findings are de-duplicated per `(file, line)` across kinds; the most specific kind wins and the rest go in `also_kinds` (P6). Skip and focus markers are anchored on a non-identifier left boundary (P7). Loop staleness is guarded by `pass-mark.pass_id`, never by the sha (P8).
- New script tests are named `<script>_test.bats`. Changed existing scripts extend their `_test.sh`. Fixtures are built in `setup()`, with no committed fixture trees.
- Fallback node positions: main column at `x=0` with `y += 140`; side branches at `x=±420`. No `layout.json`.
- No committed default for credential-bearing inputs (`teams_webhook_url` defaults to `""`).
- Commit message trailer: `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.

## Review Focus

1. **Branch names containing `/`** (for example `feat/age-filter`) passed as `branch` or `base_branch`. Clone, manifest-append (private worktree and push refspec) and the parent must all handle them. Tested in Task 3 and Task 6.
2. **A rerun after a crashed stage** leaves files from another day (for example stale `docs/plans/<slug>/2026-10-20-phase-1-task-1.md`). `list-plans` must return only this run's `run_date` files. Tested in Task 17.
3. **An `idea_path` whose basename is not kebab-case** (for example `docs/ideas/My_Idea.md`). The slug is sanitized to `my-idea`, `idea_path` is kept verbatim, and the branch name is a valid ref. Tested in Task 24.
4. **A worktree with unrelated uncommitted edits when a void happens.** `tamper-verdict` restores and commits only the gated paths, and the unrelated edits survive untouched. Tested in Task 9.
5. **A manifest that carries unknown extra fields**, whether on entries or at top level (written by a newer version). `manifest-read` tolerates them, `manifest-append` preserves them, and only an unknown stage or status is rejected. Tested in Task 5 and Task 6.

---

## Phase 0 — Tooling spike

### Task 1: Mutation-tooling spike and the registry

This is research, so the only product output is the registry data and its test. The spike notes go in a doc.

**Files:**
- Create: `samples/harden/scripts/mutation-tools.json`
- Create: `samples/harden/scripts/mutation-tools_test.bats`
- Create: `docs/superpowers/notes/2026-10-23-mutation-tooling-spike.md`

**Interfaces:**
- Produces: `mutation-tools.json` = `{"version":1,"tools":[{language, tool, tested_version, probe, install_hint, markers:[…], source_ext:[…], diff_scoping:{mode:"native"|"target-list", how}, report:{format, path}, default_test_command, test_command_injection}]}`.
  - `language` is one of `go`, `csharp`, `js`, `python`, `java`, `rust`.
  - For Java, `default_test_command` is the object `{"pom.xml":"mvn test","build.gradle":"gradle test","build.gradle.kts":"gradle test"}`. For every other language it is a string.
  - Tasks 20 and 21 consume this file.

- [ ] **Step 1: Install and probe each tool (P12).** Install everything you can. Try these, in order, and record the exact version or the exact failure output for each:
  - `brew install pipx maven gradle`, then `pipx install mutmut`
  - `go install github.com/go-gremlins/gremlins/cmd/gremlins@latest` (**REQUIRED**)
  - `dotnet tool install -g dotnet-stryker` (**REQUIRED**)
  - StrykerJS into a temp dir: `mkdir -p "$TMPDIR/strykerjs" && npm --prefix "$TMPDIR/strykerjs" i @stryker-mutator/core`
  - PIT through `mvn org.pitest:pitest-maven:help`
  - `cargo install cargo-mutants`

  Any failure goes in the registry as `tested_version: null`, with the reason in the notes. gremlins and dotnet-stryker are needed for the later smoke tests. If either one fails, put a **"BLOCKER for smoke tests"** heading at the very top of the notes doc, with the error output, and say so in the task report.
- [ ] **Step 2: For each tool that installed, find the real diff-scoping mechanism** on a 2-file toy repo.
  - Candidates: gremlins diff option, Stryker.NET `--since:<ref>`, StrykerJS `--mutate`, mutmut v3 path config written outside the worktree, PIT `targetClasses`, cargo-mutants `--in-diff`.
  - Also record the report format and path, and how to inject a test command.
  - If a tool failed to install, record the documented mechanism and mark `tested_version: null` with the failure in the notes.
- [ ] **Step 3: Write the failing registry test** `mutation-tools_test.bats`:
  - `@test "registry lists exactly the six languages"`: `jq -r '.tools[].language' | sort` equals `csharp go java js python rust`.
  - `@test "every entry has the required keys"`: every key named in the Interfaces block is present and non-null, except that `tested_version` may be null.
  - `@test "default test commands match the spec"`: `go` → `go test ./...`, `csharp` → `dotnet test`, `js` → `npm test`, `python` → `pytest`, `rust` → `cargo test`, `java` → the pom/gradle object above.
  - `@test "diff_scoping.mode is native or target-list"`.
- [ ] **Step 4: Run it and see it fail.** `bats samples/harden/scripts/mutation-tools_test.bats` fails because the file is missing.
- [ ] **Step 5: Write the registry and the notes doc.** The doc has one section per tool: install result, version, scoping evidence (the command plus an output excerpt), report sample, and open caveats.
- [ ] **Step 6: Run it and see it pass.** `bats samples/harden/scripts/mutation-tools_test.bats` gives 4 tests, 0 failures.
- [ ] **Step 7: Commit** with `docs(harden): mutation tooling spike and registry`.

---

## Phase 1 — Goal step 3: `samples/_lib`

### Task 2: Script-test harness, lib copies, idea skeleton

**Files:**
- Modify: `Makefile`. Add a `test-scripts` target, add `test-scripts` to `test-gate`, and change `install-samples` to copy every `samples/*/` directory, `_lib` included, with a glob: `cp -R samples/*/ $(DEST)/` (P9). Do not use an explicit list.
- Modify: `.github/workflows/layout.yml`. Add the step `sudo apt-get install -y shellcheck bats` before `make test-gate`.
- Create: `samples/_lib/checkpoint-push.sh`, `samples/_lib/notify-teams.sh`, byte-copied from `samples/feature-pipeline/scripts/`.
- Create: `samples/_lib/checkpoint-push_test.sh`, `samples/_lib/notify-teams_test.sh`, copied from the legacy tests. They are unchanged, because `SCRIPT_DIR` already resolves to their own directory.
- Create: `samples/_lib/idea-doc-skeleton.md`, `samples/_lib/idea-doc-skeleton_test.bats`.

**Interfaces:**
- Produces:
  - `make test-scripts` runs three things:
    1. `shellcheck $(SCRIPT_LINT)`. `SCRIPT_LINT` is a Makefile variable that lists every new or changed script explicitly; every later task appends to it.
    2. `bats` over `$(shell find samples -name '*_test.bats')`.
    3. `sh` on each of `$(wildcard samples/_lib/*_test.sh samples/*/scripts/*_test.sh)`. Any failure fails the target.
  - The include path `@lib/idea-doc-skeleton.md`.

- [ ] **Step 1: Write the failing skeleton test** `idea-doc-skeleton_test.bats`:
  - `@test "has the header fields"`: grep for each of `**Status:**`, `**Written:**`, `**Scope:**`, `**Explicitly out of scope:**`.
  - `@test "has every section in order"`: the `## ` lines, in order, are `1. Why this exists`, `2. What exists today (grounding)`, `3. The idea`, `4. Proposal`, `5. Design tensions and gotchas`, `6. Decisions locked`, `7. Non-goals (for now)`, `8. Open questions`, `9. Suggested sequencing`, `Appendix — related`.
  - `@test "contains no template markers"`: `! grep -q '{{\|}}'`.
- [ ] **Step 2: Run** `bats samples/_lib/idea-doc-skeleton_test.bats`. Expect FAIL (the file is missing).
- [ ] **Step 3: Write the skeleton** (spec §4.1). Give each section an italic one-line instruction. §2's instruction requires `file:line` citations.
- [ ] **Step 4: Copy the two scripts and their tests.** Verify that `cmp samples/_lib/checkpoint-push.sh samples/feature-pipeline/scripts/checkpoint-push.sh` and the same for `notify-teams` both exit 0.
- [ ] **Step 5: Add `test-scripts` to the Makefile** with `SCRIPT_LINT := samples/_lib/checkpoint-push.sh samples/_lib/notify-teams.sh`, and wire it into `test-gate`. Also wire the CI apt step.
- [ ] **Step 6: Run** `make test-scripts`. Expect shellcheck to be clean, the 3 bats tests to pass, and every existing `_test.sh` to report `all … passed`. If a pre-existing `_test.sh` fails at HEAD, fix that sample's test fixture in this task; do not exclude it.
- [ ] **Step 7: Commit** with `build(samples): make test-scripts; @lib copies and idea-doc skeleton`.

### Task 3: `@lib/init-workspace.sh`

**Files:**
- Create: `samples/_lib/init-workspace.sh`, `samples/_lib/init-workspace_test.bats`
- Modify: `Makefile` (`SCRIPT_LINT`)

**Interfaces:**
- Consumes: the snapshot keys `workspace`, `repo_url`, `branch`, `base_branch` and `slug`.
- Produces:
  - Modes: `["--checkout"]`, `["--detached-base"]`, `["--new-branch"]`, or `["--mode-var","<key>"]`, where the key's value is `checkout`, `detached-base` or `new-branch`.
  - Emits `{work_dir, src_dir, branch, base_ref, head_sha, run_date, short_id, slug, mode}`.
  - The layout is `<workspace>/src` (the clone) and `<workspace>/work` (the worktree). It clears `.manifest-wt` and installs the `[skip ci]` commit-msg hook.

- [ ] **Step 1: Write the failing bats suite.** `setup()` builds a bare `origin` with `main`, `feat/x` (Review Focus 1) and `dev`, and the tests use the `file://` URL. Name the tests by category:
  - **Happy path**
    - `checkout lands on the branch with the hook installed`: `git -C $work symbolic-ref --short HEAD` = `feat/x`, and `.git/hooks/commit-msg` is executable.
    - `new-branch names dev-<slug>-<date>-<short>`: the branch is `dev-my-feat-$(date +%Y-%m-%d)-<first 8 chars of basename workspace>`.
    - `detached-base is detached with empty branch`: `.branch==""` and `git symbolic-ref -q HEAD` fails.
    - `mode-var selects each mode`: runs all three.
  - **Edge cases**
    - `empty base_branch uses remote HEAD`
    - `slug sanitizes "My Feat!!" to my-feat`
    - `all-punctuation slug falls back to feature`
    - `rerun clears stale src, work and .manifest-wt`
  - **Errors** (each exits 1 with nothing on stdout)
    - `rejects -leading repo_url`, `rejects ext:: and http:// urls`
    - `rejects ref a..b and -x`
    - `checkout of a missing branch fails with "not found on origin"`
    - `no mode or two modes fails`
    - `mode-var with an absent key or an unknown value fails`
  - **Boundary**
    - `short_id is exactly 8 chars`
    - `single-character slug is kept`
  - **Integration**
    - `commits get [skip ci] except with --no-verify`
- [ ] **Step 2: Run** `bats samples/_lib/init-workspace_test.bats`. Expect every test to FAIL (the script is missing).
- [ ] **Step 3: Implement it.** Port the URL allowlist, the ref checks, the slug sanitizer and the hook verbatim from `samples/ralph-loop/scripts/init-workspace.sh`. The clone is `git clone --depth 1 "--branch=<ref>" -- <url> <src>`; checkout mode then runs `git -C src checkout --detach` before `worktree add`.
- [ ] **Step 4: Run** the suite and `shellcheck samples/_lib/init-workspace.sh`. Expect all tests to pass and shellcheck to be clean.
- [ ] **Step 5: Commit** with `feat(lib): init-workspace clone/init helper with --mode-var`.

### Task 4: `@lib/pass-mark.sh` and `@lib/finalize-docs.sh`

**Files:**
- Create: `samples/_lib/pass-mark.sh`, `samples/_lib/pass-mark_test.bats`, `samples/_lib/finalize-docs.sh`, `samples/_lib/finalize-docs_test.bats`
- Modify: `Makefile` (`SCRIPT_LINT`)

**Interfaces:**
- Produces:
  - `pass-mark.sh` takes `[]`, reads `work_dir` and `workspace`, and emits `{sha, pass_id}`. `pass_id` is the integer in `<workspace>/tamper/pass-id` plus 1 (0 when the file is absent), written back each run (P8).
  - `finalize-docs.sh` takes `["--message","<msg>","<path|glob>",…]`, reads `work_dir`, and emits `{paths:[sorted with a numeric-aware sort], committed:bool}`. It does not push.

- [ ] **Step 1: Write the failing tests.**
  - `pass-mark`:
    - Happy path: `emits HEAD sha`, and `pass_id increments 1,2,3 across runs`.
    - Edge case: `pass_id still increments when HEAD is unchanged between runs` (P8).
    - Edge case: `works on detached HEAD`.
    - Error: `unset work_dir exits 1` and `non-repo exits 1`.
    - Boundary: `single-commit repo`.
  - `finalize-docs`:
    - Happy path: `commits an uncommitted doc` (`committed:true`, and the file is in `git show --stat HEAD`).
    - Edge case: `already committed doc gives committed:false`.
    - Edge case: `phase-2 sorts before phase-10`.
    - Error: `no match exits 1 with "no document written"` and `empty file exits 1`.
    - Boundary: `exactly one match`.
- [ ] **Step 2: Run** both suites. Expect FAIL.
- [ ] **Step 3: Implement** both scripts. The sort is `sort -V`.
- [ ] **Step 4: Run** both suites plus shellcheck. Expect PASS.
- [ ] **Step 5: Commit** with `feat(lib): pass-mark and finalize-docs`.

### Task 5: `@lib/manifest-read.sh`

**Files:**
- Create: `samples/_lib/manifest-read.sh`, `samples/_lib/manifest-read_test.bats`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `work_dir`, `branch`, `slug`, plus any key named in `--explicit`.
- Produces:
  - Flags: `--fetch`, `--order a,b,…`, `--start-at-var NAME`, `--explicit k,…`, `--require k,…`, and `--optional k=default` (repeatable).
  - Emits `{exists, path, manifest, latest:{stage:entry}, done_stages, outputs, next_stage, inputs}`.
  - `outputs` holds the outputs of each stage's latest `done` entry, folded in stage order, plus the top-level `slug`, `base_branch` and `branch`.
  - `next_stage` is the start-at value if one is set, else the first not-done stage in `--order`, else `complete`.
  - The path is `.arbitron/pipeline/<slug>.json`.

- [ ] **Step 1: Write the failing suite.** Fixture manifests are written inline with `jq -n`.
  - **Happy path**
    - `reads the worktree file`
    - `--fetch reads origin, not a stale worktree copy`: commit a newer manifest to origin from a second clone; the output shows the newer entry.
  - **Detection**
    - `latest entry wins: blocked after done is not done`
    - `next is the first not-done stage in order`
    - `all done gives complete`
    - `no manifest gives the first stage and exists:false`
    - `start-at-var overrides detection`
  - **Errors** (each exits 1)
    - `malformed JSON`
    - `missing stages array`
    - `unknown stage`
    - `unknown status`
    - `empty slug`
    - `start_at outside --order`
    - `--require missing names the key and stage`: stderr contains `design_paths` and `design`.
  - **Edge cases**
    - `--explicit non-empty wins`
    - `explicit "" and [] fall through to the manifest`
    - `--optional typed defaults`: `k=[]` gives an array, `k=false` gives a bool, `k=` gives `""`.
    - `later stage key wins on collision`
    - `unknown extra fields are tolerated` (Review Focus 5): entry and top-level extras pass, and `manifest` echoes them.
  - **Boundary**
    - `zero stages`
    - `empty branch gives exists:false`
- [ ] **Step 2: Run** the suite. Expect FAIL.
- [ ] **Step 3: Implement it.** Do the folding and the input view in a single `jq` program that receives the arguments through `--arg` and `--argjson`.
- [ ] **Step 4: Run** the suite plus shellcheck. Expect PASS.
- [ ] **Step 5: Commit** with `feat(lib): manifest-read with detection and stage-input view`.

### Task 6: `@lib/manifest-append.sh`

**Files:**
- Create: `samples/_lib/manifest-append.sh`, `samples/_lib/manifest-append_test.bats`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `work_dir`, `branch`, `slug`, `base_branch?`, `workspace`.
- Produces:
  - Args: `["--workflow","<name>","<stage>","<status>","<jq filter>"]`.
  - Emits `{appended:true, path, manifest_commit, stage, status, outputs, entry}`.
  - The entry is `{stage, status, outputs, commit, finished_at, instance_id, workflow}`.
  - It runs the spec §4.6 eight-step algorithm. It commits only in `<workspace>/.manifest-wt`, on the local branch `arbitron-manifest/<short_id>`, and pushes `HEAD:refs/heads/<branch>`.

- [ ] **Step 1: Write the failing suite.** The fixture is a bare origin plus a work clone made with the Task 3 script.
  - **Happy path**
    - `creates the manifest on first append`
    - `first write records base_branch from stdin; a later append with a different base_branch does not overwrite it` (P2)
    - `second append appends`
    - `outputs stay typed (array, number, bool)`
    - `entry.workflow equals --workflow`
    - `pushes to origin`
  - **Edge cases**
    - `detached work_dir: lands on origin; work_dir stays detached at its old sha`
    - `unpushed local branch is pushed first with -u`
    - `work_dir on the branch is fast-forwarded after`
    - `unrelated dirty file in work_dir is untouched and uncommitted`
    - `remote ahead by a foreign commit: manifest worktree starts from origin`
    - `branch named feat/x works` (Review Focus 1)
    - `unknown extra manifest fields are preserved` (Review Focus 5)
  - **Errors** (each exits 1 and pushes nothing)
    - `bad stage`
    - `bad status`
    - `missing --workflow`
    - `filter yields non-object`
    - `filter calls error()`
    - `local branch diverged from origin`
    - `branch absent on origin with a detached work_dir`
  - **Boundary**
    - `concurrent append: a second clone pushes first, then one retry succeeds`
    - `conflict again on retry exits 1`
  - **Integration**
    - `never commits on a detached HEAD`: `git -C $work reflog` is unchanged in the detached case.
    - `round-trips with manifest-read`
- [ ] **Step 2: Run** the suite. Expect FAIL.
- [ ] **Step 3: Implement the script** following spec §4.6 steps 1–8.
- [ ] **Step 4: Run** the suite, `bats samples/_lib/manifest-read_test.bats` and shellcheck. Expect PASS.
- [ ] **Step 5: Commit** with `feat(lib): self-contained manifest-append`.

### Task 7: `@lib/tamper-gate.sh`, core checks and classify

**Files:**
- Create: `samples/_lib/tamper-gate.sh`, `samples/_lib/tamper-gate_test.bats`
- Modify: `Makefile`

**Interfaces:**
- Produces:
  - Invocations: `["<base>","<head>"]`, optionally with `"--tests-only"`, or `["--classify","<base>","<head>"]`.
  - It writes `{{workspace}}/tamper/findings.json`.
  - Emits `{clean, needs_judge, force_void, source_changed, history_rewritten, findings:[{id,kind,file,line,language,detail,judgeable}], warnings, blocking_count, warn_count, base, head, test_files_changed, summary}`.
  - Classify mode emits `{base, head, files:[{path,status,language,role}], source_files_by_language, test_files}`.
  - **Data tables.** The language table in spec §4.8 (source extensions, test-file patterns, assertion tokens, skip/focus markers) lives as shell data at the top of the script.
  - **Exit codes.** Exit 2 only when `work_dir` is unset or not a repo, or when `base` or `head` is not a commit.

This task covers the core checks: `test_deleted`, `test_renamed`, `assertions_dropped`, `skip_marker_added`, `history_rewritten`, de-duplication, housekeeping, and classify. Task 8 adds the config, CI and baseline checks, `--tests-only`, the Rust hunk rule, and the warning heuristic.

- [ ] **Step 1: Write the failing suite.**
  - **Fixture.** A `setup()` helper `mkrepo` commits one source file and one test file per language: `go`, `csharp` (with `App.Tests/App.Tests.csproj`), `js`, `python`, `java`, and `rust` (`tests/it.rs` only). Fixtures that need the `#[cfg(test)]` block parser belong to Task 8 (P13). A helper `change` commits a mutation. A helper `gate` runs the script and captures its JSON.
  - **Happy path**
    - `adding a test file is clean`
    - `adding assertions is clean`
    - `source-only change is clean`
  - **Markers** (`@test "marker <m> in <lang>"`): one test per marker in the spec §7.3 list, minus the Rust `src/lib.rs` placement, which moves to Task 8 (P13). That is 29 markers: Go 3, C# 4, TS/JS 11, Python 6, Java 4, Rust 1 (in `tests/it.rs`). Each asserts `.findings|length==1` and `.findings[0].kind=="skip_marker_added"`.
  - **Marker anchoring (P7), negatives** (each expects no finding)
    - `process.exit( in a JS file`
    - `benefit( in a JS file`
    - `myxit( and profit( in a JS file`
    - `.only( on a non-test identifier such as cache.only(` (anchored: the `it`/`test`/`describe` prefix must itself start at a boundary)
  - **De-duplication**
    - `skipif yields one finding listing both markers`
    - `Assumptions.assumeTrue( yields one`
    - `[Fact(Skip = "x")] yields one`
    - `two markers on two lines yield two`
    - `deleted test file yields only test_deleted`
  - **Assertion drop**: one test per language (`go t.Errorf`, `csharp Assert.Equal`, `js expect(`, `python assert`, `java assertEquals(`, `rust assert_eq!` in `tests/it.rs` (P13)). Each asserts one `assertions_dropped` whose detail matches `^[0-9]+→[0-9]+$`.
  - **Deletion and rename**
    - `deleting the test file in each language` (6 tests)
    - `R100 rename gives test_renamed`
    - `rename with dropped asserts gives both kinds`
  - **History rewritten** (each asserts exit 0, `history_rewritten:true`, `force_void:true`, `needs_judge:false`, and exactly one finding)
    - `amend of base`
    - `reset below base then recommit`
    - `rebase onto another commit`
  - **Edge cases**
    - `multiple commits in range`
    - `filename with spaces`
    - `binary files ignored`
    - `merge commit in range`
    - `stale verdict.json removed and must-fix.md rotated to .prev`
  - **Errors** (each asserts exit 2 and empty stdout)
    - `unset work_dir`
    - `base not a commit`
    - `head not a commit`
  - **Boundary**
    - `base == head is clean`
    - `equal assertion counts: no finding`
    - `one fewer assertion: one finding`
  - **Classify**
    - `classify roles and languages for a mixed set`
    - `classify test-file set equals check mode's`
- [ ] **Step 2: Run** `bats samples/_lib/tamper-gate_test.bats`. Expect FAIL.
- [ ] **Step 3: Implement the core.**
  - The ancestry check is `git merge-base --is-ancestor`.
  - Rename detection is `git diff --name-status -M`.
  - Added lines come from `git diff -U0` lines starting `+` but not `+++`, with head line numbers taken from the hunk headers.
  - Markers are matched with the left boundary `(^|[^A-Za-z0-9_$])` (P7).
  - De-duplication is a `jq` `group_by([.file,.line])` across kinds (P6). It keeps the highest-precedence kind from spec §4.8 (`history_rewritten` > `source_changed` > `baseline_edited` > `ci_config_changed` > `mutation_config_weakened` > `test_config_weakened` > `test_deleted` > `test_renamed` > `assertions_dropped` > `skip_marker_added`), records the others in `also_kinds`, and merges `detail` values with commas. Ids are `T1…` after de-duplication.
- [ ] **Step 4: Run** the suite plus shellcheck. Expect PASS.
- [ ] **Step 5: Commit** with `feat(lib): tamper-gate core checks, history-rewrite void, classify`.

### Task 8: `tamper-gate.sh`, config, CI, baseline, tests-only, Rust hunks, warning

**Files:**
- Modify: `samples/_lib/tamper-gate.sh`, `samples/_lib/tamper-gate_test.bats`

**Interfaces:**
- Produces:
  - The finding kinds `test_config_weakened`, `mutation_config_weakened`, `ci_config_changed`, `baseline_edited` and `source_changed`.
  - The warning `hardcoded_expected_value`.
  - The Rust hunk rule, which `--classify` also uses for Rust file roles.
  - The rules are spec §4.8. Section scope is resolved by scanning up from the changed line to the governing `[section]` header. The pom `<plugin>` scope is the plugin whose `artifactId` contains `pitest`. The gradle `pitest {` block and the Rust `#[cfg(test)] mod … {` block are found by brace counting that ignores `"…"`, `'…'` and `//` comments.

- [ ] **Step 1: Add the failing tests.** Every positive test asserts exactly one finding of the named kind.
  - **Config, `test_config_weakened`**
    - `jest.config.js testPathIgnorePatterns`
    - `vitest.config.ts exclude`
    - `.mocharc.yml ignore`, `.mocharc.json spec`
    - `karma.conf.js exclude`
    - `vite.config.ts test.exclude section`
    - `package.json jest.testPathIgnorePatterns`
    - `pytest.ini --ignore`
    - `pyproject pytest section`, `pyproject coverage omit`
    - `setup.cfg tool:pytest`, `setup.cfg coverage:run omit`
    - `tox.ini`, `.coveragerc omit`, `conftest collect_ignore`, `.runsettings`, `nextest.toml`
    - `pom skipTests`, `gradle test exclude`, `Cargo test = false`
  - **Config, `mutation_config_weakened`**
    - `stryker-config.json thresholds`, `stryker.conf.mjs mutate`, `stryker.config.json thresholds.break`
    - `.gremlins.yaml`, `.gremlins.yml`
    - `.cargo/mutants.toml`, `mutants.toml`
    - `pyproject tool.mutmut`, `setup.cfg mutmut`
    - `pom pitest mutationThreshold`, `gradle pitest block`
  - **Config, negatives** (no findings)
    - `package.json dependency bump`
    - `pyproject project.version bump`
    - `gradle dependencies edit beside pitest block`
  - **Config, de-duplication (P6)**
    - `config matching keyword and section rules yields one`
    - `pom pitest mutationThreshold yields one mutation_config_weakened with also_kinds test_config_weakened`
    - `gradle pitest block exclude yields one mutation_config_weakened with also_kinds test_config_weakened`
  - **CI** (8 tests, one per path in spec §4.8) → `ci_config_changed`.
  - **Baseline**
    - `edits to x.baseline.json, docs/reference/test-baseline.json, x.harden.json and x.survivors.json` (4 tests) → `baseline_edited`
    - `baseline committed before pass-mark, untouched in the pass: no finding`
  - **`--tests-only`**
    - `source change in each language` (6 tests) → `source_changed`, `force_void`, `needs_judge:false`
    - `test-only change gives no source_changed`
  - **Rust block-parser fixtures, moved here from Task 7 (P13).** These use a `src/lib.rs` that has a `#[cfg(test)] mod tests { … }` block.
    - `marker #[ignore] inside the cfg(test) mod in src/lib.rs`
    - `assert_eq! dropped inside the cfg(test) mod gives assertions_dropped`
  - **Rust hunks**
    - `edit inside cfg(test) mod is not source`
    - `edit outside the mod is source`
    - `commit touching both is source`
    - `tests/it.rs edit is not source`
    - `brace in a string literal does not close the block`
    - `deleted lines inside the block count as a test hunk`
    - `classify gives rust role by hunk`
  - **Warning**
    - `10-char literal shared with a test warns; clean stays true; no judge`
    - `7-char literal or 3-digit number does not warn`
- [ ] **Step 2: Run** the suite. Expect the new tests to FAIL and the Task 7 tests to still PASS.
- [ ] **Step 3: Implement** the remaining rules.
- [ ] **Step 4: Run** the suite plus shellcheck. Expect PASS.
- [ ] **Step 5: Commit** with `feat(lib): tamper-gate config/CI/baseline rules, tests-only, rust hunk rule`.

### Task 9: `@lib/tamper-judge.md` and `@lib/tamper-verdict.sh`

**Files:**
- Create: `samples/_lib/tamper-judge.md`, `samples/_lib/tamper-verdict.sh`, `samples/_lib/tamper-verdict_test.bats`
- Modify: `Makefile`

**Interfaces:**
- Consumes: Task 7/8 `findings.json`, plus the `{{tamper_context}}` and `tamper-gate.base` / `tamper-gate.head` variables (in the template).
- Produces:
  - Args: `["<base>", …]`, with the flags `--force-void`, `--reset`, `--restore <p>` (repeatable), `--restore-json <stdin jq path>`, `--gaps-file <p>` and `--clear <workspace-rel>` (repeatable).
  - Emits `{voided, reason, pass_base, pass_id, restored_files, must_fix_path, rulings_summary}`. `pass_id` is copied from stdin `pass-mark.pass_id` and is the staleness guard (P8).
  - `--restore-json` takes a dotted path whose segments may contain `-`. The script splits it on `.` and reads it with `getpath([...])` (P15).
  - The verdict file is `{{workspace}}/tamper/verdict.json`, shaped `{"rulings":[{"id","legitimate","reason"}]}`.
  - The must-fix file is `{{workspace}}/tamper/must-fix.md`.

- [ ] **Step 1: Write the failing suite.** Each test runs the real gate first, then writes a verdict fixture.
  - **Happy path**
    - `all legitimate: voided false, nothing restored, must-fix removed`
  - **Void**
    - `illegitimate ruling restores gated categories`: an added test file is removed and a deleted one is restored.
    - `--restore and --restore-json paths restored`
    - `revert commit created`
    - `must-fix lists the finding`
    - `--gaps-file appends "- [ ] TAMPER T1 (" in the same commit`
  - **Edge cases**
    - `--reset hard-resets`
    - `--force-void without verdict voids`
    - `history_rewritten hard-resets without --reset and a later plain push fast-forwards`
    - `--clear removes scratch only on void`
    - `nothing to restore makes no empty commit`
    - `unrelated uncommitted edits survive a void and are not committed` (Review Focus 4)
  - **Errors** (each fails closed with `voided:true`)
    - `missing verdict`
    - `malformed verdict`
    - `verdict missing a ruling`
  - **Boundary**
    - `single legitimate finding`
  - **Integration**
    - `pass_base equals the base argument`
    - `pass_id is copied from pass-mark.pass_id` (P8)
  - **Edge case (P15)**
    - `--restore-json resolve-inputs.inputs.plan_paths walks the hyphenated node id`: the stdin has `{"resolve-inputs":{"inputs":{"plan_paths":["docs/plans/x/a.md"]}}}`, and `docs/plans/x/a.md` is restored. A naive `jq .resolve-inputs.inputs…` would fail this test.
- [ ] **Step 2: Run** the suite. Expect FAIL.
- [ ] **Step 3: Write `tamper-judge.md`** (spec §4.9). It must contain no `{{` except the four variables `workspace`, `tamper_context`, `tamper-gate.base` and `tamper-gate.head`. It ends with the "count your rulings against the finding ids" contract.
- [ ] **Step 4: Implement `tamper-verdict.sh`.** Restore with `git checkout <base> -- <paths>`; remove files added in the range with `git rm`. Stage only the gated paths, the restore paths and the gaps file, and commit with `git commit -- <paths>` so unrelated changes stay out of the commit.
- [ ] **Step 5: Run** the suite plus shellcheck. Expect PASS.
- [ ] **Step 6: Commit** with `feat(lib): tamper judge template and verdict application`.

### Task 10: Go sample harness

**Files:**
- Create: `samples/sample_harness_test.go`, `samples/samples_validate_test.go`, `samples/family_test.go`
- Modify: `samples/samples_test.go` (switch to discovery)

**Interfaces:**
- Consumes: A's lib-aware `filesystem.NewFileReader(base)` with `FileExists(owner uuid.UUID, wf, rel string) bool`; `workflow.Validate`, `WithWorkflowChecker`, `WithChildContractReader` and `LintWorkflow`; the `SPAWN_PREPARED_INPUTS` code.
- Produces these test helpers, which every later task uses:
  - `allSampleNames(t) []string` returns every `samples/*/workflow.json` dir. It requires at least 4.
  - `loadSample(t, name)` is unchanged.
  - `sampleRoot(t) (base string, owner uuid.UUID)` copies all samples to `users/<owner>/<name>/` and `samples/_lib` to `shared/_lib`.
  - `profileTable(t, name) map[string]profileRow` parses the table whose header row begins `| alias |`, mapping columns by header name. `profileRow{Alias, Kind, Backend, Model string}`.
  - `modelFamily(model string) (family string, ok bool)` implements the spec §3.4 rule.
  - `nodeByID(wf, id) workflow.Node`.
  - `route(t, wf, nodeID, outputs map[string]any, inputs map[string]any) string` runs the real `ConditionalExecutor` and returns the port.
  - `onCycle(wf, id) bool`.

- [ ] **Step 1: Write the failing tests.**
  - **Happy path**
    - `TestSamplesParse` and `TestSampleInputsCarryNoSecrets` iterate `allSampleNames`.
    - `TestSamplesValidate`: for each sample, `Validate` (with the owner-bound file checker over `sampleRoot`, a `WorkflowChecker` and `ChildContractReader` over the samples, and the `ProfileChecker` from `profileTable`, or nil when the sample has no profile table) returns no error-severity findings. For `feature-pipeline-next`, once it exists, it must also return no `SPAWN_PREPARED_INPUTS`.
    - `TestNoInteractiveOrAwaitedSpawnOnCycle`: for every sample except `feature-pipeline` (legacy, out of scope; its `resolve-deps` cycle is known), there is no `INTERACTIVE_IN_LOOP` finding and no `wait_for_child` node on a cycle.
  - **Edge case**
    - `TestModelFamily`: a table where `claude-opus-5-5` is Claude, `openai-codex/gpt-6.1-sol` is GPT, `gpt-6-astra` is GPT, `us.anthropic.claude-sonnet-5-5` is Claude, `deepseek-v4-pro` is `deepseek`, and `""` gives ok=false.
  - **Error**
    - `TestErrorRoutesResolve`: every `errorBehavior.errorNodeId` in every sample, and every `defaultErrorBehavior.errorNodeId`, names an existing node.
  - **Boundary**
    - `TestArgvFiltersHaveNoTemplateBraces` (P10) checks every script `args` element in every sample. After every whole `{{name}}` template is removed (`\{\{[^{}]+\}\}`), no `{{` or `}}` may remain. The last arg of every `@lib/manifest-append.sh` node must contain neither. The test **fails if it inspected zero elements**. In this task it is seeded against the existing `ralph-loop` and legacy `feature-pipeline` script args (for example the `notify-teams` args), so it is non-vacuous from day one.
  - **Integration**
    - `TestJudgeFamilyDiffersFromDoer` (P10) has a pairing table of `{workflow, judgeNode, doerNodes []string}` that later tasks add rows to. The test **discovers** judge nodes from the graphs: every `agent_call` whose id matches `^tamper-judge` or `^judge-`. It **fails** when a discovered judge has no pairing row, or a row names a node that doesn't exist. For each row, the judge's family must differ from every doer's family, and both must be classifiable. In this task no judges exist yet, so the discovery loop finds zero and that is allowed. A forgotten row in any later task fails.
- [ ] **Step 2: Run** `go test ./samples/ -run 'TestSamples|TestNoInteractive|TestModelFamily|TestErrorRoutes|TestArgvFilters|TestJudgeFamily' -v`. Expect FAIL to compile (the helpers are missing).
- [ ] **Step 3: Implement** the helpers and tests. Run the suite against the four existing samples. If an existing sample has a genuine validation error, fix the sample in this task. Do not allowlist it.
- [ ] **Step 4: Run** `gofmt -l samples` (expect empty output) and `go test ./samples/` (expect PASS).
- [ ] **Step 5: Commit** with `test(samples): discovery, real-resolver validation, family classifier, error-route and argv guards`.

---

## Phase 2 — Goal step 4: `ralph-loop`

### Task 11: ralph scripts — init `in_place`/`baseline_path`, derive-scope `pr_base`, import-baseline, record-baseline commit

**Files:**
- Modify: `samples/ralph-loop/scripts/init-workspace.sh`, `derive-scope.sh`, `derive-scope_test.sh`, `record-baseline.sh`
- Create: `samples/ralph-loop/scripts/init-workspace_test.sh` (new; ralph has none today), `samples/ralph-loop/scripts/record-baseline_test.sh`, `samples/ralph-loop/scripts/import-baseline.sh`, `samples/ralph-loop/scripts/import-baseline_test.bats`
- Modify: `Makefile`

**Interfaces:**
- Produces:
  - `init-workspace` additionally emits `in_place` and `baseline_present`.
  - `import-baseline.sh` takes `[]`, reads `work_dir`, `workspace` and `baseline_path`, writes `<workspace>/test-baseline.json`, and emits `{imported:true, path}`.
  - `record-baseline.sh` commits `docs/reference/test-baseline.json` with the message `chore(ralph): record branch-point baseline` when the file changed.

- [ ] **Step 1: Write the failing tests.**
  - `init-workspace_test.sh`:
    - `in_place fresh and resume both stay on base_branch`
    - `in_place with empty pr_base exits 1 before git` (assert no `src` dir)
    - `in_place with pr_base == base_branch exits 1`
    - `non-boolean in_place exits 1`
    - `missing baseline_path file exits 1`
    - `baseline_present true/false`
  - `derive-scope_test.sh`:
    - `pr_base used for the merge-base`
    - `empty pr_base unchanged`
    - `unreachable pr_base falls back with the reported method`
  - `import-baseline_test.bats`:
    - Happy path: `valid file copied`.
    - Error: `invalid shape exits 1`.
    - Edge case: `missing file exits 1`.
  - `record-baseline_test.sh`:
    - `file committed and tree clean`
    - `docs/reference created when absent`
    - `unchanged file on resume makes no commit`
- [ ] **Step 2: Run** each `sh samples/ralph-loop/scripts/<x>_test.sh` and the bats file. Expect FAIL.
- [ ] **Step 3: Implement** the changes in spec §5.1, under "Script changes".
- [ ] **Step 4: Run** the tests, shellcheck on the four changed or new scripts, and `go test ./samples/ -run 'TestLoopGate|TestRalphGate'`. Expect PASS.
- [ ] **Step 5: Commit** with `feat(ralph-loop): in_place, baseline_path import, pr_base scope, committed baseline`.

### Task 12: ralph scripts — push-and-pr reuse, record-pass-commit tamper, verify-ralph-seed CHECK 5

**Files:**
- Modify: `samples/ralph-loop/scripts/push-and-pr.sh`, `push-and-pr_test.sh`, `record-pass-commit.sh`, `verify-ralph-seed.sh`, `verify-ralph-seed_test.sh`
- Create: `samples/ralph-loop/scripts/record-pass-commit_test.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `tamper-verdict` / `tamper-void` outputs `{voided, pass_id}`, `pass-mark.pass_id`, and the survivors file `{summary, focus_items:[{id}]}`.
- Produces:
  - `push-and-pr` emits `pr_number` as a JSON number (or `null`) and `pr_created`.
  - `record-pass-commit` adds `tamper_voided`.

- [ ] **Step 1: Write the failing tests.**
  - `push-and-pr_test.sh` (with a `gh` stub on PATH):
    - `reuses an open PR`
    - `pr_number is a number`
    - `shortfall block in a new PR body`
    - `shortfall comment on a reused PR`
  - `record-pass-commit_test.sh`:
    - `matching pass_id from tamper-verdict gives no progress and tamper_voided` (P8)
    - `the same from tamper-void`
    - `mismatched pass_id ignored`
    - `reset-void in pass N then a clean pass N+1 at the same sha is scored normally`: the snapshot carries `tamper-void:{voided:true,pass_id:4,pass_base:S}` and `pass-mark:{sha:S,pass_id:5}`, and the expected output is `tamper_voided:false` (P8)
  - `verify-ralph-seed_test.sh` (CHECK 5):
    - `all ids present ok`
    - `one missing exits 1 naming it`
    - `harden:other handled`
    - `unreadable seed exits 1`
    - `empty focus_seed skipped`
- [ ] **Step 2: Run** the tests. Expect FAIL.
- [ ] **Step 3: Implement** the changes. Leave the existing early-exit paths in `record-pass-commit` untouched (the "never fails" rule).
- [ ] **Step 4: Run** the tests plus shellcheck. Expect PASS.
- [ ] **Step 5: Commit** with `feat(ralph-loop): PR reuse, tamper-aware progress, focus-seed check`.

### Task 13: ralph-loop manifest, templates and wiring tests

**Files:**
- Modify: `samples/ralph-loop/workflow.json`, `templates/seed-ralph.md`, `templates/critique-replan.md`, `templates/refine-single.md`, `templates/refine-paired.md`, `templates/assess-single.md`, `templates/assess-paired.md`, `PROFILES.md`, `scripts/shared-scripts_test.sh`
- Delete: `samples/ralph-loop/scripts/checkpoint-push.sh`, `scripts/notify-teams.sh`
- Create: `samples/tamper_wiring_test.go`
- Modify: `samples/family_test.go` (add rows)

**Interfaces:**
- Consumes: Tasks 3–12.
- Produces:
  - The new ralph inputs `in_place` (bool, false), `focus_seed` (string, `""`) and `baseline_path` (string, `""`).
  - The new nodes `baseline-source?`, `import-baseline`, `checkpoint-push-baseline`, `pass-mark`, `tamper-gate`, `tamper-route`, `tamper-void`, `tamper-context`, `judge-pick`, `tamper-judge-codex`, `tamper-judge-opus` and `tamper-verdict`, with the exact args and edges in spec §5.1.

- [ ] **Step 1: Write the failing Go tests** in `tamper_wiring_test.go`.
  - **Happy path**
    - `TestRalphTamperWiring`: every `assess-*` node goes to `tamper-gate`; no `assess-* → record-pass-commit` edge remains; and both `tamper-void` and `tamper-verdict` go to `record-pass-commit`.
  - **Boundary**
    - `TestTamperRouteFirstMatch/ralph`: `route(tamper-route, {tamper-gate: {force_void:true, needs_judge:true}})` routes to `tamper-void`, and `{needs_judge:true}` routes to the judge path.
  - **Edge case**
    - `TestRalphJudgePick`: `next_kind=single_2` routes to `tamper-judge-opus`; `single_1` and `paired` route to `tamper-judge-codex`.
  - **Integration**
    - `TestBaselineCommittedBeforeFirstPass/ralph`: every path from `init-workspace` to `pass-mark` passes through `record-baseline` and then `checkpoint-push-baseline`.
  - **Integration (family rows)**: add `{ralph-loop, tamper-judge-codex, [refine-single-1, refine-paired]}` and `{ralph-loop, tamper-judge-opus, [refine-single-2]}` to `TestJudgeFamilyDiffersFromDoer`.
  - **Error**
    - Update `shared-scripts_test.sh` so it asserts that `_lib` copies are identical to the legacy ones and that the ralph copies are absent.
- [ ] **Step 2: Run** `go test ./samples/ -run 'TestRalph|TestTamperRoute|TestBaselineCommitted|TestJudgeFamily'` and `sh samples/ralph-loop/scripts/shared-scripts_test.sh`. Expect FAIL.
- [ ] **Step 3: Edit `workflow.json`** to match the spec §5.1 graph-changes table. Switch the two checkpoint-push nodes and `notify-teams` to `@lib/`, and delete the local copies. Re-space positions on the fallback grid. Make the template edits from spec §5.1, under "Template changes".
- [ ] **Step 4: Run** `go test ./samples/` (all of it, including `TestSamplesValidate`, `TestLoopGate*` and `TestRalphGateExitsOnACompletedPlan`) and `make test-scripts`. Expect PASS.
- [ ] **Step 5: Commit** with `feat(ralph-loop): tamper gate per pass, cross-family judges, @lib scripts, focus seed`.

---

## Phase 3 — Goal step 5: stage workflows

Every stage task ends with the same check: `go test ./samples/` (`TestSamplesParse`, `TestSamplesValidate`, the cycle lint, error routes and argv guards) and `make test-scripts` both pass. Every stage except `ideate` declares the optional input `base_branch` (string, `""`), which its `finish` records when it creates the manifest (P2). Each stage gets a `PROFILES.md` that follows spec §3.4: the `alias | kind | backend | model | effort | context | tools | used by` table, the following-vs-pinned note, and the pin-at-spawn note.

### Task 14: `ideate`

**Files:**
- Create: `samples/ideate/workflow.json`, `PROFILES.md`, `templates/jam.md`, `orchestration/jam.orch.md`, `scripts/finalize-ideate.sh`, `scripts/finalize-ideate_test.bats`
- Create: `samples/stage_contracts_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `@lib/init-workspace.sh`, `@lib/manifest-append.sh`, `@lib/idea-doc-skeleton.md`.
- Produces:
  - Inputs: `repo_url` (required), `base_branch` (`main`), `topic` (`""`), `teams_webhook_url` (`""`).
  - Contract: `finish.outputs = {slug, branch, idea_path}`.
  - `finalize-ideate.sh` takes `[]` and emits `{slug, branch, idea_path}`.
  - Test helper: `finishFilterKeys(t, wf) []string`, which extracts the keys of the `finish` node's jq-filter arg with the regex `(\w+):`. Every stage writes its filter in explicit `key: .path` form (P5).

- [ ] **Step 1: Write the failing tests.**
  - `finalize-ideate_test.bats`:
    - Happy path: `valid result creates dev-<slug>-<date>-<short>, commits the doc, HEAD not detached`.
    - Errors:
      - `missing result file`
      - `missing doc`
      - `empty doc`
      - `uppercase slug`
      - `leading - slug`
      - `49-char slug`

      Each exits 1, and the first two report "no idea doc written".
    - Boundary: `48-char slug ok`.
  - `stage_contracts_test.go`:
    - `TestStageContract/ideate`: `finish` is the only sink, its scriptFile is `@lib/manifest-append.sh`, its args `[0:4]` are `["--workflow","ideate","ideate","done"]`, and `finishFilterKeys` ⊇ `{slug, branch, idea_path}`.
    - `TestIdeateJamHasNoOutputSchema`.
- [ ] **Step 2: Run** the bats file and `go test ./samples/ -run 'TestStageContract/ideate|TestIdeate'`. Expect FAIL.
- [ ] **Step 3: Write the script, templates, `workflow.json` and `PROFILES.md`** as spec §5.2 describes. `jam.md` includes `{{include:@lib/idea-doc-skeleton.md}}`.
- [ ] **Step 4: Run** the stage check. Expect PASS.
- [ ] **Step 5: Commit** with `feat(ideate): standalone ideation stage`.

### Task 15: `spec`

**Files:**
- Create: `samples/spec/workflow.json`, `PROFILES.md`, `templates/brainstorm-spec.md`, `orchestration/brainstorm-spec.orch.md`. Port these from `~/.arbitron/workspaces/users/00000000-0000-0000-0000-000000000001/legacy-feature-pipeline/`.
- Modify: `samples/stage_contracts_test.go`

**Interfaces:**
- Produces:
  - Inputs: the common inputs plus `idea_path` (`""`).
  - Contract: `finish.outputs = {spec_path}`.
  - The interactive node `brainstorm-spec` runs on `interviewer-codex`. This is kept by ruling P14: live instance d77ebf0e completed `brainstorm-spec` on that profile. Do not switch it.

- [ ] **Step 1: Write the failing tests.**
  - `TestStageContract/spec`: args `[0:4]` are `["--workflow","spec","spec","done"]` and the keys include `spec_path`.
  - `TestSpecTemplateTargetsSpecTarget`: the template contains `./work/{{spec_target}}` and `{{slug}}`, and does not contain `<slug>-spec.md` or `git push`.
- [ ] **Step 2: Run** them. Expect FAIL.
- [ ] **Step 3: Write the files** as spec §5.3 describes.
- [ ] **Step 4: Run** the stage check. Expect PASS.
- [ ] **Step 5: Commit** with `feat(spec): standalone product-spec stage`.

### Task 16: `design`

**Files:**
- Create: `samples/design/workflow.json`, `PROFILES.md`, `templates/brainstorm-design.md`, `orchestration/brainstorm-design.orch.md`, `templates/align-designs.md`. These are ports.
- Modify: `samples/stage_contracts_test.go`

**Interfaces:**
- Produces:
  - Inputs: the common inputs plus `spec_path` and `idea_path` (both `""`).
  - Contract: `finish.outputs = {design_paths}`.

- [ ] **Step 1: Write the failing tests.**
  - `TestStageContract/design`.
  - `TestDesignGlobIsRunDated`: the `promote.design_glob` value contains `{{init.run_date}}-{{slug}}-design-phase-*` (Review Focus 2).
  - `TestDesignOrchHasNoPush`.
- [ ] **Step 2: Run** them. Expect FAIL.
- [ ] **Step 3: Write the files** as spec §5.4 describes.
- [ ] **Step 4: Run** the stage check. Expect PASS.
- [ ] **Step 5: Commit** with `feat(design): standalone design + alignment stage`.

### Task 17: `plan`

**Files:**
- Create: `samples/plan/workflow.json`, `PROFILES.md`, `templates/{produce-plans,align-plans,deps-assess,resolve-deps}.md`, `orchestration/resolve-deps.orch.md`, `scripts/deps-count.sh`, `scripts/deps-count_test.sh`, `scripts/list-plans.sh`, `scripts/list-plans_test.bats`
- Create: `samples/stage_gates_test.go`
- Modify: `samples/stage_contracts_test.go`, `Makefile`

**Interfaces:**
- Produces:
  - Inputs: the common inputs plus `spec_path` (`""`) and `design_paths` (`[]`).
  - Contract: `finish.outputs = {plan_paths, deps_resolved, deps_gaps}`, with `finish` status `{{plan_status}}`.
  - `deps-count.sh` takes `["<assess node id>"]` (default `deps-assess`) and emits `{needs_resolution}`, keeping the legacy output fields.
  - `list-plans.sh` takes `[]`, reads `work_dir`, `plan_dir` and `run_date`, and emits `{plan_paths}`. It matches only `<plan_dir>/<run_date>-phase-*-task-*.md` (Review Focus 2).

- [ ] **Step 1: Write the failing tests.**
  - `deps-count_test.sh`: port the legacy cases, and add `node-id arg reads deps-assess-2` and `default node id`.
  - `list-plans_test.bats`:
    - Happy path: `sorts phase then task numerically (task-10 after task-9)`.
    - Edge case: `ignores files of another run_date` (Review Focus 2).
    - Error: `none exits 1`.
    - Boundary: `single file`.
  - `stage_gates_test.go`, `TestPlanDepsGatesSendDecisionsToTheHumanOnce`:
    - `deps-gate` with `needs_resolution=7` routes to `resolve-deps`.
    - `deps-gate-2` with `7` routes to `mark-unresolved`.
    - `0` routes to `mark-resolved` on both gates.
    - `deps-gate` with `1` (unreadable) does not route to proceed.
  - `TestStageContract/plan`.
  - `TestPlanIsAcyclic`.
- [ ] **Step 2: Run** them. Expect FAIL.
- [ ] **Step 3: Write the files** as spec §5.5 describes.
- [ ] **Step 4: Run** the stage check. Expect PASS.
- [ ] **Step 5: Commit** with `feat(plan): standalone planning stage with one-pass dependency resolution`.

### Task 18: `implement` scripts

**Files:**
- Create: `samples/implement/scripts/{count-plan-steps,baseline-check,persist-baseline,import-baseline,record-baseline}.sh`, each with a matching `*_test.bats`. `record-baseline.sh` is ported from legacy; its test is new.
- Modify: `Makefile`

**Interfaces:**
- Produces:
  - `count-plan-steps.sh` takes `[]`, reads `work_dir` and `resolve-inputs.inputs.plan_paths`, and emits `{unchecked_steps, checked_steps, all_tasks_complete, tasks_remaining, unreadable}`.
  - `baseline-check.sh` emits `{present}` for `./work/<baseline_rel>`.
  - `persist-baseline.sh` validates, copies and commits, then emits `{persisted:true, path}`.
  - `import-baseline.sh` emits `{imported:true, path}`.
  - `record-baseline.sh` emits `{test_baseline, test_commands, baseline_summary, baseline_source}`.

- [ ] **Step 1: Write the failing tests.**
  - `count-plan-steps`:
    - Happy path: `mixed checkboxes`.
    - Edge cases: `all checked is complete`, `no checkboxes is incomplete and flagged`.
    - Error: `missing file reported in unreadable and incomplete`.
    - Boundary: `one unchecked step`.
  - `baseline-check`: `present` and `absent`.
  - `persist-baseline`: `commits and leaves a clean tree`, `invalid shape exits 1`.
  - `import-baseline`: `copies`, `invalid exits 1`.
  - `record-baseline`: `fallback-empty on missing file`, `lifts commands`.
- [ ] **Step 2: Run** them. Expect FAIL.
- [ ] **Step 3: Implement** the scripts.
- [ ] **Step 4: Run** them plus shellcheck. Expect PASS.
- [ ] **Step 5: Commit** with `feat(implement): plan-step counter and branch-point baseline scripts`.

### Task 19: `implement` workflow

**Files:**
- Create: `samples/implement/workflow.json`, `PROFILES.md`, `templates/{baseline-tests,impl-task,impl-assess}.md` (ports)
- Modify: `samples/stage_gates_test.go`, `samples/tamper_wiring_test.go`, `samples/family_test.go`, `samples/stage_contracts_test.go`

**Interfaces:**
- Produces:
  - Inputs: the common inputs plus `plan_paths` (`[]`), `max_impl_iterations` (`200`) and `impl_iteration` (`0`).
  - Contract: `finish` status `{{impl_status}}`, `outputs = {status, blocked_reason, baseline_path}`.

- [ ] **Step 1: Write the failing tests.**
  - `TestImplGateCompletionIsArithmetic`:
    - `count-plan-steps.all_tasks_complete=false` with `impl-assess` claiming completion → `pass-mark`.
    - `all_remaining_blocked` absent → not `mark-blocked`.
    - `impl_iteration=200` → `mark-cap`.
    - Complete plus `all_remaining_blocked:true` → `mark-done`.
  - `TestTamperRouteFirstMatch/implement`.
  - `TestBaselineCommittedBeforeFirstPass/implement`: both the measure path and the import path reach `record-baseline` before `pass-mark`, and the measure path passes through `persist-baseline → checkpoint-push-baseline`.
  - Family row `{implement, tamper-judge, [impl-task]}`.
  - `TestStageContract/implement`.
  - `TestImplTemplateHasNoPush`.
- [ ] **Step 2: Run** them. Expect FAIL.
- [ ] **Step 3: Write the files** as spec §5.7 describes.
- [ ] **Step 4: Run** the stage check. Expect PASS.
- [ ] **Step 5: Commit** with `feat(implement): standalone TDD implementation stage with tamper gate`.

### Task 20: `harden` scripts, part 1 — scope and detect

**Files:**
- Create: `samples/harden/scripts/{scope,detect}.sh` with matching `*_test.bats`
- Modify: `Makefile`

**Interfaces:**
- Consumes:
  - the Task 1 registry
  - `@lib/tamper-gate.sh --classify` output in the snapshot, under the key `classify`
  - `resolve-inputs.inputs.{base_ref, baseline_path}` and `resolve-inputs.outputs.base_branch`
- Produces:
  - `scope.sh` emits `{base_ref, merge_base}`.
  - `detect.sh` emits `{languages:[{language, tool, status, reason, targets, test_command, test_command_source}], runnable_count, skip_reason}`. It exits 1 listing every `install_hint` when a supported tool's probe fails.

- [ ] **Step 1: Write the failing tests.**
  - `scope`:
    - Happy path: `explicit base_ref`, `manifest fallback`.
    - Edge case: `unshallow needed`.
    - Error: `neither exits 1`, `no merge-base exits 1`.
  - `detect`. Tools are PATH stubs; `PATH` is restricted to stub dirs plus the system dirs.
    - Happy path: `supported+present is runnable`.
    - Edge case: `no registry entry is skipped with reason`.
    - Error: `probe failure exits 1 listing every hint`.
    - C# targeting:
      - `csharp: one test project`
      - `csharp: two test projects`
      - `csharp: no referencing test project is skipped`
    - Boundary: `no changed source gives runnable_count 0 and skip_reason`.
    - Test command:
      - `baseline suite command used with test_command_source baseline`
      - `no baseline uses registry default per language` (6 cases, including `mvn test` with `pom.xml` and `gradle test` with `build.gradle`)
      - `baseline without that language uses default`
- [ ] **Step 2: Run** them. Expect FAIL.
- [ ] **Step 3: Implement** both scripts. `detect.sh` reads the registry from `$(dirname "$0")/mutation-tools.json`.
- [ ] **Step 4: Run** them plus shellcheck. Expect PASS.
- [ ] **Step 5: Commit** with `feat(harden): scope and language/tool detection with test-command fallback`.

### Task 21: `harden` scripts, part 2 — mutate, adapters, score, equivalence, report

**Files:**
- Create: `samples/harden/scripts/{mutate,mutate-go,mutate-csharp,mutate-js,mutate-python,mutate-java,mutate-rust,score,equivalence,report}.sh` with matching `*_test.bats` (the adapters share one file, `mutate-adapters_test.bats`)
- Modify: `Makefile`

**Interfaces:**
- Consumes: `detect.languages` and the registry.
- Produces:
  - `mutate.sh` takes `[]` or `["--survivors-only"]` and writes `{{workspace}}/harden/state.json`, shaped `{mutants:{"<file>:<line>:<mutator>:<replacement>":{status, language, tool, file, line, mutator, replacement}}, approved_equivalents:[], rounds:[]}`. It emits totals.
  - Each `mutate-<lang>.sh` takes `["<targets json file>","<test command>","<out json>"]` and writes the normalized list `[{file, line, mutator, replacement, status}]` with `status` ∈ `killed`, `timeout`, `survived`, `no_coverage`, `excluded`.
  - `score.sh` emits `{score, gate_passed, killed, survived, no_coverage, timeout, total, approved_equivalents, denominator, survivor_files}` and writes `harden/survivors-round.json`.
  - `equivalence.sh --count` emits `{claim_count}`. `equivalence.sh --record` emits `{approved, rejected}`.
  - `report.sh` writes and commits `.arbitron/pipeline/<slug>.harden.json` and `.survivors.json`, then emits `{result, skip_reason, score, gate_passed, survivors_path, focus_seed_path, harden_report_path}`.

- [ ] **Step 1: Write the failing tests.**
  - `mutate`:
    - Happy path: `dispatches per runnable language and normalizes`.
    - Edge case: `--survivors-only re-runs only survivor files and keeps earlier kills`.
  - Adapters: one `normalizes <tool> report` test per language. The report literal comes from the spike notes and is built inline in `setup()`. Also `tool missing stub fails`.
  - `score`:
    - `status mapping` (timeout counts as killed; no_coverage counts as survived)
    - `compile errors excluded`
    - `equivalents shrink the denominator`
    - `0 mutants gives null score and passes`
    - `80.00 passes`
    - `79.99 fails`
  - `equivalence`:
    - `--count missing is 0`, `--count malformed is 0 with warning`, `--count n`
    - `--record keeps only true; unruled rejected; files removed`
  - `report`:
    - `one item per file`
    - `26 files yields 25 items ending harden:other`
    - `focus_seed_path empty when passed`
    - `skipped run: result skipped, score null, gate_passed true, empty focus_items`
    - `both files committed`
- [ ] **Step 2: Run** them. Expect FAIL.
- [ ] **Step 3: Implement** the scripts. Adapters use the `diff_scoping.how` that the registry records, write temporary config only under `{{workspace}}/harden/`, and leave the worktree unchanged. Each adapter requires a clean tree before it starts, and afterwards restores with `git checkout -- .` and `git clean -fd` (never `-x`). The cleanliness test (P11) runs each adapter with a stub tool that **deliberately** modifies a tracked source file and adds an untracked `stryker-config.json`, then asserts `git status --porcelain` is empty. Without the restore step, that test fails.
- [ ] **Step 4: Run** them plus shellcheck. Expect PASS.
- [ ] **Step 5: Commit** with `feat(harden): mutation dispatch, adapters, scoring, equivalence and report`.

### Task 22: `harden` workflow

**Files:**
- Create: `samples/harden/workflow.json`, `PROFILES.md`, `templates/strengthen.md`, `templates/judge-equivalent.md`
- Modify: `samples/stage_gates_test.go`, `samples/tamper_wiring_test.go`, `samples/family_test.go`, `samples/stage_contracts_test.go`

**Interfaces:**
- Produces:
  - Inputs: the common inputs plus `base_ref` (`""`), `baseline_path` (`""`), `gate_threshold` (`80`), `max_rounds` (`3`) and `harden_round` (`0`).
  - Contract: `finish.outputs = {result, skip_reason, score, gate_passed, survivors_path, focus_seed_path, harden_report_path}`.

- [ ] **Step 1: Write the failing tests.**
  - `TestHardenGateBoundary`:
    - `gate-0` with `gate_passed:true` → `report`.
    - `harden-gate` in round 2 of 3 with `false` → `pass-mark`.
    - Round 3 → `report`.
    - Absent `score-r` in round 1 → `pass-mark`.
  - `TestTamperRouteFirstMatch/harden`: `force_void` routes to `void-round`.
  - `TestHardenVoidNodesClearScratch`: the args of both `void-round` and `tamper-verdict` contain `--clear harden/equivalence-claims.json` and `--clear harden/equivalence-verdicts.json`.
  - `TestHardenHasWorkSkips`: `detect.runnable_count=0` → `report`.
  - Family rows: `{harden, tamper-judge, [strengthen]}` and `{harden, judge-equivalent, [strengthen]}`.
  - `TestStageContract/harden`: `finishFilterKeys` equals exactly `{result, skip_reason, score, gate_passed, survivors_path, focus_seed_path, harden_report_path}`. That requires the explicit `key: .report.key` filter (P5).
- [ ] **Step 2: Run** them. Expect FAIL.
- [ ] **Step 3: Write the files** as spec §5.6.3 describes. `strengthen.md` states the tests-only rule, the claims-file format and the must-fix-first rule.
- [ ] **Step 4: Run** the stage check. Expect PASS.
- [ ] **Step 5: Commit** with `feat(harden): diff-scoped mutation hardening stage`.

---

## Phase 4 — Goal step 6

### Task 23: `pr-review` sample sync

**Files:**
- Modify: `samples/pr-review/workflow.json`, `samples/pr-review/PROFILES.md`
- Create: `samples/pr_review_sync_test.go`

- [ ] **Step 1: Write the failing tests.**
  - `TestPRReviewProfileRemap`: `manager` uses `reviewer-codex-high`; `senior-engineer` uses `coder-sonnet5-high`; `verify-fix` uses `approver-codex-high`; the `verify-fix` description contains `Sonnet 5.5`; and `gitVariable == "pr_branch"`.
  - `TestPRReviewProfilesTableMatches`: every alias used in `workflow.json` appears in `profileTable`, and neither `reviewer-sonnet5-high` nor `coder-opus-medium` remains.
  - Family row: `{pr-review, verify-fix, [senior-engineer]}`.
- [ ] **Step 2: Run** them. Expect FAIL.
- [ ] **Step 3: Make the edits** in spec §5.8.
- [ ] **Step 4: Run** `go test ./samples/`. Expect PASS, including `TestSampleInputsCarryNoSecrets`.
- [ ] **Step 5: Commit** with `chore(pr-review): sync manager/senior-engineer profile remap into samples`.

---

## Phase 5 — Goal step 7: parent

### Task 24: Parent scripts — intake, intake-check, adopt-ideate, halt

**Files:**
- Create: `samples/feature-pipeline-next/scripts/{intake,intake-check,adopt-ideate,halt}.sh` with matching `*_test.bats`
- Modify: `Makefile`

**Interfaces:**
- Produces:
  - `intake.sh` emits `{mode: resume|idea|fresh, init_mode: checkout|new-branch|detached-base, slug, repo_slug, base_branch}`. `base_branch` is the explicit value if one was given; otherwise `main` in fresh and idea mode, and `""` in resume mode (P2b).
  - `intake-check.sh` emits `{slug, branch, work_dir, idea_path, record_idea, base_branch}`. In resume mode `base_branch` is the explicit value, else the manifest's top-level `base_branch` from the worktree, else it exits 1 with "resume branch has no recorded base_branch; pass base_branch" (P2b).
  - `adopt-ideate.sh` emits `{branch, slug}`.
  - `halt.sh` always exits 1.

- [ ] **Step 1: Write the failing tests.**
  - `intake`:
    - Happy path: `three modes with init_mode`.
    - Edge case: `idea-mode slug from basename`. `docs/ideas/My_Idea.md` gives `my-idea`, and `idea_path` is not rewritten (Review Focus 3).
    - Errors:
      - `start_at=ideate with branch exits 1`
      - `start_at=ideate with idea_path exits 1 with "idea_path given — ideate is skipped; omit start_at or use start_at=spec"` (P3)
      - `start_at=plan with no branch exits 1`
      - `invalid start_at exits 1`
      - `non-GitHub url exits 1`
    - `repo_slug` derivation:
      - `repo_slug from https, ssh and scp forms`
      - Boundary: `file:// gives empty repo_slug`.
  - `intake-check` (fixture: a bare origin and a clone made with Task 3):
    - Slug:
      - `slug discovery 0/1/2 manifests`
      - `explicit slug wins`
    - Resume mode:
      - `resume with idea_path: must exist; record_idea true even with an ideate entry`
      - `resume, no idea_path, no ideate entry, empty start_at exits 1 with "resume branch has no idea doc; pass idea_path or start_at=spec"`
      - `resume, no ideate entry, start_at=spec ok with record_idea false`
      - `resume, ideate entry, no idea_path ok`
      - `resume with no manifest at all, start_at=spec and an explicit base_branch is ok (record_idea false)` (P2)
    - `base_branch` resolution (P2b):
      - `intake: fresh and idea with empty base_branch emit main; explicit kept; resume with empty emits ""`
      - `intake-check: explicit base_branch wins over the manifest`
      - `intake-check: resume of a branch cut from dev without base_branch emits base_branch=dev from the manifest`
      - `intake-check: resume with no explicit and no manifest base_branch exits 1 with "resume branch has no recorded base_branch; pass base_branch"`
      - Integration: `resume from dev: intake-check output piped into @lib/manifest-read.sh --explicit base_branch --require base_branch yields inputs.base_branch=dev`. This simulates `promote-check` followed by `pre-harden`/`pre-refine`.
    - Idea mode: `idea mode idea_path missing on base exits 1`.
    - `outputs correct`.
  - `adopt-ideate`:
    - Happy path: `valid`.
    - Errors (each exits 1): `missing branch`, `empty slug`, `non-string`.
  - `halt`: `stage message`, `generic message when vars absent`, `always exit 1`.
- [ ] **Step 2: Run** them. Expect FAIL.
- [ ] **Step 3: Implement** the scripts. The `intake.sh` slug sanitizer is the same as in `init-workspace.sh`; copy it, because lib scripts cannot be sourced.
- [ ] **Step 4: Run** them plus shellcheck. Expect PASS.
- [ ] **Step 5: Commit** with `feat(feature-pipeline-next): intake, resume rules, adopt-ideate, halt`.

### Task 25: `feature-pipeline-next` workflow

**Files:**
- Create: `samples/feature-pipeline-next/workflow.json`, `PROFILES.md` (no model nodes; it states that and lists each child's `PROFILES.md`)
- Create: `samples/parent_test.go`
- Modify: `samples/samples_validate_test.go` (activate the `SPAWN_PREPARED_INPUTS` assertion)

**Interfaces:**
- Consumes: every stage contract, the ralph-loop inputs and the pr-review inputs.
- Produces: the nodes, args, routing, `defaultErrorBehavior`, per-node `errorBehavior`, and the spawn `inputs` maps exactly as spec §5.9 gives them.

- [ ] **Step 1: Write the failing tests** in `parent_test.go`.
  - **Happy path**
    - `TestParentIsAcyclic`.
    - `TestParentRouteCoversEveryStage`: `route` maps each of the eight stages, and `complete`, to the §5.9 targets, and an unknown value to `notify-step-failed`.
  - **Edge case**
    - `TestParentGates`:
      - `plan-ok?` with `deps_resolved:false` → `fail-plan-blocked`.
      - `impl-ok?` with status `blocked` → `fail-impl-blocked`.
      - `record-idea?` with `record_idea:true` → `record-provided-idea`, and `false` → `fresh?`.
    - `TestParentFreshModeSkipsDetect` (P1): `fresh?` routes `intake.mode=fresh` → `ideate`, and `resume` and `idea` → `detect`. No path from `intake` to `detect` passes through `ideate`.
  - **Error**
    - `TestParentErrorRouting`:
      - `defaultErrorBehavior` routes to `notify-step-failed`.
      - Every spawn and script node, except `notify-done`, `notify-failed`, `notify-step-failed` and `halt`, has an explicit route to `notify-step-failed`.
      - The three `notify-*` nodes carry `{"type":"skip"}`, and their only successors are `done` (for `notify-done`) and `halt` (for the other two). Notify is best effort (P4).
      - `halt` carries `{"type":"halt"}`.
      - Every spawn has both `success` and `failure` edges.
  - **Boundary**
    - `TestParentSpawnsAreFullyMapped`:
      - For every spawn, every key in §5.9 is present.
      - Every required child input with no default is mapped.
      - Every mapped key is declared by the child.
      - The `spec`, `design`, `plan`, `implement` and `harden` spawns map `base_branch` to `{{base_branch}}`. `pre-harden` and `pre-refine` args contain `--explicit base_branch` (P2).
    - `TestParentResumeBaseBranchFromManifest` (P2b):
      - The parent's `base_branch` input default is `""`.
      - Running `promote-intake` and then `promote-check` through the real `TransformExecutor` with `intake-check.base_branch=dev` (and an empty explicit value) leaves the store's `base_branch` = `dev`.
      - `ResolveTypedMap` of the `spec`…`harden` spawn maps gives `base_branch=dev`.
      - `pre-harden` and `pre-refine` read it via `--explicit base_branch`.
      - `ResolveTypedMap` against a fixture store keeps the types: `design_paths` and `plan_paths` stay `[]any`, `pr_number` stays `float64`, and `in_place` stays `true`.
  - **Integration**
    - `TestStageContractsLineUp`:
      - Every `childOutput.finish.outputs.<k>` that the parent reads is among that child's `finishFilterKeys`.
      - Every `pre-*` `--require` key is produced by some stage's finish keys, or is one of `base_branch`, `branch` or `slug`.
      - Every `manifest-append` node's `--workflow` equals its own workflow name.
      - `TestFinishFilterKeysNonEmpty` (P5): for each of the six stages, `finishFilterKeys` returns at least one key, so the line-up check cannot pass vacuously.
    - `TestSamplesValidate` reports no `SPAWN_PREPARED_INPUTS` for the parent.
- [ ] **Step 2: Run** `go test ./samples/ -run 'TestParent|TestStageContractsLineUp|TestSamplesValidate'`. Expect FAIL.
- [ ] **Step 3: Write `workflow.json`** as spec §5.9 describes, positioned on the fallback grid.
- [ ] **Step 4: Run** `go test ./samples/` and `make test-scripts`. Expect PASS.
- [ ] **Step 5: Commit** with `feat(feature-pipeline-next): composable parent pipeline`.

### Task 26: AGENTS.md and doc updates

**Files:**
- Modify: `AGENTS.md`
  - The "Agent backend vocabulary" list becomes `claude-code`, `opencode`, `codex` and `pi`.
  - Add the sentence "For cross-family rules, family is the model vendor (after stripping provider prefixes), not the backend."
  - Add one short "Pipeline stage workflows" paragraph naming the six stages, the parent, the manifest path, and `@lib` tamper gate usage.
- Create: `samples/agents_doc_test.go`

- [ ] **Step 1: Write the failing test.** `TestAgentsDocBackendVocabulary` reads `../AGENTS.md` and asserts it contains each backend constant from `domain/workflow` (`workflow.BackendClaudeCode`, `BackendOpencode`, `BackendCodex`, `BackendPi`) inside backticks.
- [ ] **Step 2: Run** it. Expect FAIL (`codex` and `pi` are missing).
- [ ] **Step 3: Edit AGENTS.md.**
- [ ] **Step 4: Run** `go test ./samples/ -run TestAgentsDoc`. Expect PASS.
- [ ] **Step 5: Commit** with `docs(agents): backend vocabulary, vendor-family rule, pipeline stages`.

---

## Phase 6 — Full gates

### Task 27: Full gate run

- [ ] **Step 1:** Run `gofmt -l .`. Expect empty output.
- [ ] **Step 2:** Run `go test ./...`. Expect PASS, including `go test ./samples/ -run TestSamplesValidate -v` with every sample listed and passing.
- [ ] **Step 3:** Run `make test-scripts`. Expect shellcheck to be clean, every bats suite to show 0 failures, and every `_test.sh` to report "passed".
- [ ] **Step 4:** Run `make test-install` and `make test-release`, which are the existing bats and shellcheck targets. Expect PASS. `make samples-tarball` must include `_lib` and the seven new folders: check with `tar -tzf dist/arbitron_samples_*.tar.gz | grep -E '^\./(_lib|ideate|spec|design|plan|implement|harden|feature-pipeline-next)/' | head`.
- [ ] **Step 5:** Run `make test-gate` (the CI gate, which now includes `test-scripts`). Expect PASS.
- [ ] **Step 6:** If any gate fails, fix the root cause in the owning task's files and re-run from Step 1. Then commit with `chore: fp-next workflow content passes full gates` (only if a fix was needed).
