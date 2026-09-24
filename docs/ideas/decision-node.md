# Decision Node — a learned `conditional`

**Status:** idea / not started. No code, no model downloaded, no decision to build.
**Written:** 2026-09-23
**Scope:** a new `decision` node type, backed by a new `DecisionProvider` port. It sends a short text and a typed question to a fast, non-generative classifier (Von, Laya, Prompt Guard, or an LLM acting as one) and routes on the answer, with an `uncertain` port for low-confidence results.
**Explicitly out of scope:** replacing `llm_call` for generation, and replacing agent judgment over large inputs (whole plans, repos, long transcripts). Those need an LLM, and §5 explains why.

---

## 1. Why this exists

A recurring Arbitron pattern is **making an LLM choose a branch**:
1. an `llm_call` produces some structured fields
2. a `transform` or two tidies them up
3. a `conditional` routes on the result

It works, but it has three problems:

1. **The structure is a coin flip.** `llm_call`'s `outputSchema` is only *requested* in the prompt. The claude-cli chat path doesn't enforce it. Every `required` field is a chance that a malformed response kills the run, and this has cost real runs before. The usual fix has been tightening templates.
2. **It's slow and paid** for what is often a one-word decision: done or not, approved or not, which of four things is this.
3. **It has no notion of confidence.** A hesitant LLM answer and a certain one route the same way.

There is a class of model built for exactly this: **decision models**. Given a text and a typed question, they return a choice with calibrated probabilities in one forward pass, in milliseconds, locally, and for free. They can't produce malformed output, because they don't generate text at all.

Arbitron should be able to use them. The right shape is a node that is **a `conditional` whose rule is learned instead of hand-written.**

---

## 2. What exists today (grounding)

**Provider ports.**
- `domain/engine/ports.go:45` defines `ChatProvider` (prompt → text, no tools).
- `:54` defines `AgentProvider` (an agentic mission in a workspace).
- Profiles have a kind: `chat` or `agent` (`domain/provider/provider.go:17-19`).
- Nodes declare intent; the profile and its connection decide which vendor and backend serve it.

There is no port for "classify this".

**Conditionals are first-match, hand-written rules** (`domain/engine/executor_conditional.go`). Each rule compares a variable path against a value, and ports come from each rule's `output` plus a `defaultOutput`. Validation requires an edge for every port (`domain/workflow/validation.go:422`).

**The pattern in a shipped workflow.** feature-pipeline's implementation loop (`samples/feature-pipeline/workflow.json`):
- `impl-assess` (`:765`) is an `llm_call` whose `outputSchema` requires `all_tasks_complete`, `tasks_remaining` and `notes`, with an optional `all_remaining_blocked`.
- `impl-inc` increments a counter.
- `impl-gate` (`:822`) is a `conditional` routing to `exit`, `blocked` or `loop`, based on those booleans and an iteration cap.

That is three nodes and a paid LLM call, and a missing boolean can derail the loop.

---

## 3. The candidate models

