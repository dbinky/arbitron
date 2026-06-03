# Interactive Agent Node + Agent YOLO Execution — Design

**Status:** Draft · **Date:** 2026-06-02 · **Type:** Engine + workflow design

## 1. Purpose & Outcome

Arbitron's `interactive` node is a human-in-the-loop **chat** session: it streams an LLM turn, pauses (`waiting`), resumes on the human's reply, and completes when the model emits a completion sentinel whose JSON becomes the node's structured output. This works for small structured results but **fails for large documents**: a brainstorm that must return a full spec (or worse, multiple design-phase docs) as one JSON sentinel exceeds `claude -p`'s per-turn output cap, the closing tag never arrives, the sentinel never matches, and the instance is stuck in `waiting` forever. `claude -p` exposes no max-output flag, so the cap can't be raised; and the node's `maxTokens` config is a dead no-op because `buildArgs` forwards no token limit. This was discovered dogfooding the feature-pipeline: the spec brainstorm got stuck exactly this way.

A second limitation surfaced alongside it: today's `interactive` node is a **tool-suppressed chat** backend, so an interactive brainstorm cannot actually read the repo it was told to ground itself in — it fabricates "I skimmed the README." (By contrast, the autonomous `agent_call` backend **already runs tool-enabled and YOLO** — the claude-cli agent adapter hardcodes `--dangerously-skip-permissions` with the full tool scope — so its file edits work. The gap is specifically that *interactivity* has only ever been wired to the suppressed chat path; there is no tool-enabled interactive backend.)

**Outcome:** introduce an **`interactive_agent`** node — a human-in-the-loop session backed by a **tool-enabled, YOLO** `claude -p` (the same execution posture the autonomous agent adapter already uses) — so doc-producing interactive steps **write artifacts to the workspace via tool calls** and return only tiny metadata through the sentinel. The output-cap failure mode is structurally eliminated, and the brainstorm can genuinely read `./src`.

### Non-goals

