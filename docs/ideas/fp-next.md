# feature-pipeline-next — a composable pipeline built from standalone stage workflows

**Status:** idea, design agreed in conversation (2026-10-23); not started.
**Written:** 2026-10-23
**Scope:**
- A new `feature-pipeline-next` parent workflow: a short chain of awaited `spawn_workflow` stages.
- Six new standalone stage workflows: `ideate`, `spec`, `design`, `plan`, `implement`, `harden`.
- Changes to `ralph-loop` and `pr-review`.
- A deterministic test-tampering gate.
- Mutation testing across languages.
- Two engine prerequisites: an explicit `inputs` map on `spawn_workflow`, and a shared `@lib` folder.

**Explicitly out of scope:**
- The other practitioner ideas surveyed (hidden acceptance scenarios, parallel implementation over the dependency graph, retro stage, running-app evaluator, spec ambiguity detection, best-of-N). These are deferred, not rejected (§7).
- Changing the legacy pipeline.
- The `1m` context cleanup ([1m-cleanup.md](1m-cleanup.md)).

---

## 1. Why this exists

`legacy-feature-pipeline` is one 66-node graph. Every concern lives inline: intake, fuzzy resume detection, the spec and design brainstorms, alignment, planning, the dependency gate, baseline, the implementation loop, a hand-copied ralph loop, and push/PR. That causes three problems:

- **It can't be run in pieces.** "Just re-plan this branch" or "just refine this branch" means rerunning the whole pipeline in resume mode. Resume relies on fuzzy doc detection, which is what lost 8h on run 42e12d54.
- **Shared processes drift.** The inline ralph copy and the standalone `ralph-loop` have diverged. Scripts like `notify-teams.sh`, `checkpoint-push.sh` and `init-workspace.sh` are copy-pasted per workflow.
- **Improvements land once, in one place.** A fix to ralph in the pipeline doesn't reach standalone ralph runs, and the reverse is also true.

Arbitron is now composable. `spawn_workflow` starts a child and can wait for it. `parallel_group` fans out and gathers. So the pipeline can become **a few big steps, each a standalone workflow**, with `feature-pipeline-next` as just one way of chaining them.

The rebuild also adds **an ideation stage** in front: the open-ended jam session that is currently run by hand in Claude Code. It adds **two quality gates** the pipeline lacks: a test-tampering gate and mutation testing.

## 2. What exists today (grounding)

- **Spawn inputs are LLM-only.** `spawn_workflow` always has an LLM prepare child inputs from parent variables (`domain/engine/child_input_preparer.go:75`, called from `domain/engine/executor_spawn.go:323`). There is no direct mapping. The code notes that "an author-declared allowlist of exposed parent variables" is backlogged. LLM structured output is not enforced on the claude-cli path, so every exact-string handoff (repo URL, branch, slug, path) is a coin flip.
- **`parallel_group` already has the right shape.** Its tasks take `inputs` of typed literals and `{{templates}}` (`domain/workflow/group.go:81`), resolved by `variable.ResolveTypedMap` (`domain/engine/group_task_vars.go:62`).
- **A child gets its own workspace** (`domain/engine/executor_spawn.go:364`). State between stages must pass through git and explicit values.
- **File access is confined per workflow.** `{{include:}}` and script resolution can't leave the workflow's own folder (`domain/engine/template_include.go:72`, `adapter/filesystem/file_reader.go:68`). Lookup is the owner's private partition, then shared.
- **Templates and scripts are read live by workflow name.** `loadTemplate` and `ResolvePath` take `wf.Name` from the instance's frozen snapshot (`domain/engine/engine.go:1298`). Renaming a workflow strands its running instances' file lookups. This was found on 2026-10-23 when renaming `feature-pipeline` → `legacy-feature-pipeline` under live instance `d77ebf0e`. The legacy files were restored as a plain folder at `users/<owner>/feature-pipeline/` to keep it running.
- **`ralph-loop` always cuts a new branch.** It creates `dev-<slug>-<date>-<id>` from `base_branch` unless that branch already carries `docs/reference/focus-areas.md` (`ralph-loop/scripts/init-workspace.sh:131-151`). It opens its PR against `base_branch`, or against `pr_base`.
- **`pr-review`** takes `repo`, `pr_number` and `fix_scope` (`must | must-should | all-but-defer | none`). On 2026-10-23 it was remapped: the manager to `reviewer-codex-high` (GPT 6.1 Sol, high) and the senior engineer to `coder-sonnet5-high` (Sonnet 5.5, high). Verify stays on `approver-codex-high`. This is in the live manifest only, and `samples/pr-review` is not yet synced.
- **Interactive nodes must not be in loop bodies, and neither may awaited spawns** (AGENTS.md). Neither appears in a loop here.
- **Idea docs** (`docs/ideas/*.md`) follow a fixed skeleton: a status/written/scope/out-of-scope header, then why, what exists today (with file:line), the idea, the proposal, tensions, decisions locked, non-goals, open questions, sequencing, and related docs.

