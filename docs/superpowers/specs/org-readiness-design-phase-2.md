# Org-Readiness Design — Phase 2: Pluggable Agent Backend + Universal Provider Routing

**Spec:** `docs/specs/org-readiness-spec.md` (Phase 2)
**Phase order:** 2 of 3 (Auth → Agent/Provider → Triggers)

## Overview

Make the **agent backend** (the CLI that runs an agentic loop: `claude-code` or
`opencode`) a first-class selection axis that is **independent of the provider**
(the connection that supplies endpoint + key + model). A workflow author chooses
the backend **per agent node**; an unspecified node falls back to a deployment
default. Every node — chat and agent — can route through the org's LiteLLM
gateway (or any configured provider). The deployment default for this org is
**Claude Code → LiteLLM**.

LiteLLM exposes **both** an Anthropic-compatible surface (`/v1/messages`) and an
OpenAI-compatible surface (`/v1/chat/completions`), so Claude Code (via
`ANTHROPIC_BASE_URL`) and opencode (via `OPENAI_BASE_URL`) can both target it.

## Background (current behavior)

`adapter/provider/factory.go::AgentProviderFor` dispatches on the *provider*:
empty/`claude-cli` → the pre-built claude-cli agent adapter (subscription only),
everything else → `adapter/opencode`. The claude-cli agent adapter builds its
subprocess env from `procenv.BaseEnv()` (`adapter/claudecli/agent_adapter.go:52`)
and cannot target a custom endpoint/key. So today: claude-code = subscription
only; non-claude agent = opencode only. This phase decouples the two.

## Scope

In scope: a `Backend` selection axis (node → profile → deployment default);
factory routing by backend; claude-cli endpoint/key injection; confirm opencode
LiteLLM mapping; ship both CLIs in image + installer.

Out of scope: new provider adapters (the existing families cover LiteLLM via the
`openai`/`litellm` ids and Anthropic via `anthropic`); chat-tier routing (already
works through `litellm`).

## Architecture & components

### The backend axis (node → profile → binding)

- **`domain/workflow/workflow.go`** — add optional `Backend string` to
  `NodeConfig` (values: `""`, `"claude-code"`, `"opencode"`). Validated in
  `domain/workflow/validation.go` against the allowed set; `""` = inherit.
- **`domain/config/config.go`** — add `Backend string` to `ProfileConfig`; add a
  deployment-level default (e.g. `llm.defaultagentbackend`, default
  `claude-code`).
- **`domain/provider/provider.go`** — add `Backend` to `Profile` and to
  `ResolvedBinding`.
- **`adapter/provider/static.go`** — `StaticProfileResolver.Resolve` copies
  `Profile.Backend` → `ResolvedBinding.Backend`.
- **`domain/engine/executor_agent.go`** — after resolving the binding, compute
  the effective backend with precedence **node > profile > deployment default**
  and set it on the binding before calling the selector.

### Factory routes on backend

- **`adapter/provider/factory.go::AgentProviderFor`** dispatches on
  `binding.Backend` (not the provider):
  - `claude-code` → the claude-cli agent adapter, configured from the binding
    (endpoint+key+model); empty connection → subscription path.
  - `opencode` → `adapter/opencode`, as today, with the binding's provider env.
  - unset → deployment default (claude-code here).
  This keeps `Deps.ClaudeAgent`/`ClaudeChat` but allows the claude adapter to run
  against any connection, not just subscription.

### Claude-cli endpoint injection (call-time)

- Thread the `ResolvedBinding` to the claude-cli agent run so credentials are
  injected **per call** (stateless adapter), in `adapter/claudecli/agent_adapter.go`
  / `shared.go`:
  - if `binding.BaseURL != ""` → add `ANTHROPIC_BASE_URL=<baseURL>`
  - if `binding.RevealKey` yields a key → add `ANTHROPIC_API_KEY=<key>` (revealed
    at call time, never logged, never via the static allowlist)
  - empty connection → no injection → subscription path (unchanged)
