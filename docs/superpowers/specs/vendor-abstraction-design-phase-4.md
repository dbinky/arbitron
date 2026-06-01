# Vendor-Abstraction MVP — Design Phase 4: Native Ralph Loop & Acceptance

**Date:** 2026-06-01 · **Status:** Design approved; pending implementation plan
**Spec:** `docs/specs/feature-pipeline-replacement-mvp.md`
**Depends on:** Phases 1–3 (both ports, both agent backends, all three chat providers, connections/profiles).

## Overview

Phase 4 delivers the **native ralph refinement loop**, replacing the external ralph-o-matic server with an in-engine cycle, and the end-to-end **acceptance harness** proving criteria #1–#7. The engine already permits cyclic graphs (no acyclicity check; `transform`/`conditional` re-execute on revisit; no unique `(instance, node)` constraint), so this phase builds **no new engine primitive** — it delivers a worked-example workflow on the supported cyclic-edge idiom, the documentation of its constraints, and the cross-configuration acceptance tests. This is the lowest-code, highest-proof phase.

Realizes acceptance **#4** (configuration interchangeability) and **#5** (native loop), and ties #1–#3/#6/#7 together in a single end-to-end run.

---

## 1. The Native Ralph Loop (cyclic-edge idiom)

Per spec §7, the loop is expressed with existing node kinds and existing engine behavior:

```
        ┌─────────────────────────── back-edge ──────────────────────────┐
        ▼                                                                 │
  [agent_call]  ──►  [llm_call]  ──►  [transform]  ──►  [conditional] ────┘
   edit toward        assess: run /     increment        done? OR
   focus areas        inspect tests,    iteration         counter ≥ max
   (AgentProvider)    remaining gaps    counter           ├─ loop (back-edge)
                      (ChatProvider)                       └─ exit
```

1. **`agent_call`** edits the workspace toward the current focus areas (AgentProvider — claude-cli or opencode).
2. **`llm_call`** assesses: inspects test results and remaining gaps, emits a structured `{done, notes}` (ChatProvider — tool-suppressed).
3. **`transform`** increments an iteration counter held in instance variables.
4. **`conditional`** evaluates *(done? OR counter ≥ max)* → routes out to the exit, else back along the **back-edge** to the `agent_call`.

`RALPH.md`, `focus-areas`, and `gaps` are ordinary files in the instance `{{workspace}}` that the `agent_call` reads and rewrites each pass (spec §7). No external server, no `ralph-o-matic` dependency.

### Why no new primitive

The engine's graph walker (`domain/engine/engine.go`) already follows edges until none remain and re-executes `transform`/`conditional` on revisit. A first-class loop construct is a non-goal (spec §9). The iteration counter is a plain instance variable; it persists across passes and is visible in the trace (acceptance #5).

---

## 2. Constraints (documented, enforced where cheap)

- **No `interactive` in a loop body.** The interactive executor short-circuits on a prior succeeded `(instance, node)` execution (it returns its first answer on every subsequent pass). Loop bodies use only `agent_call` / `llm_call` / `transform` / `conditional` / `tool_call`. This is documented in the workflow-authoring guidance; optionally a **validation lint** can warn when an `interactive` node sits on a cycle (detected via a back-edge reachability check). The lint is advisory, not a hard gate, to honor "cyclic graphs allowed."
- **No engine runaway guard (accepted MVP risk, spec §9).** Termination depends solely on the author's `conditional`. A malformed loop can run indefinitely; this is documented in the loop guidance and the README. The planned future backstop (max node-executions and/or wall-clock per instance) is noted as out of scope.

---

## 3. Worked-Example Workflow (deliverable artifact)

A committed example workflow folder demonstrating the loop, runnable under **both** configurations by repointing profile aliases only (acceptance #4):

- `workflow.json` — the 4-node cycle above, nodes referencing aliases `editor` (agent) and `assessor` (chat). Identical graph for both configs.
- Template files for the `agent_call` mission and the `llm_call` assessment.
- A seed `RALPH.md`/`focus-areas`/`gaps` set, or instructions for the first pass to create them in `{{workspace}}`.
- Two profile-registry settings exercised in tests: a **Claude** binding (`editor`→claude agent, `assessor`→claude chat) and an **open-weight** binding (`editor`→opencode agent on an open-weight model, `assessor`→OpenRouter/Azure chat). Same graph, only aliases repointed.

---

## 4. Acceptance Harness (criteria #1–#7)

End-to-end tests, each mapping to a spec §8 criterion:

1. **#1 Local Claude run** — the worked-example pipeline runs end-to-end on the Claude subscription, no API keys (`llm_call` tool-suppressed `claude -p`; `agent_call` `claude -p` with tools).
2. **#2 All three chat providers** — flip the `assessor` profile across Claude / Azure AI Foundry / OpenRouter; each runs correctly with a single profile-reference change.
3. **#3 Both agent backends** — run `editor` via `claude -p` and via `opencode` on a non-Claude/open-weight model.
4. **#4 Configuration interchangeability** — the **same** `workflow.json` runs under the Claude and open-weight configs by changing only profile references; assert no node-kind/edge/template diff between runs.
5. **#5 Native loop** — the loop iterates multiple passes and exits on its `conditional`; assert the iteration counter is persisted in instance variables and visible in the execution trace.
6. **#6 Capability validation** — an `agent_call` bound to a chat-only profile fails `workflow_validate` with a clear error before any execution.
7. **#7 Connection safety & delivery** — a profile resolves base-url+key; the key is injected into the `opencode` subprocess for a non-Claude model and appears in **neither** workflow files **nor** logs; a multi-region case (two Azure connections, different endpoints/keys) resolves each node to the correct endpoint.

---

## 5. Documentation Deliverables

- A "Native Ralph Loop" section in the project docs/README: the idiom, the constraints (no interactive in a loop, no runaway guard), and the worked example.
- Migration note: the external `ralph-o-matic` server and the standalone feature-pipeline skill are superseded by the native loop + the (separately-specced) native feature-pipeline workflow.

---

## 6. Testing Strategy (strict TDD, 5 scenario categories)

- **Loop mechanics** (engine-level, existing primitives): counter increments across passes; exits on `done`; exits on `counter ≥ max`; back-edge revisits the agent node; counter persisted + in trace; malformed/no-exit loop documented (not auto-killed).
- **Worked-example workflow validation** — passes `workflow_validate`; the `agent_call`-on-chat-profile negative variant fails (#6).
- **Interactive-in-loop lint** (if built) — flags an interactive node on a cycle; does not flag a linear interactive node; advisory severity.
- **Acceptance harness** — the seven criteria above, each as a scenario; configuration-diff assertion for #4; log-capture secrecy assertion for #7.

## 7. Sequencing (each built test-first)

1. Worked-example workflow folder + templates.
2. Loop-mechanics tests over existing engine primitives (confirm counter/trace/exit).
3. Optional interactive-in-loop advisory lint.
4. Acceptance harness (#1–#7).
5. Documentation + migration note.

## 8. Acceptance Mapping

- **#4 Configuration interchangeability** and **#5 Native loop** delivered directly.
- The harness exercises **#1–#7** as the named acceptance test for the whole vendor-abstraction layer (spec §1: this layer's acceptance test is the feature-pipeline-replacement workflow, whose content is specced separately).