## 3. The idea

```
feature-pipeline-next
  intake → detect ─┬→ [ideate]* → spec → design → plan → implement → harden → ralph-loop → pr-review → done
                   └→ (start at the first stage the manifest says is missing; start_at overrides)
  * skipped when idea_path is given
  every stage: awaited spawn_workflow, explicit inputs map; failure → notify-failed → stop
```

**Every stage is a standalone workflow first.**
- It takes `repo_url` and `branch` (plus a few stage inputs), clones its own workspace, does its work, pushes, and records what it produced in a manifest on the branch.
- You can run any stage on any branch without the parent.
- The parent is thin: intake, detection, and a straight chain of spawns. It has no loops.

**Shared state is the git branch plus one manifest file.** Nothing important lives only in a stage's workspace.

## 4. Proposal

### 4a. Stage workflows and contracts

**Common inputs:** `repo_url`, `branch`, `slug`, `teams_webhook_url`.

| Workflow | Extra inputs | Writes | Returns (`childOutput`) | Interactive | Profile direction |
|---|---|---|---|---|---|
| `ideate` | `base_branch`, `topic?` | `docs/ideas/<slug>.md`, creates branch `dev-<slug>-<date>-<id>` | `slug`, `branch`, `idea_path` | yes | `interviewer-opus-high` |
| `spec` | `idea_path` | `docs/specs/<date>-<slug>-spec.md` | `spec_path` | yes | port legacy `brainstorm-spec` |
| `design` | `spec_path`, `idea_path` | `docs/designs/<date>-<slug>-design-phase-N.md` (+ alignment) | `design_paths[]` | yes | port legacy `brainstorm-design` + `align-designs` |
| `plan` | `spec_path`, `design_paths` | `docs/plans/<slug>/…` | `plan_paths[]`, `deps_resolved` | only on unresolved deps | port legacy produce/align plans, deps gate, `resolve-deps` |
| `implement` | `plan_paths` | code commits, `.arbitron/pipeline/<slug>.baseline.json` | `status: done\|blocked`, `blocked_reason?` | no | port legacy baseline + impl loop, plus tamper gate |
| `harden` | `base_ref` | strengthened tests, `.arbitron/pipeline/<slug>.harden.json` | `score`, `gate_passed`, `survivors_path` | no | new (§4d) |
| `ralph-loop` | `base_branch`=feature branch, `pr_base`=real base, `in_place=true`, `focus_seed`=survivors | refinement commits, PR | `pr_number`, `pr_url` | no | existing, plus changes (§4f) |
| `pr-review` | `repo`, `pr_number`, `fix_scope` | review comments, pushed fixes | verdict | no | existing (remapped) |

Spec and Design are **two separate stages**. Design can be redone without redoing Spec, and Spec can run alone, replacing legacy `spec_only`. Both receive the idea doc. Spec uses it as clarification material for a product-only spec. Design uses it for its implementation shape.

### 4b. Parent: `feature-pipeline-next`

- **Inputs:**
  - `repo_url`
  - `base_branch` (default `main`)
  - `topic?`
  - `idea_path?`: a file **committed on the branch**. If given, Ideate is skipped. If given but missing, the run fails fast.
  - `branch?`: resume onto an existing feature branch.
  - `start_at?`: one of `ideate|spec|design|plan|implement|harden|refine|review`.
  - `review_fix_scope` (default `all-but-defer`)
  - `teams_webhook_url`
