# RALPH — LunaRoute provider refinement

## Goal

Make sure the LunaRoute provider on branch `dev-lunaroute-provider-2026-11-10-35f3bb3e` does what it promises, with every path
**wired-and-fed** in production. An LLM working through MCP must be able to:

- configure a LunaRoute connection;
- list its models and their known or unknown capabilities;
- save a pinned profile;
- run chat, codex and pi work against the hosted gateway, without disclosing a credential and without silently substituting a
  model, backend or provider.

Model-family safeguards must hold for every LunaRoute id and alias.

## Source documents (read these, do not re-derive them)

- Spec: `docs/specs/2026-11-10-lunaroute-provider-spec.md`
- Designs: `docs/designs/2026-11-10-lunaroute-provider-design-phase-{1,2,3,4}.md`
- Binding decisions and **operator rulings R1–R7**: `docs/designs/2026-11-10-lunaroute-provider-decisions.md`.
  R7 (2026-11-12) limits the release to **chat, codex and pi**. Claude-code and opencode on LunaRoute are refused at
  `domain/provider/lunaroute_validate.go:lunaRouteEnabledBackends`.
- Plan graph: `docs/plans/lunaroute-provider/README.md` (32 task files alongside)
- Evidence register: `docs/evidence/lunaroute-provider/register.yaml`
- Author guide: `docs/setup/lunaroute.md`
- Pipeline records: `.arbitron/pipeline/lunaroute-provider*.json` (the baseline, the mutation-harden report and its survivors)

## Scope

**How the scope was derived.** The method was `branch-only` against `main`. In this worktree the branch is a **single
squashed commit** (`e810813`), and `main` is not present locally. So the 4109-file "changed" list is the **whole
repository**, not this branch's diff. Most of it (layout, dashboard, spawn, queue, teams notifications, samples) belongs to
earlier work and is **out of scope**. Do not review it.

The harden report records the branch's real merge base as `a39fad8`. That commit cannot be read here, so the scope is the
LunaRoute provider work as defined by the spec, designs and plans above. Concretely, it is every production file that
handles LunaRoute:

- `domain/provider/lunaroute_*.go`, `model_discovery.go`, and the LunaRoute branches of `selection.go`,
  `profile_service.go` and `canonical.go`
- `domain/modelidentity/family.go`, `version.go` (LunaRoute ids)
- `domain/engine/executor_agent.go` (`BackendExplicit`, `model_evidence`)
- `adapter/provider/{factory,dynamic,path_inventory,pinned_route,model_snapshot,interactive_stream_selector,lunaroute_agent,lunaroute_chat}.go`
- `adapter/probe/{factory,lunaroute}.go`, `adapter/procenv/httpx.go` (no-redirect client)
- `adapter/openrouter/provider.go` (classification hooks)
- `adapter/codex/{lunaroute,provider,rollout}.go`, `adapter/pi/{lunaroute,provider,model_probe,args,env}.go`
- `adapter/sqlite/connection_repo.go` and its generated queries
- `adapter/mcp/{tools_connection,tools_connection_models,tools_profile,lunaroute_backend_hint,authoring_guidance}.go`
- `adapter/config/capabilities.go` and `capabilities.default.yaml` (`lunaroute_models`)
- `cmd/arbitron/{main,connection}.go` (root wiring)
- `samples/family_test.go`
- the `test/lunaroute/` evidence and register suites

The test files `cmd/arbitron/lunaroute_*_test.go` are the root sink tests that reviews cite.

**Dormant by ruling (do not refine their emission):** `adapter/claudecli/lunaroute_env.go` and the LunaRoute branches of
`adapter/opencode/*`. R7 keeps them in the tree but disabled at the gate. Reviews check only that production cannot reach
them (Area 2). See `docs/reference/gaps-identified.md` → `Won't Fix`.

**Live verification is operator-run (R6).** Agents never hold an `lr_` key. A skipped live test is expected, and it is
never evidence of readiness.

## Tracking files

- `docs/reference/focus-areas.md`: 10 single-system behaviours, each approved twice (wiring lens, then failure lens).
- `docs/reference/paired-reviews.md`: 6 integration seams, one approval each covering both directions, plus the `## No seam`
  audit.
- `docs/reference/gaps-identified.md`: the open gaps, `## Closed` and `## Won't Fix`.

## Test commands

The pipeline input for test commands reached this run as an unsubstituted template. The commands below are the recorded
suites from `.arbitron/pipeline/lunaroute-provider.baseline.json`, which the harden stage also used. Use them exactly, and
narrow them by package or name for targeted runs:

| Suite | Command |
|---|---|
| go | `go test -timeout 30m ./...` |
| dashboard | `cd dashboard && npx tsc --noEmit && npx vitest run` |
| script-bats | `bats $(find samples -name '*_test.bats')` |
| script-sh | `for t in samples/_lib/*_test.sh samples/*/scripts/*_test.sh; do sh $t; done` |

Targeted example: `go test -timeout 30m ./domain/provider/ -run LunaRoute`.

## Branch-point test baseline

The baseline input also arrived as an unsubstituted template. The recorded baseline file says all four suites built and
**none failed**: go 14041 passed, dashboard 1572, bats 784, sh 23. Any failure is therefore NEW unless the verify-tests
script records otherwise.

## Pass budget

