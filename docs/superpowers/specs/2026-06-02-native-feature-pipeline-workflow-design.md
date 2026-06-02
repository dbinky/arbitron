# Native Feature-Pipeline Workflow — Design

**Status:** Draft · **Date:** 2026-06-02 · **Type:** Workflow design (authored as an Arbitron workflow folder, not Go code)

## 1. Purpose & Outcome

This spec defines the **native feature-pipeline Arbitron workflow** — the node-by-node content that the vendor-abstraction MVP spec (`docs/specs/feature-pipeline-replacement-mvp.md` §1, §9) explicitly deferred to "a separate spec" and named as the MVP's acceptance test. It recreates the behavior of the legacy `dbinky-skill-set` `/feature-pipeline` skill (and the skills it composes — `superpowers:brainstorming`, `superpowers:writing-plans`, `superpowers:executing-plans`, the alignment-reviewer/draft-implementer personas, and `auto-ralph-prep`) as a **single, self-contained Arbitron workflow folder**.

**Primary goal:** the entire pipeline is **wholly owned by the workflow configuration** — every process that the legacy skill delegated to an external `Skill` tool or plugin file is **distilled native into the workflow's own template/script files**. At runtime the workflow has **no dependency on any skill, plugin, or external file** (the sole external touch is `git`/`gh` for the final PR handoff).

**Fidelity stance:** *faithful but arbitron-native.* All of the legacy pipeline's review rigor is preserved as distinct nodes (separate design-alignment and plan-alignment passes; per-task TDD implementation), while the mechanics are modernized: ralph-o-matic's external server becomes the **native in-engine ralph loop**, and Teams/`ralph-o-matic notify` calls become **arbitron in-app notifications**.

### Non-goals

- Not authoring the vendor-abstraction layer itself (that is the separate, in-flight MVP work).
- Not inlining the PR-review personas — by decision, the final phase **pushes a branch, opens a GitHub PR, and hands off** to the existing standalone `workspaces/pr-review/` workflow (which is GitHub-Action-triggered on PR open).
- Not shipping the open-weight backend configuration yet (graph uses profile **aliases** so it drops in later by repointing bindings — no graph change).
- No parallel fan-out (deferred in the MVP); dynamic task lists are iterated one-at-a-time via the draft-impl loop.

## 2. Inputs

| Input | Type | Default | Notes |
|---|---|---|---|
| `repo_url` | string | — (required) | GitHub repo URL to clone and work in. |
| `slug` | string | — (optional) | Feature slug; auto-derived from the spec brainstorm if absent. |
| `spec_only` | boolean | `false` | Stop after spec + design brainstorm (mirrors legacy `--spec-only`). |
| `max_impl_iterations` | number | `200` | Backstop cap on the draft-impl loop. |
| `max_ralph_iterations` | number | `200` | Backstop cap on the ralph refinement loop. |
| `impl_iteration` | number | `0` | Draft-impl loop counter (numeric before first increment). |
| `ralph_iteration` | number | `0` | Ralph loop counter. |

A `run_date` variable (`YYYY-MM-DD`) is captured once in `init-workspace` and reused in all artifact filenames for human readability.

## 3. Workspace & Isolation Model

- `init-workspace` (script) clones `repo_url` into `{{workspace}}/src`, derives/accepts the slug, captures `run_date`, then creates an isolated **git worktree** at `{{workspace}}/work` on a new branch `dev-{slug}-{run_date}` (the date suffix keeps concurrent instances on the same repo from colliding in a shared remote namespace). All subsequent nodes operate in `{{workspace}}/work`. Note the branch slug is the *repo-derived* slug fixed at init time; the *feature* slug used for artifact filenames is re-promoted (via a `repromote-slug` transform) after the spec brainstorm from the slug the brainstorm recommends, so the branch name and the artifact slug may legitimately differ.
- Each instance has its own `{{workspace}}`, so **multiple feature-pipeline instances can run against the same `repo_url` concurrently** with no collision — each gets a separate clone + worktree producing a different spec/feature/branch.

## 4. Graph