- **Intake.** With `idea_path`, intake derives `slug` from the filename and creates the feature branch. Otherwise Ideate creates it.
- **Detect.** Reads `.arbitron/pipeline/<slug>.json` on the branch and starts at the first stage in order with no successful entry. `start_at` overrides detection. Detection never guesses from filenames.
- **Spawns.** Each stage is an awaited `spawn_workflow` with a full explicit `inputs` map, so no LLM input preparation happens anywhere in the pipeline.
- **Routing.**
  - A stage `failure` goes to `notify-failed`: a Teams card with the stage, the child instance link and the reason, then stop.
  - Implement `blocked` gets the same handling, with the blocked reason.
  - A Harden miss is **not** a failure (§4d).

### 4c. Manifest

`.arbitron/pipeline/<slug>.json`:

```json
{ "slug": "...", "base_branch": "main", "branch": "dev-...",
  "stages": [ { "stage": "spec", "status": "done", "outputs": {"spec_path": "..."},
                "commit": "abc123", "finished_at": "..." } ] }
```

- Each stage appends one entry, written only when it finishes. Implement may write `status: "blocked"`, which detection treats as not done.
- Stages themselves always do their work when called. A standalone run never silently skips.

### 4d. New stages

**`ideate`**
1. `init` clones `base_branch`.
2. `jam` is an `interactive_agent` on `interviewer-opus-high` (Opus 5.5, high, native 1M context). Its template has two acts:
   - **Jam:** no skills, no structured questioning, no steering toward decisions or a plan. Go broad and deep, follow tangents, and read the repo to ground the talk. It opens on `topic`, or with "what are we talking about?".
   - **Write-up**, only on request: propose a slug, write `docs/ideas/<slug>.md` from `{{include:@lib/idea-doc-skeleton.md}}`, revise until approved, then write `.arbitron/ideate-result.json` containing `{slug}`. The user ends the session.
3. `finalize` (script) validates the kebab-case slug and that the doc exists. It then creates the branch, commits, pushes, and writes the manifest. If no doc was written, the stage fails with "no idea doc written". Pushing is a script rather than agent work because Codex interactive nodes have no egress, and this keeps the stage backend-neutral.

**`harden`**: mutation testing on changed code
1. `init` clones the branch.
2. `detect` maps each language to its tool:

   | Language | Tool |
   |---|---|
   | Go | gremlins |
   | C# | Stryker.NET |
   | TS/JS | StrykerJS |
   | Python | mutmut |
   | Java | PIT |
   | Rust | cargo-mutants |

   - **Unsupported language:** recorded as `skipped` with the reason, and the stage continues.
   - **Supported but the tool isn't installed:** the stage **fails** with install instructions. That's a setup problem, not a quality signal.
3. `mutate` runs only on code changed since `base_ref`, using each tool's diff mode (Stryker `--since`, gremlins `--diff`, cargo-mutants `--in-diff`, PIT SCM mode, mutmut path lists). Output is normalized to `{killed, survived, no_coverage, mutants[]}`. Timeout is about 1h.
4. **Loop, up to 3 rounds** (`agent_call` → script → `conditional`; no interactive nodes):
   - **strengthen:** `coder-sonnet5-high` writes tests for the survivors. It may change test files only; any source-file change fails the round. It may claim a mutant is equivalent, with a justification.
   - **judge-equivalent:** `approver-codex-high` approves or rejects each claim. The doer and the approver are always different model families.
   - **rescore:** re-runs the remaining survivors only, then the tamper gate runs.
   - **gate:** exits when the score is at least 80% or after round 3.
5. **Score** = killed ÷ (total − approved equivalents). Uncovered mutants count as survivors.
6. **Finish:** commit, push, and write `harden.json`. Return `gate_passed` and `survivors_path`.

**On a miss (below 80% after 3 rounds):** the pipeline **continues to ralph-loop**. The survivors are seeded as named focus items via `focus_seed`, and the PR and Teams notification show the shortfall.

### 4e. Test-tampering gate (`@lib/tamper-gate.sh`)