| Model | Size | Shape | Notes |
|---|---|---|---|
| [Von](https://github.com/wfzyx/von) | ModernBERT-large, ~395M | choose / judge / rate | Apache-2.0. CPU, Apple Metal, CUDA. Python/TS library, CLI, and an HTTP server (`von serve`). Calibrated confidence. Training mix includes security and adversarial data. |
| [Laya](https://huggingface.co/convaiinnovations/laya) | ModernBERT-large, 421M (English); mmBERT-base, 322M (multilingual) | `choice` / `score` / `noul` (yes/no) | Apache-2.0. ~33 ms on a T4 GPU, ~200–460 ms on CPU. **About 320 tokens of text per question** in English (1,024 total for the multilingual checkpoint). Reported calibration error (ECE) 0.081. |
| Prompt Guard 2 (Meta) | 86M / 22M | fixed labels: benign / injection / jailbreak | Single-purpose. 512-token window; long text is chunked, which stays cheap at this size. |
| Any chat LLM | — | prompt with options, parse the answer | The fallback adapter (§4c): slower and paid, but lets the node run anywhere. |

Every number here is the vendors' own claim, and none of them measures our tasks. Treat them as starting points for evaluation (§8).

---

## 4. Proposal

### 4a. The `decision` node

```json
{
  "id": "review-verdict",
  "type": "decision",
  "config": {
    "model_profile": "decide-local",
    "input": "{{senior-review.text}}",
    "question": "What is the reviewer's overall verdict?",
    "kind": "choice",
    "options": ["approve", "request_changes", "reject"],
    "minConfidence": 0.8
  }
}
```

- **`kind`** is `choice` (pick one option), `yes_no`, or `score` (an ordinal scale, bucketed into named options).
- **Each option is an output port**, plus a required **`uncertain`** port taken when the top probability is below `minConfidence`.
- **Validation** requires an edge for every option and for `uncertain`, the same rule that already covers conditionals.
- **Output** is `{choice, confidence, probabilities: {option: p}}`. Downstream nodes and templates can read it (`{{review-verdict.confidence}}`), and it lands in the trace.
- **The `input` template is resolved first**, and the node fails loudly if the resolved text exceeds the profile's model limit (§5). It never silently truncates.

### 4b. Cheap first, expensive only when needed

The `uncertain` port is what makes this more than a faster conditional:

```
                    ┌─ approve ──────────▶ …
decision (local) ───┼─ request_changes ──▶ …
                    └─ uncertain ─▶ llm_call (paid) ─▶ conditional ─▶ …
```

Clear cases are routed in milliseconds for free. Only ambiguous ones pay for an LLM. Or, in a Teams thread, they go to a human (see [teams-collaboration.md](teams-collaboration.md)). Routing and confidence are recorded, so it's visible how often the cheap path is enough.

### 4c. `DecisionProvider` port and a `decision` profile kind

A third provider port, alongside chat and agent:

```go
type DecisionProvider interface {
    Decide(ctx context.Context, req DecisionRequest) (*DecisionResult, error)
}
// DecisionRequest{Input, Question, Kind, Options}
// DecisionResult{Choice, Probabilities}
```

A new profile kind, `decision`, picks the adapter and model:

- **Von:** HTTP to a local `von serve`. The connection is its endpoint, with no key.
- **Laya:** HTTP to a local server around the checkpoint.
- **Prompt Guard:** fixed labels. Its profile *declares* its options, and validation rejects a node whose options don't match.
- **LLM-backed:** wraps an existing `ChatProvider`. It prompts with the options and reads back one of them, with a constrained-output retry. Confidence comes from the model's self-report or from a repeat vote. This makes every `decision` node runnable on a machine with no local model, and makes A/B testing a profile swap.

The node type stays generic, `decision`, never `von`, per the rule against vendor-specific node types. Which model answers is a profile concern.

### 4d. Also available to agents

The same port can be exposed as an MCP tool (`decide`), so agents and the future chat agent can make fast, cheap classifications mid-task. This is secondary; the node is the main use.

---

## 5. Where it fits and where it doesn't

**The hard limit is input length.** These models read hundreds of tokens, not tens of thousands. That decides everything about where they fit:

**Good fits (short text, a closed set of answers):**
- **Classifying an LLM's prose into ports.** Let an `llm_call` write a free-text verdict, then use a `decision` node to route it. This removes the `outputSchema` coin flip: the LLM does what it's good at (writing), and routing gets a structurally valid answer every time.
- **Routing an incoming message** to the right workflow or branch, such as `@arbitron` messages or webhook payloads.
- **Triaging a failure** from the tail of an error log: flaky test, quota exhausted, real bug, or network.
- **Prompt-injection screening** of external text (Jira, GitHub, the web) before an agent reads it, chunked. See [arbitron-chat-agent.md](arbitron-chat-agent.md) §5.
- **Sentiment and intent** of short human replies, for *informational* routing only. Approval votes stay explicit button presses.

**Poor fits:**
- **`impl-assess` itself.** Judging whether every task in a plan is done means reading the plan and the work, far beyond a few hundred tokens. The decision node can't replace that judgment. At most it can route the assessor's *prose* verdict: `llm_call` writes → `decision` classifies → no schema to fail. That's the realistic win for the loop in §2, not deleting the LLM call.
- Anything that needs reasoning across long documents, code, or tool use.
- Open-ended outputs. If you can't list the options in advance, it isn't a decision.

---

## 6. Design tensions and gotchas

- **Who runs the model?** Arbitron shouldn't embed a 400M-parameter model in its Go binary. The natural shape is a **local sidecar**: `von serve` as its own launchd service next to `dev.arbitron`. Arbitron then only knows an endpoint. Model weights (roughly 1–2 GB) and the service's lifecycle are operator concerns, like the CLI harnesses today.
- **Health.** A decision profile whose sidecar is down should fail like any other provider. There is already a backend-health adapter to hang this on. Whether a failed decision falls through to `uncertain` or errors the node is a real choice (open question 2).
- **Calibration drifts with the task.** The vendors' calibration numbers are for their benchmarks. A `minConfidence` of 0.8 means something different on our text. Thresholds need tuning against recorded decisions, which is why recording confidence and routing (§4b) matters from day one.
- **Option wording is part of the model input.** Renaming an option can change its answers, so options should be stable identifiers with an optional description, not free-form labels. The description can change without changing the port name.
- **Zero-shot vs fine-tuned.** Out of the box these are zero-shot. Fine-tuning on Arbitron's own recorded decisions (the assessor's verdicts, review outcomes) is possible later, but it's a separate project with its own data questions.
- **Nondeterminism is lower, not zero.** Same input, same model, same answer. But a model upgrade changes answers, so the profile should pin a model version.

---

## 7. Decisions locked

- A **new node type, `decision`**, not a configured `llm_call`. Classification and generation have different contracts.
- **Options are output ports**, plus a required **`uncertain`** port gated by `minConfidence`.
- A **new `DecisionProvider` port** and **`decision` profile kind**. The node name stays vendor-neutral.
- An **LLM-backed adapter** exists, so the node runs without local models.

---

## 8. Open questions

1. **First model.** Von (richest runtime, HTTP server, Metal) versus Laya (published CPU numbers, a multilingual option). Decide by running both on a small labeled set drawn from real Arbitron text: reviewer verdicts, error-log tails, sample `@arbitron` messages.
2. **Provider failure.** If the sidecar is down, route to `uncertain` (degrade to the expensive path) or fail the node (surface the outage)? Leaning `uncertain`, with a trace warning, but that can hide a dead sidecar for days.
3. **`score` kind.** Is an ordinal kind worth it at launch, or are `choice` and `yes_no` enough?
4. **Multi-label.** Some questions want several labels ("which areas does this change touch?"). That doesn't fit single-port routing. Leave it out, or add a non-routing `labels` output?
5. **Chunked input.** For long text (injection screening), which rule combines per-chunk answers: any-positive, max-confidence, or majority? Probably a per-profile setting, with any-positive as the default for Prompt Guard.

---

## 9. Suggested sequencing

1. **Evaluation spike (throwaway).** Run Von and Laya locally against ~100 labeled real examples across three tasks: verdict classification, failure triage, injection screening. Measure accuracy, calibration and latency on the Mac. This decides whether to build at all.
2. **Port and LLM-backed adapter.** `DecisionProvider`, the `decision` profile kind, and the LLM adapter, so the node exists and is testable without a local model.
3. **The `decision` node.** Executor, validation (option ports + `uncertain`), trace output, and dashboard rendering of the new ports.
4. **Local adapter.** The winner of step 1, as a sidecar with a health check.
5. **First real use.** Replace one schema-fragile verdict (`llm_call` + `outputSchema` + `conditional`) in a shipped workflow with `llm_call` (prose) → `decision`. Compare failure rates.
6. **MCP `decide` tool**, and injection screening for the chat agent.

---

## Appendix — related

- [arbitron-chat-agent.md](arbitron-chat-agent.md): injection screening is a `decision` node use case.
- [teams-collaboration.md](teams-collaboration.md): `uncertain` can route to a human in the thread.
- `samples/feature-pipeline/workflow.json`: `impl-assess` / `impl-gate`, the schema-fragile pattern this targets.