- **`adapter/procenv/procenv.go`** — add `ANTHROPIC_BASE_URL` to the base
  allowlist (non-secret). The **key is injected at call time only**, not added to
  the allowlist.
- Model selection: pass the binding `Model` to the claude `-m`/model flag as the
  adapter already does for the default model.

### opencode (confirm only)

`adapter/opencode/env_providers.go` already maps `openai` →
`OPENAI_API_KEY`+`OPENAI_BASE_URL`. LiteLLM is configured as an `openai`-family
connection with the gateway `BaseURL`; opencode runs `-m openai/<model>`. Add a
`litellm`→`openai` alias only if we want `litellm` to be a valid agent provider
id symmetric with the chat tier (low-risk one-line table/canonicalize entry).

### Deployment packaging

- **`Dockerfile`** — install **both** `@anthropic-ai/claude-code` and opencode
  (pinned versions); ensure both are on `PATH` for the service user.
- **Installer** (`install.sh` / `install.ps1`) — the optional agent-backend step
  installs both CLIs (opencode is currently optional/absent).

## Data flow (agent node execution)

1. Engine reaches an agent node → resolves its profile alias to a
   `ResolvedBinding` (provider/endpoint/key/model).
2. Effective backend = node.Backend ?? profile.Backend ?? deployment default.
3. `AgentProviderFor(binding)` returns the claude-cli or opencode adapter.
4. The adapter launches the CLI with the binding's endpoint+key+model injected
   (claude-cli: `ANTHROPIC_BASE_URL`/key; opencode: `OPENAI_BASE_URL`/key + `-m`).
5. Calls flow to LiteLLM → Azure AI Foundry (or a directly-configured provider).

## Error handling / security

- **Fail closed on bad backend:** an unknown backend value → validation error at
  workflow load, never a silent tool-less run.
- **Credential isolation:** keys revealed per call via `binding.RevealKey`,
  injected into that subprocess env only; never added to the global allowlist,
  never logged.
- **Permission posture** unchanged: existing per-profile `AllowedTools` and the
  opencode allow-all operator knob still apply to whichever backend runs.

## Testing (strict TDD)

Across the project's scenario categories (happy/validation/error/boundary/
integration):

- **Backend precedence**: node overrides profile overrides default; empty at all
  levels → deployment default; unknown value → validation error.
- **Factory routing**: `claude-code` binding → claude adapter; `opencode`
  binding → opencode adapter; with/without a connection.
- **claude-cli injection**: base_url+key present → env carries
  `ANTHROPIC_BASE_URL`+`ANTHROPIC_API_KEY`; empty connection → subscription env
  (no base_url); key never appears in logs; model flag set from binding.
- **opencode mapping**: litellm/openai connection → `OPENAI_BASE_URL` + `-m
  openai/<model>`.
- **End-to-end (stubbed runner)**: an agent node with each backend completes
  against a fake LiteLLM endpoint; a chat node routes through `litellm`.

## Integration points (anchors)

- `domain/workflow/workflow.go` (`NodeConfig.Backend`), `validation.go`
- `domain/config/config.go` (`ProfileConfig.Backend`, default backend)
- `domain/provider/provider.go` (`Profile.Backend`, `ResolvedBinding.Backend`)
- `adapter/provider/static.go` (resolver copy), `adapter/provider/factory.go`
  (`AgentProviderFor` dispatch)
- `adapter/claudecli/agent_adapter.go`, `shared.go`, `adapter/procenv/procenv.go`
- `adapter/opencode/env_providers.go`, `provider_id.go`
- `domain/engine/executor_agent.go` (effective-backend computation)
- `Dockerfile`, `install.sh`, `install.ps1`
- `cmd/arbitron/main.go` (adapter wiring / default backend)

## Acceptance (from spec)

claude-code and opencode agent nodes each complete against the gateway; a chat
node routes through the gateway; a node with no backend uses the default; a node
can target a direct provider; a fresh deployment has both CLIs available.
