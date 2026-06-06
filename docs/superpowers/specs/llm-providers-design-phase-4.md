# LLM Providers — Phase 4 Design: Agent path on modern opencode

**Spec:** `docs/specs/llm-providers-spec.md`
**Phase:** 4 of 5
**Depends on:** Phases 1–3 establish the provider strings & connections this phase
routes agents through.

## Goal

Rewrite `adapter/opencode` from the legacy v0.0.55 single-shot contract to the **modern
opencode** `run -m <provider>/<model>` contract so the **agent / tool-using** path works
across **all** providers (open-by-default permissions + broad tools), not just Anthropic.
Claude CLI's agent path (`adapter/claudecli`) is unchanged.

## Background: the binary constraint

The current adapter (`adapter/opencode/args.go`, `provider.go`, `env_providers.go`)
targets opencode **v0.0.55** (old Go variant): no `run` subcommand, no `--model`/provider
routing, no per-tool permission flag, single-shot only. The code comments already flag
this and localize the contract so a newer binary can be swapped in. The modern `opencode`
(SST) exposes `opencode run [message] -m provider/model`, per-provider auth (env / config),
and a permission configuration. This phase makes that swap.

## Required binary

Document a **minimum opencode version** (the first SST release with stable `run -m` +
permission config) as a runtime prerequisite, surfaced in config/docs and verified at
startup with a clear error if an incompatible binary is found. `resolveBinary` in
`provider.go` is retained; a version probe is added.

## Components & changes (all within `adapter/opencode`)

### 1. `args.go` — modern contract

Emit `opencode run <prompt> -m <provider>/<model>` with:
- working directory via the run's cwd (the `{{workspace}}` pin, security-critical —
  keep the existing empty-WorkDir guard);
- model routing from `binding.Provider` + `binding.Model` → `-m provider/model`
  (map arbitron provider strings → opencode provider ids: `anthropic-api`/`anthropic`→
  `anthropic`, `openai`→`openai`, `openrouter`→`openrouter`, `ollama`→`ollama`,
  `bedrock`/`amazon-bedrock`→`amazon-bedrock`, `azure-*`→`azure`);
- **tool scope**: thread `mission.ToolScope` / `binding.AllowedTools` into opencode's
  tool/permission selection (previously dropped on the floor);
- output format suitable for parsing the final assistant text + steps.

### 2. Open-by-default permission posture

Modern opencode has no single `--dangerously-skip-permissions` flag; permissions are
configured. The adapter generates an **allow-all permission posture by default** for the
run (e.g. an ephemeral opencode config / flags granting all tool permissions), unless the
profile restricts it:
- **Default:** skip-permissions ON, broad tools — friction-free agent execution.
- **Operator restriction (two knobs):** (a) a config switch to disable the allow-all
  posture; (b) a per-profile `AllowedTools` allowlist (already plumbed via
  `ResolvedBinding.AllowedTools`) that narrows the tool set. Absent config, behavior is
  broadly open. Mirrors the claudecli agent posture (`--dangerously-skip-permissions` +
  `--allowedTools`) so both runners express the same open-by-default contract.

### 3. `env_providers.go` — extend the auth table

Keep the table as the single source of truth, extended for the full provider set
(`anthropic`, `openai`, `openrouter`, `azure`, `ollama`, `amazon-bedrock`). For Bedrock,
the env contract carries the AWS creds (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`,
`AWS_SESSION_TOKEN` when present, `AWS_REGION`) sourced from the connection's Extras +
sealed `RevealKey` composite (same payload shape as Phase 3). Local Ollama needs only a
base-url env, no key.

### 4. `provider.go` — drive `run`

Build the credential env first (fail before launch on a RevealKey/unknown-provider
error — keep the existing no-partial-env-launch invariant), launch via the shared
`claudecli.CommandRunner` seam, drain stdout, parse the final assistant text/steps/usage
into `engine.AgentResult`.

### 5. Factory & fail-closed

`AgentProviderFor` continues routing all non-Claude agent bindings to opencode. Add a
**fail-closed** path: a provider the binary genuinely cannot drive (unknown to opencode's
provider map / unsupported by the installed version) returns a clear, actionable error
naming the provider and node — never a silent tool-less run.

## Data flow

`agent_call` / `interactive_agent` node → agent executor → `ResolvedBinding{Kind: agent,
Provider, Model, AllowedTools, RevealKey}` → `Factory.AgentProviderFor` →
`opencode.Provider` → `opencode run <prompt> -m provider/model` (allow-all perms by
default) with per-provider auth env.

## Error handling

- Incompatible/missing opencode binary → startup/first-call error with the required
  version.
- Unknown provider mapping → fail-closed agent error.
- Mission timeout / non-zero exit → context error preferred on cancel, else a wrapped
  mission-failed error (existing behavior retained).

## Testing (strict TDD, 5 categories)

- **Happy-path:** with a fake `CommandRunner`, an agent mission for each provider emits
  the correct `run -m provider/model` argv and parses a final result.
- **Input-validation:** empty WorkDir rejected (security guard retained); empty model
  rejected; provider→opencode-id mapping covered per provider.
- **Error-handling:** RevealKey failure aborts before launch (no subprocess); non-zero
  exit and timeout surfaced; unknown provider fails closed.
- **Edge-cases:** session/multi-step parsing; Bedrock composite creds expand to the AWS
  env quartet; local-Ollama base-url env, no key.
- **Security:** default posture grants broad tools/permissions; a profile `AllowedTools`
  allowlist measurably narrows the emitted tool scope; the allow-all switch can be
  disabled by config; revealed secrets are injected only into the subprocess env, never
  argv or logs.

## Out of scope (this phase)

Chat-path adapters (Phases 1–3), validation CLI (Phase 5), changing the Claude CLI agent
adapter.