| Item | Value |
|---|---|
| Single-system areas | 10 |
| Paired seams | 6 |
| Implied minimum passes (2 × singles + paired) | 2 × 10 + 6 = **26** |
| Budget (500 × 0.35) | **175** |
| Fits | yes (26 ≤ 175; about 6.7× headroom for gap closing and re-review) |

## Definition of done

The repo may already be red on arrival. Pre-existing failures are NOT this branch's job and must NOT block the loop.
"Done" means ALL of:

- Every single-system review has **2 approvals**, one per lens; every paired review has **1**, covering both directions.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every area is **wired-and-fed**, verified by grep rather than by a green isolated test.
- Every paired row's **BREAK** has been shown impossible or caught.
- **No NEW test failures** beyond the recorded baseline, and nothing that built at baseline is broken.

An unfed production path is never an acceptable no-op. Fix it, or escalate it explicitly with the operator ruling that
covers it.

## What every review checks

- For every new exported field, collaborator or config the branch added, run `grep -rn <Symbol>` over the whole repo. Is it
  constructed or populated outside `*_test.go`? If it is only ever set in tests, the review fails.
- For every new component: is it assembled into the real composition root (`cmd/arbitron/main.go`), so the live path
  reaches it? Name the wiring site, or fail the review.
- Does any production call site pass `nil`, empty or hardcoded values where a real producer should feed it (the capability
  rules, the probe selector, `BackendExplicit`, the stored provider)?
- Do any tests pass only because a fake ignores a load-bearing argument? Does an "integration" test assert an intermediate
  hop instead of the sink (the HTTP request, the subprocess argv/env, the persisted row or node conversation)?
- Does the area have happy, success, failure, error and edge coverage? Would at least one test **fail** if its production
  input were nil or empty?

## Role: single-review-1

*The WIRING lens: does the path exist and is it fed? Refined and assessed by two
different model families. The first independent look at an area.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One single-system area per iteration.** Fix the most important issue, then stop.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, the assigned row and its `### Area N` block in `docs/reference/focus-areas.md`, and the OPEN items in `docs/reference/gaps-identified.md` (grep for `[ ]`; do not read the closed history).
2. **Read the code** — every file in the area's **Files** list, entry to sink.
3. **Cross-reference** — work the area's **Review 1 — wiring lens** checks with real greps; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run targeted tests** — the tests covering what you changed, using `go test -timeout 30m ./...` · `cd dashboard && npx tsc --noEmit && npx vitest run` · `bats $(find samples -name '*_test.bats')` · `for t in samples/_lib/*_test.sh samples/*/scripts/*_test.sh; do sh $t; done`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Does the area meet every wiring-lens check?

**Step B** — Record a recommendation; tick NOTHING. The approver ticks Review 1 Status in `docs/reference/focus-areas.md`.

**Step C** — Commit, and never push: the workflow pushes the pass once the tamper gate has ruled on it. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

## Role: single-review-2

*The FAILURE lens: how does it fail? Review 1 already passed on the wiring lens.
This pass asks a different question, on a different model family — that
difference is the entire reason two approvals exist.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One single-system area per iteration.** Fix the most important issue, then stop.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, the assigned row and its `### Area N` block in `docs/reference/focus-areas.md`, and the OPEN items in `docs/reference/gaps-identified.md` (grep for `[ ]`; do not read the closed history).
2. **Read the code** — every file in the area's **Files** list, entry to sink.
3. **Cross-reference** — work the area's **Review 2 — failure lens** checks: nil and empty inputs, timeouts, partial failure and rollback, concurrency, ordering; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run targeted tests** — the tests covering what you changed, using `go test -timeout 30m ./...` · `cd dashboard && npx tsc --noEmit && npx vitest run` · `bats $(find samples -name '*_test.bats')` · `for t in samples/_lib/*_test.sh samples/*/scripts/*_test.sh; do sh $t; done`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Does the area meet every failure-lens check?

**Step B** — Record a recommendation; tick NOTHING. The approver ticks Review 2 Status in `docs/reference/focus-areas.md`.

**Step C** — Commit, and never push: the workflow pushes the pass once the tamper gate has ruled on it. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

## Role: paired-review

*Phase 2: runs only once every single-system box is ticked. One row is one seam,
reviewed in BOTH directions in the same pass. Refined and assessed by two
different model families.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **One seam per iteration, both directions; batch further rows only when they share the context you have already loaded.** Fix the most important issue, then stop.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, the assigned row in `docs/reference/paired-reviews.md`, the `### Area` blocks of both behaviours it joins, and the OPEN items in `docs/reference/gaps-identified.md` (grep for `[ ]`).
2. **Read the code** — both sides of the seam and the seam itself, following the row's TRACE.
3. **Cross-reference** — demonstrate that the row's BREAK cannot happen: a test that goes red, or a cited guard at `file:symbol`; check what each side assumes about the other; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run targeted tests** — the tests covering what you changed, using `go test -timeout 30m ./...` · `cd dashboard && npx tsc --noEmit && npx vitest run` · `bats $(find samples -name '*_test.bats')` · `for t in samples/_lib/*_test.sh samples/*/scripts/*_test.sh; do sh $t; done`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Is the seam sound in both directions, with its BREAK demonstrated impossible or caught?

**Step B** — Record a recommendation per row; tick NOTHING. The approver ticks each row it can verify in `docs/reference/paired-reviews.md`, each cited separately.

**Step C** — Commit, and never push: the workflow pushes the pass once the tamper gate has ruled on it. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**
