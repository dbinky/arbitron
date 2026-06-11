# Codex Agent Backend — Product Spec

**Slug:** `codex-backend`
**Status:** Draft
**Date:** 2026-06-11

## 1. Problem / Motivation

Arbitron's agent workflow nodes can run on the Anthropic subscription (through the
`claude-code` backend) or on metered, per-token API keys (through the `opencode`
backend). There is currently no way to run agent nodes on a **ChatGPT / Codex
subscription**.

Two concrete gaps follow from this:

- Users who already pay for a ChatGPT plan cannot put that included capacity to
  work inside Arbitron workflows. Their only route to an OpenAI model today is a
  separately-billed OpenAI API key via the `opencode` backend.
- Users cannot build a **mixed-model pipeline** — for example a GPT-driven
  creative/planning phase alongside an Opus-driven engineering phase — without
  paying per-token API fees for the GPT half.

## 2. Goals & Outcomes

- Introduce a third agent backend, **`codex`**, that any agent-kind profile can
  select.
- Run agent work on the user's **ChatGPT subscription** — with **no OpenAI API
  key** required or stored anywhere in Arbitron.
- Support **both** agent node types:
  - `agent_call` (batch) nodes, and
  - `interactive_agent` (live-streaming) nodes.
  This makes `codex` the first non-Claude backend usable on interactive nodes.
- Enable **mixed-model pipelines**: each workflow node already chooses its model
  and backend independently, so a single pipeline can run some nodes on a GPT
  model via the ChatGPT subscription and others on Opus 4.8 via the Anthropic
  subscription.

## 3. User-Facing Capabilities

- **Select the backend per profile.** A user sets `backend: codex` on an
  agent profile and points its `model` at a GPT model. That profile is then
  usable by any `agent_call` or `interactive_agent` node, exactly like the
  existing `claude-code` and `opencode` profiles.
- **Subscription-based, key-free.** The codex backend draws its credentials from
  the user's existing Codex login on the host machine. Arbitron never asks for,
  stores, or injects an OpenAI API key for this backend.
- **Live streaming on interactive nodes.** An `interactive_agent` node bound to
  a codex profile streams the model's output into the interactive session in
  real time — the same conversational UX as the existing Claude interactive
  path.
- **Unattended (`--yolo`) posture.** Codex runs with approvals and sandboxing
  bypassed so pipeline nodes can freely read, edit, and execute as needed for
  end-to-end automation. This is the backend's operating posture for v1.
- **Preflight readiness.** Arbitron can report whether codex is installed and
  logged in, surfacing a clear *codex not ready* state **before** a run, rather
  than only failing partway through. The readiness states are: ready,
  not installed, and installed-but-not-logged-in.
- **Fail-closed, legible errors.** When codex is missing, not logged in, or
  returns an unusable result, the node fails with a clear error that names the
  cause. The backend never silently degrades to a tool-less or mis-routed run.

## 4. Worked Example (shipped with the feature)

The sample `feature-pipeline` profiles are updated to demonstrate the split:

| Profile      | Backend       | Model       | Subscription used         |
| ------------ | ------------- | ----------- | ------------------------- |
| `interviewer`| `codex`       | `gpt-5.5`   | ChatGPT / Codex           |
| `planner`    | `claude-code` | `opus-4.8`  | Anthropic                 |
| `coder`      | `claude-code` | `opus-4.8`  | Anthropic                 |
| `assessor`   | `claude-code` | (unchanged) | Anthropic                 |

With this configuration the pipeline's creative/interview phase (both
`interactive_agent` nodes) runs on GPT‑5.5 through the ChatGPT subscription,
while design alignment, planning, and implementation run on Opus 4.8 through the
Anthropic subscription. The example is a turnkey, runnable demonstration of the
mixed-model capability.

> The model id `gpt-5.5` is the intended interviewer model; the exact id used is
> whatever the user's Codex install exposes, and is editable in config.

## 5. Prerequisites (user-provided)

- The user has installed the Codex CLI on the host.
- The user has completed `codex login` (Sign in with ChatGPT) on the host, so a
  valid subscription session exists for Arbitron to reuse.

These are documented prerequisites. The Arbitron installer is **not** changed to
install or log in to Codex.

## 6. Non-Goals (out of scope for v1)

- **OpenAI API-key mode** in the codex backend. API-key OpenAI usage is already
  available today through the `opencode` backend; the codex backend is
  subscription-only.
- **Shared / multi-user (Azure) deployment** of the codex backend, which would
  require a per-user ChatGPT-subscription auth story in a containerized,
  multi-tenant context. This is a candidate for a later phase, not v1.
- **Installer changes** to auto-install or auto-login Codex.
- **Chat-tier nodes** (`llm_call` / `interactive_llm`). The codex backend is
  agent-tier only; chat-tier OpenAI access already exists via the
  OpenAI-compatible chat adapter.

## 7. Success Criteria

- An `agent_call` node bound to a `backend: codex` profile completes a mission on
  the ChatGPT subscription with **no** OpenAI API key configured.
- An `interactive_agent` node bound to a `backend: codex` profile streams output
  live and emits its declared `outputSchema` JSON result.
- The updated sample `feature-pipeline` runs end-to-end with a GPT‑5.5
  interviewer and an Opus‑4.8 coder, each on its respective subscription.
- The readiness check correctly distinguishes ready / not-installed /
  not-logged-in.
- Selecting `backend: codex` on a profile passes workflow validation; an agent
  node bound to such a profile loads and runs without special-casing by the
  author.

## 8. Risks & Considerations

- **Security posture.** The `--yolo` posture is RCE-equivalent on
  model-influenced prompts — the same risk class as the `opencode` allow-all
  posture and the `claude-code` skip-permissions posture. For v1 this is an
  accepted, operator-chosen posture for **local, single-user, unattended** use,
  and is documented as such.
- **Subscription terms.** Driving a ChatGPT subscription through automated agent
  runs is subject to OpenAI's terms for that subscription; this is the user's
  responsibility, consistent with how the Anthropic subscription is used via
  `claude-code`.
- **Output-format dependence.** Interactive streaming and structured-result
  extraction depend on Codex's machine-readable output; the design phase will
  pin the exact mechanism and a fallback for format drift.
