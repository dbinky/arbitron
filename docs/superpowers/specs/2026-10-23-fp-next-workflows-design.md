# fp-next workflow content — `@lib` contents, ralph-loop changes, stage workflows, pr-review sync, and `feature-pipeline-next`

**Status:** design approved 2026-10-23 by the orchestrator on the user's behalf. All 21 clarifying answers were accepted as recommended, review rulings R1–R14 are folded in, and pre-flight rulings P1–P15 are folded in. Ready for implementation planning.
**Parent idea:** [docs/ideas/fp-next.md](../../ideas/fp-next.md) §3–§6, §9 steps 3–7.
**Depends on:** [2026-10-23-fp-next-engine-prereqs-design.md](2026-10-23-fp-next-engine-prereqs-design.md) (sub-project A): spawn `inputs` and `@lib` resolution. It lands first.
**Classification:** architectural. Seven new workflows, a shared script library, a changed `ralph-loop` input contract, and a reworked sample test harness.

This spec is organized in implementation order, from goal step 3 to step 7. The harden tooling spike (§5.6.1) is **implementation-plan task 1**, ahead of step 3.

---

## 1. Purpose and success criteria

**Outcome.** The legacy 66-node pipeline is replaced by six standalone stage workflows (`ideate`, `spec`, `design`, `plan`, `implement`, `harden`) plus the existing `ralph-loop` and `pr-review`, chained by a thin parent, `feature-pipeline-next`. Shared scripts live once in `samples/_lib`. Two new quality gates are added: a deterministic test-tampering gate and diff-scoped mutation testing.

**Success criteria.**

- **Standalone stages.** Every stage runs on any branch without the parent. A "re-plan this branch" run needs only `repo_url`, `branch` and `slug`, because upstream paths fall back to the manifest.
- **No LLM handoffs in the parent.** The parent makes zero LLM input-preparation calls. Every spawn maps every key, and validation reports no `SPAWN_PREPARED_INPUTS` warning.
- **Resume.** A parent rerun on a branch resumes at the first stage that the manifest does not record as `done`, and `start_at` overrides that.
  - Ideate never runs on resume (R1).
  - Detection never guesses from filenames.
- **Deterministic tamper gate.** It runs after every implement task, every ralph pass and every harden round.
  - Its judgeable findings are judged by the model family opposite to that pass's doer.
  - Equivalent-mutant claims are judged the same way.
- **Loop safety.** No interactive node and no awaited spawn sits on a cycle, and the parent graph is acyclic.
- **Failures are visible.** Every parent failure, whether a stage failure or a node error, sends a Teams card and ends the parent `failed` (R10).
- **Tested scripts and samples.**
  - Every new or changed script is shellcheck-clean and tested.
  - `make test-scripts` runs in `test-gate` and in CI.
  - Every sample validates clean against the real `@lib` resolver.

## 2. Decisions (locked)

