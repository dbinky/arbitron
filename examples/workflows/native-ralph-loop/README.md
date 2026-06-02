# Native Ralph Loop — Worked Example

A runnable worked example of the **native ralph refinement loop** expressed on
existing engine primitives only: a 4-node cyclic-edge graph with a back-edge. There
is **no new engine primitive** — just `agent_call`, `llm_call`, `transform`, and
`conditional` wired into a cycle.

This folder is **data** (a workflow definition plus its templates and a workspace
seed set). The same `workflow.json` runs under **both** the Claude-subscription and
the open-weight configurations: nodes reference only profile **aliases**
(`editor`, `assessor`), never a vendor. To switch vendors you repoint the alias
bindings (below); the graph is byte-identical.

## The graph

```
editor (agent_call)  ──►  assessor (llm_call)  ──►  increment-counter (transform)
   ▲                                                          │
   │ output: loop (back-edge)                                 ▼
   └──────────────────────────  loop-or-exit (conditional)  ◄─┘
                                          │ output: exit
                                          ▼
                                  summary (transform, terminal)
```

- **`editor`** — `agent_call`, the entrypoint. Tools enabled. Reads `RALPH.md`,
  `focus-areas.md`, `gaps.md` from `{{workspace}}` and makes the smallest correct
  edit toward the top focus area, then rewrites those tracking files. Template:
  `mission.md`.
- **`assessor`** — `llm_call`, tool-suppressed reasoning. Emits a structured
  `{ done: boolean, notes: string }` result (`outputSchema`). The `done` field is
  read by the conditional as `assessor.done`. Template: `assessment.md`.
- **`increment-counter`** — `transform`, adds 1 to the numeric `iteration` counter.
- **`loop-or-exit`** — `conditional`, routes to `exit` or `loop` (see below).
- **`summary`** — `transform`, terminal node (no outgoing edges) so the walk ends
  cleanly and the `exit` port reaches a real node for connectivity validation.

## How `done OR counter >= max` is encoded

The conditional executor returns the output port of the **first matching**
condition, else `defaultOutput`. There is **no boolean-OR operator**, so the OR is
expressed as **two condition entries that both route to the same `exit` port**
(first-match wins):

```json
"conditions": [
  {"field": "assessor.done", "operator": "eq",  "value": true,             "output": "exit"},
  {"field": "iteration",     "operator": "gte", "value": "{{max_iterations}}", "output": "exit"}
],
"defaultOutput": "loop"
```

- If the assessor said `done: true` → first condition matches → `exit`.
- Else if `iteration >= max_iterations` → second condition matches → `exit`.
- Otherwise → `defaultOutput: loop` → the **back-edge** to `editor` for another pass.

Both `exit` conditions and the `loop` default have matching edges from
`loop-or-exit`, satisfying `validateConditionalEdges`. The back-edge is an ordinary
edge — the engine has no acyclicity check — and it does not create an unreachable
node, so `validateConnectivity` (BFS from `editor`) passes.

The `iteration` counter is declared as an **input with `default: 0`**, so it exists
and is numeric before the first `increment`.

## Workspace seed set

`seed/RALPH.md`, `seed/focus-areas.md`, and `seed/gaps.md` are **workspace
content**, not part of `workflow.json`. Before the first pass, the harness copies
this seed set into the instance `{{workspace}}` (alternatively, the first `editor`
pass creates them). The `editor` reads and rewrites them each iteration; the
`assessor` reasons over their summarized contents.

## Two alias bindings (same graph, different vendors)

The graph references aliases only. Bind them in the profile registry. **Keys never
live in these files** — they live only in the connection store, supplied via the
management surface; the snippets below name a `connectionRef` and the connection's
key is revealed lazily at call/launch time. The Claude subscription path uses an
empty `connectionRef` (ambient login, no key).

### Claude binding (subscription path)

```yaml
profiles:
  - alias: editor          # agent_call -> agent profile, tools enabled
    kind: agent
    connectionRef: ""       # empty = claude-cli no-key subscription (ambient login)
    model: claude-sonnet-4
  - alias: assessor        # llm_call -> chat profile, tool-suppressed
    kind: chat
    connectionRef: ""       # empty = claude-cli no-key subscription
    model: claude-sonnet-4
```

### Open-weight binding (connection-backed)

```yaml
# Connections (metadata only; the API key is sealed in the connection store,
# never in config — supplied via the management surface).
connections:
  - name: opencode-openweight
    provider: opencode
    baseURL: https://example-openweight-endpoint/v1
  - name: openrouter-chat
    provider: openrouter
    baseURL: https://openrouter.ai/api/v1

profiles:
  - alias: editor          # same alias, now an opencode agent on an open-weight model
    kind: agent
    connectionRef: opencode-openweight   # key env-injected at launch
    model: qwen/qwen3-coder
  - alias: assessor        # same alias, now an OpenRouter (or Azure AI Foundry) chat
    kind: chat
    connectionRef: openrouter-chat
    model: deepseek/deepseek-r1
```

Both bindings keep the aliases `editor`/`assessor`; only the profile/connection
targets differ. Swapping between them does not touch `workflow.json`.

## Validation

`workflow.json` passes `workflow_validate` (structural checks, connectivity,
conditional-edge integrity, and — with a profile checker that knows
`editor`=agent / `assessor`=chat — capability validation) under either binding. The
negative variant (binding `editor` to a chat-only profile) is reserved for the
acceptance harness; it must **fail** validation with `PROFILE_KIND_MISMATCH`.
