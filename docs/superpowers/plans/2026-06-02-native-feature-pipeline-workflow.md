# Native Feature-Pipeline Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Author the self-contained `feature-pipeline` Arbitron workflow (manifest + templates + scripts) via the arbitron MCP tools, recreating the legacy `/feature-pipeline` skill with no runtime skill/plugin dependency.

**Architecture:** A workflow folder under `workspaces/feature-pipeline/` containing one `workflow.json` (≈26 nodes), 10 prompt templates, 2 interactive orchestration files, and 5 shell scripts. Authored entirely through the MCP tools `workflow_create`, `workflow_update`, `workflow_write_file`, `workflow_validate`. The "test" at each task is `workflow_validate` returning the expected result (errors before wiring is complete; clean once wired), plus targeted `jq`/shell smoke checks on the script files. The design spec is `docs/superpowers/specs/2026-06-02-native-feature-pipeline-workflow-design.md` — read it first.

**Tech Stack:** Arbitron (Go workflow engine) · MCP tools over `http://localhost:8080/mcp` · `/bin/sh` + `git` + `gh` + `jq` + `curl` for scripts · `claude -p` profiles (chat + agent).

---

## Prerequisites (do once, before Task 1)

- [ ] **Confirm the arbitron server is running** and the MCP tools are connected:
  Run: `claude mcp list`
  Expected: `arbitron: http://localhost:8080/mcp (HTTP) - ✓ Connected`
  If it shows `✗ Failed to connect`: start it with `./arbitron serve > /tmp/arbitron-serve.log 2>&1 &` (rebuild first with `make build` if stale), then **reconnect the MCP server in Claude Code** (restart the session or re-init the `arbitron` server) so the `workflow_*` tools load.

- [ ] **Confirm the host has `git`, `gh` (authenticated), `jq`, and `curl`:**
  Run: `git --version && gh auth status && jq --version && curl --version | head -1`
  Expected: all succeed; `gh auth status` shows a logged-in account. If `gh` is not authenticated, the final `push-and-pr` task will fail loudly (by design) — note it and continue authoring; auth can be fixed before a real run.