| # | Decision |
|---|---|
| Q1 | **Stage contract.** Every stage ends in the script node `finish`, an `@lib/manifest-append.sh` call (§4.6). Its output is the stage's return contract. The parent reads `{{<spawn-id>.childOutput.finish.status}}` and `{{<spawn-id>.childOutput.finish.outputs.<key>}}`. |
| Q2 | **Parent data flow.** Each spawn is preceded by `pre-<stage>`, an `@lib/manifest-read.sh` call that fetches the branch, reads the manifest, checks prerequisites (failing closed) and emits a typed `inputs` object. Spawns map `{{pre-<stage>.inputs.<k>}}` as lone references, so arrays and numbers keep their types. Only the strings `branch` and `slug` (plus `work_dir`, `init_mode` and `repo_slug`) become top-level variables, through transforms fed by script outputs that never fail open. Transform `set` turns arrays into strings and leaves unresolved refs as literals, so arrays never pass through a transform. |
| Q3 | **Standalone fallback.** Upstream path inputs on stages are optional, resolved in this order: an explicit input, then the latest `done` manifest entry, then a fail with a named message. |
| Q4 | **Provided idea.** A given `idea_path` must exist on `branch`, or on `base_branch` when no branch is given. If it is missing, fail fast. A given `idea_path` is **always** recorded as a provided-idea `ideate` entry and passed to `pre-spec` and `pre-design` through `--explicit` (R1). In idea mode, intake creates `dev-<slug>-<date>-<parentShortId>`. |
| Q5 | **refine/review entries.** These are written by the parent (`record-refine`, `record-review`). `pr_number` is a number. `ralph-loop` and `pr-review` stay pipeline-agnostic. |
| Q6 | **Tamper in ralph-loop.** `pass-mark` sits between `select-work` and `work-router`. One shared chain runs after `assess-*` and before `record-pass-commit`. A void restores the gated files and the approval tracking files, and writes must-fix items into `gaps-identified.md`. `record-pass-commit` scores a void as no progress. |
| Q7 | **Judge family.** The judge is from the vendor family opposite to the pass's doer (§3.4). This **overrides** the idea doc's literal `approver-codex-high`, and a sample test asserts it for every tamper and equivalence judge. |
| Q8 | **ralph-loop extras.** `baseline_path` imports the committed baseline. derive-scope uses `pr_base`. An open PR is reused. `in_place` requires a distinct `pr_base`. The PR shows any harden shortfall. The branch-point baseline is committed before the first pass (R3). |
| Q9 | **focus_seed.** Survivors are grouped per file under `harden:<path>` ids, capped at 25 items. `verify-ralph-seed` CHECK 5 fails closed if an id is missing. The seed is honoured on a fresh seed and on replan, and ignored on a plain resume. |
| Q10 | **implement.** The deterministic `count-plan-steps` node gates completion. Hitting the iteration cap with work left means `blocked`. A committed baseline is reused and never re-measured. |
| Q11 | **plan.** The deps loop is unrolled into one resolve pass, so there is no cycle. Unresolved deps give `blocked` and `deps_resolved=false`. Plans go to `docs/plans/<slug>/<date>-phase-N-task-M.md`, and templates get explicit paths. |
| Q12 | **Interactive stages.** A finalize script verifies outputs on disk at a target path computed by init, and the sentinel is not trusted. Profiles: spec uses `interviewer-codex` (kept, proven in live run d77ebf0e), design uses `interviewer-opus-high`, align uses `reviewer-codex-high`. The spec stage keeps the slug it is given. |
| Q13 | **Parent failure.** `notify-failed` or `notify-step-failed` runs, then `halt` (exit 1), so the parent instance ends `failed`. Recovery is a parent rerun. |
| Q14 | **harden.** The test command comes from the baseline, or from the registry default when there is none (R6). Adapters and the registry JSON live in the harden workflow. C# targets come from the ProjectReference graph. Spike first, with target-list fallback. Scoring is in §5.6.4. A source change or a history rewrite voids the round. The mutant key is (file, line, mutator, replacement). |
| Q15 | **tamper-gate failure modes.** Exit 2 is reserved for bad inputs. A history rewrite inside a pass is a non-judgeable **void** (`history_rewritten`) (R4). A missing or malformed verdict voids the pass. The heuristic only warns. Any baseline edit inside a pass is a finding. Findings are de-duplicated per (file, line) across kinds, with the most specific kind winning (R9, P6). |
| Q16 | **Script tests.** New scripts get `<name>_test.bats` with fixtures built in `setup()`, and existing scripts extend their `_test.sh`. `make test-scripts` is wired into `test-gate` and CI. No `isTestFile` rule change is needed (§7.1). |
| Q17 | **ralph-loop and `@lib`.** ralph-loop uses `@lib/checkpoint-push.sh` and `@lib/notify-teams.sh` and deletes its own copies. Its `init-workspace.sh` stays local. |
| Q18 | **Profiles.** Only existing aliases are used. Following vs pinned is admin configuration. Each new workflow gets a `PROFILES.md`. |
| Q19 | **Layout.** Positions come from arrange-on-save (PR #85), with a simple fallback grid in the samples. |
| Q20 | **pr-review sync.** Two profile swaps, the verify-fix description, and the `PROFILES.md` rows. |
| Q21 | **Sample tests.** Samples are discovered rather than listed. `TestSamplesValidate` uses A's resolver. Lint is clean. The parent is acyclic. Every `errorNodeId` resolves. |
| Names | The workflow names are those §4a of the idea doc locks: `ideate`, `spec`, `design`, `plan`, `implement`, `harden`, `feature-pipeline-next`. The risk of global-name collisions is accepted. |

## 3. Shared conventions

### 3.1 Layout on disk

- **New sample folders:**
  - `samples/_lib/`
  - `samples/{ideate,spec,design,plan,implement,harden}/`
  - `samples/feature-pipeline-next/`
- **Workflow folder contents:** each workflow folder holds `workflow.json` and `PROFILES.md`, plus `templates/`, `orchestration/` and `scripts/` as needed.
- **Workspace layout:**
  - `{{workspace}}/src` is the clone.
  - `{{workspace}}/work` is the worktree that agents work in (`./work`).
  - `{{workspace}}/.manifest-wt` is manifest-append's private worktree (§4.6).
  - Scratch goes under `{{workspace}}/tamper/` and `{{workspace}}/harden/`, never inside `./work`.
- **`make install-samples`** copies every `samples/*/` directory, `_lib` included, using a glob (`cp -R samples/*/ $(DEST)/`) rather than an explicit list (P9).
- **Common stage inputs:**

  | Input | Type | Required |
  |---|---|---|
  | `repo_url` | string | yes |
  | `branch` | string | yes (not on `ideate`) |
  | `slug` | string | yes (not on `ideate`) |
  | `base_branch` | string | no, default `""`. Recorded as the manifest's top-level `base_branch` when this stage's `finish` creates the manifest (P2). `ideate` keeps its own default of `main`. |
  | `teams_webhook_url` | string | no, default `""` |

- **Secrets:** no credential-bearing input carries a committed default. `TestSampleInputsCarryNoSecrets` enforces this.

### 3.2 Node and argv rules

- **Push only from scripts.** Agents and interactive sessions commit only. Pushes come from `@lib/checkpoint-push.sh` or `@lib/manifest-append.sh`. This keeps stages backend-neutral, because codex interactive has no egress.
- **Route only on deterministic evidence.**
  - Neither `agent_call` nor `llm_call` on claude-cli enforces `outputSchema`. Routed fields therefore come from verdict files, script counts, or input counters.
  - LLM-emitted fields are read only where they are advisory and absence reads as false (`impl-assess.all_remaining_blocked`).
- **Conditionals are first-match.** The deterministic evidence comes first, and each gate's `description` says why.
- **Loop safety.** No interactive node and no `wait_for_child` spawn sits on a cycle (§7.2).
- **Agent cwd** is `{{workspace}}`. Templates reference `./work/...`. Shared text comes in through `{{include:@lib/...}}`.
- **Script node shape.** Every script node is `{"scriptFile": …, "interpreter": "/bin/sh", "args": [ … ], "timeoutSeconds": N}`.
  - **Exact argv (R12).** The `args` arrays in this spec are exact and are written as JSON. No shell runs, so quotes are not part of any argument. Each element is resolved with `variable.Resolve` (arrays and objects become JSON text) and passed as one argv entry.
  - **jq filter arguments** must not contain the character pairs `{{` or `}}`, because `variable.Resolve` would treat them as template markers. Write nested objects with a space, for example `{a: {b: .c} }`. A sample test asserts this (§7.2).
  - **Array-valued inputs** never travel through argv. Scripts read them from the stdin snapshot by key.
- **Error routing (R10).** The engine supports `"errorBehavior": {"type": "route", "errorNodeId": "<id>"}` (`domain/engine/engine.go`, `case "route"`). A failing node jumps to `errorNodeId` with no edge required. An empty `errorNodeId` fails the instance.
  - Validation checks only the behaviour type (`validateErrorBehaviors`), not that the target exists, so a sample test asserts every `errorNodeId` names a real node (§7.2).
  - The workflow-level `defaultErrorBehavior` uses the same shape.
- **Fallback layout grid.** Positions use a main column at `x=0` with `y += 140`, and branches at `x=±420`. Arrange-on-save replaces them. No `layout.json` is committed.

### 3.3 Manifest schema (`.arbitron/pipeline/<slug>.json`, committed on the branch)

```json
{
  "slug": "age-filtering",
  "base_branch": "main",
  "branch": "dev-age-filtering-2026-10-23-1a2b3c4d",
  "stages": [
    { "stage": "spec", "status": "done",
      "outputs": { "spec_path": "docs/specs/2026-10-23-age-filtering-spec.md" },
      "commit": "abc123…", "finished_at": "2026-10-23T18:04:11Z",
      "instance_id": "1a2b3c4d-…", "workflow": "spec" }
  ]
}
```

- **Stage order** (for detection): `ideate, spec, design, plan, implement, harden, refine, review`.
- **Status** is `done | blocked`.
  - A failed stage writes no entry.
  - Entries are append-only, and the latest entry per stage wins.
  - A harden run with no runnable language is `done` with `outputs.result = "skipped"`. It is not a separate status: detection must count it as finished, or every resume would rerun it.
- **Merged outputs view.** The outputs of each stage's latest `done` entry, folded in stage order (a later stage wins on a key collision), plus the manifest's top-level `slug`, `base_branch` and `branch`.
- **Output keys:**

| Stage | Output keys (type) |
|---|---|
| `ideate` | `slug` (string), `branch` (string), `idea_path` (string), `provided` (bool, present only on provided-idea entries) |
| `spec` | `spec_path` (string) |
| `design` | `design_paths` (array of string) |
| `plan` | `plan_paths` (array of string), `deps_resolved` (bool), `deps_gaps` (string, the JSON text of `deps-assess-2.gaps` when blocked, else `""`) |
| `implement` | `status` (`done`/`blocked`), `blocked_reason` (string), `baseline_path` (string) |
| `harden` | `result` (`scored`/`skipped`), `skip_reason` (string), `score` (number or null), `gate_passed` (bool), `survivors_path` (string), `focus_seed_path` (string, `""` when the gate passed or was skipped), `harden_report_path` (string) |
| `refine` | `pr_number` (number), `pr_url` (string) |
| `review` | `push_ok` (bool), `fix_scope` (string) |

- **Sibling artifacts:**
  - `.arbitron/pipeline/<slug>.baseline.json` (implement)
  - `.arbitron/pipeline/<slug>.harden.json` and `.arbitron/pipeline/<slug>.survivors.json` (harden)
  - `docs/reference/test-baseline.json` (ralph-loop)

### 3.4 Profiles and the vendor-family rule (R14)

**Vendor family** is decided by the profile's **model id**, never by its backend. Pi and opencode are multi-provider. The rule:

1. Strip any provider prefix up to and including the last `/`, so `openai-codex/gpt-6.1-sol` becomes `gpt-6.1-sol` and `anthropic/claude-opus-5-5` becomes `claude-opus-5-5`. Bedrock-style ids drop the region and vendor qualifiers, so `us.anthropic.claude-…` becomes `claude-…`.
2. Classify the result:
   - `claude-*` → **Claude**
   - `gpt-*`, or `o<digit>…` → **GPT**
   - anything else → its own family, named by the first `-` segment, for example `glm`, `deepseek`, `kimi`
3. A profile with an empty model inherits the deployment default. It is **unclassifiable** and may not be used as a doer or judge in a cross-family pair.

Aliases used by the new workflows, and their families (all exist live):

| Family | Aliases (model) |
|---|---|
| Claude | `interviewer-opus-high`, `coder-opus-high`, `coder-opus-medium`, `planner-opus-high`, `assessor-opus-high`, `approver-opus-medium` (claude-opus-5-5); `coder-sonnet5-high`, `coder-sonnet5-medium`, `assessor-sonnet5-high` (claude-sonnet-5-5) |
| GPT | `interviewer-codex`, `reviewer-codex-high`, `approver-codex-high`, `coder-codex-high` (gpt-6.1-sol, codex backend); `critic-codex-high` (gpt-6-astra); **`coder-pi-sol-high` (`openai-codex/gpt-6.1-sol`, pi backend)** |

- **`PROFILES.md` table.** Each workflow's `PROFILES.md` has the columns `alias | kind | backend | model | effort | context | tools | used by`. The family test computes the family from the `model` column using the rule above. There is no hand-written family column that could drift.
- **Profile notes.** `PROFILES.md` also notes two things:
  - Following vs pinned is an admin profile property.
  - Each child run pins its models when it is spawned.
- **AGENTS.md deliverable** (part of this work). The "Agent backend vocabulary" list becomes `claude-code`, `opencode`, `codex`, `pi`, matching `domain/workflow/workflow.go`. Add one sentence: for cross-family rules, family is the model vendor, not the backend.

---

## 4. Step 3 — `samples/_lib` contents

**Script conventions.** Lib scripts are self-contained and never source a sibling, because `@lib` resolves private-then-shared per file (A §4.2). Every lib script:

- reads the snapshot JSON on stdin
- writes exactly one JSON object to stdout and diagnostics to stderr
- is `#!/bin/sh`, POSIX, and shellcheck-clean
- uses only `git`, `jq`, `sed`, `awk`, `grep -E`, and `curl` (`notify-teams` only)

**Contents.** A ships `README.md`. This step adds:

| File | Kind | Purpose |
|---|---|---|
| `idea-doc-skeleton.md` | include | The idea-doc skeleton. |
| `init-workspace.sh` | script | Clone/init helper. |
| `checkpoint-push.sh` | script | Byte-identical copy of the current shared script. |
| `notify-teams.sh` | script | Byte-identical copy of the current shared script. |
| `pass-mark.sh` | script | Records a pass's start commit. |
| `manifest-read.sh` | script | Manifest read, detection, and stage-input view. |
| `manifest-append.sh` | script | Appends a stage entry, commits in its own worktree, pushes. |
| `finalize-docs.sh` | script | Verifies and commits doc outputs. Removes three copies across ideate, spec and design. |
| `tamper-gate.sh` | script | Deterministic tamper check, plus `--classify`. |
| `tamper-judge.md` | template | Judge instructions and the verdict-file contract. |
| `tamper-verdict.sh` | script | Applies a verdict: void or stand. |
| `*_test.bats`, `*_test.sh` | tests | Not seeded (§7.1). |

### 4.1 `idea-doc-skeleton.md`

The skeleton follows the fixed shape of `docs/ideas/*.md`:

- **Header:** a title, `**Status:**`, `**Written:**`, `**Scope:**` (bullets) and `**Explicitly out of scope:**` (bullets), then `---`.
- **Sections**, each with a one-line italic instruction:
  - `## 1. Why this exists`
  - `## 2. What exists today (grounding)`, where every claim cites `file:line`
  - `## 3. The idea`
  - `## 4. Proposal`
  - `## 5. Design tensions and gotchas`
  - `## 6. Decisions locked`
  - `## 7. Non-goals (for now)`
  - `## 8. Open questions`
  - `## 9. Suggested sequencing`
  - `## Appendix — related`
- **No template markers.** The file contains no `{{` or `}}`.

### 4.2 `init-workspace.sh`

**stdin:** `workspace`, `repo_url`, plus `branch`, `base_branch` and `slug` as the mode needs.

**Args:** exactly one mode. Either one of `["--checkout"]`, `["--detached-base"]` or `["--new-branch"]`, or `["--mode-var", "<stdin key>"]`, where the key's value must be `checkout`, `detached-base` or `new-branch`. Any other value, or no mode, exits 1.

| Mode | Behaviour | Used by |
|---|---|---|
| `checkout` | Shallow-clones `branch` and adds the worktree on it. If the branch is missing on origin, exit 1 with "branch <b> not found on origin". | Every stage except ideate. The parent in resume mode. |
| `detached-base` | Clones `base_branch`, or the remote HEAD when it is empty. Adds a detached worktree at the tip. | Ideate. The parent in fresh mode. |
| `new-branch` | Clones `base_branch` and creates the worktree branch `dev-<slug>-<run_date>-<short_id>`. | The parent in idea mode. |

**Validation is ported verbatim** from the current `init-workspace.sh`:

- the `repo_url` transport allowlist and the leading-`-` ban
- `git check-ref-format` on the refs, plus the leading-`-` ban
- the slug sanitizer

**Setup steps:**

- Stale `src`, `work` and `.manifest-wt` are removed. Removal goes through `git worktree prune` when `src` exists.
- The `[skip ci]` commit-msg hook is installed, also ported verbatim.

**Emits** `{work_dir, src_dir, branch, base_ref, head_sha, run_date, short_id, slug, mode}`.

- `branch` is `""` in detached-base mode.
- `short_id` is the first 8 characters of `basename(workspace)`.

### 4.3 `checkpoint-push.sh`, `notify-teams.sh`

**Source.** These are byte-identical copies of `samples/feature-pipeline/scripts/checkpoint-push.sh` and `notify-teams.sh`. Today those match the ralph-loop and pr-review copies.

**Repointed drift test.** `samples/ralph-loop/scripts/shared-scripts_test.sh` is repointed to assert two things:

- `samples/_lib/{checkpoint-push,notify-teams}.sh` are byte-identical to the legacy copies, which stay because legacy is out of scope.
- `ralph-loop/scripts/` no longer carries them.

### 4.4 `pass-mark.sh`

**Args:** `[]`.

**stdin:** `work_dir`.

**Emits** `{sha, pass_id}`.

- `sha` is HEAD.
- `pass_id` is a per-pass nonce (P8). `pass-mark` reads the integer in `{{workspace}}/tamper/pass-id` (0 when the file is absent), adds 1, writes it back, and emits it. A pass id therefore never repeats within an instance, even when a reset-void returns HEAD to the same sha.

**Exits 1** when `work_dir` is unset or is not a worktree.

### 4.5 `manifest-read.sh`

**stdin:** `work_dir`, `branch`, `slug`, plus any variables named by `--explicit`.

**Args** (each repeatable; comma lists are allowed):

| Arg | Effect |
|---|---|
| `--fetch` | `git fetch origin <branch>`, then read `origin/<branch>:<path>`. Without it, read the worktree file. |
| `--order a,b,c` | Stage order for detection. |
| `--start-at-var NAME` | A non-empty stdin `NAME` overrides detection. It must appear in `--order`, else exit 1. |
| `--explicit k1,k2` | A non-empty stdin `k` wins over the manifest. Non-empty means a non-empty string, a non-empty array, any number, or any bool. |
| `--require k1,k2` | After `--explicit`, each `k` must be present and non-empty, else exit 1. |
| `--optional k=default` | Applies if `k` is absent after `--explicit` and the manifest. A default that parses as JSON is typed; otherwise it is a string. `k=` means `""`. |

**Behaviour:**

- An empty `slug` exits 1.
- An empty `branch`, or no manifest, gives `exists:false` and an empty view.
- A malformed manifest exits 1 with "malformed manifest <path>: <reason>". Malformed means:
  - invalid JSON
  - no `stages` array
  - an unknown stage or status
- `next_stage` is chosen in this order:
  1. the start-at override
  2. otherwise, the first stage in `--order` whose latest entry is not `done`
  3. otherwise, `complete`
- Failure messages name the key, the branch, and the likely missing stage.

**Emits** `{exists, path, manifest, latest:{<stage>: entry}, done_stages, outputs:{merged view}, next_stage, inputs:{explicit ∪ required ∪ optional keys}}`.

### 4.6 `manifest-append.sh` (self-contained; R2, R13)

**Args:** `["--workflow", "<workflow name>", ["--no-skip-ci",] "<stage>", "<status>", "<jq filter>"]`.

- `--no-skip-ci` commits with `--no-verify`, past the `[skip ci]` commit-msg hook. The parent's `record-refine` and `record-review` pass it: their commits land on top of ralph's one CI commit and become the PR head, which must get a CI run.

- `<status>` may be a resolved template.
- `<jq filter>` is evaluated against the stdin snapshot and must yield an object, which becomes `outputs`. Values stay typed.

**stdin:** `work_dir`, `branch`, `slug`, `base_branch` (optional), `workspace`.

**Algorithm.** It never commits in `work_dir`, and never on a detached HEAD:

1. **Validate.** The stage and status must be in the vocabularies, `branch` and `slug` must be non-empty, `--workflow` must be non-empty, and the filter must yield an object. Otherwise exit 1, having changed nothing.
2. **Push stage work (fetch first).** If `work_dir`'s HEAD is the local branch `refs/heads/<branch>`, first fetch `origin/<branch>`. If the branch is on origin and the local HEAD is an ancestor of it (the common case for the parent's `record-refine` and `record-review`, whose `work_dir` is behind the child's pushes), there is nothing to push: only set the upstream. Otherwise push: `git -C <work_dir> push -u origin <branch>`. A non-fast-forward rejection exits 1 with "local <branch> diverged from origin". This step is what pushes a stage's own commits, and what creates a branch that is not on the remote yet (ideate, intake idea mode). If `work_dir` is detached, as in the parent's fresh mode, skip this step: there is nothing local to push.
   - git's stderr is kept on every fetch and push. A fetch that fails for any reason other than "couldn't find remote ref" (network, auth, a bad remote) exits 1 with "fetch of <branch> from origin failed" and git's message, never as a missing branch or a divergence. A push that fails other than as a race (`[rejected]`, a ref-lock failure) exits 1 with git's message and is not retried.
3. **Fetch.** `git -C <work_dir> fetch origin +refs/heads/<branch>:refs/remotes/origin/<branch>`. If the branch is absent on origin, exit 1 with "branch <branch> not on origin".
4. **Check out the private worktree.** `git -C <work_dir> worktree add -B arbitron-manifest/<short_id> <workspace>/.manifest-wt origin/<branch>`. If `.manifest-wt` already exists and is a live worktree of this clone, reset it to `origin/<branch>` with `checkout -B`; a stale one (its gitdir pruned or another clone's) is pruned, removed and re-added. The worktree is **on a named local branch**, never detached.
5. **Append.** Create the file if absent: `{slug, base_branch, branch, stages: []}`. Then append `{stage, status, outputs, commit: <origin/<branch> sha before this commit>, finished_at: <UTC ISO-8601>, instance_id: basename(workspace), workflow: <--workflow>}`.
6. **Commit.** `git add` the manifest path only. Commit with the message `chore(pipeline): record <stage> <status>`. The `[skip ci]` hook applies, because the hooks dir is shared across worktrees, unless `--no-skip-ci` is given.
7. **Push.** `git -C .manifest-wt push origin HEAD:refs/heads/<branch>`. On a non-fast-forward: re-fetch, reset `.manifest-wt` to the new `origin/<branch>`, re-append from step 5, and retry **once**. A second failure exits 1.
8. **Fast-forward the caller's branch.** If `work_dir` is on `<branch>`, run `git -C <work_dir> merge --ff-only origin/<branch>`, so later nodes see the manifest commit. If that fails, warn on stderr; the push already succeeded.

**Emits** `{appended:true, path, manifest_commit, stage, status, outputs, entry}`. This is the stage contract.

### 4.7 `finalize-docs.sh`

**Args:** `["--message", "<commit message>", "<path or glob>", …]`.

**stdin:** `work_dir`.

**Behaviour:**

- Expand the globs.
- Require at least one regular, non-empty match. Otherwise exit 1 with "no document written at <patterns>".
- `git add` the matches. Commit only if something is staged.
- Matches are sorted with a numeric-aware sort.
- No push.

**Emits** `{paths, committed}`.

### 4.8 `tamper-gate.sh`

**Invocation.**

- Check mode: `["<base>", "<head>"]`, optionally followed by `"--tests-only"`. `<head>` is usually `"HEAD"`.
- Classify mode: `["--classify", "<base>", "<head>"]`.

**stdin:** `work_dir`, `workspace`.

**Exit 2 means bad inputs only, and nothing is emitted.** Bad inputs are:

- `work_dir` unset or not a worktree
- `<base>` or `<head>` failing `git rev-parse --verify <x>^{commit}`

**History rewritten inside the pass (R4).** If `<base>` is a valid commit but not an ancestor of `<head>` (amend, reset or rebase during the pass), the gate:

- exits **0**
- emits the non-judgeable finding `history_rewritten`
- sets `force_void:true`

It does not attempt any further checks, because the diff is meaningless.

**Start-of-run housekeeping:**

- Delete a stale `{{workspace}}/tamper/verdict.json`.
- Rotate `{{workspace}}/tamper/must-fix.md` to `must-fix.prev.md`.

**Writes** `{{workspace}}/tamper/findings.json`.

**Emits:**

```json
{ "clean": false, "needs_judge": true, "force_void": false,
  "source_changed": false, "history_rewritten": false,
  "findings": [ { "id": "T1", "kind": "skip_marker_added", "file": "pkg/a_test.go", "line": 42,
                  "language": "go", "detail": "t.Skip(", "judgeable": true } ],
  "warnings": [ { "id": "W1", "kind": "hardcoded_expected_value", "file": "pkg/a.go", "line": 7, "detail": "…" } ],
  "blocking_count": 1, "warn_count": 1, "base": "<sha>", "head": "<sha>",
  "test_files_changed": ["pkg/a_test.go"], "summary": "1 finding, 1 warning over 2 commits" }
```

**Derived fields:**

- `clean` = no findings. Warnings never affect it.
- `force_void` = `source_changed || history_rewritten`.
- `needs_judge` = some judgeable finding exists `&& !force_void`.
- `base == head` gives `clean`.

**De-duplication (R9, P6).** Findings are keyed by `(file, line)`, across kinds.

- When several kinds hit the same `(file, line)`, **one** finding is kept. It takes the most specific kind by this precedence: `history_rewritten` > `source_changed` > `baseline_edited` > `ci_config_changed` > `mutation_config_weakened` > `test_config_weakened` > `test_deleted` > `test_renamed` > `assertions_dropped` > `skip_marker_added`. The other kinds are listed in the finding's `also_kinds` array.
- So a pitest edit in `pom.xml` or `build.gradle`, which matches both the keyword rule and the plugin/block rule, yields one `mutation_config_weakened` finding with `also_kinds:["test_config_weakened"]`.

- `line` is the head line number for added-line findings. It is `0` for file-level findings: deleted, renamed, assertions dropped, config, CI, baseline, source.
- Several markers or rules matching the same key produce **one** finding. Its `detail` lists the matched markers, separated by commas. For example, `@pytest.mark.skipif` also matches `@pytest.mark.skip`, `Assumptions.assumeTrue(` also matches `assumeTrue(`, and `[Fact(Skip = "x")]` also matches `Skip =`.

**Marker anchoring (P7).** Every skip or focus marker matches only at the start of a line or after a non-identifier character, using the left-boundary `(^|[^A-Za-z0-9_$])`. A marker that begins with `.` or `[` or `@` keeps that boundary as well. As a result, `process.exit(`, `benefit(`, `profit(` and `myxit(` never match `xit(` or `fit(`.

**Classification.** Patterns are anchored on the worktree-relative path.

| Lang | Source ext (marker) | Test files | Assertion tokens (counted per test file) | Skip / focus markers (on added lines, any file of the language) |
|---|---|---|---|---|
| Go | `.go` (`go.mod`) | `*_test.go` | `t.Error`, `t.Errorf`, `t.Fatal`, `t.Fatalf`, `assert.`, `require.`, `Expect(` | `t.Skip(`, `t.Skipf(`, `t.SkipNow(` |
| C# | `.cs` (`*.csproj`) | `*Tests.cs`, `*Test.cs`, and `.cs` under a dir holding `*Tests.csproj` or `*.Tests.csproj` | `Assert.`, `.Should()`, `.Verify(` | `[Ignore`, `Skip =`, `Skip=`, `[Explicit` |
| TS/JS | `.ts .tsx .js .jsx .mjs .cjs` (`package.json`) | `*.test.*`, `*.spec.*`, `__tests__/**` | `expect(`, `assert(`, `assert.` | `it.skip`, `test.skip`, `describe.skip`, `xit(`, `xtest(`, `xdescribe(`, `it.only`, `test.only`, `describe.only`, `fit(`, `fdescribe(` |
| Python | `.py` (`pyproject.toml`/`setup.py`/`setup.cfg`) | `test_*.py`, `*_test.py`, `tests/**/*.py`, `conftest.py` | `assert `, `self.assert`, `pytest.raises` | `@pytest.mark.skip`, `@pytest.mark.skipif`, `@pytest.mark.xfail`, `@unittest.skip`, `pytest.skip(`, `self.skipTest(` |
| Java | `.java` (`pom.xml`/`build.gradle*`) | `src/test/**`, `*Test.java`, `*Tests.java`, `*IT.java` | `assert[A-Z]\w*\(`, `assertThat(`, `verify(` | `@Disabled`, `@Ignore`, `assumeTrue(`, `Assumptions.assume` |
| Rust | `.rs` (`Cargo.toml`) | `tests/**/*.rs`, plus `#[cfg(test)]` mod blocks in any `.rs` (hunk-level, below) | `assert!`, `assert_eq!`, `assert_ne!`, `debug_assert` | `#[ignore` |

**Rust hunk-level rule (R5).** For an `.rs` file outside `tests/`, the gate computes the line ranges of `#[cfg(test)]` module blocks at both base and head.

- **Finding a block.** A `#[cfg(test)]` attribute line followed by `mod <name> {` opens a block. The block closes at its matching `}`, found by brace counting that ignores braces inside string literals, char literals and `//` comments.
- **Classifying hunks.** Each hunk from `git diff -U0 <base> <head> -- <file>` is a **test hunk** if all its changed lines (head-side for additions, base-side for deletions) lie inside test-block ranges. Otherwise it is a **source hunk**.
- **Under `--tests-only`,** any source hunk sets `source_changed`, while test hunks do not.
- **Assertion counting** counts inside the test blocks of a file.

**Findings.** All are judgeable unless noted otherwise.

| Kind | Rule |
|---|---|
| `test_deleted` | `git diff --name-status -M <base> <head>` shows `D` for a test file. |
| `test_renamed` | `R*` for a test file, at any similarity. |
| `assertions_dropped` | For a test file present at both ends, the head count is **strictly less** than the base count. `detail` is `base→head`. |
| `skip_marker_added` | An added line contains a skip or focus marker. |
| `test_config_weakened` | A dedicated test-runner config file changed: `jest.config.*`, `vitest.config.*`, `.mocharc*` (any extension), `karma.conf.*`, `pytest.ini`, `tox.ini`, `.coveragerc`, `*.runsettings`, `nextest.toml`, or `conftest.py` (when lines touching `collect_ignore`, `collect_ignore_glob` or `pytest_collection_modifyitems` change). **Or** a changed line in a multi-purpose file falls inside a test section, which is resolved by scanning up to the governing section header: `pyproject.toml` `[tool.pytest.ini_options]` and `[tool.coverage.*]`; `setup.cfg` `[tool:pytest]` and `[coverage:*]`; `vite.config.*` inside its `test:` block. **Or** a changed line in `package.json`, `pom.xml`, `build.gradle*`, `Cargo.toml` or `vite.config.*` matches `exclude\|ignore\|skip\|testPathIgnore\|coveragePathIgnore\|testMatch\|threshold\|omit\|maven\.test\.skip\|skipTests\|testFailureIgnore\|harness\|test *= *false`. |
| `mutation_config_weakened` | A dedicated mutation config file changed: `.gremlins.yaml`, `.gremlins.yml`, `stryker-config.json`, `stryker.conf.*`, `stryker.config.*`, `.cargo/mutants.toml`, `mutants.toml`. **Or** a changed line falls inside `pyproject.toml` `[tool.mutmut]`, `setup.cfg` `[mutmut]`, a `pom.xml` `<plugin>` block whose `artifactId` contains `pitest`, or a `build.gradle*` `pitest {` block (brace-matched). |
| `ci_config_changed` | Any change under `.github/workflows/**`, `.gitlab-ci.yml`, `azure-pipelines*.yml`, `.circleci/**`, `Jenkinsfile`, `.buildkite/**`, `bitbucket-pipelines.yml`, `.travis.yml`. |
| `baseline_edited` | Any change in the pass to `.arbitron/pipeline/*.baseline.json`, `docs/reference/test-baseline.json`, `.arbitron/pipeline/*.harden.json` or `.arbitron/pipeline/*.survivors.json`. The recording steps commit these **before** the first `pass-mark` (R3), so they are never in a gated range. |
| `source_changed` | **`--tests-only` only. Not judgeable.** A source file, or a Rust source hunk, changed. |
| `history_rewritten` | **Not judgeable.** `<base>` is not an ancestor of `<head>`. |

**Warning `hardcoded_expected_value`.** This heuristic is never judged on its own. It fires when a string literal of 8 or more characters, or a number of 4 or more digits, appears on an added source line and also in a test file at head.

**`--classify` mode** emits `{base, head, files:[{path, status, language, role}], source_files_by_language, test_files}` and performs no checks. `role` is one of `source`, `test`, `config` (test or mutation), `ci` or `other`. For a Rust file, the role is `source` if any changed hunk is a source hunk, otherwise `test`.

### 4.9 `tamper-judge.md`

**Context variable.** The template uses `{{tamper_context}}`, a string each workflow sets in a transform.

**What the judge does:**

- It reads `{{workspace}}/tamper/findings.json`, the diff `git -C ./work diff {{tamper-gate.base}}..{{tamper-gate.head}}`, and the files that `{{tamper_context}}` names.
- It rules on **every** finding id: does the plan or the work justify the change?
- It treats warnings as context only.
- It edits nothing except the verdict file.

**Verdict file.** The judge writes exactly `{{workspace}}/tamper/verdict.json` in this shape: `{"rulings":[{"id":"T1","legitimate":false,"reason":"…"}]}`.

**Closing check.** The template ends with a "count your rulings against the finding ids" output contract.

### 4.10 `tamper-verdict.sh`

**Args:** `["<base>", …flags]`. The flags are:

| Flag | Effect |
|---|---|
| `--force-void` | Void without reading a verdict. |
| `--reset` | Void by `git reset --hard <base>`. |
| `--restore <path>` | Also restore this path to `<base>`. Repeatable. |
| `--restore-json <stdin jq path>` | Read an array of worktree-relative paths from stdin (for example `resolve-inputs.inputs.plan_paths`) and treat each as `--restore`. Argv would stringify an array, so arrays come through stdin. |
| `--gaps-file <path>` | Also append must-fix items to this repo file. |
| `--clear <workspace-relative path>` | On void, delete this scratch path. Repeatable (R8). |

**stdin:** `work_dir`, `workspace`, `pass-mark.pass_id`, and the keys used by `--restore-json`. The `--restore-json` argument is a dotted path whose segments may contain `-` (for example `resolve-inputs.inputs.plan_paths`). The script splits it on `.` and evaluates `getpath(["resolve-inputs","inputs","plan_paths"])`, never a raw jq expression (P15).

**It voids when any of these holds:**

- `--force-void` is given
- `findings.json` reports `force_void`
- the verdict is missing or malformed, or lacks a ruling for some finding id (fail closed)
- any ruling has `legitimate:false`

**How it voids:**

- With `--reset`, **or when the findings report `history_rewritten`**: `git reset --hard <base>`. `<base>` descends from the last pushed state, so a later plain push still fast-forwards.
- Otherwise:
  1. Restore to `<base>` every test, test-config, mutation-config, CI and baseline file changed in `<base>..HEAD`. Files added in that range are removed, and deleted ones are restored.
  2. Restore the `--restore` and `--restore-json` paths too.
  3. Commit `revert(tamper): void pass <base8>..<head8>`, but only if anything differs.

**After a void:**

- Delete every `--clear` path.
- Write `{{workspace}}/tamper/must-fix.md`, headed "MUST FIX before anything else this pass", listing each illegitimate or unruled finding with its reason.
- With `--gaps-file`, also append `- [ ] TAMPER <id> (<kind>, <file>): <reason>` lines to that file, inside the same revert commit.

**When the pass stands:** delete any `must-fix.md` this pass produced.

**Emits** `{voided, reason, pass_base, pass_id, restored_files, must_fix_path, rulings_summary}`. `pass_id` is copied from `pass-mark.pass_id` and is the staleness guard (§6, P8). `pass_base` is informational only.

---

## 5. Steps 4–5 — workflows

**Notation.** Nodes are listed as `id — type — config — routing`.

- **Script nodes** give `scriptFile`, the exact `args` array (JSON), and `timeoutSeconds`. The interpreter is always `/bin/sh`.
- **Edges** follow list order unless a routing note says otherwise.
- **Shorthand rows.**
  - A row that gives only an args array (for example "`init` — `["--checkout"]`") uses the same `scriptFile` and timeout as the first full occurrence of that node id in this spec: `init` = `@lib/init-workspace.sh`, 600 s.
  - In the parent, every `pre-*` node is `scriptFile: "@lib/manifest-read.sh"`.
  - `promote` transforms list the variables they `set`. Each value is `{{init.<same name>}}` unless it is given explicitly.

### 5.1 Step 4 — `ralph-loop` changes

**New optional inputs:**

| Input | Type | Default | Meaning |
|---|---|---|---|
| `in_place` | boolean | `false` | Refine `base_branch` itself and open the PR into `pr_base`. |
| `focus_seed` | string | `""` | Worktree-relative path to a survivors file (§5.6.5). |
| `baseline_path` | string | `""` | Worktree-relative path to a committed branch-point baseline. |

**Script changes:**

- **`init-workspace.sh`** (stays local):
  - When `in_place=true`, `BRANCH=base_branch` on both the fresh path and the resume path. Resume detection and `force_fresh` are unchanged.
  - When `in_place=true` and `pr_base` is empty or equal to `base_branch`, exit 1 before any git work ("in_place requires a pr_base different from base_branch, or no PR can be opened"). A non-boolean `in_place` also exits 1.
  - A non-empty `baseline_path` must exist at the tip, otherwise exit 1.
  - Emits `in_place` and `baseline_present`.
- **`derive-scope.sh`**: when `pr_base` is non-empty, fetch it and use it in place of the default branch in all three strategies.
- **`push-and-pr.sh`**:
  - It first runs `gh pr list --head <branch> --base <base> --state open --json number,url`. If a PR is open, it reuses it (`pr_created:false`).
  - `pr_number` is emitted as a JSON number (`null` when no PR).
  - When `focus_seed` is readable, the PR gets a `> **Harden shortfall:** <summary>` block: in the body of a new PR, or as a `gh pr comment` on a reused one.
- **`record-baseline.sh`** (R3):
  - After copying `{{workspace}}/test-baseline.json` to `docs/reference/test-baseline.json`, it **commits that file** with the message `chore(ralph): record branch-point baseline`, but only when it changed.
  - It creates `docs/reference/` if that directory is absent. The current copy is conditional on the directory existing.
  - The resume path's restored file is already committed, so no commit is needed there.
- **`record-pass-commit.sh`**:
  - When either `tamper-verdict` or `tamper-void` reports `voided:true` **and** that output's `pass_id == pass-mark.pass_id` (P8), it emits `progress_evidence:false`, `boxes_ticked:0` and `tamper_voided:true`.
  - The "never fails" rule is kept.
- **`verify-ralph-seed.sh`**, new CHECK 5:
  - When `focus_seed` is non-empty, every `focus_items[].id` must appear verbatim in `docs/reference/focus-areas.md`. Otherwise exit 1, listing the missing ids.
  - An unreadable seed file also exits 1.
  - CHECK 5 applies to both `verify-ralph-seed` and `verify-replan`.

**Template changes:**

- `seed-ralph.md` and `critique-replan.md` gain a "Seeded focus items" section, active when `{{focus_seed}}` is non-empty. It instructs:
  - read `./work/{{focus_seed}}`
  - make every id a named area, or name it inside one with its exact id string
  - use its survivors as Review-1 evidence
  - count it against the CHECK 2 budget
- `refine-*.md` and `assess-*.md` gain one line: "If `{{workspace}}/tamper/must-fix.md` exists, address it before anything else this pass."

**Graph changes:**

| Node | Type / config | Routing |
|---|---|---|
| `baseline-source?` | conditional: `init-workspace.baseline_present eq true` → `import-baseline` | default → `baseline-tests`. Inserted between `archive-ralph` and `baseline-tests`. |
| `import-baseline` | script `scripts/import-baseline.sh`, `[]`, 60. Validates the `record-baseline` shape and copies `./work/<baseline_path>` to `{{workspace}}/test-baseline.json`. | → `seed-ralph` |
| `record-baseline` | unchanged node; the script now commits (above) | → `checkpoint-push-baseline` (both paths) |
| `checkpoint-push-baseline` | script `@lib/checkpoint-push.sh`, `[]`, 120 | → `select-work`. The baseline is committed and pushed before the first `pass-mark`. |
| `pass-mark` | script `@lib/pass-mark.sh`, `[]`, 30 | between `select-work` and `work-router` |
| `tamper-gate` | script `@lib/tamper-gate.sh`, `["{{pass-mark.sha}}", "HEAD"]`, 300 | `assess-single-1`, `assess-single-2` and `assess-paired` all go here, not to `record-pass-commit` |
| `tamper-route` | conditional, first-match: `tamper-gate.force_void eq true` → `tamper-void`; `tamper-gate.needs_judge eq true` → `tamper-context` | default → `record-pass-commit` |
| `tamper-void` | script `@lib/tamper-verdict.sh`, `["{{pass-mark.sha}}", "--force-void", "--restore", "docs/reference/focus-areas.md", "--restore", "docs/reference/paired-reviews.md", "--gaps-file", "docs/reference/gaps-identified.md"]`, 120 | → `record-pass-commit` |
| `tamper-context` | transform: `tamper_context` = `ralph pass {{ralph_iteration}}: {{select-work.next_kind}} on {{select-work.target_id}} ({{select-work.target_label}}); tracking in ./work/RALPH.md and ./work/docs/reference/` | → `judge-pick` |
| `judge-pick` | conditional: `select-work.next_kind eq single_2` → `tamper-judge-opus` | default → `tamper-judge-codex`. The doers are `refine-single-1` (Sonnet, Claude), `refine-single-2` (`coder-codex-high`, GPT) and `refine-paired` (Opus, Claude). |
| `tamper-judge-codex` | agent_call `approver-codex-high`, `@lib/tamper-judge.md`, 1800 | → `tamper-verdict` |
| `tamper-judge-opus` | agent_call `approver-opus-medium`, `@lib/tamper-judge.md`, 1800 | → `tamper-verdict` |
| `tamper-verdict` | script `@lib/tamper-verdict.sh`, `["{{pass-mark.sha}}", "--restore", "docs/reference/focus-areas.md", "--restore", "docs/reference/paired-reviews.md", "--gaps-file", "docs/reference/gaps-identified.md"]`, 120 | → `record-pass-commit` |
| `checkpoint-push-pass` | script `@lib/checkpoint-push.sh`, `[]`, 120. The refine and assess agents commit only (their templates and the seeded RALPH.md role blocks say never push), so a pass reaches origin only after the gate has ruled, together with any revert commit. | between `record-pass-commit` and `count-review-plan` |

- `checkpoint-push-seed` and `checkpoint-push-replan` switch to `@lib/checkpoint-push.sh` with `[]`.
- `notify-teams` switches to `@lib/notify-teams.sh` with its current `args` unchanged.
- The local copies of both scripts are deleted.
- `TestLoopGate*` and `TestRalphGateExitsOnACompletedPlan` must still pass.

### 5.2 `ideate`

**Inputs:**

| Input | Type | Default |
|---|---|---|
| `repo_url` | string | required |
| `base_branch` | string | `main` |
| `topic` | string | `""` |
| `teams_webhook_url` | string | `""` |

**Nodes:**

1. **`init`** — script `@lib/init-workspace.sh`, `["--detached-base"]`, 600.
2. **`promote`** — transform. `work_dir` = `{{init.work_dir}}`, `run_date` = `{{init.run_date}}`.
3. **`jam`** — `interactive_agent`, `interviewer-opus-high`, `templates/jam.md`, `orchestration/jam.orch.md`, with no `outputSchema`. The user ends the session, and `EndSession` completes the node with an empty output (`domain/engine/conversation.go`).
   - **Act 1, the jam.**
     - No skills, no structured questioning, and no steering toward decisions or a plan.
     - Go broad and deep, follow tangents, and read `./work` read-only.
     - Open on `{{topic}}`, or with "Okay, what are we talking about?" when it is empty.
   - **Act 2, the write-up, only on request.**
     1. Propose a kebab-case slug.
     2. Write `./work/docs/ideas/<slug>.md` from `{{include:@lib/idea-doc-skeleton.md}}`.
     3. Revise until the human approves.
     4. Write `{{workspace}}/.arbitron/ideate-result.json` = `{"slug":"<slug>"}`.
     5. Commit locally, without pushing.
     6. Tell the human they may end the session.
   - **Write limits.** It writes nothing outside `docs/ideas/` and the result file. The failure clause (paste the full doc) is ported from `brainstorm-spec.md`.
4. **`finalize`** — script `scripts/finalize-ideate.sh`, `[]`, 120.
   - **Checks.** The result file must exist. The slug must match `^[a-z0-9]+(-[a-z0-9]+)*$` and be at most 48 characters. `docs/ideas/<slug>.md` must exist and be non-empty. Otherwise exit 1 with "no idea doc written" or the specific reason.
   - **Then:**
     - `git switch -c dev-<slug>-<run_date>-<short_id>` (a named branch; the worktree was detached)
     - commit the doc if it is uncommitted
   - **Emits** `{slug, branch, idea_path}`.
5. **`promote-result`** — transform. `slug` = `{{finalize.slug}}`, `branch` = `{{finalize.branch}}`.
6. **`finish`** — script `@lib/manifest-append.sh`, `["--workflow", "ideate", "ideate", "done", "{slug: .slug, branch: .branch, idea_path: .finalize.idea_path}"]`, 300. It pushes the new branch (§4.6 step 2), then records the entry.

### 5.3 `spec`

**Inputs:** the common inputs plus `idea_path` (string, `""`).

**Nodes:**

1. **`init`** — `@lib/init-workspace.sh`, `["--checkout"]`, 600.
2. **`promote`** — transform. `work_dir` = `{{init.work_dir}}`, `run_date` = `{{init.run_date}}`, `spec_target` = `docs/specs/{{init.run_date}}-{{slug}}-spec.md`.
3. **`resolve-inputs`** — `@lib/manifest-read.sh`, `["--explicit", "idea_path", "--optional", "idea_path="]`, 120.
4. **`brainstorm-spec`** — `interactive_agent`, `interviewer-codex`. It ports legacy `templates/brainstorm-spec.md` and `orchestration/brainstorm-spec.orch.md` with these changes:
   - The target is exactly `./work/{{spec_target}}`.
   - The slug is fixed: "the slug is `{{slug}}`".
   - A new "Clarification material" paragraph says: when `{{resolve-inputs.inputs.idea_path}}` is non-empty, read it first and use it only to clarify a **product-only** spec.
   - The completion result is `{spec_path}`.
   - Commit only, no push.
5. **`checkpoint-push-spec`** — `@lib/checkpoint-push.sh`, `[]`, 120. Pushes the approved session before finalize, so even a misnamed spec file survives: an unpushed spec lives only in a workspace that is deleted when the run ends.
6. **`finalize-spec`** — `@lib/finalize-docs.sh`, `["--message", "docs: add {{slug}} product spec", "{{spec_target}}"]`, 120.
7. **`finish`** — `@lib/manifest-append.sh`, `["--workflow", "spec", "spec", "done", "{spec_path: .[\"finalize-spec\"].paths[0]}"]`, 300.

### 5.4 `design`

**Inputs:** the common inputs plus `spec_path` (string, `""`) and `idea_path` (string, `""`).

**Nodes:**

1. **`init`** — `["--checkout"]`.
2. **`promote`** — `work_dir`, `run_date`, and `design_glob` = `docs/designs/{{init.run_date}}-{{slug}}-design-phase-*.md`.
3. **`resolve-inputs`** — `@lib/manifest-read.sh`, `["--explicit", "spec_path,idea_path", "--require", "spec_path", "--optional", "idea_path="]`, 120.
4. **`brainstorm-design`** — `interactive_agent`, `interviewer-opus-high`. It ports legacy `brainstorm-design.md` and `.orch.md`:
   - The spec path is explicit: `{{resolve-inputs.inputs.spec_path}}`.
   - The idea doc, when one exists, supplies the implementation shape.
   - It writes `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-N.md`.
   - Commit only. The push in the orchestration text is removed; the next node pushes instead.
5. **`checkpoint-push-brainstorm`** — `@lib/checkpoint-push.sh`, `[]`, 120. Pushes the approved session before finalize, so even a misnamed design file survives a later failure.
6. **`finalize-designs`** — `@lib/finalize-docs.sh`, `["--message", "docs: add {{slug}} phased designs", "{{design_glob}}"]`, 120.
7. **`checkpoint-push-designs`** — `@lib/checkpoint-push.sh`, `[]`, 120. Pushes the finalized designs before `align-designs`, whose retries can be exhausted (a codex quota wall).
8. **`align-designs`** — `agent_call`, `reviewer-codex-high`, 1800. It ports legacy `align-designs.md`:
   - The spec path is explicit.
   - Design paths come from `{{finalize-designs.paths}}`.
   - Commit only.
9. **`finalize-aligned`** — `@lib/finalize-docs.sh`, `["--message", "docs: align {{slug}} designs", "{{design_glob}}"]`, 120.
10. **`finish`** — `@lib/manifest-append.sh`, `["--workflow", "design", "design", "done", "{design_paths: .[\"finalize-aligned\"].paths}"]`, 300.

### 5.5 `plan`

**Inputs:** the common inputs plus `spec_path` (string, `""`) and `design_paths` (array, `[]`).

**Nodes:**

1. **`init`** — `["--checkout"]`.
2. **`promote`** — `work_dir`, `run_date`, and `plan_dir` = `docs/plans/{{slug}}`.
3. **`resolve-inputs`** — `@lib/manifest-read.sh`, `["--explicit", "spec_path,design_paths", "--require", "spec_path,design_paths"]`, 120.
4. **`produce-plans`** — `agent_call`, `coder-opus-high`, 3600. It ports legacy `produce-plans.md`:
   - Inputs are explicit.
   - Output goes to `./work/{{plan_dir}}/{{run_date}}-phase-{N}-task-{M}.md`.
   - Commit only.
5. **`checkpoint-push-plans`** — `@lib/checkpoint-push.sh`, `[]`, 120.
6. **`align-plans`** — `agent_call`, `reviewer-codex-high`, 1800. It ports `align-plans.md`, with the plan glob `./work/{{plan_dir}}/*.md`. Commit only.
7. **`deps-assess`** — `llm_call`, `assessor-opus-high`. The legacy template and schema are ported verbatim.
8. **`deps-count`** — `scripts/deps-count.sh`, `["deps-assess"]`, 60. This is the legacy script, except that the assess node id is now `$1` (default `deps-assess`). It still fails closed.
9. **`deps-gate`** — conditional. `deps-count.needs_resolution eq 0` → `mark-resolved`. The default is `resolve-deps`.
10. **`resolve-deps`** — `interactive_agent`, `interviewer-opus-high`. It ports `resolve-deps.md` and `.orch.md`, with gaps from `{{deps-assess.gaps}}`. Commit only.
11. **`push-resolution`** — `@lib/checkpoint-push.sh`, `[]`, 120.
12. **`align-plans-2`** — the same config as node 6.
13. **`deps-assess-2`** — the same config as node 7.
14. **`deps-count-2`** — `scripts/deps-count.sh`, `["deps-assess-2"]`, 60.
15. **`deps-gate-2`** — conditional. `deps-count-2.needs_resolution eq 0` → `mark-resolved`. The default is `mark-unresolved`.
16. **`mark-resolved`** — transform. `plan_status` = `done`, `deps_resolved` = `true` (bool literal), `deps_gaps` = `""`.
17. **`mark-unresolved`** — transform. `plan_status` = `blocked`, `deps_resolved` = `false`, `deps_gaps` = `{{deps-assess-2.gaps}}` (JSON text).
18. **`list-plans`** — `scripts/list-plans.sh`, `[]`, 60.
    - It sorts `./work/{{plan_dir}}/*.md` by phase, then task, numerically.
    - If there are none, it exits 1.
    - Emits `{plan_paths}`.
19. **`checkpoint-push`** — `@lib/checkpoint-push.sh`, `[]`, 120.
20. **`finish`** — `@lib/manifest-append.sh`, `["--workflow", "plan", "plan", "{{plan_status}}", "{plan_paths: .[\"list-plans\"].plan_paths, deps_resolved: .deps_resolved, deps_gaps: .deps_gaps}"]`, 300.

The graph is acyclic, and `resolve-deps` runs at most once.

### 5.6 `harden`

#### 5.6.1 Tooling spike (implementation-plan task 1)

Before writing the adapters, install each tool locally and record its entry in `samples/harden/scripts/mutation-tools.json`. Each entry holds:

- `language`, `tool`, `tested_version`
- `probe` (the presence and version command)
- `install_hint`
- `diff_scoping`, either `{"mode": "native", "how": …}` or `{"mode": "target-list", "how": …}`
- `report` (format and path)
- `default_test_command` (R6)
- how to inject a test command

| Lang | Tool | Diff scoping to verify | Fallback | `default_test_command` |
|---|---|---|---|---|
| Go | gremlins | diff/changed-files option | package/file targets | `go test ./...` |
| C# | Stryker.NET | `--since:<ref>` | `--mutate` file globs | `dotnet test` |
| TS/JS | StrykerJS | none expected | `--mutate` changed files | `npm test` |
| Python | mutmut | path list (v3 config-driven) | temp config **outside** the worktree | `pytest` |
| Java | PIT | SCM/GIT (paid arcmutate) | `targetClasses` from changed files | `mvn test` if `pom.xml` exists, else `gradle test` |
| Rust | cargo-mutants | `--in-diff <file>` | `--file` list | `cargo test` |

**Rules for adapters:**

- The registry records what each tool **really** supports.
- No adapter leaves edits in the worktree. Temporary config lives under `{{workspace}}/harden/`.

#### 5.6.2 Inputs

| Input | Type | Default |
|---|---|---|
| common inputs | | |
| `base_ref` | string | `""` (falls back to the manifest `base_branch`) |
| `baseline_path` | string | `""` (falls back to implement's `baseline_path`) |
| `gate_threshold` | number | `80` |
| `max_rounds` | number | `3` |
| `harden_round` | number | `0` |

#### 5.6.3 Nodes

| # | id | Type / config | Routing / purpose |
|---|---|---|---|
| 1 | `init` | `@lib/init-workspace.sh`, `["--checkout"]`, 600 | |
| 2 | `promote` | transform: `work_dir`, `run_date` | |
| 3 | `resolve-inputs` | `@lib/manifest-read.sh`, `["--explicit", "base_ref,baseline_path", "--optional", "base_ref=", "--optional", "baseline_path="]`, 120 | |
| 4 | `scope` | `scripts/scope.sh`, `[]`, 600 | The base is `resolve-inputs.inputs.base_ref`, else `resolve-inputs.outputs.base_branch`. If there is neither, exit 1 ("no base_ref given and no manifest base_branch"). Fetch at depth 200, unshallowing if needed, then find the merge-base. Emits `{base_ref, merge_base}`. With no merge-base, exit 1. |
| 5 | `classify` | `@lib/tamper-gate.sh`, `["--classify", "{{scope.merge_base}}", "HEAD"]`, 300 | |
| 6 | `detect` | `scripts/detect.sh`, `[]`, 300 | For each language with changed **source** files: (a) no registry entry → `skipped` with reason "no mutation tool registered for <lang>"; (b) the probe fails → **exit 1**, listing every missing tool's `install_hint`. The probe runs the command the adapter runs (for C#, `dotnet-stryker --help` or `dotnet stryker --help`; Stryker.NET's `--version` is a dashboard option that takes a value), from `work_dir`, or for JS from the changed files' package, so a repo-local tool is found; (c) otherwise `runnable`. C# targets are the test projects whose `ProjectReference`s include a project with a changed file; if there are none, `skipped` ("no test project references changed code"). **Test command (R6):** taken from the baseline file (`resolve-inputs.inputs.baseline_path` if it is non-empty and exists in `./work`; the suite whose `paths` cover the language's changed files, else the first suite with that language's command). With no baseline or no matching suite, use the registry `default_test_command` and set `test_command_source: "registry-default"` (otherwise `"baseline"`). Emits `{languages:[{language, tool, status, reason, targets, test_command, test_command_source}], runnable_count, skip_reason}`. `skip_reason` is set when `runnable_count` is 0: "no changed source files", or a join of the per-language skip reasons. |
| 7 | `has-work?` | conditional: `detect.runnable_count eq 0` → `report` | default → `mutate` |
| 8 | `mutate` | `scripts/mutate.sh`, `[]`, 3900 | Dispatches `scripts/mutate-<lang>.sh` per runnable language and normalizes the results into `{{workspace}}/harden/state.json`. |
| 9 | `score-0` | `scripts/score.sh`, `[]`, 120 | Emits `{score, gate_passed, killed, survived, no_coverage, timeout, total, approved_equivalents, denominator, survivor_files}` and writes `{{workspace}}/harden/survivors-round.json`. |
| 10 | `gate-0` | conditional: `score-0.gate_passed eq true` → `report` | default → `pass-mark` |
| 11 | `pass-mark` | `@lib/pass-mark.sh`, `[]`, 30 | loop head |
| 11a | `state-snapshot` | `scripts/state-guard.sh`, `["--snapshot"]`, 30 | Hashes `harden/state.json` and `survivors-round.json` into its node output (out of the agent's reach) and copies them aside. strengthen has Write access to the workspace, and rescore keeps earlier kills, so an edited state would raise the score without a test. |
| 12 | `strengthen` | agent_call `coder-sonnet5-high`, `templates/strengthen.md`, 5400 | Writes tests only, aimed at the mutants in `survivors-round.json`. It may write `{{workspace}}/harden/equivalence-claims.json` (`{claims:[{mutant_id, justification}]}`). It reads `must-fix.md` first. Commit only. |
| 12a | `clean-check` | `scripts/clean-check.sh`, `["--discard"]`, 60 | Discards uncommitted changes, and removes the previous round's `tamper/findings.json` so a void that skips the gate never reports stale findings in `must-fix.md`. |
| 12b | `state-check` | `scripts/state-guard.sh`, `["--verify", "state-snapshot"]`, 30 | Restores any edited score file from the copy and emits `tampered`; an altered copy exits 1. `dirty?` (first-match) then routes `clean-check.dirty` → `void-dirty` and `state-check.tampered` → `void-state`, each a `tamper-verdict --force-void --reason <uncommitted_changes_discarded \| score_state_edited> --reset` with the equivalence `--clear`s, → `harden-inc`. |
| 13 | `tamper-gate` | `@lib/tamper-gate.sh`, `["{{pass-mark.sha}}", "HEAD", "--tests-only"]`, 300 | |
| 14 | `tamper-route` | conditional, first-match: `tamper-gate.force_void eq true` → `void-round`; `tamper-gate.needs_judge eq true` → `tamper-context` | default → `count-claims` |
| 15 | `void-round` | `@lib/tamper-verdict.sh`, `["{{pass-mark.sha}}", "--force-void", "--reset", "--clear", "harden/equivalence-claims.json", "--clear", "harden/equivalence-verdicts.json"]`, 120 | → `harden-inc` |
| 16 | `tamper-context` | transform: `tamper_context` = `harden round {{harden_round}}: tests written to kill surviving mutants listed in {{workspace}}/harden/survivors-round.json; only test files may change` | |
| 17 | `tamper-judge` | agent_call `approver-codex-high`, `@lib/tamper-judge.md`, 1800 | The doer is Sonnet (Claude), so the judge is GPT. |
| 18 | `tamper-verdict` | `@lib/tamper-verdict.sh`, `["{{pass-mark.sha}}", "--reset", "--clear", "harden/equivalence-claims.json", "--clear", "harden/equivalence-verdicts.json"]`, 120 | |
| 19 | `voided?` | conditional: `tamper-verdict.voided eq true` → `harden-inc` | default → `count-claims`. This node is entered only straight after `tamper-verdict`, so its value is never stale. |
| 20 | `count-claims` | `scripts/equivalence.sh`, `["--count"]`, 60 | Emits `{claim_count}`. A missing file counts as 0; a malformed one counts as 0 and logs a warning. It first deletes any `equivalence-verdicts.json`, so strengthen cannot pre-write rulings for a judge that then fails to write its own. |
| 21 | `claims?` | conditional: `count-claims.claim_count gt 0` → `judge-equivalent` | default → `rescore` |
| 22 | `judge-equivalent` | agent_call `approver-codex-high`, `templates/judge-equivalent.md`, 1800 | Writes `{{workspace}}/harden/equivalence-verdicts.json` (`{rulings:[{mutant_id, equivalent, reason}]}`). |
| 23 | `record-equivalence` | `scripts/equivalence.sh`, `["--record"]`, 60 | Merges `equivalent:true` rulings into `state.json`. An unruled claim counts as rejected. Removes the claims and verdicts files. |
| 24 | `rescore` | `scripts/mutate.sh`, `["--survivors-only"]`, 3900 | Re-runs only the files that still have survivors and merges by mutant key. Earlier kills stand. |
| 25 | `score-r` | `scripts/score.sh`, `[]`, 120 | |
| 26 | `harden-inc` | transform: increment `harden_round` | |
| 27 | `harden-gate` | conditional, first-match: `score-r.gate_passed eq true` → `report`; `harden_round gte {{max_rounds}}` → `report` | default → `pass-mark`. A voided round leaves a stale or absent `score-r`, which is never `true` (otherwise the loop would already have exited). |
| 28 | `report` | `scripts/report.sh`, `[]`, 120 | Writes and commits `.arbitron/pipeline/<slug>.harden.json` and `.survivors.json`, committed together. Emits `{result, skip_reason, score, gate_passed, survivors_path, focus_seed_path, harden_report_path}`. |
| 29 | `finish` | `@lib/manifest-append.sh`, `["--workflow", "harden", "harden", "done", "{result: .report.result, skip_reason: .report.skip_reason, score: .report.score, gate_passed: .report.gate_passed, survivors_path: .report.survivors_path, focus_seed_path: .report.focus_seed_path, harden_report_path: .report.harden_report_path}"]`, 300 | A miss or a skip is still `done`. The filter is written in explicit `key: .path` form so the contract test can parse its keys (P5). |

**No-runnable-language case (R6).** `has-work?` → `report` emits:

- `result: "skipped"`
- `skip_reason` (from `detect`)
- `score: null`
- `gate_passed: true`
- `focus_seed_path: ""`

`harden.json` records each language's status and reason. The survivors file is written with an empty `focus_items` list and a summary of "harden skipped: <reason>".

#### 5.6.4 Scoring and state

- **Mutant key:** `(file, line, mutator, replacement)`.
- **Counting:**
  - `killed` and `timeout` count as killed.
  - `survived` and `no coverage` count as survived.
  - Compile errors and tool-ignored mutants are excluded from `total`.
- **Score:** `killed ÷ (total − approved_equivalents) × 100`, to 2 decimal places.
  - With a denominator of 0: `score: null`, `gate_passed: true`, `result: "scored"`, and the reason "no mutants".
  - `gate_passed` = `score ≥ gate_threshold`, so 80.00 passes and 79.99 fails.
- **`state.json`:** `{mutants:{<key>:{status, language, tool, file, line, mutator, replacement}}, approved_equivalents:[keys], rounds:[{round, voided, score}]}`.
- **`harden.json`:** `{slug, base_ref, merge_base, threshold, result, skip_reason, score, gate_passed, rounds, languages:[{language, tool, version, status, reason, test_command, test_command_source}], totals, approved_equivalents, finished_at}`.

#### 5.6.5 Survivors file and `focus_seed`

```json
{ "summary": "mutation score 72.41% (gate 80%) after 3 rounds; 23 survivors in 6 files",
  "score": 72.41, "threshold": 80, "gate_passed": false,
  "focus_items": [ { "id": "harden:pkg/limits/limits.go", "file": "pkg/limits/limits.go",
                     "mutants": [ { "line": 41, "mutator": "CONDITIONALS_BOUNDARY", "replacement": "<=" } ] } ] }
```

- **One item per file**, sorted by survivor count, descending.
- **Overflow:** beyond 25 files, items 25 onward are merged into `harden:other`.
- **Always written.** The file exists on every run.
- **`focus_seed_path`** is `survivors_path` when `gate_passed` is false, otherwise `""`.

### 5.7 `implement`

**Inputs:**

| Input | Type | Default |
|---|---|---|
| common inputs | | |
| `plan_paths` | array | `[]` |
| `max_impl_iterations` | number | `200` |
| `impl_iteration` | number | `0` |

**Nodes:**

1. **`init`** — `["--checkout"]`.
2. **`promote`** — transform: `work_dir`, `run_date`, and `baseline_rel` = `.arbitron/pipeline/{{slug}}.baseline.json`.
3. **`resolve-inputs`** — `@lib/manifest-read.sh`, `["--explicit", "plan_paths", "--require", "plan_paths"]`, 120.
4. **`baseline-check`** — `scripts/baseline-check.sh`, `[]`, 30. Emits `{present}` for `./work/{{baseline_rel}}`.
5. **`baseline-source?`** — conditional. `baseline-check.present eq true` → `import-baseline`. Default → `baseline-tests`.
6. **`baseline-tests`** — `agent_call`, `coder-sonnet5-medium`, 3600. Verbatim port of legacy `baseline-tests.md`.
7. **`persist-baseline`** — `scripts/persist-baseline.sh`, `[]`, 60. Validates the result, copies it to `./work/{{baseline_rel}}`, and commits `chore(pipeline): record {{slug}} branch-point baseline`.
8. **`checkpoint-push-baseline`** — `@lib/checkpoint-push.sh`, `[]`, 120. Routes to `record-baseline`.
9. **`import-baseline`** — `scripts/import-baseline.sh`, `[]`, 60. Copies the committed file to `{{workspace}}/test-baseline.json`, then routes to `record-baseline`.
10. **`record-baseline`** — the ported legacy script, `[]`, 60. This and both earlier routes leave the baseline committed before the first `pass-mark` (R3).
11. **`pass-mark`** — `@lib/pass-mark.sh`, `[]`, 30. Loop head.
12. **`impl-task`** — `agent_call`, `coder-pi-sol-high` (GPT), 5400. Ports legacy `impl-task.md`:
    - It reads an explicit plan list, `{{resolve-inputs.inputs.plan_paths}}`.
    - Its first step is to fix `{{workspace}}/tamper/must-fix.md` if that file exists.
    - It commits only.
13. **`tamper-gate`** — `@lib/tamper-gate.sh`, `["{{pass-mark.sha}}", "HEAD"]`, 300.
14. **`tamper-route`** — conditional, first-match:
    1. `tamper-gate.force_void eq true` → `tamper-void`.
    2. `tamper-gate.needs_judge eq true` → `tamper-context`.
    3. Default → `checkpoint-push-impl`.
15. **`tamper-void`** — `@lib/tamper-verdict.sh`, `["{{pass-mark.sha}}", "--force-void", "--restore-json", "resolve-inputs.inputs.plan_paths"]`, 120. Routes to `checkpoint-push-impl`.
16. **`tamper-context`** — transform. Sets `tamper_context` to `implement pass {{impl_iteration}}: one task from the plans listed in resolve-inputs.inputs.plan_paths; a task that removes a feature may legitimately remove its tests`.
17. **`tamper-judge`** — `agent_call`, `approver-opus-medium` (Claude, because the doer is GPT), `@lib/tamper-judge.md`, 1800.
18. **`tamper-verdict`** — `@lib/tamper-verdict.sh`, `["{{pass-mark.sha}}", "--restore-json", "resolve-inputs.inputs.plan_paths"]`, 120. Routes to `checkpoint-push-impl`.
19. **`checkpoint-push-impl`** — `@lib/checkpoint-push.sh`, `[]`, 120.
20. **`impl-assess`** — `llm_call`, `assessor-opus-high`. Ported, with explicit plan paths. Only `all_remaining_blocked` is used for routing.
21. **`count-plan-steps`** — `scripts/count-plan-steps.sh`, `[]`, 60.
    - Counts `- [ ]` and `- [x]` across `plan_paths`.
    - Emits `{unchecked_steps, checked_steps, all_tasks_complete, tasks_remaining, unreadable}`.
    - If any file is unreadable, it reports `all_tasks_complete:false`.
22. **`impl-inc`** — transform. Increments `impl_iteration`.
23. **`impl-gate`** — conditional, first-match:
    1. `count-plan-steps.all_tasks_complete eq true` → `mark-done`.
    2. `impl-assess.all_remaining_blocked eq true` → `mark-blocked`.
    3. `impl_iteration gte {{max_impl_iterations}}` → `mark-cap`.
    4. Default → `pass-mark`.
24. **`mark-done` / `mark-blocked` / `mark-cap`** — transforms that set `impl_status` and `blocked_reason`:

    | Node | `impl_status` | `blocked_reason` |
    |---|---|---|
    | `mark-done` | `done` | `""` |
    | `mark-blocked` | `blocked` | `{{impl-assess.notes}}` |
    | `mark-cap` | `blocked` | `iteration cap {{max_impl_iterations}} reached; {{count-plan-steps.unchecked_steps}} unchecked plan steps remain` |

25. **`finish`** — `@lib/manifest-append.sh`, `["--workflow", "implement", "implement", "{{impl_status}}", "{status: .impl_status, blocked_reason: .blocked_reason, baseline_path: .baseline_rel}"]`, 300.

### 5.8 Step 6 — `pr-review` sample sync

**`samples/pr-review/workflow.json`:**

- `manager.config.model_profile` changes from `reviewer-sonnet5-high` to `reviewer-codex-high`.
- `senior-engineer.config.model_profile` changes from `coder-opus-medium` to `coder-sonnet5-high`.
- In the `verify-fix.description`, "senior engineer writes on Opus 5.5" becomes "senior engineer writes on Sonnet 5.5". The gpt-6.1-sol wording stays.
- Keep everything else in the sample, including `gitVariable: "pr_branch"` and the empty webhook default.

**`samples/pr-review/PROFILES.md`:**

- The "fix" row becomes `senior-engineer` Sonnet 5.5, certified by `verify-fix` on gpt-6.1-sol. This is cross-family under §3.4.
- Add rows for `reviewer-codex-high` (agent \| codex \| gpt-6.1-sol \| high \| default \| Read,Write,Edit,Grep,Glob,Bash \| manager) and `coder-sonnet5-high` (agent \| claude-code \| claude-sonnet-5-5 \| high \| 1m \| Read,Write,Edit,Grep,Glob,Bash \| senior-engineer).
- Remove the `reviewer-sonnet5-high` and `coder-opus-medium` rows, since the workflow no longer uses them.

The default `fix_scope` stays `must-should`.

### 5.9 Step 7 — `feature-pipeline-next` (parent)

**Inputs:**

| Input | Type | Default |
|---|---|---|
| `repo_url` | string | required |
| `base_branch` | string | `""` (resolved by intake/intake-check, P2b) |
| `topic` | string | `""` |
| `idea_path` | string | `""` |
| `branch` | string | `""` |
| `slug` | string | `""` |
| `start_at` | string | `""` |
| `review_fix_scope` | string | `all-but-defer` |
| `teams_webhook_url` | string | `""` |

`slug` disambiguates a resumed branch that has several manifests.

**Error routing (R10).**

- **Workflow default.** `"defaultErrorBehavior": {"type": "route", "errorNodeId": "notify-step-failed"}`.
- **Per-node.** Every spawn and script node also carries `"errorBehavior": {"type": "route", "errorNodeId": "notify-step-failed"}` explicitly, so that a change to the workflow default cannot silently drop it. The script nodes are `intake`, `clone`, `intake-check`, `record-provided-idea`, `detect`, `adopt-ideate`, `pre-*`, `record-*` and `final-read`.
- **Notify nodes are best effort (P4).** `notify-done`, `notify-failed` and `notify-step-failed` carry `"errorBehavior": {"type": "skip"}`. The engine's `skip` marks the node skipped and continues along its normal successor edge (`domain/engine/engine.go`, `case "skip"`). So `notify-done` always proceeds to `done`, and both failure notifiers always proceed to `halt`, whether or not Teams accepted the card. `notify-teams.sh` already exits 0 on HTTP failure. `skip` covers the remaining cases, such as a jq error or a timeout.
- **Halt node.** `halt` carries `"errorBehavior": {"type": "halt"}`. It is meant to fail, and must not loop.
- **Spawn ports vs node errors.** A child that ends `failed` or `cancelled` is not a node error. It routes through the spawn's `failure` port.

**Mode rules** (intake and intake-check, R1):

| Mode | When | `init_mode` | Rules |
|---|---|---|---|
| `resume` | `branch` given | `checkout` | `start_at=ideate` → exit 1 (ideate never runs on resume). After clone: if `slug` is empty, discover it from the single `.arbitron/pipeline/*.json`; 0 or more than 1 → exit 1 asking for `slug`. A given `idea_path` must exist on the branch, and is **recorded** (`record_idea=true`). If there is no `idea_path`, the manifest has no `done` ideate entry, and `start_at` is empty → exit 1 with "resume branch has no idea doc; pass idea_path or start_at=spec". |
| `idea` | `idea_path` given, no `branch` | `new-branch` | `start_at=ideate` → exit 1 with "idea_path given — ideate is skipped; omit start_at or use start_at=spec" (P3). The slug is the sanitized basename of `idea_path` without `.md`. `idea_path` must exist in the new worktree, which was cut from `base_branch`. `record_idea=true`. |
| `fresh` | neither given | `detached-base` | A `start_at` other than `""` or `ideate` → exit 1. `record_idea=false`. The parent skips `detect` and routes straight to `ideate`, because there is no slug or manifest yet (P1). |

**`base_branch` (P2, P2b).** The parent default is `""`, and the value is resolved once, in this order:

1. **The explicit value**, if the caller passed one. It wins in every mode.
2. **Resume mode:** the manifest's top-level `base_branch` on the branch, which `intake-check` reads from the cloned worktree. If there is no explicit value **and** no manifest `base_branch` (no manifest, or an empty field), `intake-check` exits 1 with "resume branch has no recorded base_branch; pass base_branch".
3. **Fresh and idea modes:** `main`. `intake` resolves this **before** `clone`, so the base clone and the new branch are cut from it.

- **Who resolves what.** `intake` emits `base_branch`: the resolved value for fresh and idea mode, and the explicit value or `""` for resume. `promote-intake` sets the top-level `base_branch`. `intake-check` emits the final resolved value, and `promote-check` sets the top-level `base_branch` again. Every later node reads that one top-level value.
- **Who uses it.** Every stage spawn maps `base_branch: "{{base_branch}}"`, so whichever stage first creates the manifest records it. `pre-harden` and `pre-refine` take it through `--explicit base_branch`, which is now always the resolved value, with the manifest as a defensive fallback.
- **Example.** A resume of a branch cut from `dev`, run without passing `base_branch`, resolves to `dev` from the manifest. Harden then diffs against `dev`, and ralph opens its PR into `dev`.

**`repo_slug`.** Intake derives `owner/name` from the `https://github.com/…`, `ssh://git@github.com/…` and `git@github.com:…` forms. A non-GitHub URL exits 1. `file://` URLs are allowed for tests, with `repo_slug=""`.

**Nodes:**

| # | id | Type / config | Routing |
|---|---|---|---|
| 1 | `intake` | `scripts/intake.sh`, `[]`, 60. Validation only, no git. Emits `{mode, init_mode, slug, repo_slug, base_branch}`. | → `promote-intake` |
| 2 | `promote-intake` | transform: `init_mode` = `{{intake.init_mode}}`, `slug` = `{{intake.slug}}`, `repo_slug` = `{{intake.repo_slug}}`, `base_branch` = `{{intake.base_branch}}` | → `clone` |
| 3 | `clone` | `@lib/init-workspace.sh`, `["--mode-var", "init_mode"]`, 600 | → `intake-check` |
| 4 | `intake-check` | `scripts/intake-check.sh`, `[]`, 60. Applies the mode rules after cloning and resolves `base_branch` (P2b). Emits `{slug, branch, work_dir, idea_path, record_idea, base_branch}`. | → `promote-check` |
| 5 | `promote-check` | transform: `slug`, `branch`, `work_dir` and `base_branch` from `intake-check` | → `record-idea?` |
| 6 | `record-idea?` | conditional: `intake-check.record_idea eq true` → `record-provided-idea` | default → `fresh?` |
| 6a | `fresh?` | conditional: `intake.mode eq fresh` → `ideate` (P1) | default → `detect` |
| 7 | `record-provided-idea` | `@lib/manifest-append.sh`, `["--workflow", "feature-pipeline-next", "ideate", "done", "{slug: .slug, branch: .branch, idea_path: .[\"intake-check\"].idea_path, provided: true}"]`, 300. In idea mode this pushes the new branch and creates the manifest. In resume mode it appends to the existing manifest. | → `detect` |
| 8 | `detect` | `@lib/manifest-read.sh`, `["--fetch", "--order", "ideate,spec,design,plan,implement,harden,refine,review", "--start-at-var", "start_at"]`, 120 | → `route` |
| 9 | `route` | conditional: `detect.next_stage eq ideate` → `ideate`; `eq spec` → `pre-spec`; `eq design` → `pre-design`; `eq plan` → `pre-plan`; `eq implement` → `pre-implement`; `eq harden` → `pre-harden`; `eq refine` → `pre-refine`; `eq review` → `pre-review`; `eq complete` → `final-read` | default → `notify-step-failed` |
| 10 | `ideate` | spawn `ideate`, `wait_for_child`, inputs below | success → `adopt-ideate`; failure → `fail-ideate` |
| 11 | `adopt-ideate` | `scripts/adopt-ideate.sh`, `[]`, 30. Reads `ideate.childOutput.finish.outputs.branch` and `.slug` from stdin. If either is missing, empty, or not a string, exit 1 ("ideate returned no branch/slug"). Emits `{branch, slug}`. | → `promote-ideate` |
| 12 | `promote-ideate` | transform: `branch` = `{{adopt-ideate.branch}}`, `slug` = `{{adopt-ideate.slug}}` | → `pre-spec` |
| 13 | `pre-spec` | `@lib/manifest-read.sh`, `["--fetch", "--explicit", "idea_path", "--optional", "idea_path="]`, 120 | → `spec` |
| 14 | `spec` | spawn `spec` | success → `pre-design`; failure → `fail-spec` |
| 15 | `pre-design` | `["--fetch", "--explicit", "idea_path", "--require", "spec_path", "--optional", "idea_path="]`, 120 | → `design` |
| 16 | `design` | spawn `design` | success → `pre-plan`; failure → `fail-design` |
| 17 | `pre-plan` | `["--fetch", "--require", "spec_path,design_paths"]`, 120 | → `plan` |
| 18 | `plan` | spawn `plan` | success → `plan-ok?`; failure → `fail-plan` |
| 19 | `plan-ok?` | conditional: `plan.childOutput.finish.outputs.deps_resolved eq true` → `pre-implement` | default → `fail-plan-blocked` |
| 20 | `pre-implement` | `["--fetch", "--require", "plan_paths"]`, 120 | → `implement` |
| 21 | `implement` | spawn `implement` | success → `impl-ok?`; failure → `fail-implement` |
| 22 | `impl-ok?` | conditional: `implement.childOutput.finish.status eq done` → `pre-harden` | default → `fail-impl-blocked` |
| 23 | `pre-harden` | `["--fetch", "--explicit", "base_branch", "--require", "base_branch", "--optional", "baseline_path="]`, 120 | → `harden` |
| 24 | `harden` | spawn `harden` | success → `pre-refine` (a miss continues); failure → `fail-harden` |
| 25 | `pre-refine` | `["--fetch", "--explicit", "base_branch", "--require", "base_branch", "--optional", "focus_seed_path=", "--optional", "baseline_path="]`, 120 | → `refine` |
| 26 | `refine` | spawn `ralph-loop` | success → `record-refine`; failure → `fail-refine` |
| 27 | `record-refine` | `@lib/manifest-append.sh`, `["--workflow", "feature-pipeline-next", "--no-skip-ci", "refine", "done", "{pr_number: .refine.childOutput[\"push-and-pr\"].pr_number, pr_url: .refine.childOutput[\"push-and-pr\"].pr_url} \| if (.pr_number \| type) != \"number\" then error(\"ralph-loop opened no PR\") else . end"]`, 300 | → `pre-review` |
| 28 | `pre-review` | `["--fetch", "--require", "pr_number"]`, 120 | → `review` |
| 29 | `review` | spawn `pr-review` | success → `record-review`; failure → `fail-review` |
| 30 | `record-review` | `@lib/manifest-append.sh`, `["--workflow", "feature-pipeline-next", "--no-skip-ci", "review", "done", "{push_ok: (.review.childOutput[\"record-verdict\"].push_ok // false), fix_scope: .review_fix_scope}"]`, 300 | → `final-read` |
| 31 | `final-read` | `@lib/manifest-read.sh`, `["--fetch"]`, 120 | → `notify-done` |
| 32 | `notify-done` | `@lib/notify-teams.sh`, `["feature-pipeline-next complete — {{slug}}", "All stages finished. PR {{final-read.outputs.pr_url}}; harden result {{final-read.outputs.result}}, gate passed {{final-read.outputs.gate_passed}} (score {{final-read.outputs.score}}).", "good", "Repo={{repo_url}}", "Branch={{branch}}", "PR={{final-read.outputs.pr_url}}", "HardenResult={{final-read.outputs.result}}", "HardenScore={{final-read.outputs.score}}", "HardenGatePassed={{final-read.outputs.gate_passed}}", "ReviewPushOk={{final-read.outputs.push_ok}}"]`, 60. Error behaviour is `skip`. | → `done` |
| 33 | `done` | transform: `pipeline_status` = `complete` | terminal |
| 34 | `fail-<s>` for `s` in ideate, spec, design, plan, implement, harden, refine, review | transform: `failed_stage` = `<s>`, `failed_child` = `{{<s>.childInstanceId}}`, `failed_reason` = `child workflow ended {{<s>.childState}}` | → `notify-failed` |
| 35 | `fail-plan-blocked` | transform: `failed_stage` = `plan`, `failed_child` = `{{plan.childInstanceId}}`, `failed_reason` = `unresolved plan dependencies: {{plan.childOutput.finish.outputs.deps_gaps}}` | → `notify-failed` |
| 36 | `fail-impl-blocked` | transform: `failed_stage` = `implement`, `failed_child` = `{{implement.childInstanceId}}`, `failed_reason` = `{{implement.childOutput.finish.outputs.blocked_reason}}` | → `notify-failed` |
| 37 | `notify-failed` | `@lib/notify-teams.sh`, `["feature-pipeline-next stopped at {{failed_stage}} — {{slug}}", "{{failed_reason}}", "error", "Stage={{failed_stage}}", "Child=/instances/{{failed_child}}", "Repo={{repo_url}}", "Branch={{branch}}"]`, 60. Error behaviour is `skip`. | → `halt` |
| 38 | `notify-step-failed` | `@lib/notify-teams.sh`, `["feature-pipeline-next: a pipeline step failed — {{slug}}", "A pipeline step (intake, clone, a manifest read or append, or a spawn) failed outside a stage. Open the run trace for the failing node.", "error", "Repo={{repo_url}}", "Branch={{branch}}"]`, 60. Error behaviour is `skip`. | → `halt` |
| 39 | `halt` | `scripts/halt.sh`, `[]`, 30. Error behaviour is `halt`. Reads `failed_stage` and `failed_reason` from stdin, prints "pipeline stopped: <stage>: <reason>" (or "pipeline stopped: a pipeline step failed" when they are absent), and exits 1. | terminal (failed) |

The `\|` inside node 27's args stands for a plain `|`; it is escaped only for the Markdown table. The top-level `slug`, `branch` and `repo_url` references in notify args always resolve, because they are declared parent inputs with defaults.

**Spawn `inputs` maps.** Every key is listed, with no shorthand. Optional child inputs that are not listed take the child's defaults, and those defaults are named here:

```json
"ideate":    { "repo_url": "{{repo_url}}", "base_branch": "{{base_branch}}", "topic": "{{topic}}",
               "teams_webhook_url": "{{teams_webhook_url}}" }
"spec":      { "repo_url": "{{repo_url}}", "branch": "{{branch}}", "slug": "{{slug}}", "base_branch": "{{base_branch}}",
               "idea_path": "{{pre-spec.inputs.idea_path}}", "teams_webhook_url": "{{teams_webhook_url}}" }
"design":    { "repo_url": "{{repo_url}}", "branch": "{{branch}}", "slug": "{{slug}}", "base_branch": "{{base_branch}}",
               "spec_path": "{{pre-design.inputs.spec_path}}", "idea_path": "{{pre-design.inputs.idea_path}}",
               "teams_webhook_url": "{{teams_webhook_url}}" }
"plan":      { "repo_url": "{{repo_url}}", "branch": "{{branch}}", "slug": "{{slug}}", "base_branch": "{{base_branch}}",
               "spec_path": "{{pre-plan.inputs.spec_path}}", "design_paths": "{{pre-plan.inputs.design_paths}}",
               "teams_webhook_url": "{{teams_webhook_url}}" }
"implement": { "repo_url": "{{repo_url}}", "branch": "{{branch}}", "slug": "{{slug}}", "base_branch": "{{base_branch}}",
               "plan_paths": "{{pre-implement.inputs.plan_paths}}", "teams_webhook_url": "{{teams_webhook_url}}" }
               // defaults: max_impl_iterations=200, impl_iteration=0
"harden":    { "repo_url": "{{repo_url}}", "branch": "{{branch}}", "slug": "{{slug}}", "base_branch": "{{base_branch}}",
               "base_ref": "{{pre-harden.inputs.base_branch}}", "baseline_path": "{{pre-harden.inputs.baseline_path}}",
               "teams_webhook_url": "{{teams_webhook_url}}" }
               // defaults: gate_threshold=80, max_rounds=3, harden_round=0
"refine":    { "repo_url": "{{repo_url}}", "base_branch": "{{branch}}", "slug": "{{slug}}",
               "pr_base": "{{pre-refine.inputs.base_branch}}", "in_place": true,
               "focus_seed": "{{pre-refine.inputs.focus_seed_path}}", "baseline_path": "{{pre-refine.inputs.baseline_path}}",
               "teams_webhook_url": "{{teams_webhook_url}}" }
               // defaults: force_fresh=false, replan=false, scope_note="", max_ralph_iterations=500,
               //           ralph_iteration=0, no_progress_streak=0, max_no_progress_streak=12
"review":    { "repo": "{{repo_slug}}", "pr_number": "{{pre-review.inputs.pr_number}}",
               "fix_scope": "{{review_fix_scope}}", "teams_webhook_url": "{{teams_webhook_url}}" }
```

**Spawn mode.** Every spawn is `wait_for_child`, with both ports wired.

**Resume never reaches ideate.** In resume and idea modes, detection never returns `ideate`. Either an ideate entry exists, or `start_at` is a later stage, or `intake-check` has already failed. A sample test pins this invariant through the `intake-check` rules (§7.4).

---

## 6. Error handling and gotchas

- **Fail closed:**
  - malformed manifest → exit 1
  - missing required output → exit 1, naming the key
  - judge verdict missing or malformed → void
  - unreadable plan → not complete
  - bad tamper inputs → exit 2
  - unresolved spawn mapping → the node fails (A), and in the parent it routes to `notify-step-failed`
  - an `adopt-ideate` value missing → exit 1
- **History rewrite is a void, not an error (R4).** A rewrite resets to the pass base, so the loop keeps running and the pushed history never needs a force push.
- **Never fail a long loop on bookkeeping.** `record-pass-commit` keeps its "never fails" rule.
- **Stale outputs in loops.**
  - `tamper-verdict` and `tamper-void` are trusted only when their `pass_id == pass-mark.pass_id` (P8). A sha guard would be defeated by a reset-void, which returns the next pass to the same sha.
  - `voided?` reads only on the edge that just ran `tamper-verdict`.
  - `tamper-gate` removes stale verdicts and rotates `must-fix.md`.
  - Harden void nodes clear per-round claims and verdicts (R8).
- **No commits on a detached HEAD (R2).** `manifest-append.sh` uses its own worktree on a named branch. A detached `work_dir`, as in the parent's fresh mode, is never written to.
- **The baseline is committed before the first pass (R3).** So `baseline_edited` cannot fire in pass 1, and a void cannot remove the baseline.
- **`@lib` seeding is first-write-only** (A §4.6). Later lib changes reach live installs only through `workflow_write_file name=@lib scope=shared`, a step the deploy checklist (fp-next §9 step 8) must include.
- **Live-read cuts both ways.** A broken lib edit breaks every running instance, so lib changes go through PRs and `make test-scripts`.
- **Retrying a child doesn't wake the parent.** Recover with a parent rerun.
- **Interactive stages wait for humans.** Automation must not answer them. Use `start_at` on pre-seeded branches.
- **Mutation tools must be present** locally and in the work ACA image. A missing tool fails harden and lists the install hints.

## 7. Test infrastructure and enumerated tests

### 7.1 Infrastructure

- **Script test naming.** New script tests are `<script>_test.bats` files, placed next to the script.
  - A's seeder excludes test files with `isTestFile`: the stem without its final extension must end in `_test`. `tamper-gate_test.bats` has the stem `tamper-gate_test`, so it is excluded. **No rule change is needed.**
  - Fixtures are built in `setup()` (`git init`, a bare `origin`, `file://` remotes). No fixture trees are committed.
  - Helpers are defined per file.
- **Existing script tests.** Changed existing scripts extend their `_test.sh`.
  - The copied `samples/_lib/{checkpoint-push,notify-teams}_test.sh` are the legacy tests, with only the `SCRIPT_DIR` resolution adjusted.
- **`make test-scripts`** (new):
  1. `shellcheck` on every new or changed `samples/**/*.sh`, listed explicitly.
  2. `bats` on every `samples/**/*_test.bats`.
  3. `sh` on every `samples/_lib/*_test.sh` and `samples/*/scripts/*_test.sh`. A non-zero exit fails the target.
- **Wiring.** `test-gate` gains `test-scripts`. `.github/workflows/layout.yml` installs `shellcheck` and `bats` with apt.
- **Go sample tests** stay in `samples/*_test.go`.

### 7.2 Go sample tests (`samples/`)

**Happy:**

- **`TestSamplesParse`, `TestSampleInputsCarryNoSecrets`.** Both iterate over every `samples/*/workflow.json`, with a minimum-count guard.
- **`TestSamplesValidate`.** For every sample, `workflow.Validate` must return zero errors and no `SPAWN_PREPARED_INPUTS` for `feature-pipeline-next`. It runs with:
  - the real filesystem file checker over a temp root, where samples are copied to `users/<owner>/<name>/` and `samples/_lib` to `shared/_lib`, so A's resolver handles `@lib/`
  - a `WorkflowChecker` and a `ChildContractReader` over the samples
  - a `ProfileChecker` built from each `PROFILES.md`
- **`TestNoInteractiveOrAwaitedSpawnOnCycle`.** `LintWorkflow` returns no `INTERACTIVE_IN_LOOP`, and no `wait_for_child` spawn sits on a cycle. This covers `plan` specifically.
- **`TestParentIsAcyclic`.**

**Edge:**

- **`TestParentRouteCoversEveryStage`.** The real `ConditionalExecutor` routes each of the 8 stages and `complete` to the node §5.9 names, and an unknown stage to `notify-step-failed`.
- **`TestJudgeFamilyDiffersFromDoer` (R7, R14, P10).** Judge nodes are **discovered** from the graphs: every `agent_call` whose id matches `^tamper-judge` or `^judge-`. The test fails when a discovered judge has no declared doer pairing, so a forgotten node cannot pass. Families are computed from `PROFILES.md` model ids by the §3.4 rule:
  - ralph: `judge-pick` sends `single_2` to the opus judge (`coder-codex-high` is GPT) and `single_1` and `paired` to the codex judge (both doers are Claude)
  - implement: `coder-pi-sol-high`, which must classify as **GPT** through the `openai-codex/` prefix, is judged by `approver-opus-medium` (Claude)
  - harden: `coder-sonnet5-high` is judged by `approver-codex-high` for both `tamper-judge` and `judge-equivalent`
  - an unclassifiable model on any doer or judge fails the test
  - a unit table for the classifier covers `claude-opus-5-5`, `openai-codex/gpt-6.1-sol`, `gpt-6-astra`, `us.anthropic.claude-sonnet-5-5`, `deepseek-v4-pro` and the empty model
- **`TestHardenGateBoundary`.** `gate_passed:true` → `report`. Round 2 of 3 failing → loop. Round 3 → `report`. An absent `score-r` in round 1 → loop.

**Error:**

- **`TestImplGateCompletionIsArithmetic`:**
  - `count-plan-steps` incomplete while `impl-assess` claims completion → loop
  - `all_remaining_blocked` absent → not blocked
  - cap reached → `mark-cap`
  - complete beats `blocked:true`, because the gate is first-match
- **`TestPlanDepsGatesSendDecisionsToTheHumanOnce`:**
  - `deps-gate` with 7 open decisions → `resolve-deps`
  - `deps-gate-2` with 7 → `mark-unresolved`
  - 0 → `mark-resolved` on both gates
  - unreadable output (1) → not proceed
- **`TestErrorRoutesResolve` (R10, P4).** Every `errorNodeId` in every sample names an existing node. Every parent spawn and script node except `notify-*` and `halt` routes to `notify-step-failed`. The `notify-*` nodes carry `skip`, and `halt` carries `halt`. The parent's `defaultErrorBehavior` is `route` to `notify-step-failed`.
- **`TestParentFreshModeSkipsDetect` (P1).** `fresh?` routes `intake.mode=fresh` to `ideate`, and `resume` and `idea` to `detect`.

**Boundary:**

- **`TestTamperRouteFirstMatch`.** In ralph, implement and harden, `force_void:true` together with `needs_judge:true` routes to the void node.
- **`TestRalphTamperWiring`.** Every `assess-*` node feeds `tamper-gate`, and no `assess-* → record-pass-commit` edge is left. `tamper-void` and `tamper-verdict` both feed `record-pass-commit`.
- **`TestBaselineCommittedBeforeFirstPass` (R3).**
  - ralph: every path into `select-work` from the fresh and resume paths passes through `record-baseline → checkpoint-push-baseline`.
  - implement: both baseline paths reach `record-baseline` before `pass-mark`, and the measure path passes through `persist-baseline → checkpoint-push-baseline`.
- **`TestArgvFiltersHaveNoTemplateBraces` (R12, P10).** Every script `args` element in every sample is checked. After every whole `{{name}}` template is removed, no `{{` or `}}` may remain. The jq filter element of every `@lib/manifest-append.sh` node must contain neither. The test fails if it inspected zero argv elements.

**Integration:**

- **`TestParentSpawnsAreFullyMapped`:**
  - every required child input with no default is mapped
  - every mapped key is declared by the child
  - every key listed in §5.9 is present, with none missing
  - `ResolveTypedMap` keeps the types: arrays stay arrays, `pr_number` stays a number, `in_place` stays a bool
- **`TestStageContractsLineUp`:**
  - every `childOutput.finish.*` path the parent reads names a `finish` node in that child whose filter or status argument provides the key
  - every `pre-*` `--require` key is produced by some stage's `finish` filter, or is a manifest top-level key
  - every `manifest-append` node passes `--workflow` equal to its own workflow's name (R13)
- **Existing ralph tests.** `TestLoopGate*` and `TestRalphGateExitsOnACompletedPlan` still pass.

### 7.3 bats per `@lib` script

**`init-workspace_test.bats`:**

- *Happy:*
  - `--checkout` → on the branch, with the hook installed
  - `--new-branch` → `dev-<slug>-<date>-<short>`
  - `--detached-base` → detached, `branch:""`
  - `--mode-var init_mode` selects each of the three modes
- *Edge:*
  - an empty `base_branch` → the remote HEAD
  - slug sanitizing, falling back to `feature`
  - stale `src`, `work` and `.manifest-wt` are cleared on rerun
- *Error:*
  - `repo_url` forms: leading `-`, `ext::`, `http://`
  - bad refs
  - `--checkout` of a missing branch
  - no mode, or two modes
  - `--mode-var` with an absent key or an unknown value
- *Boundary:*
  - `short_id` is exactly 8 characters
  - a single-character slug
- *Integration:* `[skip ci]` is appended to commits, but not with `--no-verify`.

**`pass-mark_test.bats`:**

- *Happy:* emits the sha.
- *Edge:* a detached HEAD.
- *Error:* unset or not a repo → exit 1.
- *Boundary:* a single-commit repo.

**`manifest-read_test.bats`:**

- *Happy:*
  - reads the worktree file
  - `--fetch` reads origin, not a stale worktree copy
- *Detection:*
  - the latest entry wins: `blocked` after `done` counts as not done
  - first not-done stage
  - all done → `complete`
  - no manifest → the first stage
  - `--start-at-var` overrides
- *Error:*
  - malformed JSON
  - an unknown stage or status
  - an empty slug
  - a `start_at` outside the order
  - a missing `--require` → exit 1, with the message naming the key and the stage
- *Edge:*
  - `--explicit` wins
  - an explicit `""` or `[]` falls through
  - typed `--optional` defaults
  - a later stage's key wins on a collision
- *Boundary:*
  - zero stages
  - an empty branch → `exists:false`
- *Integration:* a round trip with `manifest-append`.

**`manifest-append_test.bats` (R2, R13):**

- *Happy:*
  - creates the manifest on the first append, and the second append appends
  - outputs stay typed
  - the entry's `workflow` equals `--workflow`
  - pushes to the bare origin
- *Edge:*
  - **detached `work_dir`:** the commit lands on origin, `work_dir` stays detached at its old sha, and `git -C work_dir log -1` is unchanged
  - **local branch not on the remote yet** (as in ideate): it is pushed first with `-u`, then the manifest commit lands on top
  - **`work_dir` on the branch:** fast-forwarded to include the manifest commit afterwards
  - **an unrelated dirty file in `work_dir`:** untouched, and not committed
  - **remote ahead by a foreign commit:** the manifest worktree starts from origin
- *Error:*
  - a bad stage or status
  - a missing `--workflow`
  - a filter that yields a non-object, or a filter that calls `error()` → exit 1 with nothing pushed
  - a local branch that has diverged from origin → exit 1
  - a branch absent on origin with a detached `work_dir` → exit 1
- *Boundary:*
  - a concurrent append (another clone pushes first) → re-fetch and one retry, which succeeds
  - a conflict again on the retry → exit 1
- *Integration:* across all cases, no commit object is ever created whose parent is a detached HEAD in `work_dir` (checked against `work_dir`'s reflog), and the emitted `outputs` equal the entry's.

**`finalize-docs_test.bats`:**

- *Happy:* commits an uncommitted doc and sorts the paths.
- *Edge:*
  - an already-committed doc → `committed:false`
  - numeric-aware order: `phase-2` before `phase-10`
- *Error:* no match, or an empty file → exit 1.
- *Boundary:* exactly one match.

**`tamper-gate_test.bats`.** The fixture is a base commit with one test file and one source file per language, plus a change commit for each case. Every positive case asserts **exactly one** finding of the expected kind (R9), unless stated otherwise.

- *Happy:*
  - adding a test file → clean
  - adding assertions → clean
  - a source-only change without `--tests-only` → clean
- *Skip and focus markers* (one test per marker):
  - **Go:** `t.Skip(`, `t.Skipf(`, `t.SkipNow(`
  - **C#:** `[Ignore]`, `[Fact(Skip = "x")]`, `[Fact(Skip="x")]`, `[Explicit]`
  - **TS/JS:** `it.skip`, `test.skip`, `describe.skip`, `xit(`, `xtest(`, `xdescribe(`, `it.only`, `test.only`, `describe.only`, `fit(`, `fdescribe(`
  - **Python:** `@pytest.mark.skip`, `@pytest.mark.skipif(`, `@pytest.mark.xfail`, `@unittest.skip`, `pytest.skip(`, `self.skipTest(`
  - **Java:** `@Disabled`, `@Ignore`, `assumeTrue(`, `Assumptions.assumeTrue(`
  - **Rust:** `#[ignore]` in `tests/x.rs`, and inside a `#[cfg(test)] mod` in `src/lib.rs`
- *De-duplication (R9):*
  - `@pytest.mark.skipif(` → one finding whose detail lists both markers
  - `Assumptions.assumeTrue(` → one
  - `[Fact(Skip = "x")]` → one
  - two different markers on two lines → two findings
  - the same file deleted and its assertions counted → only `test_deleted`, with no extra `assertions_dropped`
  - a config file matching both the keyword rule and the section rule → one finding
  - a pitest edit matching both the keyword and plugin rules → one `mutation_config_weakened` with `also_kinds:["test_config_weakened"]` (P6)
- *Marker anchoring (P7), negatives:* `process.exit(` and `benefit(` in a JS file, and `myxit(` → no finding.
- *Assertion drop* (one test per language): Go `t.Errorf`, C# `Assert.Equal`, TS `expect(`, Python `assert `, Java `assertEquals(`, Rust `assert_eq!`. Each gives one `assertions_dropped` with `base→head`.
- *Deleted and renamed test files:*
  - deletion, one per language → `test_deleted`
  - R100 rename → `test_renamed`
  - rename with edits → `test_renamed`, plus `assertions_dropped` when the count drops
- *Config edits (R7).* One test per family, each giving exactly one finding of the stated kind.
  - **`test_config_weakened`:**
    - `jest.config.js` `testPathIgnorePatterns`
    - `vitest.config.ts` `exclude`
    - `.mocharc.yml` `ignore`; `.mocharc.json` `spec` change
    - `karma.conf.js` `exclude`
    - `vite.config.ts` `test: { exclude }` (section rule)
    - `package.json` `jest.testPathIgnorePatterns`; **negative:** a dependency bump → none
    - `pytest.ini` `addopts = --ignore`
    - `pyproject.toml` `[tool.pytest.ini_options] addopts` (section rule); `[tool.coverage.run] omit`; **negative:** a `[project] version` bump → none
    - `setup.cfg` `[tool:pytest] addopts` (section rule); `[coverage:run] omit`
    - `tox.ini`; `.coveragerc` `omit`; `conftest.py` `collect_ignore`; `*.runsettings`; `nextest.toml`
    - `pom.xml` `<skipTests>`
    - `build.gradle` `test { exclude … }`
    - `Cargo.toml` `test = false`
  - **`mutation_config_weakened`:**
    - `stryker-config.json` `thresholds`
    - `stryker.conf.mjs` `mutate` exclusion
    - `stryker.config.json` `thresholds.break`
    - `.gremlins.yaml` threshold; `.gremlins.yml` threshold
    - `.cargo/mutants.toml` `exclude_globs`; `mutants.toml` `exclude_re`
    - `pyproject.toml` `[tool.mutmut] paths_to_mutate` (section rule)
    - `setup.cfg` `[mutmut] paths_to_mutate`
    - `pom.xml` pitest `<mutationThreshold>` (plugin-block rule)
    - `build.gradle` `pitest { mutationThreshold = 50 }` (block rule); **negative:** an unrelated `dependencies {}` edit in the same file → none
- *CI edits* (one each → `ci_config_changed`): `.github/workflows/ci.yml`, `.gitlab-ci.yml`, `azure-pipelines.yml`, `.circleci/config.yml`, `Jenkinsfile`, `.buildkite/pipeline.yml`, `bitbucket-pipelines.yml`, `.travis.yml`.
- *Baseline edits:* `.arbitron/pipeline/x.baseline.json`, `docs/reference/test-baseline.json`, `x.harden.json`, `x.survivors.json` → `baseline_edited`. **R3 integration:** a baseline committed **before** `pass-mark`, followed by a pass that leaves it alone → no `baseline_edited`.
- *`--tests-only`:*
  - a source change in each language → `source_changed:true`, `force_void:true`, `needs_judge:false`
  - test-only changes → none
- *Rust hunk rule (R5):*
  - an edit only inside a `#[cfg(test)] mod tests { … }` block in `src/lib.rs` → **no** `source_changed`
  - an edit only in a function outside the block in the same file → `source_changed`
  - one commit touching both → `source_changed`
  - an edit in `tests/it.rs` → no `source_changed`
  - braces inside a string literal in the test block do not end the block early
  - deleting lines inside the block, measured on the base side → test hunk
- *History rewritten (R4)* (each case exits **0** with `history_rewritten`, `force_void:true`, `needs_judge:false`, and no other findings):
  - the pass amends the base commit
  - the pass resets below the base and recommits
  - the pass rebases onto another commit
- *Warning:*
  - a 10-character string shared between a test and source → a warning, `clean` true, no judge
  - **negative:** a 7-character string or a 3-digit number → none
- *Edge:*
  - multiple commits in the range
  - a filename with spaces
  - binary files are ignored
  - a merge commit in the range
  - a stale `verdict.json` is removed, and `must-fix.md` is rotated to `.prev`
- *Error (exit 2, nothing on stdout):*
  - `work_dir` unset
  - base is not a commit
  - head is not a commit
- *Boundary:*
  - `base == head` → clean
  - equal assertion counts → none; one fewer → a finding
- *`--classify`:*
  - roles and languages for a mixed set
  - Rust role follows the hunk rule
  - the same test-file classification as check mode
- *Integration:* gate → verdict fixture → `tamper-verdict` (below).

**`tamper-verdict_test.bats`:**

- *Happy:* everything ruled legitimate → `voided:false`, nothing restored, and `must-fix.md` removed.
- *Void:*
  - an illegitimate ruling → the gated categories are restored to base: added test files removed, deleted ones restored
  - `--restore` and `--restore-json` paths are restored
  - a revert commit is created
  - `must-fix.md` lists the finding
  - `--gaps-file` appends `- [ ] TAMPER …` lines inside the same commit
- *Edge:*
  - `--reset` → hard reset
  - `--force-void` with no verdict → voided
  - `findings.history_rewritten` → hard reset even without `--reset`, and a later plain push fast-forwards against the bare origin
  - `--clear` removes the named scratch files **only** on void (R8)
  - nothing to restore → no empty commit
- *Error (fail closed):* a verdict that is missing, malformed, or missing a ruling → voided.
- *Boundary:* a single finding, ruled legitimate.
- *Integration:* gate → verdict → `record-pass-commit` (ralph). It honours `voided` only when `pass_id == pass-mark.pass_id`, and a stale verdict from the previous pass is ignored, even at the same sha.

**`checkpoint-push` and `notify-teams`:** the copied legacy `_test.sh` files. Their byte-identity is checked in `shared-scripts_test.sh`.

### 7.4 Per-workflow script tests

**ralph-loop** (extended `_test.sh` unless marked new):

- **`init-workspace`:**
  - with `in_place`, both the fresh and resume paths stay on `base_branch`
  - `in_place` with an empty `pr_base`, or with `pr_base == base_branch`, exits 1 before any git work
  - a non-boolean `in_place` exits 1
  - a missing `baseline_path` exits 1
  - `baseline_present` is reported correctly
- **`derive-scope`:**
  - uses `pr_base` when it is set
  - with an empty `pr_base`, behaviour is unchanged
  - with an unreachable `pr_base`, it falls back
- **`push-and-pr`** (with a mocked `gh`):
  - reuses an existing PR
  - `pr_number` is numeric
  - the shortfall block goes in the body of a new PR, and in a comment on a reused PR
- **`record-baseline`** (R3):
  - the file is committed and the worktree left clean
  - `docs/reference/` is created when absent
  - a resume with an unchanged file makes no commit
- **`record-pass-commit`:**
  - a matching `pass_id` from either void node → no progress
  - a mismatched one is ignored
  - **a reset-void in pass N, then a clean pass N+1 at the same sha → pass N+1 is scored normally (P8)**
- **`verify-ralph-seed` CHECK 5:**
  - all ids present → ok
  - one id missing → exit 1, naming it
  - `harden:other` handled
  - an unreadable seed → exit 1
  - an empty `focus_seed` → skipped
- **`import-baseline_test.bats`** (new): a valid file is copied; an invalid shape exits 1.
- **`shared-scripts_test.sh`:** the repointed identity check.

**implement:**

- **`count-plan-steps`:**
  - mixed checkboxes
  - all checked → complete
  - no boxes → incomplete and flagged
  - a missing file → `unreadable` and incomplete
- **`baseline-check`, `persist-baseline`, `import-baseline`:** baseline present or absent, an invalid shape, a commit created, and a clean tree afterwards.

**plan:**

- **`deps-count`:** the node-id argument and its default, plus the legacy cases.
- **`list-plans`:** numeric sort (`task-10` after `task-9`); none found → exit 1.

**ideate — `finalize-ideate`:**

- valid → a named branch created, the doc committed, outputs correct, HEAD no longer detached
- missing result, missing doc, empty doc, or bad slug (uppercase, leading `-`, 49 chars) → exit 1
- 48 chars → ok

**harden:**

- **`scope`:**
  - finds a merge-base, including when unshallowing is needed
  - explicit `base_ref` vs the manifest fallback
  - neither → exit 1
- **`detect`:**
  - an unsupported language → skipped, with a reason
  - a probe failure → exit 1, listing every install hint
  - C# targets (one, two or none)
  - no changed source → `runnable_count:0`, with `skip_reason`
  - **R6:**
    - with a baseline, the language's suite command is used and `test_command_source:"baseline"`
    - no baseline file → the registry default for each language (`go test ./...`, `dotnet test`, `npm test`, `pytest`, `mvn test`/`gradle test` chosen by marker file, `cargo test`) and `"registry-default"`
    - a baseline without that language → the default
- **`mutate`:**
  - the dispatcher with stubbed tools on `PATH` → a normalized state
  - `--survivors-only` merges by key and keeps earlier kills
- **`score`:**
  - the status mapping
  - compile errors excluded
  - equivalents shrink the denominator
  - 0 mutants → null and passed
  - 80.00 passes, 79.99 fails
- **`equivalence`:**
  - `--count`: missing, malformed, or n claims
  - `--record` keeps only `true` rulings; unruled claims count as rejected; files removed
- **`report`:**
  - a scored run → one item per file, 26 files → 25 items ending in `harden:other`, and `focus_seed_path` empty when the gate passed
  - **skipped (R6)** → `result:"skipped"`, `score:null`, `gate_passed:true`, the reason recorded, and an empty `focus_items`
  - both files committed
- **Adapters:** normalization against inline report literals recorded during the spike, plus a "tool missing" stub. **Cleanliness (P11):** each adapter runs against a stub tool that deliberately writes into the worktree, modifying a tracked source file and adding an untracked config file. After the adapter runs, `git status --porcelain` must be empty. Adapters restore with `git checkout -- .` and `git clean -fd` (never `-x`, which would delete ignored build dirs). They also require a clean tree before they start.

**feature-pipeline-next:**

- **`intake`:**
  - all three modes and their `init_mode`
  - the idea-mode slug
  - `start_at=ideate` with a branch → exit 1
  - `start_at=ideate` with `idea_path` → exit 1 with the P3 message
  - `start_at=plan` with no branch → exit 1
  - an invalid `start_at` → exit 1
  - `repo_slug` from the https, ssh and scp URL forms
  - a non-GitHub URL → exit 1
  - `file://` → empty `repo_slug`
- **`intake-check` (R1, P2):**
  - slug discovery with 0, 1 or 2 manifests; an explicit slug wins
  - `base_branch` resolution (P2b):
    - explicit wins over the manifest
    - **a resume of a branch cut from `dev`, with no `base_branch` passed, emits `base_branch=dev` from the manifest**
    - a resume with no explicit value and no manifest `base_branch` → exit 1 with the exact message
    - integration: piping the `intake-check` output for a `dev`-based resume into `@lib/manifest-read.sh --explicit base_branch --require base_branch` (as `pre-harden`/`pre-refine` run it) yields `inputs.base_branch=dev`
  - a resume with no manifest, `start_at=spec` and an explicit `base_branch` → ok. A Go parent test then checks that `spec`'s spawn maps `base_branch`, and a manifest-append bats test checks that a first write records it.
- **`intake` (P2b):** fresh and idea mode with an empty `base_branch` → emits `main`. An explicit value is kept. Resume with an empty value → emits `""`.
- **`TestParentResumeBaseBranchFromManifest` (Go, P2b).** Run `promote-check` through the real `TransformExecutor` with `intake-check.base_branch=dev` and an empty top-level `base_branch`. Assert three things:
  - the store's `base_branch` is `dev`
  - the `pre-harden` and `pre-refine` args carry `--explicit base_branch`
  - `ResolveTypedMap` of the `spec`, `design`, `plan`, `implement` and `harden` spawn maps gives `base_branch=dev`

  The `harden.base_ref` and `refine.pr_base` values come from `pre-harden`/`pre-refine`, whose `--explicit base_branch` picks up the same `dev`. The bats integration test in §7.4 checks that leg.
  - resume:
    - with `idea_path`, it must exist, and `record_idea:true` even when an ideate entry already exists
    - **no `idea_path`, no ideate entry and an empty `start_at` → exit 1** with the exact message
    - no ideate entry with `start_at=spec` → ok with `record_idea:false`
    - an ideate entry present with no `idea_path` → ok
  - idea mode: `idea_path` missing on the base → exit 1
  - outputs are correct
- **`adopt-ideate` (R10):**
  - valid → `{branch, slug}`
  - a missing branch, an empty slug, or a non-string value → exit 1
- **`halt`:** exits 1 with the stage message, or with the generic message when the vars are absent.

### 7.5 End-to-end (manual, after merge — fp-next §9 step 8)

These runs use toy Go and C# repos with pre-seeded branches, and `start_at`, so that no interactive stage is answered by automation:

- `start_at=implement` → implement → harden → refine → review.
- A weak test suite → a harden miss → focus items in ralph → the PR shows the shortfall.
- A Python-only change with mutmut absent → harden fails with an install hint. Repeated with a language that has no registry entry → harden is skipped and the pipeline continues.
- An unjustified `t.Skip` in an implement pass → the pass is voided and reverted.
- A resume on a branch with no idea doc and no `start_at` → the parent fails fast. The Teams card arrives from `notify-step-failed`, and the parent ends `failed`.

## 8. Out of scope

- Changing `legacy-feature-pipeline` or `samples/feature-pipeline`. They remain the identity reference for the `_lib` copies.
- Engine changes, which belong to sub-project A.
- The cutover rename (fp-next §9 step 9).
- Running harden inside ralph-loop (OQ1).
- A `lib_*` MCP family (OQ2).
- The deferred practitioner ideas (fp-next §7).
- Migrating the legacy `_test.sh` files to bats.
- Shellcheck-cleaning untouched scripts.