```
[init-workspace]  script       clone repo_url → {{workspace}}/src; capture run_date;
      │                        git worktree add {{workspace}}/work (branch dev-{slug})
      ▼
[notify-start]    tool_call    in-app notification: pipeline started for {slug}
      ▼
── INTERACTIVE (user present) ───────────────────────────────────────────────
[brainstorm-spec]    interactive  native brainstorm → {slug?, spec_markdown}
      ▼
[write-spec]         script       write docs/specs/{run_date}-{slug}-spec.md + commit
      ▼
[brainstorm-design]  interactive  native brainstorm → {phases:[{title, markdown}]}
      ▼
[write-designs]      script       write docs/designs/{run_date}-{slug}-design-phase-{N}.md + commit
──────────────────────────────────────────────────────────────────────────────
      ▼
[spec-only?]      conditional   spec_only == true → [done-spec] (terminal) ; else continue
      │ continue        ───── USER WALKS AWAY; everything below is automated ─────
      ▼
[align-designs]   agent_call    read spec + designs; fix in place; commit            (planner)
      ▼
[produce-plans]   agent_call    native writing-plans → task files; commit            (planner)
      ▼                          docs/plans/{run_date}-{slug}-implementation-phase-{N}-task-{M}.md
[align-plans]     agent_call    read spec + designs + plans; fix; commit             (planner)
      ▼
╔═ DRAFT-IMPL LOOP ═══════════════════════════════════════════════════════════╗
║ [impl-task]    agent_call   implement NEXT incomplete task, strict TDD, commit (coder) ║
║      ▼                                                                              ║
║ [impl-assess]  llm_call     {all_tasks_complete, tasks_remaining, notes}      (assessor) ║
║      ▼                                                                              ║
║ [impl-inc]     transform    impl_iteration++                                        ║
║      ▼                                                                              ║
║ [impl-gate]    conditional  all_tasks_complete OR impl_iteration ≥ max → exit ; else back ║
╚═══════════════════════════════════════════════════════════════════════════════╝
      ▼
[seed-ralph]      agent_call    generate RALPH.md / focus-areas.md / gaps.md; commit  (planner)
      ▼
╔═ RALPH REFINEMENT LOOP (native, replaces ralph-o-matic) ════════════════════╗
║ [refine]        agent_call   edit toward focus areas; run tests; update files (coder) ║
║      ▼                                                                              ║
║ [refine-assess] llm_call     {done, notes}                                    (assessor) ║
║      ▼                                                                              ║
║ [refine-inc]    transform    ralph_iteration++                                      ║
║      ▼                                                                              ║
║ [refine-gate]   conditional  done OR ralph_iteration ≥ max → exit ; else back       ║
╚═══════════════════════════════════════════════════════════════════════════════╝
      ▼
[push-and-pr]     tool_call    git push; gh pr create → auto-triggers existing pr-review workflow
      ▼
[notify-done]     tool_call    in-app notification: PR #N opened for {slug}
      ▼
[done]            transform    terminal: pipeline_status = "complete"
```

### Legacy-phase → node mapping

| Legacy phase | Node(s) |
|---|---|
| 1 Init (slug, dirs, branch) | `init-workspace` |
| 2 Spec brainstorm (interactive) | `brainstorm-spec` + `write-spec` |
| 3 Design brainstorm (interactive) | `brainstorm-design` + `write-designs` |
| `--spec-only` gate | `spec-only?` |
| 5 Design alignment | `align-designs` |
| 6 Plan production (writing-plans) | `produce-plans` |
| 7 Plan alignment | `align-plans` |
| 8 Draft implementation (executing-plans) | draft-impl loop |
| 9 Ralph prep (auto-ralph-prep) | `seed-ralph` |
| 10 Ralph submit → refinement | native ralph loop |
| 11–12 PR review + handoff | `push-and-pr` → existing `pr-review` workflow |
| Teams notifications | `notify-start` / `notify-done` + engine auto-notifications |

## 5. Folder Layout

```
workspaces/feature-pipeline/
  workflow.json
  templates/
    brainstorm-spec.md      brainstorm-design.md
    align-designs.md        produce-plans.md       align-plans.md
    impl-task.md            impl-assess.md
    seed-ralph.md           refine.md              refine-assess.md
  scripts/
    init-workspace.sh       write-spec.sh          write-designs.sh
    push-and-pr.sh          notify.sh
```

## 6. De-Skilled Native Content

Each template carries, inline, the process the legacy skill delegated — no `Skill` invocation at runtime.

- **`brainstorm-spec.md` / `brainstorm-design.md`** — the full `superpowers:brainstorming` process distilled: explore context, ask one question at a time (multiple-choice preferred), propose 2–3 approaches with a recommendation, present the design in 200–300-word sections validating each, YAGNI ruthlessly. The **design** template additionally bakes in the project's **strict-TDD requirement** (tests for happy / success / failure / error / edge scenarios) at design-presentation time, and instructs the model to emit multiple design-phase docs. These run on the `interactive` node and emit finished markdown as structured output; the brainstorming skill's "ready for implementation / worktree / writing-plans" handoff is dropped (the workflow owns that flow).
- **`produce-plans.md`** — `superpowers:writing-plans` distilled: zero-context engineer, bite-sized 2–5-minute TDD steps (write failing test → run to confirm fail → minimal impl → run to confirm pass → commit), exact file paths, complete code, DRY/YAGNI/frequent commits, the standard plan-doc header + task structure. The skill's "offer execution choice" handoff is dropped (execution is the draft-impl loop).
- **`impl-task.md`** — `superpowers:executing-plans` distilled and reframed for **one task per pass**, unattended (no human checkpoints): read the plan files, pick the next incomplete task, follow its steps exactly, run the specified verifications, commit, and report. The "finishing-a-development-branch" handoff is dropped.
- **`align-designs.md` / `align-plans.md`** — the alignment-reviewer persona: review holistically for terminology, data models, API contracts, dependency ordering, duplication, and gaps; fix issues directly in the docs; commit. `align-plans` reviews the full spec + designs + plans hierarchy.
- **`seed-ralph.md`** — `auto-ralph-prep` distilled: generate `RALPH.md`, `focus-areas.md`, `gaps.md` from the spec/designs/plans.
- **`refine.md` / `refine-assess.md`** — adapted from arbitron's existing `examples/workflows/native-ralph-loop/` templates (already native): edit toward the top focus area, run tests, rewrite the tracking files each pass; assess emits `{done, notes}`.

