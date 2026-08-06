# Workflow Spawn Node — Design Phase 2: Child Input Preparation

## Purpose

Build the component that turns the parent's available context into startup
values for a child workflow, using the child's declared inputs as the contract.
This is the spec's "LLM-assisted input preparation", isolated behind its own
port so the nondeterministic part of the feature is testable — and
replaceable — without touching workflow execution.

The spec's requirement that "if required startup values cannot be produced, the
spawn node fails" is enforced **here**, by this component, before any child
instance is created.

## Architecture

A new domain component and port in `domain/engine`. It has exactly one job:

> Given a child workflow's declared input contract, the parent's available
> context, and optional author guidance, produce a validated set of startup
> values — or fail.

It is deliberately **not** merged into the executor. Keeping it separate means:

- Its tests need a fake chat provider and nothing else — no instance
  repository, no queue manager, no engine.
- The executor (Phase 3) never touches an LLM, prompt assembly, or JSON
  parsing.
- Its internals — prompt shape, schema synthesis, retry posture — can change
  without any consumer changing, which is the isolation test this design is
  held to.

The component takes the LLM-facing half of `LLMCallExecutor`'s dependency set
(`domain/engine/executor_llm.go:23-27`): a profile resolver and a chat provider
selector. It reuses that executor's proven call sequence (resolve profile →
assert binding kind → select provider → chat → parse) rather than inventing a
second way to make a structured LLM call.