- **Deterministic.** Input is the commit before a pass and the head after it.
- **Checks:**
  - deleted or renamed test files
  - fewer assertions per test file
  - new skip markers (`t.Skip`, `[Ignore]`/`Skip=`, `it.skip`/`xit`, `@pytest.mark.skip`, `@Disabled`, `#[ignore]`)
  - test or mutation config edits that exclude paths or lower thresholds
  - CI config edits
  - hard-coded expected values copied from test inputs. This one is heuristic and only warns.
- **Clean:** the pass proceeds, at no cost.
- **Findings:** a cross-family judge (`approver-codex-high`) rules on each finding, because a plan may legitimately delete a feature and its tests. If any ruling is illegitimate, the pass doesn't count, the test changes are reverted to the pre-pass commit, and the findings become must-fix input to the next pass.
- **Where it runs:** after each `implement` task, after each `ralph-loop` pass, and after each `harden` round.

### 4f. Changes to existing workflows

**`ralph-loop`**
- New input `in_place`: refine `base_branch` directly, with no new branch, and open the PR into `pr_base`.
- New input `focus_seed`: a path to items the seeder must include as named focus areas.
- The tamper gate runs in each pass's verify step.
- These changes benefit standalone ralph runs too.

**`pr-review`**: sync the 2026-10-23 profile remap into `samples/pr-review`. The pipeline passes `fix_scope` from `review_fix_scope` (default `all-but-defer`).

### 4g. Engine prerequisite 1: explicit `inputs` on `spawn_workflow`

- **Config:** `"inputs": {"branch": "{{intake.branch}}", "design_paths": "{{design.childOutput.design_paths}}", "in_place": true}`. Same semantics as `parallel_group` task inputs, resolved with `variable.ResolveTypedMap`. A lone `{{ref}}` keeps its type, so arrays and objects pass intact. `workspace` is reserved.
- **Validation:** keys must be declared inputs of the child. This is checked at validate time when the child can be resolved, and always at spawn time.
- **Fail closed:** a mapped value that won't resolve fails the node. It is never passed to the LLM to guess.
- **LLM preparer:** runs **only** when a required input is unmapped and has no default, and only for those inputs. Mapped values always win. A fully mapped spawn makes no LLM call.
- **Dashboard:** the spawn node inspector shows the mapping.

### 4h. Engine prerequisite 2: shared library `@lib`

- **Location:** a reserved `_lib` folder in each scope root (`users/<owner>/_lib`, `shared/_lib`).
- **Resolution:** a path starting `@lib/`, in `{{include:}}`, `templateFile`, `scriptFile` or `orchestrationFile`, resolves private-then-shared through the existing `safeJoin` containment. `@lib/../x` and symlinks that escape the folder are rejected.
- **Read live, like templates.** A library fix reaches running instances. That's deliberate, and the same as templates today.
- **Management:** the MCP `workflow_list_files`, `workflow_read_file`, `workflow_write_file` and `workflow_delete_file` tools accept `@lib` as the workflow name plus an optional `scope` (`private` by default; `shared` needs admin). List ignores `scope` and returns both. REST has no library routes: it only reads `@lib` files through the shared file reader (private then shared). `_lib` is a reserved workflow name, excluded from `workflow_list`, and can't be created as a workflow.
- **Shipping:** `samples/_lib/` ships with the samples and the installers.
- **First contents:**
  - `idea-doc-skeleton.md`
  - `tamper-gate.sh`
  - manifest read/append helpers
  - `notify-teams.sh`
  - `checkpoint-push.sh`
  - the clone/init helper

## 5. Design tensions and gotchas

- **Renames strand running instances** (§2). Build under `feature-pipeline-next`. Only after `d77ebf0e` finishes: delete the restored `users/<owner>/feature-pipeline/` folder, then rename `-next` → `feature-pipeline`. The restored folder's `workflow.json` says `legacy-feature-pipeline`, so `workflow_list` shows that name twice until then.
- **Retrying a failed child doesn't wake the parent.** The parent already took `failure`, so resuming is always a parent rerun, and detection takes it from there.
- **`@lib` is live-read, which works both ways.** A broken library edit breaks every running instance that reads it next. Library scripts need tests, and they should change through PRs.
- **Mutation tools must exist where agents run:** locally and in the work Azure Container Apps image. This is the same class of gap as the fresh-install model profiles.
- **Mutation runtime.** Diff-scoped runs keep it tolerable, but a big change can still take an hour or more per round.
- **Tamper-gate false positives.** Legitimate deletions go to the judge, so the gate never hard-blocks on its own. The heuristic check only warns.
- **Equivalent-mutant claims are a gate bypass if unchecked.** That's why a different family always judges them.
- **Interactive stages and humans.** `ideate`, `spec`, `design` and `plan` (on deps) wait for a human. Automated testing must not answer them. It should exercise later stages via `start_at` on pre-seeded branches.