## 7. Structured-Output Schemas

- `brainstorm-spec` → `{ slug?: string, spec_markdown: string }`
- `brainstorm-design` → `{ phases: [ { title: string, markdown: string } ] }`
- `impl-assess` → `{ all_tasks_complete: boolean, tasks_remaining: number, notes: string }`
- `refine-assess` → `{ done: boolean, notes: string }`

Conditionals: `impl-gate` routes on `impl-assess.all_tasks_complete == true` OR `impl_iteration >= {{max_impl_iterations}}`; `refine-gate` routes on `refine-assess.done == true` OR `ralph_iteration >= {{max_ralph_iterations}}`. First-match-to-exit, default routes back along the loop's back-edge (same idiom as `examples/workflows/native-ralph-loop/`).

## 8. Model Profiles

Profiles referenced by **alias**; all bound to `claude -p` for the first (Claude-subscription) configuration. Open-weight config later repoints aliases only — no graph edits.

| Alias | Kind | Used by | Claude binding |
|---|---|---|---|
| `interviewer` | chat | `brainstorm-spec`, `brainstorm-design` | tool-suppressed `claude -p`, streaming |
| `assessor` | chat | `impl-assess`, `refine-assess` | tool-suppressed `claude -p` |
| `planner` | agent | `align-designs`, `produce-plans`, `align-plans`, `seed-ralph` | `claude -p` with tools |
| `coder` | agent | `impl-task`, `refine` | `claude -p` with tools |

Kind-matching is validated: binding an `agent_call` to a chat alias (or vice-versa) fails `workflow_validate` before any run.

## 9. Error Handling

Mirrors the legacy hard-fail vs. tolerate split:

- **Structure/doc nodes** (`align-designs`, `produce-plans`, `align-plans`, `seed-ralph`): `errorBehavior` = retry 2× → on persistent failure `halt`. A halt drives the instance to `failed`, which the engine auto-notifies.
- **Draft-impl loop**: exits on `all_tasks_complete` (or the iteration cap). **Red tests are tolerated here** — exactly as the legacy pipeline noted ("ralph refinement will fix them"). `impl-assess` reports completion based on *tasks*, not test-green.
- **Ralph loop**: `refine-assess.done` is the gate that demands green tests + closed gaps.
- **Loop safety:** the MVP has no engine runaway guard; the `iteration ≥ max` term in each loop's conditional is the backstop. No `interactive` node appears in either loop body (per the MVP constraint — interactive executors short-circuit on a prior succeeded execution).

## 10. Notifications

`notify.sh` (a `script` node) is invoked at automation start (`notify-start`) and PR-opened (`notify-done`). **Authoring finding:** arbitron's REST API has **no create-notification endpoint** — notifications are created *internally by the engine* on state transitions. So `notify.sh` is **best-effort only**: it POSTs but treats a non-2xx as a soft `notified:false` and still exits 0, so it never blocks the pipeline. The actual user-facing notifications come from the engine auto-emitting on `waiting` (the interactive pauses → "needs interaction"), `failed`, and `completed`. The explicit notify nodes are retained as forward-compatible no-ops (they light up if a create endpoint is ever added). A genuine "PR #N opened with URL" milestone notification would require either such an endpoint or a custom transport — noted as a follow-up.

## 11. Acceptance Criteria

1. The workflow validates (`workflow_validate`) with all profile-kind contracts satisfied.
2. A run clones the repo, creates the `dev-{slug}-{run_date}` worktree, and conducts the interactive spec + design brainstorms, writing dated artifacts to `docs/specs`, `docs/designs`, and (later) `docs/plans` with commits.
3. `spec_only == true` halts cleanly after `write-designs`.
4. Design-alignment, plan-production, and plan-alignment run as distinct committing passes.
5. The draft-impl loop iterates tasks one-at-a-time (TDD) and exits on completion or the cap, with `impl_iteration` visible in the trace.
6. The ralph loop iterates and exits on `refine-assess.done` or the cap, with `ralph_iteration` in the trace.
7. `push-and-pr` pushes `dev-{slug}-{run_date}` and opens a PR; the existing `pr-review` workflow triggers on PR open.
8. Two concurrent instances on the same `repo_url` complete without collision.
9. No runtime dependency on any external skill/plugin (only `git`/`gh` for the PR handoff).

## 12. Open Items (resolved at authoring)

- Exact arbitron notifications REST endpoint / CLI for `notify.sh`.
- Whether `gh` auth is present on the host; `push-and-pr.sh` must fail loudly (and notify) if not.
- Confirm `interactive` node structured-output + completion-sentinel mechanics against the current adapter when wiring `brainstorm-*`.