- No max-output-tokens plumbing to `claude -p` (it has no such flag; and it would only raise a ceiling we'd hit again). The dead `maxTokens` field is left for a separate cleanup decision, not relied upon.
- No opencode interactive-agent yet (claude-cli only); the opencode YOLO equivalent lands with that backend.
- No change to the chat nodes' tool-suppressed contract.
- No per-tool-call human approval UX (claude `--print` can't prompt; YOLO in isolated worktrees is the accepted model).

## 2. Node Model

Fill the missing cell of the capability × interactivity matrix:

| | autonomous | interactive |
|---|---|---|
| **chat** (reason, no side effects) | `llm_call` | `interactive_llm` *(renamed from `interactive`)* |
| **agent** (tools, writes files) | `agent_call` | `interactive_agent` *(new)* |

- **Rename `interactive` → `interactive_llm`.** Update the `NodeType` constant, the executor registry, validation, and every `node.Type == NodeTypeInteractive` site (engine, conversation handler, http handler). **Hard rename — no back-compat alias** (no important persisted instances; an alias is a trap). A grep confirms no keyed site is missed.
- **Add `interactive_agent`.** It reuses the *same* interactive machinery (pause → `waiting` → resume on human message → repeat → completion sentinel); the only difference is the streaming backend it uses.
- **Profile-kind validation** applies to both: `interactive_llm` requires a **chat** profile; `interactive_agent` requires an **agent** profile. This also closes the current gap where the validator does not kind-check `interactive` nodes at all.

## 3. `interactive_agent` Execution

The key simplification: the *only* difference between the two interactive types is the arguments handed to `claude -p`. Pause/resume (`--resume <sessionId>`), the auto-appended completion sentinel, conversation persistence, and the `waiting` lifecycle are identical and already exist.

- **Factor the interactive core.** Extract the shared pause/resume/sentinel/conversation logic from today's `InteractiveExecutor` into a reusable core. Register two thin executors over it — `interactive_llm` and `interactive_agent` — differing only in the streaming backend they pass to the core.
- **Two streaming backends, one CLI path.** `buildArgs` already exposes `SuppressTools` and `SkipPermissions`:
  - `interactive_llm` → tool-suppressed stream (`SuppressTools:true`), no workspace side effects — today's behavior, renamed.
  - `interactive_agent` → **tool-enabled stream**: `SuppressTools:false`, `--dangerously-skip-permissions`, `Dir = {{workspace}}`. The agent reads `./src`, writes `./work/...`, and `git commit`s, all mid-conversation.
- **Stream carries tool events.** `--output-format stream-json` interleaves `tool_use`/`tool_result` with assistant text. The stream parser must tolerate these (and should surface them so the dashboard shows the agent's actions), while still accumulating assistant text and detecting the completion sentinel. Because the document is written to disk via tools, **the sentinel payload stays tiny** (`{slug, spec_path}`), so the per-turn output cap is never in the artifact's path.
- **Resume across turns** uses the existing `--resume <sessionId>`; the session preserves prior tool use and context.
- **Profile/WorkDir:** `interactive_agent` resolves an agent profile and runs with `WorkDir = {{workspace}}` (the instance root), exactly like `agent_call`.

## 4. Agent YOLO Execution (capability split)

The permission model follows the chat/agent capability split, and is **already the reality** for the autonomous backends — this section makes it explicit and extends it to the new interactive-agent path:

- **Chat backends are tool-suppressed by construction** (`llm_call`, `interactive_llm`): the claude-cli chat adapter emits `--allowedTools ""`, no skip-permissions — no tools, no side effects, safe regardless of prompt. Unchanged.
- **Agent backends run YOLO by construction** (`agent_call`, and now `interactive_agent`): the claude-cli agent path emits `--dangerously-skip-permissions` with the full tool scope. `agent_call` **already** does this (hardcoded in the agent adapter); `interactive_agent`'s new tool-enabled streaming path must adopt the **same** posture. Justification: each instance works in an **isolated per-instance worktree** under `{{workspace}}`. The opencode backend gets its YOLO equivalent when it lands.
- **No `agent_call` change is required** — it already edits files correctly. The only new YOLO surface is `interactive_agent`'s streaming backend.
- **`toolScope`** remains available to *narrow* an agent node's tools (already supported on `agent_call`); the default is full.
- **Accepted residual risk:** `--dangerously-skip-permissions` is RCE-equivalent and Bash is not jailed to the worktree (it runs as the host user). This is the *existing* posture for agent work, accepted for self-hosted, single-user, author-trusted prompts — documented, not hidden.

## 5. Feature-Pipeline Workflow Changes

- **`brainstorm-spec` / `brainstorm-design` become `interactive_agent`** (referencing an agent profile). The human Q&A is unchanged — one question per turn, propose approaches, present sections, get approval. On approval the agent **writes the artifact(s) itself and commits**, emitting only a compact sentinel:
  - spec → writes `docs/specs/{run_date}-{slug}-spec.md`; emits `{slug, spec_path}`.
  - design → writes each `docs/designs/{run_date}-{slug}-design-phase-{N}.md`; emits `{slug, design_paths}`. The multi-phase JSON blob is gone.
- **Remove `write-spec` + `scripts/write-spec.sh` and `write-designs` + `scripts/write-designs.sh`** — the agent writes and commits directly. The graph gets simpler.
- **Keep `repromote-slug`** (set the global `slug` from `brainstorm-spec.slug`).
- **Update templates** `brainstorm-spec.md` / `brainstorm-design.md` and their orchestration files: genuinely read `./src`, run the one-question-at-a-time brainstorm, and on approval write the dated file(s) into `./work`, `git add`/`commit`, then emit the compact sentinel — never the document itself.
- **Profiles:** add/repoint an agent-kind profile for the brainstorm role (e.g. an agent `interviewer`, or reuse `planner`). The chat `assessor` profile is unchanged.

## 6. Acceptance Criteria

1. `interactive` is renamed to `interactive_llm` with no remaining `NodeTypeInteractive` references; existing chat-interactive behavior is unchanged under the new name.
2. `interactive_agent` exists, is registered, and validates: it requires an agent profile; binding it to a chat profile fails `workflow_validate` (and `interactive_llm` bound to an agent profile likewise fails).
3. An `interactive_agent` session streams, lets the agent write a file in `{{workspace}}` via tools, emits a compact sentinel, self-completes, and advances the instance — with no document carried in the sentinel.
4. Multi-turn resume preserves session context across human replies.
5. `agent_call` and `interactive_agent` run with `--dangerously-skip-permissions` + tools; `llm_call`/`interactive_llm` remain tool-suppressed.
6. The feature-pipeline runs end-to-end on the arbitron repo: the spec and **multi-phase design** brainstorms complete via `interactive_agent` writing their own committed artifacts, then the automated tail (align → produce → impl loop → ralph loop → push/PR) proceeds — the original stuck-in-`waiting` failure does not recur.
7. A pre-implementation spike confirms tool-enabled `claude -p --output-format stream-json --resume` streams, writes files, and resumes across turns headlessly.

## 7. Testing (strict TDD — all five scenario categories)

- **Rename:** no `NodeTypeInteractive` remains; `interactive_llm` resolves; behavior parity.
- **Validation:** profile-kind happy paths for both interactive types; kind-mismatch each way fails with a clear error; regression that both interactive types are now kind-checked.
- **`interactive_agent` executor / tool-enabled stream:** happy (stream + tool write + sentinel + advance); success (multi-turn resume); failure (stream error → clean node failure); error (malformed/absent sentinel; profile-resolution error); edge (sentinel parsed amid interleaved tool events; empty assistant text; agent writes nothing yet still terminates).
- **YOLO args:** agent paths emit `--dangerously-skip-permissions` + tools; chat paths stay suppressed.
- **Workflow:** feature-pipeline validates with renamed/new node types and agent-profile brainstorm nodes.

## 8. Risks & Open Items (resolved during implementation)

- **Tool-enabled streaming + resume** is the one new behavior — de-risk first with the §6.7 spike; if headless tool-streaming resume proves unreliable, fall back to single-turn-per-resume with the workspace files carrying continuity. **Spike result (2026-06-02): PASS.** `claude -p --verbose --output-format stream-json --dangerously-skip-permissions` streamed a `Write` `tool_use` and created the file (turn 1), and `--resume <sessionId>` streamed an `Edit` `tool_use` and modified it (turn 2), `is_error:false` both turns. **Required condition:** `ANTHROPIC_API_KEY` must be unset (a set-but-invalid key forces API mode and fails with "Invalid API key"); `procenv.BaseEnv()` already strips it, so the arbitron-spawned path is correct. No fallback needed.
- **Stream parser** must not mis-handle `tool_use`/`tool_result` frames when scanning for the sentinel — covered by an edge test.
- **Dashboard display** of agent tool activity in an interactive session: surface minimally (event lines); rich rendering is out of scope.
- **Dead `maxTokens` field** on interactive nodes: either wire it through or remove it from the schema — decide during implementation; the design does not depend on it.