## 6. Decisions locked

- Stages are **standalone-first** workflows. The parent only chains them.
- **Ideate** is a new first stage on `interviewer-opus-high`, skipped when `idea_path` (committed on the branch) is given. It takes an optional `topic`.
- **Spec and Design are separate stages.** Both receive the idea doc.
- **Resume** works by manifest detection plus a `start_at` override.
- **The test-tampering gate** runs in implement, ralph-loop and harden.
- **Mutation testing** across languages (Stryker.NET for C# and peers): strengthen for up to 3 rounds, then gate at 80% on changed code. A miss continues into ralph with the survivors as focus items.
- **`spawn_workflow` gets an explicit `inputs` map**, with LLM preparation only for unmapped required inputs.
- **Shared code lives in an engine-level `@lib` folder.** No copy-sync, no utility child workflows.
- **`pr-review`:** manager on GPT 6.1 Sol high, senior engineer on Sonnet 5.5 high, verify unchanged. Default `fix_scope` is `all-but-defer`.

## 7. Non-goals (for now)

These are deferred from the practitioner survey (2026-10-23), not rejected:
- hidden acceptance scenarios
- parallel implementation from the dependency graph
- retro/lessons stage
- running-app evaluator
- spec ambiguity detection
- best-of-N on risky tasks
- sampled fast test runs
- scope-creep diffing

Also not in scope:
- changing `legacy-feature-pipeline`
- per-stage cost dashboards

## 8. Open questions

1. Should `harden` also run inside `ralph-loop`, or just once before it?
2. Should the `@lib` MCP surface be a dedicated `lib_*` tool family later?
3. Stryker.NET needs a solution or project context. Where do multi-solution C# repos get their target list?
4. Should the per-language mutation tool registry live in `@lib` as data (YAML) so new languages don't need script edits?

## 9. Suggested sequencing

1. **Engine: `spawn_workflow` `inputs` map.** Validation, executor and preparer narrowing, dashboard inspector. TDD.
2. **Engine: `@lib`.** Resolution in the file reader for all four file kinds, the reserved name, MCP file tools, and samples/installer packaging. TDD.
3. **`samples/_lib` contents:** `tamper-gate.sh` (bats-tested per language), manifest helpers, notify, checkpoint-push, init, idea skeleton.
4. **`ralph-loop`:** `in_place`, `focus_seed`, tamper gate.
5. **Stage workflows:** `ideate`, `spec`, `design`, `plan`, `implement`, `harden`.
6. **`pr-review` samples sync.**
7. **Parent `feature-pipeline-next`.**
8. **Deploy and verify:** rebuild and hot-swap the launchd service, install the workflows and `_lib` live, validate everything, and smoke-test on toy Go and C# repos via `start_at`.
9. **Cutover**, after `d77ebf0e` finishes: delete the restored folder, then rename to `feature-pipeline`.

## Appendix — related

- [1m-cleanup.md](1m-cleanup.md)
- `docs/specs/2026-07-27-workflow-spawn-node-spec.md`, `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md`
- `~/.arbitron/workspaces/users/<owner>/legacy-feature-pipeline/`: source templates and scripts to port.
- Practitioner sources: Anthropic, [harness design for long-running apps](https://anthropic.com/engineering/harness-design-long-running-apps); Kent Beck, [augmented coding](https://newsletter.kentbeck.com/p/augmented-coding-beyond-the-vibes); Meta, [LLM mutation testing](https://engineering.fb.com/2025/09/30/security/llms-are-the-key-to-mutation-testing-and-better-compliance/).