- [ ] **Confirm `script.envpassthrough` allows git/gh auth into subprocesses.** Scripts run under a controlled env (engine env is NOT inherited). For `push-and-pr.sh` to authenticate, the relevant vars must be passlisted. Check `~/.arbitron/config.yaml` for a `script:` block; if absent, plan to add (Task 8):
  ```yaml
  script:
    envpassthrough:
      - HOME
      - PATH
      - GH_TOKEN
      - GITHUB_TOKEN
  ```
  (`HOME`/`PATH` let `git`/`gh`/`jq` resolve; `GH_TOKEN`/`GITHUB_TOKEN` carry auth. Adjust to the host's actual auth mechanism.)

---

## Key engine contracts (reference — confirmed from source)

These govern every node config. Do not deviate.

- **Node output namespacing:** a node's structured output is stored under its node id. Downstream templates reference it as `{{node-id.field}}`; conditionals use `field: "node-id.field"`. Inputs/promoted vars are referenced by bare name (`{{slug}}`).
- **`script` node:** references `scriptFile` (relative to the workflow folder). The full variable snapshot is piped as **JSON on stdin**; the script **MUST print a JSON object to stdout** (becomes the node output). cwd = the script's own directory, so scripts must `cd` into the workspace path read from stdin. Per-node `env` and `interpreter` (default `/bin/sh`) supported. `args` get `{{var}}` substitution.
- **`tool_call` node:** runs `command` + `args` (args get `{{var}}` substitution); stdout **must be JSON**. Does NOT pipe stdin. → We use **`script`** nodes (not `tool_call`) for notify/push-pr because `gh`/`curl` don't emit clean JSON on their own.
- **`agent_call` node:** reads `templateFile` (mission), resolves `{{vars}}`, requires an **agent**-kind `model_profile`; runs with `WorkDir = {{workspace}}` (the instance root — NOT overridable). Our git worktree lives at `{{workspace}}/work`, so missions instruct the agent to operate in `./work`.
- **`llm_call` node:** reads `templateFile`, requires a **chat**-kind `model_profile`; `outputSchema` forces structured JSON output. No file side effects.
- **`interactive` node:** reads BOTH `orchestrationFile` and `templateFile` (concatenated), requires a chat profile; the engine **auto-appends a completion-sentinel instruction derived from `outputSchema`** (do NOT hand-write a sentinel). Pauses the instance to `waiting` and notifies the user. MUST NOT appear in a loop body.
- **`transform` node:** `operations` of `set` / `increment` / `decrement` / `append` / `delete`. `set` resolves `{{}}` in string values. Cannot write the reserved name `workspace`.
- **`conditional` node:** `conditions` (each `{field, operator, value, output}`) evaluated in order, first match wins; else `defaultOutput`. Operators: `exists, eq, neq, gt, gte, lt, lte, contains`. The comparison `value` may carry `{{var}}` templates.
- **Validation (`workflow_validate`):** requires exactly the entry node(s) marked `entrypoint`, unique node IDs, edges referencing real nodes, every conditional output port having a matching edge, **all nodes reachable** (so each loop/branch needs a reachable terminal), valid input types (`string/number/boolean/object/array`), and `model_profile` kind matching the node kind. `workspace` is a reserved input name.

---

## File Structure

```
workspaces/feature-pipeline/
  workflow.json
  orchestration/
    brainstorm-spec.orch.md      # interactive: machine framing for spec brainstorm
    brainstorm-design.orch.md    # interactive: machine framing for design brainstorm
  templates/
    brainstorm-spec.md           brainstorm-design.md
    align-designs.md             produce-plans.md          align-plans.md
    impl-task.md                 impl-assess.md
    seed-ralph.md                refine.md                 refine-assess.md
  scripts/
    init-workspace.sh            write-spec.sh             write-designs.sh
    notify.sh                    push-and-pr.sh
```

Each file has one responsibility. The two interactive nodes split machine framing (`orchestration/*.orch.md`, terse and stable) from the human-facing brainstorm process (`templates/brainstorm-*.md`, the distilled skill).

---

## Task 1: Scaffold workflow with skeleton manifest

**Files:**
- Create (via MCP `workflow_create`): `workspaces/feature-pipeline/workflow.json`

- [ ] **Step 1: Create the workflow with the full input schema and a 2-node skeleton (entry → terminal).**

Call `workflow_create` with this manifest. (Two real nodes now; we grow `nodes`/`edges` in later tasks via `workflow_update`.)

```json
{
  "name": "feature-pipeline",
  "author": "arbitron",
  "description": "End-to-end feature pipeline: clone a repo, interactively brainstorm a spec and phased designs, then automatically align docs, produce plans, implement task-by-task (TDD), refine via the native ralph loop, and open a PR. Self-contained: all brainstorming/planning/execution processes are native template content with no external skill dependency.",
  "defaultErrorBehavior": { "type": "retry", "retries": 2, "retryDelaySeconds": 30 },
  "inputs": [
    { "name": "repo_url", "type": "string", "description": "GitHub repo URL to clone and work in.", "required": true },
    { "name": "slug", "type": "string", "description": "Feature slug; auto-derived from the spec brainstorm if empty.", "required": false, "default": "" },
    { "name": "spec_only", "type": "boolean", "description": "Stop after spec + design brainstorm.", "required": false, "default": false },
    { "name": "max_impl_iterations", "type": "number", "description": "Backstop cap on the draft-impl loop.", "required": false, "default": 200 },
    { "name": "max_ralph_iterations", "type": "number", "description": "Backstop cap on the ralph refinement loop.", "required": false, "default": 200 },
    { "name": "impl_iteration", "type": "number", "description": "Draft-impl loop counter.", "required": false, "default": 0 },
    { "name": "ralph_iteration", "type": "number", "description": "Ralph loop counter.", "required": false, "default": 0 }
  ],
  "nodes": [
    { "id": "init-workspace", "type": "script", "label": "Init workspace", "entrypoint": true,
      "description": "Clone repo_url into {{workspace}}/src, derive slug + run_date, create the dev-{slug} worktree at {{workspace}}/work.",
      "config": { "scriptFile": "scripts/init-workspace.sh", "interpreter": "/bin/sh", "timeoutSeconds": 600 } },
    { "id": "done", "type": "transform", "label": "Done (terminal)",
      "description": "Terminal node. Records final status so the walk ends cleanly.",
      "config": { "operations": [ { "variable": "pipeline_status", "operation": "set", "value": "complete" } ] } }
  ],
  "edges": [
    { "source": "init-workspace", "target": "done" }
  ]
}
```

- [ ] **Step 2: Validate. Expect a file-reference error (the script does not exist yet).**

Call `workflow_validate` with `{ "name": "feature-pipeline" }`.
Expected: a validation error citing missing `scripts/init-workspace.sh` (file-existence check), and otherwise structurally valid (one entrypoint, reachable terminal). This is the "red" state — the manifest is wired but its script is missing.

- [ ] **Step 3: Commit the scaffold.**

```bash
git add workspaces/feature-pipeline/workflow.json
git commit -m "feat(feature-pipeline): scaffold workflow manifest skeleton"
```

---

## Task 2: init-workspace, var promotion, and notifications

**Files:**
- Create: `scripts/init-workspace.sh`, `scripts/notify.sh`
- Modify: `workflow.json` (add `promote-vars`, `notify-start`; rewire entry)

- [ ] **Step 1: Write `scripts/init-workspace.sh`** via `workflow_write_file` (path `scripts/init-workspace.sh`).

```sh
#!/bin/sh
# Reads the variable snapshot as JSON on stdin; clones repo_url into
# {{workspace}}/src, derives slug + run_date, creates the dev-<slug> worktree at
# {{workspace}}/work, and emits {slug, run_date, work_dir, src_dir} as JSON.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace')"
REPO_URL="$(printf '%s' "$IN" | jq -r '.repo_url')"
SLUG_IN="$(printf '%s' "$IN" | jq -r '.slug // ""')"

[ -n "$WS" ] && [ "$WS" != "null" ] || { echo "workspace not set" >&2; exit 1; }
[ -n "$REPO_URL" ] && [ "$REPO_URL" != "null" ] || { echo "repo_url not set" >&2; exit 1; }

SRC="$WS/src"
WORK="$WS/work"
RUN_DATE="$(date +%Y-%m-%d)"

# Derive a slug from the repo name if none was supplied (overridden later by the
# spec brainstorm's suggested slug, if any).
if [ -z "$SLUG_IN" ] || [ "$SLUG_IN" = "null" ]; then
  SLUG="$(basename "$REPO_URL" .git | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9' '-' | sed 's/^-//;s/-$//')"
else
  SLUG="$SLUG_IN"
fi

git clone --quiet "$REPO_URL" "$SRC" 1>&2
DEFAULT_BRANCH="$(git -C "$SRC" symbolic-ref --short HEAD)"
# Unique branch name keeps concurrent instances on the same repo from colliding
# in any shared remote namespace.
BRANCH="dev-$SLUG-$RUN_DATE"
git -C "$SRC" worktree add --quiet -b "$BRANCH" "$WORK" "$DEFAULT_BRANCH" 1>&2

jq -n --arg slug "$SLUG" --arg rd "$RUN_DATE" --arg work "$WORK" --arg src "$SRC" --arg br "$BRANCH" \
  '{slug:$slug, run_date:$rd, work_dir:$work, src_dir:$src, branch:$br}'
```

- [ ] **Step 2: Write `scripts/notify.sh`** (path `scripts/notify.sh`).

```sh
#!/bin/sh
# Posts an in-app notification to the arbitron REST API and echoes a JSON ack.
# The message is passed as arg 1 (with {{var}} substitution done by the engine).
# NOTE: confirm the notifications endpoint/payload against the running server
# (see Task 2 Step 3). Adjust URL/JSON keys to match before relying on it.
set -eu
MSG="${1:-}"
BASE="${ARBITRON_BASE_URL:-http://localhost:8080}"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/notifications" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg m "$MSG" '{type:"info", message:$m}')" || echo 000)"
jq -n --arg msg "$MSG" --arg code "$CODE" '{notified:($code|startswith("2")), http_code:$code, message:$msg}'
```

- [ ] **Step 3: Confirm the notifications REST contract before trusting `notify.sh`.**

Run: `curl -s http://localhost:8080/api/notifications -X POST -H 'Content-Type: application/json' -d '{"type":"info","message":"probe"}' -w '\n%{http_code}\n'`
Expected: a 2xx and a created notification. If the path/shape differs, inspect `adapter/http/` for the notifications route and update `notify.sh`'s URL and JSON keys to match. (The engine also auto-notifies on `waiting`/`failed`/`completed`, so a misconfigured `notify.sh` degrades gracefully — but fix it.)

- [ ] **Step 4: Add `promote-vars` (transform) and `notify-start` (script) and rewire the head of the graph.** Call `workflow_update` with the manifest, inserting these nodes into `nodes` and replacing the head edges. New nodes:

```json
{ "id": "promote-vars", "type": "transform", "label": "Promote vars",
  "description": "Lift init-workspace outputs to top-level vars for clean downstream references.",
  "config": { "operations": [
    { "variable": "slug", "operation": "set", "value": "{{init-workspace.slug}}" },
    { "variable": "run_date", "operation": "set", "value": "{{init-workspace.run_date}}" },
    { "variable": "work_dir", "operation": "set", "value": "{{init-workspace.work_dir}}" },
    { "variable": "branch", "operation": "set", "value": "{{init-workspace.branch}}" }
  ] } },
{ "id": "notify-start", "type": "script", "label": "Notify: started",
  "description": "In-app notification that the pipeline has started.",
  "config": { "scriptFile": "scripts/notify.sh", "interpreter": "/bin/sh", "timeoutSeconds": 30,
    "args": ["Feature pipeline started for {{slug}} on branch {{branch}} (repo {{repo_url}})."] } }
```

Replace the head edges so the chain is `init-workspace → promote-vars → notify-start → done` (the `→ done` edge is temporary; later tasks insert the real body between `notify-start` and `done`):

```json
{ "source": "init-workspace", "target": "promote-vars" },
{ "source": "promote-vars", "target": "notify-start" },
{ "source": "notify-start", "target": "done" }
```

- [ ] **Step 5: Validate. Expect clean (structurally valid, all referenced files exist).**

Call `workflow_validate`. Expected: no errors (profile checks are skipped until profiles exist / are referenced — the script nodes reference no profile). If it still flags missing files, re-check the `workflow_write_file` paths.

- [ ] **Step 6: Smoke-test the scripts in isolation** (they're plain `sh` + `jq`):

```bash
echo '{"workspace":"/tmp/fp-smoke","repo_url":"https://github.com/octocat/Hello-World.git","slug":""}' \
  | sh workspaces/feature-pipeline/scripts/init-workspace.sh
```
Expected: clones into `/tmp/fp-smoke/src`, creates `/tmp/fp-smoke/work`, prints a JSON object with `slug`, `run_date`, `work_dir`, `src_dir`, `branch`. Clean up: `rm -rf /tmp/fp-smoke`.

- [ ] **Step 7: Commit.**

```bash
git add workspaces/feature-pipeline/
git commit -m "feat(feature-pipeline): init-workspace, var promotion, start notification"
```

---

## Task 3: Interactive brainstorm nodes (spec + design)

**Files:**
- Create: `orchestration/brainstorm-spec.orch.md`, `templates/brainstorm-spec.md`, `scripts/write-spec.sh`, `orchestration/brainstorm-design.orch.md`, `templates/brainstorm-design.md`, `scripts/write-designs.sh`
- Modify: `workflow.json` (add `brainstorm-spec`, `write-spec`, `brainstorm-design`, `write-designs`)

- [ ] **Step 1: Write `orchestration/brainstorm-spec.orch.md`** (machine framing — terse, stable):

```markdown
You are conducting an interactive product-spec brainstorm with a human partner over a chat interface. Hold one thread of conversation: ask exactly one question per turn and wait for the reply. Do not write files. When (and only when) the human has approved a complete spec, emit the final spec per the completion instruction appended below — its `spec_markdown` field carries the entire spec document as Markdown, and `slug` is a short url-safe slug you recommend.
```

- [ ] **Step 2: Write `templates/brainstorm-spec.md`** (the distilled `superpowers:brainstorming` process, spec-flavored):

```markdown
# Brainstorm: Product Specification

We are turning an idea into a **product specification** — features and outcomes only, **no implementation details** (no architecture, tech choices, schemas, or APIs).

## Process (follow exactly)
1. **Understand context.** The cloned repo is at `./src`. Skim its README/docs to ground the conversation.
2. **Ask one question at a time.** Prefer multiple-choice; keep to a single question per turn. Focus on: purpose, target users, constraints, success criteria, scope boundaries. Break big topics into several single questions.
3. **Explore approaches.** When the shape is clear, propose 2–3 product approaches with trade-offs; lead with your recommendation and why.
4. **Present the spec in sections** of 200–300 words (purpose, user-facing features, outcomes/success criteria, explicit non-goals). After each section ask whether it looks right. Revise on feedback.
5. **YAGNI ruthlessly** — cut speculative features.
6. **Get explicit approval** of the whole spec before finishing.

## Output
When approved, the spec document (Markdown, starting with a top-level `# <Feature> — Product Specification` heading) is the value of `spec_markdown`. Recommend a short kebab-case `slug` (2–3 meaningful words).

## Topic
{{repo_url}} — the feature to specify: (the human will describe it; begin by asking your first clarifying question).
```

- [ ] **Step 3: Write `scripts/write-spec.sh`** (path `scripts/write-spec.sh`):

```sh
#!/bin/sh
# Writes the brainstormed spec into the worktree under docs/specs and commits.
# Reads vars from stdin JSON; emits {spec_path} as JSON.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
RUN_DATE="$(printf '%s' "$IN" | jq -r '.run_date')"
# A brainstorm-suggested slug overrides the init-derived one when present.
SLUG="$(printf '%s' "$IN" | jq -r '.["brainstorm-spec"].slug // .slug')"
MD="$(printf '%s' "$IN" | jq -r '.["brainstorm-spec"].spec_markdown')"

[ -n "$MD" ] && [ "$MD" != "null" ] || { echo "spec_markdown empty" >&2; exit 1; }
mkdir -p "$WORK/docs/specs"
SPEC_PATH="docs/specs/$RUN_DATE-$SLUG-spec.md"
printf '%s\n' "$MD" > "$WORK/$SPEC_PATH"
git -C "$WORK" add "$SPEC_PATH" 1>&2
git -C "$WORK" commit --quiet -m "docs: add $SLUG product spec" 1>&2
jq -n --arg p "$SPEC_PATH" --arg slug "$SLUG" '{spec_path:$p, slug:$slug}'
```

- [ ] **Step 4: Write `orchestration/brainstorm-design.orch.md`:**

```markdown
You are conducting an interactive implementation-design brainstorm with a human partner over a chat interface. Ask one question per turn and wait. Do not write files. The approved product spec is included below for context. When the human approves the complete set of phased designs, emit them per the completion instruction — `phases` is an ordered array of `{title, markdown}` objects, one per logical implementation phase.
```

- [ ] **Step 5: Write `templates/brainstorm-design.md`** (distilled brainstorming, design-flavored, with the strict-TDD requirement baked in):

```markdown
# Brainstorm: Implementation Design

Turn the approved spec into an **implementation design**, split into **logical phases** (one design doc per phase). This is high-level implementation: architecture, components, data flow, integration seams, error handling, and testing strategy.

## Spec (context)
{{write-spec.spec_path}} was just written in the worktree. The full spec text:
---
{{brainstorm-spec.spec_markdown}}
---

## Process (follow exactly)
1. Read the spec thoroughly; skim `./src` for existing patterns to follow.
2. Ask one question per turn (multiple-choice preferred) to resolve design decisions: phase boundaries, component responsibilities, interfaces, dependencies between phases, error handling.
3. Propose 2–3 architectural approaches with trade-offs; lead with your recommendation.
4. Present each phase design in 200–300-word sections; validate each with the human before moving on.
5. Design for isolation: small units, clear interfaces, one responsibility each.
6. **Testing — STRICT TDD (REQUIRED in every phase's design):** each phase must specify tests for **all five** scenario categories — happy-path, success, failure, error, and edge-case. State this requirement explicitly in each design doc's testing section.
7. YAGNI ruthlessly. Get explicit approval of the full set before finishing.

## Output
When approved, `phases` is an ordered array; each element `{title, markdown}` where `markdown` is one complete phase design document (starting with `# <Feature> — Design Phase N: <title>`). The number of phases is whatever the work logically needs.
```

- [ ] **Step 6: Write `scripts/write-designs.sh`** (path `scripts/write-designs.sh`):

```sh
#!/bin/sh
# Writes each brainstormed design phase to docs/designs and commits. Reads vars
# from stdin JSON; emits {design_count, design_glob} as JSON.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
RUN_DATE="$(printf '%s' "$IN" | jq -r '.run_date')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug')"
mkdir -p "$WORK/docs/designs"
N="$(printf '%s' "$IN" | jq '.["brainstorm-design"].phases | length')"
[ "$N" -ge 1 ] || { echo "no design phases" >&2; exit 1; }
i=0
while [ "$i" -lt "$N" ]; do
  PHASE_NO=$((i + 1))
  printf '%s' "$IN" | jq -r ".[\"brainstorm-design\"].phases[$i].markdown" \
    > "$WORK/docs/designs/$RUN_DATE-$SLUG-design-phase-$PHASE_NO.md"
  i=$PHASE_NO
done
git -C "$WORK" add docs/designs/ 1>&2
git -C "$WORK" commit --quiet -m "docs: add $SLUG phased designs ($N phases)" 1>&2
jq -n --argjson n "$N" --arg g "docs/designs/$RUN_DATE-$SLUG-design-phase-*.md" \
  '{design_count:$n, design_glob:$g}'
```

- [ ] **Step 7: Add the four nodes** via `workflow_update`. Insert between `notify-start` and `done`; rewire `notify-start → brainstorm-spec → write-spec → brainstorm-design → write-designs → done` (the `→ done` tail is temporary again).

```json
{ "id": "brainstorm-spec", "type": "interactive", "label": "Brainstorm: spec",
  "description": "Interactive spec brainstorm; emits {slug, spec_markdown}.",
  "config": { "orchestrationFile": "orchestration/brainstorm-spec.orch.md", "templateFile": "templates/brainstorm-spec.md",
    "model_profile": "interviewer",
    "outputSchema": { "type": "object", "required": ["spec_markdown"], "properties": {
      "slug": { "type": "string", "description": "Recommended kebab-case slug." },
      "spec_markdown": { "type": "string", "description": "The complete product spec as Markdown." } } } } },
{ "id": "write-spec", "type": "script", "label": "Write spec",
  "description": "Write docs/specs/{date}-{slug}-spec.md and commit.",
  "config": { "scriptFile": "scripts/write-spec.sh", "interpreter": "/bin/sh", "timeoutSeconds": 60 } },
{ "id": "brainstorm-design", "type": "interactive", "label": "Brainstorm: design",
  "description": "Interactive design brainstorm; emits {phases:[{title,markdown}]}.",
  "config": { "orchestrationFile": "orchestration/brainstorm-design.orch.md", "templateFile": "templates/brainstorm-design.md",
    "model_profile": "interviewer",
    "outputSchema": { "type": "object", "required": ["phases"], "properties": {
      "phases": { "type": "array", "items": { "type": "object", "required": ["title", "markdown"], "properties": {
        "title": { "type": "string" }, "markdown": { "type": "string" } } } } } } } },
{ "id": "write-designs", "type": "script", "label": "Write designs",
  "description": "Write docs/designs/{date}-{slug}-design-phase-N.md and commit.",
  "config": { "scriptFile": "scripts/write-designs.sh", "interpreter": "/bin/sh", "timeoutSeconds": 120 } }
```

Edges (replace the temporary `notify-start → done`):
```json
{ "source": "notify-start", "target": "brainstorm-spec" },
{ "source": "brainstorm-spec", "target": "write-spec" },
{ "source": "write-spec", "target": "brainstorm-design" },
{ "source": "brainstorm-design", "target": "write-designs" },
{ "source": "write-designs", "target": "done" }
```

- [ ] **Step 8: Validate. Expect ONE class of error: unknown `model_profile` "interviewer"** (profiles are defined in Task 9). All file references resolve; structure is valid. This is expected "red" — record it; it clears in Task 9.

> If you prefer to keep validation green throughout, define the four profiles (Task 9 Step 1) now, before this validate. Either order works; the plan defines profiles last so the graph is complete first.

- [ ] **Step 9: Commit.**

```bash
git add workspaces/feature-pipeline/
git commit -m "feat(feature-pipeline): interactive spec + design brainstorm nodes"
```

---

## Task 4: spec-only gate

**Files:** Modify `workflow.json` (add `spec-only?` conditional + `done-spec` terminal).

- [ ] **Step 1: Add the conditional and a spec-only terminal** via `workflow_update`.

```json
{ "id": "spec-only?", "type": "conditional", "label": "Spec only?",
  "description": "If spec_only, stop after designs; else continue to the automated pipeline.",
  "config": { "conditions": [ { "field": "spec_only", "operator": "eq", "value": true, "output": "stop" } ],
    "defaultOutput": "continue" } },
{ "id": "done-spec", "type": "transform", "label": "Done (spec-only, terminal)",
  "description": "Terminal node for the --spec-only path.",
  "config": { "operations": [ { "variable": "pipeline_status", "operation": "set", "value": "spec-only-complete" } ] } }
```

Rewire: `write-designs → spec-only?`; `spec-only? --stop--> done-spec`; `spec-only? --continue--> align-designs` (the `continue` edge targets `align-designs`, added in Task 5 — until then, temporarily target `done` so the graph stays connected/valid).

```json
{ "source": "write-designs", "target": "spec-only?" },
{ "source": "spec-only?", "target": "done-spec", "output": "stop" },
{ "source": "spec-only?", "target": "done", "output": "continue", "label": "TEMP until Task 5" }
```
(Remove the old `write-designs → done` edge.)

- [ ] **Step 2: Validate.** Expected: same single "interviewer profile unknown" error as Task 3; conditional ports both have edges; `done-spec` reachable. No new errors.

- [ ] **Step 3: Commit.**

```bash
git add workspaces/feature-pipeline/
git commit -m "feat(feature-pipeline): spec-only gate"
```

---

## Task 5: Alignment + plan-production agent nodes

**Files:**
- Create: `templates/align-designs.md`, `templates/produce-plans.md`, `templates/align-plans.md`
- Modify: `workflow.json` (add `align-designs`, `produce-plans`, `align-plans`)

- [ ] **Step 1: Write `templates/align-designs.md`** (alignment-reviewer persona, designs scope):

```markdown
# Mission: Align Design Docs to Spec

You are a meticulous alignment reviewer working **unattended** in the git worktree at `./work` (your cwd is the instance workspace root). The user is unavailable — do not ask questions; fix issues directly.

## Inputs
- Spec: `./work/{{write-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`

## Do
1. Read the spec and ALL design phase docs.
2. Review them holistically for alignment:
   - Terminology consistency (same names for same concepts)
   - Data-model / component / interface consistency across phases
   - Each phase's output feeding correctly into the next (integration seams)
   - Coverage: every spec requirement appears in some phase; no contradictions
   - Strict-TDD testing sections present in every phase (happy/success/failure/error/edge)
3. Fix problems **directly in the design docs**. If alignment is already clean, make no edits.
4. Commit in the worktree:
   `git -C ./work add docs/designs/ && git -C ./work commit -m "docs: align {{slug}} design phases to spec"`
   (If nothing changed, skip the commit.)

Report a one-paragraph summary of what you changed (or "already aligned").
```

- [ ] **Step 2: Write `templates/produce-plans.md`** (distilled `superpowers:writing-plans`):

```markdown
# Mission: Produce Implementation Plans

You are writing **detailed, code-level implementation plans** for a skilled engineer who has zero context for this codebase and questionable test taste. Work **unattended** in the worktree at `./work`. Do not ask questions.

## Inputs
- Spec: `./work/{{write-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`

## Output
For each design phase, write one or more **task plan files** at:
`./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-{N}-task-{M}.md`
(N = design phase number, M = task number within the phase).

## Rules (follow exactly)
- **Bite-sized steps (2–5 min each)**, using checkbox syntax `- [ ]`. The canonical TDD cycle per unit of behavior:
  write the failing test → run it, confirm it fails → minimal implementation → run it, confirm it passes → commit.
- **Strict TDD:** include tests for happy-path, success, failure, error, and edge-case scenarios.
- **Exact file paths** always. **Complete code** in every code step (never "add validation" / "handle errors" — show the code). **Exact commands** with expected output.
- DRY, YAGNI, frequent commits.
- Each task file starts with a header: a one-line Goal, 2–3 line Architecture, and Tech Stack line.
- Do not include any "offer execution choice" or sub-skill handoff — execution is handled by the pipeline.

## Finish
`git -C ./work add docs/plans/ && git -C ./work commit -m "docs: write {{slug}} implementation plans"`
Report the number of task files written.
```

- [ ] **Step 3: Write `templates/align-plans.md`** (alignment-reviewer, full hierarchy):

```markdown
# Mission: Align Implementation Plans

You are an alignment reviewer working **unattended** in the worktree at `./work`. Review the full document hierarchy and fix issues directly. The user is unavailable.

## Inputs
- Spec: `./work/{{write-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`

## Review for
- Spec → design alignment (requirements coverage, terminology)
- Design → plan alignment (architectural decisions reflected in tasks)
- Plan → plan consistency: shared types, function signatures, API contracts match across tasks
- Dependency ordering: a task that consumes a type defines/uses it consistently with the task that produces it
- No duplicate work; no gaps (every design requirement has a plan task)
- Strict-TDD coverage present in each task (happy/success/failure/error/edge)

Fix problems directly in the plan docs (and designs/spec if a defect there causes the misalignment).

## Finish
`git -C ./work add docs/plans/ docs/designs/ docs/specs/ && git -C ./work commit -m "docs: align {{slug}} implementation plans"`
(Skip the commit if nothing changed.) Report a one-paragraph summary.
```

- [ ] **Step 4: Add the three agent nodes** via `workflow_update` and wire `…continue… → align-designs → produce-plans → align-plans → done` (tail temporary).

```json
{ "id": "align-designs", "type": "agent_call", "label": "Align designs",
  "description": "Read spec + designs; fix alignment in place; commit.",
  "config": { "templateFile": "templates/align-designs.md", "model_profile": "planner", "timeoutSeconds": 1800 } },
{ "id": "produce-plans", "type": "agent_call", "label": "Produce plans",
  "description": "Write detailed TDD task plans for each design phase; commit.",
  "config": { "templateFile": "templates/produce-plans.md", "model_profile": "planner", "timeoutSeconds": 3600 } },
{ "id": "align-plans", "type": "agent_call", "label": "Align plans",
  "description": "Align spec+designs+plans; fix in place; commit.",
  "config": { "templateFile": "templates/align-plans.md", "model_profile": "planner", "timeoutSeconds": 1800 } }
```

Edges (replace the temporary `spec-only? --continue--> done`):
```json
{ "source": "spec-only?", "target": "align-designs", "output": "continue" },
{ "source": "align-designs", "target": "produce-plans" },
{ "source": "produce-plans", "target": "align-plans" },
{ "source": "align-plans", "target": "done", "label": "TEMP until Task 6" }
```

- [ ] **Step 5: Validate.** Expected errors now: unknown profiles `interviewer` AND `planner` (cleared in Task 9). No structural errors. agent_call→planner will validate as kind-correct once `planner` is defined as an **agent** profile.

- [ ] **Step 6: Commit.**

```bash
git add workspaces/feature-pipeline/
git commit -m "feat(feature-pipeline): design-align, plan-production, plan-align nodes"
```

---

## Task 6: Draft-implementation loop

**Files:**
- Create: `templates/impl-task.md`, `templates/impl-assess.md`
- Modify: `workflow.json` (add `impl-task`, `impl-assess`, `impl-inc`, `impl-gate`; back-edge)

- [ ] **Step 1: Write `templates/impl-task.md`** (distilled `superpowers:executing-plans`, one task per pass):

```markdown
# Mission: Implement the Next Plan Task (strict TDD)

You are implementing a feature **one plan task per pass**, working **unattended** in the git worktree at `./work`. The user is unavailable — never stop to ask; if blocked, make the best correct decision and note it in your summary.

## Inputs
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`
- Each plan task uses `- [ ]` checkboxes per step. A task is "done" when all its steps are checked.

## Do (this pass only)
1. List the plan task files in order. Find the **first task with any unchecked `- [ ]` step**. That is THIS pass's task. If every task is fully checked, make no code changes and report `all_tasks_complete = true`.
2. Execute that task's steps **exactly and in order**, honoring strict TDD: write the failing test, run it and confirm it fails, write the minimal implementation, run it and confirm it passes.
3. As you complete each step, check its box (`- [x]`) in the plan file.
4. Run the task's specified verifications. Commit using the task's commit step (or `git -C ./work add -A && git -C ./work commit -m "feat({{slug}}): <task title>"`).
5. Do NOT start the next task — one task per pass.

Red tests elsewhere in the suite are tolerated at this stage; later refinement will close them. Focus on completing THIS task's own steps correctly.

Report which task you completed and whether any plan tasks still have unchecked steps.
```

- [ ] **Step 2: Write `templates/impl-assess.md`** (chat assessment, structured output):

```markdown
# Assess: Draft-Implementation Progress

You are assessing implementation progress. You have no tools — reason only from the information below and emit a structured verdict.

## Context
- Plans live at `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`, using `- [ ]` / `- [x]` per step.
- The most recent implementation pass reported: {{impl-task}}

## Decide
- `all_tasks_complete`: true only if EVERY plan task has all steps checked (`- [x]`) — i.e. there is no remaining unchecked `- [ ]` step anywhere.
- `tasks_remaining`: your best integer estimate of plan tasks that still have unchecked steps.
- `notes`: one sentence on what remains (or "all tasks complete").

Base the decision on tasks/steps completion, NOT on whether the whole test suite is green (test-greenness is the ralph loop's job).
```

- [ ] **Step 3: Add the loop nodes** via `workflow_update`, wired `align-plans → impl-task → impl-assess → impl-inc → impl-gate`, with `impl-gate --loop--> impl-task` (back-edge) and `impl-gate --exit--> done` (tail temporary).

```json
{ "id": "impl-task", "type": "agent_call", "label": "Implement next task",
  "description": "Implement the next incomplete plan task, strict TDD, commit.",
  "config": { "templateFile": "templates/impl-task.md", "model_profile": "coder", "timeoutSeconds": 3600 } },
{ "id": "impl-assess", "type": "llm_call", "label": "Assess impl progress",
  "description": "Emit {all_tasks_complete, tasks_remaining, notes}.",
  "config": { "templateFile": "templates/impl-assess.md", "model_profile": "assessor",
    "outputSchema": { "type": "object", "required": ["all_tasks_complete", "tasks_remaining", "notes"], "properties": {
      "all_tasks_complete": { "type": "boolean" },
      "tasks_remaining": { "type": "number" },
      "notes": { "type": "string" } } } } },
{ "id": "impl-inc", "type": "transform", "label": "impl_iteration++",
  "config": { "operations": [ { "variable": "impl_iteration", "operation": "increment", "value": 1 } ] } },
{ "id": "impl-gate", "type": "conditional", "label": "Impl loop or exit",
  "description": "Exit when all tasks complete OR iteration cap reached; else loop.",
  "config": { "conditions": [
    { "field": "impl-assess.all_tasks_complete", "operator": "eq", "value": true, "output": "exit" },
    { "field": "impl_iteration", "operator": "gte", "value": "{{max_impl_iterations}}", "output": "exit" } ],
    "defaultOutput": "loop" } }
```

Edges (replace the temporary `align-plans → done`):
```json
{ "source": "align-plans", "target": "impl-task" },
{ "source": "impl-task", "target": "impl-assess" },
{ "source": "impl-assess", "target": "impl-inc" },
{ "source": "impl-inc", "target": "impl-gate" },
{ "source": "impl-gate", "target": "impl-task", "output": "loop", "label": "back-edge: next task" },
{ "source": "impl-gate", "target": "done", "output": "exit", "label": "TEMP until Task 7" }
```

- [ ] **Step 4: Validate.** Expected: only unknown-profile errors (`interviewer`, `planner`, `coder`, `assessor`). The cyclic back-edge is allowed (no acyclicity check). Confirm `impl-gate` has edges for both `loop` and `exit` ports and no structural errors.

- [ ] **Step 5: Commit.**

```bash
git add workspaces/feature-pipeline/
git commit -m "feat(feature-pipeline): task-iterating draft-implementation loop"
```

---

## Task 7: Seed-ralph + native ralph refinement loop

**Files:**
- Create: `templates/seed-ralph.md`, `templates/refine.md`, `templates/refine-assess.md`
- Modify: `workflow.json` (add `seed-ralph`, `refine`, `refine-assess`, `refine-inc`, `refine-gate`; back-edge)

- [ ] **Step 1: Write `templates/seed-ralph.md`** (distilled `auto-ralph-prep`):

```markdown
# Mission: Seed Ralph Refinement Files

Working **unattended** in the worktree at `./work`, generate the three ralph tracking files at the repo root of the worktree, then commit. The user is unavailable.

## Inputs
- Spec: `./work/{{write-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`

## Create (in `./work`)
- `RALPH.md` — the refinement charter: the goal of this feature, how to run the project's tests/build, the definition of done (all plan tasks implemented, full test suite green incl. happy/success/failure/error/edge, designs satisfied), and links to the spec/designs/plans.
- `focus-areas.md` — an ordered, prioritized list of the most important remaining work to reach done (failing tests first, then gaps vs. the plans/designs).
- `gaps-identified.md` — concrete known gaps between current code and the spec/designs/plans (missing tests, unimplemented behavior, TODOs).

## Finish
`git -C ./work add RALPH.md focus-areas.md gaps-identified.md && git -C ./work commit -m "chore: seed {{slug}} ralph refinement files"`
Report the top 3 focus areas.
```

- [ ] **Step 2: Write `templates/refine.md`** (native ralph mission — adapted from `examples/workflows/native-ralph-loop/mission.md`):

```markdown
# Mission: Ralph Refinement Pass

Working **unattended** in the worktree at `./work`, make the **smallest correct improvement** toward "done". The user is unavailable.

## Each pass
1. Read `./work/RALPH.md`, `./work/focus-areas.md`, `./work/gaps-identified.md`.
2. Run the project's test suite (and build) to see current status.
3. Pick the **single highest-priority** item from `focus-areas.md` (failing tests first). Make the smallest correct change that advances it — following strict TDD where adding behavior (failing test for happy/success/failure/error/edge → minimal code → green).
4. Re-run the affected tests.
5. **Update the tracking files in place:** strike completed focus areas, add newly discovered gaps, reprioritize.
6. Commit: `git -C ./work add -A && git -C ./work commit -m "refine({{slug}}): <what you changed>"`.

Make progress every pass; never leave the tree uncommitted.
```

- [ ] **Step 3: Write `templates/refine-assess.md`** (adapted from `examples/workflows/native-ralph-loop/assessment.md`):

```markdown
# Assess: Refinement Done?

You are assessing whether refinement is complete. Reason only from the information below; emit a structured verdict.

## Context
- Tracking files live at `./work/RALPH.md`, `./work/focus-areas.md`, `./work/gaps-identified.md`.
- The most recent refinement pass reported: {{refine}}

## Decide
- `done`: true only when the definition of done in RALPH.md is satisfied — all plan tasks implemented, the full test suite green (including happy/success/failure/error/edge), no remaining focus areas or gaps.
- `notes`: one sentence on the remaining blocker, or "done".

Be conservative: if any focus area or gap remains, `done = false`.
```

- [ ] **Step 4: Add seed + loop nodes** via `workflow_update`, wired `impl-gate --exit--> seed-ralph → refine → refine-assess → refine-inc → refine-gate`, with `refine-gate --loop--> refine` and `refine-gate --exit--> push-and-pr` (the `push-and-pr` target is added in Task 8 — until then, temporarily target `done`).

```json
{ "id": "seed-ralph", "type": "agent_call", "label": "Seed ralph files",
  "description": "Generate RALPH.md/focus-areas.md/gaps-identified.md; commit.",
  "config": { "templateFile": "templates/seed-ralph.md", "model_profile": "planner", "timeoutSeconds": 1800 } },
{ "id": "refine", "type": "agent_call", "label": "Refine (ralph pass)",
  "description": "Smallest correct improvement toward focus areas; run tests; update tracking files; commit.",
  "config": { "templateFile": "templates/refine.md", "model_profile": "coder", "timeoutSeconds": 3600 } },
{ "id": "refine-assess", "type": "llm_call", "label": "Assess refinement",
  "description": "Emit {done, notes}.",
  "config": { "templateFile": "templates/refine-assess.md", "model_profile": "assessor",
    "outputSchema": { "type": "object", "required": ["done", "notes"], "properties": {
      "done": { "type": "boolean" }, "notes": { "type": "string" } } } } },
{ "id": "refine-inc", "type": "transform", "label": "ralph_iteration++",
  "config": { "operations": [ { "variable": "ralph_iteration", "operation": "increment", "value": 1 } ] } },
{ "id": "refine-gate", "type": "conditional", "label": "Refine loop or exit",
  "description": "Exit when done OR ralph cap reached; else loop.",
  "config": { "conditions": [
    { "field": "refine-assess.done", "operator": "eq", "value": true, "output": "exit" },
    { "field": "ralph_iteration", "operator": "gte", "value": "{{max_ralph_iterations}}", "output": "exit" } ],
    "defaultOutput": "loop" } }
```

Edges (replace the temporary `impl-gate --exit--> done`):
```json
{ "source": "impl-gate", "target": "seed-ralph", "output": "exit" },
{ "source": "seed-ralph", "target": "refine" },
{ "source": "refine", "target": "refine-assess" },
{ "source": "refine-assess", "target": "refine-inc" },
{ "source": "refine-inc", "target": "refine-gate" },
{ "source": "refine-gate", "target": "refine", "output": "loop", "label": "back-edge: another pass" },
{ "source": "refine-gate", "target": "done", "output": "exit", "label": "TEMP until Task 8" }
```

- [ ] **Step 5: Validate.** Expected: only unknown-profile errors. Both loops cyclic and allowed; both gates have `loop`+`exit` edges. No structural errors.

- [ ] **Step 6: Commit.**

```bash
git add workspaces/feature-pipeline/
git commit -m "feat(feature-pipeline): seed-ralph + native ralph refinement loop"
```

---

## Task 8: Push, open PR, final notification

**Files:**
- Create: `scripts/push-and-pr.sh`
- Modify: `workflow.json` (add `push-and-pr`, `notify-done`; final wiring); `~/.arbitron/config.yaml` (`script.envpassthrough`)

- [ ] **Step 1: Write `scripts/push-and-pr.sh`** (path `scripts/push-and-pr.sh`):

```sh
#!/bin/sh
# Pushes the feature branch and opens a PR. Reads vars from stdin JSON; emits
# {pr_url, pr_number, branch} as JSON. Fails loudly if gh is unauthenticated.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug')"
BRANCH="$(printf '%s' "$IN" | jq -r '.branch')"

gh auth status 1>&2 || { echo "gh not authenticated" >&2; exit 1; }
git -C "$WORK" push --quiet -u origin "$BRANCH" 1>&2
PR_URL="$(cd "$WORK" && gh pr create --fill --head "$BRANCH" --title "feat: $SLUG" \
  --body "Automated feature pipeline for $SLUG. Spec/designs/plans under docs/. Ready for multi-persona PR review.")"
PR_NUMBER="$(printf '%s' "$PR_URL" | sed 's:.*/::')"
jq -n --arg u "$PR_URL" --arg n "$PR_NUMBER" --arg b "$BRANCH" '{pr_url:$u, pr_number:$n, branch:$b}'
```

- [ ] **Step 2: Ensure `script.envpassthrough` is set** in `~/.arbitron/config.yaml` (so `git`/`gh` get `HOME`/`PATH`/token). Add the block from Prerequisites if missing, then restart `arbitron serve`. Verify with the smoke run in Step 5.

- [ ] **Step 3: Add the final nodes** via `workflow_update`, wired `refine-gate --exit--> push-and-pr → notify-done → done`.

```json
{ "id": "push-and-pr", "type": "script", "label": "Push & open PR",
  "description": "Push the feature branch and open a GitHub PR (auto-triggers the pr-review workflow on PR open).",
  "config": { "scriptFile": "scripts/push-and-pr.sh", "interpreter": "/bin/sh", "timeoutSeconds": 300,
    "env": { "GH_TOKEN": "{{GH_TOKEN}}" } } },
{ "id": "notify-done", "type": "script", "label": "Notify: PR opened",
  "description": "In-app notification that the PR is open.",
  "config": { "scriptFile": "scripts/notify.sh", "interpreter": "/bin/sh", "timeoutSeconds": 30,
    "args": ["Feature pipeline complete for {{slug}}: PR {{push-and-pr.pr_url}} opened. PR review will run on open."] } }
```

> The `env.GH_TOKEN` line forwards a per-node value only if `GH_TOKEN` is a resolvable var; if auth rides on `envpassthrough` instead, omit `env` here. Keep exactly one auth path.

Edges (replace the temporary `refine-gate --exit--> done`):
```json
{ "source": "refine-gate", "target": "push-and-pr", "output": "exit" },
{ "source": "push-and-pr", "target": "notify-done" },
{ "source": "notify-done", "target": "done" }
```

- [ ] **Step 4: Validate.** Expected: only unknown-profile errors remain. The graph is now complete: single entrypoint `init-workspace`, two terminals (`done`, `done-spec`), both loops closed, all ports edged.

- [ ] **Step 5: Smoke-test `push-and-pr.sh` argument plumbing** (without a real push) by dry-reading:
  Run: `sh -n workspaces/feature-pipeline/scripts/push-and-pr.sh && echo "syntax ok"`
  Expected: `syntax ok`. (A full push happens only during a real instance run.)

- [ ] **Step 6: Commit.**

```bash
git add workspaces/feature-pipeline/
git commit -m "feat(feature-pipeline): push + open PR + completion notification"
```

---

## Task 9: Define profiles and final validation

**Files:** Modify `~/.arbitron/config.yaml` (add the four profile aliases); validate the whole workflow.

- [ ] **Step 1: Add the four profiles** to the `profiles:` list in `~/.arbitron/config.yaml`. All bound to the local `claude -p` backend (chat = tool-suppressed; agent = tools on). `model: ""` means "use the claude-cli default model."

```yaml
profiles:
  - { alias: interviewer, kind: chat,  model: "" }
  - { alias: assessor,    kind: chat,  model: "" }
  - { alias: planner,     kind: agent, model: "" }
  - { alias: coder,       kind: agent, model: "" }
```

Restart the server so the new profiles load: stop the running `arbitron serve`, then `./arbitron serve > /tmp/arbitron-serve.log 2>&1 &`. (Reconnect the MCP server in Claude Code if needed.)

- [ ] **Step 2: Final validate — expect CLEAN (no errors).**

Call `workflow_validate` with `{ "name": "feature-pipeline" }`.
Expected: zero errors. Kind-matching now passes: `interactive`/`llm_call` nodes → chat profiles (`interviewer`/`assessor`); `agent_call` nodes → agent profiles (`planner`/`coder`). If any `INTERACTIVE_IN_LOOP` advisory appears, confirm it does NOT reference `brainstorm-spec`/`brainstorm-design` (they are pre-loop) — those two interactive nodes must not be on a cycle. There should be no such advisory.

- [ ] **Step 3: Structural smoke check of the assembled manifest** via `workflow_get` (or `jq` on the file):

```bash
jq '{nodes: (.nodes|length), edges: (.edges|length),
     entrypoints: [.nodes[]|select(.entrypoint==true)|.id],
     agent_nodes: [.nodes[]|select(.type=="agent_call")|.id],
     loops: [.edges[]|select(.label|test("back-edge"))| (.source+"→"+.target)]}' \
  workspaces/feature-pipeline/workflow.json
```
Expected: one entrypoint (`init-workspace`); agent nodes = `align-designs, produce-plans, align-plans, impl-task, seed-ralph, refine`; two back-edges (`impl-gate→impl-task`, `refine-gate→refine`).

- [ ] **Step 4: Confirm every template/script/orchestration file referenced by the manifest exists** via `workflow_list_files`, cross-checked against the manifest's `templateFile`/`scriptFile`/`orchestrationFile` values. No dangling references.

- [ ] **Step 5: Commit.**

```bash
git add workspaces/feature-pipeline/
git commit -m "feat(feature-pipeline): finalize; workflow validates clean"
```

---

## Task 10: End-to-end dry run (optional but recommended)

**Files:** none (operational verification).

- [ ] **Step 1: Trigger a `spec_only` instance** against a small throwaway repo to exercise the interactive path without the long automated tail:
  Use the running skill `workflow-running` (or the MCP trigger tool) with inputs `{ repo_url: <small repo>, spec_only: true }`.
  Expected: instance reaches `waiting` at `brainstorm-spec` (you get an interaction notification); after you complete the spec + design brainstorms, it writes `docs/specs/...` and `docs/designs/...` in the worktree and ends at `done-spec`.

- [ ] **Step 2: Inspect the worktree and trace.** Confirm the dated spec/design files exist in `{{workspace}}/work` and the trace shows `init-workspace → … → write-designs → spec-only? (stop) → done-spec`.

- [ ] **Step 3:** Only run a full (non-spec-only) instance against a real target once the spec-only path is confirmed and `gh` auth is verified, since it runs the full automated tail (plans → impl loop → ralph loop → PR).

---

## Self-Review (completed against the design spec)

**Spec coverage:** every design §4 node is created (Tasks 1–8); the §6 de-skilled templates are written (Tasks 3, 5, 6, 7); §7 schemas appear on the interactive/llm_call nodes; §8 profiles in Task 9; §9 error handling via `defaultErrorBehavior` (retry 2×) + impl/ralph gate caps; §10 notifications via `notify.sh`; the §11 acceptance criteria map to Task 9 (validate), Task 10 (dry run), and the spec-only gate (Task 4). The two loops use only agent/llm/transform/conditional (no interactive in a cycle), per the §9 constraint.

**Placeholder scan:** no "TBD"/"implement later". Two items are explicitly deferred-with-instructions, not placeholders: the notifications endpoint shape (Task 2 Step 3 verifies and adjusts) and the `gh`/env-passthrough auth path (Prerequisites + Task 8 Step 2) — both have concrete verification steps.

**Type/name consistency:** node ids, profile aliases (`interviewer`/`assessor`/`planner`/`coder`), output fields (`spec_markdown`, `phases[].{title,markdown}`, `all_tasks_complete`/`tasks_remaining`, `done`), and promoted vars (`slug`/`run_date`/`work_dir`/`branch`) are used identically across templates, scripts, schemas, and conditionals.