It deliberately does **not** hold a `FileReader`. The optional guidance template
is read — and its `{{substitutions}}` resolved against the parent's variable
store — by the caller (Phase 3's executor), which already holds both the
`FileReader` and the store; the preparer receives the resulting prose as a plain
string. That is what makes the isolation claim above literally true: its tests
need a fake chat provider and nothing else.

## Components

### 1. `ChildInputPreparer` port

**One job:** declare the contract, so the executor depends on an interface
rather than on an LLM.

The contract, stated semantically:

- **Takes:** the child workflow's declared input fields; the parent's context
  as a flat map; optional author guidance prose; and the profile alias to
  resolve for the call.
- **Returns:** a map of startup values that satisfies the child's contract, or
  an error explaining which required inputs could not be produced.
- **Guarantees:** on success, every required input of the child is present and
  non-null. On failure, no partial result is returned — the caller cannot
  accidentally spawn with half a payload.

Because the return value is "values that satisfy the contract", the caller
needs no knowledge of how they were obtained. A future non-LLM implementation
(static mapping, cached result) satisfies the same port.

### 2. Default preparer implementation

**One job:** implement the port using a tool-suppressed chat call.

Its internal responsibilities, in order:

1. **Synthesize the output contract** from the child's declared input fields.
   Each declared input contributes its name, declared type, description, and
   required flag. The child's declaration is the authority — the parent never
   names the fields.
2. **Assemble the prompt** from the child's contract, the parent's context, and
   the optional guidance prose.
3. **Instruct for structured output.** `LLMCallExecutor` appends an explicit
   JSON-only instruction when a node declares an output schema
   (`domain/engine/executor_llm.go:51-58`), with a comment recording *why*: the
   claude-cli adapter does not pass the schema to the model, and without the
   instruction the model intermittently wraps its JSON in prose or a code
   fence — the documented root cause of repeated structured-output parse
   failures. The preparer reuses that same helper. It does not re-solve a
   solved problem, and it inherits the fix if that helper improves.
4. **Resolve the profile and assert the binding kind**, failing closed via
   `assertBindingKind` before any provider is selected — the Phase 1 kind-guard
   extension is what makes this assertion meaningful for the spawn type.
5. **Select the concrete chat backend** from the resolved binding via
   `ChatProviderSelector`, keeping every concrete adapter out of the domain.
6. **Parse and validate.** Parse the response as an object; then check it
   against the child's contract.

### 3. Contract validation

**One job:** decide whether the produced values actually satisfy the child.

The check is the *authority* for the spec's failure requirement. It rejects:

- a missing required input,
- a required input present but null,
- a response that is not a JSON object at all.

It **accepts** and passes through:

- absent optional inputs (the child's own trigger-time defaulting handles them,
  exactly as `TriggerWithDepartment` already does at `engine.go:355-370`),
- extra keys not in the child's contract. The trigger path already tolerates
  and seeds extra variables (`engine.go:372-383`), and its `SetInput`
  reserved-name guard prevents a produced value from overwriting an
  engine-seeded variable such as `workspace`. Re-implementing that rejection
  here would duplicate a guard that already exists one layer down.

Type coercion is **not** attempted. The child's declared types are carried into
the prompt as guidance, and the produced values are passed through as parsed.
The trigger path is the single place input semantics are enforced; adding a
second, subtly different enforcement here would create two sources of truth
about what a valid input is.

## Data Flow

```
[caller supplies] child input contract  ──┐
                  parent context map    ──┤
                  guidance prose (opt)  ──┼──► prompt assembly
                  profile alias         ──┘         │
                                                    ▼
                            resolve profile ──► assert binding kind (chat)
                                                    │
                                                    ▼
                                    select chat provider (per binding)
                                                    │
                                                    ▼
                                       tool-suppressed chat call
                                                    │
                                                    ▼
                                        parse response as object
                                                    │
                                                    ▼
                              validate against child's required inputs
                                       │                      │
                                  satisfied              unsatisfied
                                       │                      │
                                       ▼                      ▼
                            startup values map          error naming the
                                                        missing inputs
```

Nothing in this flow touches an instance, a queue, or a workflow file other
than the optional guidance template.

## Data Provenance

| Input | Producer that exists today |
| --- | --- |
| Child workflow's declared inputs | `workflow.LoadVisibleFiltered` (`domain/workflow/resolve.go:33`) → `workflow.Workflow.Inputs` (`[]workflow.InputField`, `domain/workflow/workflow.go:76-82`). **Called by the executor in Phase 3, which passes the result in** — the preparer never loads a workflow itself. |
| Parent context map | `variable.VariableStore.Snapshot()`. The store itself is the `vars` argument of `NodeExecutor.Execute` (`domain/engine/executor.go:13`); **Phase 3's executor calls `Snapshot()` and passes the resulting map in** — the preparer takes a flat map, not the store. Contains the run's inputs and all prior node outputs. |
| Optional guidance prose | `engine.FileReader.ReadFile(owner, workflowName, relativePath)` (`domain/engine/ports.go:17-21`), the same call `LLMCallExecutor` makes at `executor_llm.go:42`. Owner comes from the run context, seeded at `engine.go:711-713`. **Called by the executor in Phase 3, which resolves the template's variables and passes the resulting prose in** — the preparer never reads a file. |
| Chat model + backend | `provider.ProfileResolver.Resolve` and `engine.ChatProviderSelector.ChatProviderFor` (`domain/engine/ports.go:60-62`) — the identical pair `LLMCallExecutor` holds (`executor_llm.go:24-26`), wired in `cmd/arbitron/main.go` |
| Structured-output instruction | `structuredOutputInstruction` in `domain/engine`, used at `executor_llm.go:56` |
| Binding-kind assertion | `assertBindingKind` (`domain/engine/kind_guard.go:41`), made effective for the spawn type by Phase 1 |

Every input above traces to an existing producer. There are no unresolved
dependencies in this phase.

**Deferred to plans:**

- Composition-root construction of the preparer in `cmd/arbitron/main.go` and
  its injection into the executor — Phase 3 owns the wiring; this phase's
  component is constructed only by tests until then.
- The exact prompt wording and the exact JSON-schema shape synthesized from
  `InputField`.
- Whether `MaxTokens`/`Temperature` for the preparation call are configurable
  per node or fixed.

## Integration Seams

- `domain/engine/ports.go` — the `ChildInputPreparer` port declaration, beside
  the existing provider-selector ports.
- New file in `domain/engine` for the default implementation, following the
  one-concern-per-file layout the package already uses.
- Reuses `structuredOutputInstruction`, `assertBindingKind`,
  `ChatProviderSelector`, `provider.ProfileResolver` — all existing.
- Consumed by `SpawnWorkflowExecutor` in Phase 3. Nothing else consumes it.

## Error Handling

Every failure returns an error; none returns partial values.

- **Guidance file unreadable** — owned by the **caller** (Phase 3's executor),
  which errors naming the node and the path before the preparer is invoked. A
  configured template that cannot be read is a misconfiguration, not an optional
  extra. An *unconfigured* guidance file is not an error, and reaches the
  preparer as empty prose.
- **Profile resolution failure / binding-kind mismatch** — error before any
  provider is selected. Mismatch is wrapped `ErrBindingKindMismatch` so it
  stays `errors.Is`-checkable.
- **Provider call failure** — wrapped and returned. No retry inside the
  preparer: node-level retry already exists in `executeWithRetry`
  (`engine.go:997`) driven by the node's `ErrorBehavior`. A private retry loop
  here would multiply against it invisibly.
- **Unparseable response** — error. Because this is the single most likely
  runtime failure, the error message includes a bounded excerpt of the
  response so an operator can diagnose it from the trace without re-running.
- **Contract not satisfied** — error naming **which** required inputs are
  missing. This is the spec's "spawn node fails" condition. The message is
  written to be actionable by a workflow author reading the failed node's trace
  entry, since the fix is almost always in the guidance prose or the parent's
  upstream nodes.

All of these surface to the executor as a plain error, which the engine then
routes through the node's configured `ErrorBehavior` — retry, fallback output,
or error-node routing — with no special-casing. The preparer has no knowledge
of workflow control flow.

## Testing Strategy — STRICT TDD

Tests precede implementation, driven by a fake `ChatProvider` and nothing else.
All five categories are required. (The guidance *file* — its read, its
substitution, and its unreadable-file error — is covered by Phase 3's executor
tests, since Phase 3 owns that read.)

- **Happy path** — a child declaring two required and one optional input, a
  parent context containing plausible values, and a cooperative fake provider
  yields a map satisfying the contract, and the call is made against the
  resolved binding's model.
- **Success** — supplied guidance prose reaches the prompt, and omitting it is
  not an error; the structured-output instruction is appended; the parent's node outputs (not
  just its inputs) are visible in the prompt; a child with zero declared inputs
  succeeds with an empty map without calling the provider at all; extra keys in
  the response are passed through rather than stripped.
- **Failure** — the response omits a required input → error naming that input;
  a required input is present but null → same treatment as missing; the
  response is a JSON array or a bare string rather than an object → error. Each
  asserts no partial map is returned alongside the error.
- **Error** — provider returns an error → wrapped and surfaced; profile
  resolution fails → error; provider selection fails → error; resolved binding is
  agent-kind → `ErrBindingKindMismatch` under `errors.Is`, asserted to occur
  **before** any provider selection so no tool-enabled provider is ever
  constructed.
- **Edge case** — response wrapped in a markdown code fence still parses (the
  documented real-world failure mode from `executor_llm.go:51-55`); response
  with leading prose before the JSON; an empty parent context with a child that
  requires inputs fails cleanly rather than producing fabricated-looking empty
  strings; a very large parent context does not panic; an optional input absent
  from the response is left absent rather than filled with a zero value, so the
  child's own defaulting applies.
