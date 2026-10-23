# Agent Tool-Execution Security Model

## TL;DR

Arbitron's agent nodes (`interactive_agent`, `agent_call`) run the underlying CLI
(claude-cli / opencode / codex) with **full, unconfirmed tool access**. The
per-profile `AllowedTools` allowlists are **defense-in-depth / intent
documentation only — they are NOT a security boundary.** The actual isolation
boundary is the **OS/container** the agent runs in.

## Why the allowlist is not a boundary (verified, PR #18)

Agent profiles invoke claude-cli with `--dangerously-skip-permissions` (required
so the agent can act without a human approving every tool call). Empirical
testing of claude-cli 2.1.x showed:

| Invocation | Tool outside allowlist | Result |
|---|---|---|
| `--allowedTools "Bash(gh:*)"` **+ `--dangerously-skip-permissions`** | `whoami` | **executes** (`permission_denials: []`) |
| `--allowedTools ""` **+ `--dangerously-skip-permissions`** | `whoami` | **executes** |
| `--allowedTools "Bash(gh:*)"` (**no** skip-permissions) | `whoami` | **denied** |

`--dangerously-skip-permissions` **bypasses `--allowedTools` entirely.** The
allowlist is only enforced when skip-permissions is absent (in which case a
disallowed tool is cleanly *denied*, not prompted, in headless `-p` mode).

**opencode** (installed v0.0.55 Go build) exposes **no per-tool permission
mechanism at all** (see `adapter/opencode/args.go`), so there is nothing to
scope; opencode agents run with whatever tools that build grants.

**codex** (codex-cli 0.137.0) runs `codex exec
--dangerously-bypass-approvals-and-sandbox` (see `adapter/codex/args.go`). As the
flag name states, this **bypasses BOTH approvals AND codex's own sandbox** — the
same fully-open v1 posture as claude-cli's `--dangerously-skip-permissions` and
opencode's allow-all. codex `exec` exposes **no per-tool allowlist flag**, so the
`AllowedTools` scope is intent-only here too (it cannot be emitted to the CLI).
Running model-influenced prompts under this posture is **RCE-equivalent**; it is
accepted for **local, single-user, unattended** use where every prompt input is
trusted, and is contained *only* by the OS/container boundary below — never by a
CLI flag. Unlike the credential-bearing providers, codex carries **no Arbitron
connection or API key**: it authenticates via the host's ChatGPT subscription
session in `~/.codex` (see `adapter/codex/env.go`; the engine sets **no
`OPENAI_API_KEY`**), and a backend-scoped readiness check
(`adapter/codex/readiness.go`, surfaced on `system_config`) reports
`ready` / `not-installed` / `not-logged-in` so an unusable backend is caught
before a run rather than mid-mission. codex is **subscription-only and
agent-tier only**; API-key OpenAI access stays on the **opencode** backend.

## The boundary that IS real: the container

Because tool execution cannot be constrained at the CLI layer for a
skip-permissions agent, **agents MUST run inside an isolated sandbox**:

- **No host secrets reachable** — no API keys, SSH keys, cloud credentials, or
  `.env` files on disk or in env (the engine already strips `ANTHROPIC_API_KEY`
  via `procenv.BaseEnv`; the deployment must extend this to all secrets).
- **Controlled network egress** — restrict outbound traffic to what the workflow
  needs (e.g. github.com for `gh`), to bound SSRF/exfiltration.
- **Per-instance workspace** — the agent's `WorkDir` is pinned under the
  per-instance workspace (`filesystem.WorkspaceManager`); the container should
  not bind-mount anything else writable.
- **Untrusted input awareness** — `pr-review` personas read attacker-authored PR
  content. Prompt injection that drives arbitrary tool calls is in-scope and is
  contained *only* by the sandbox, not by the allowlist.

The provided `Dockerfile` runs as a non-root user; a production deployment is
responsible for the egress controls and secret isolation above.

**Connection keys in agent processes.** A profile with a `connection_ref`
reveals that connection's API key into the env of the agent process it routes
through. This has always applied to batch `agent_call` runs. From the
latest-model profiles release it also applies to **interactive claude-code
turns** (`interactive_agent`). Those turns previously ran with an empty
binding. The key is therefore live in a skip-permissions process that runs
model-chosen tools, and a prompt-injected tool call can read it. Scope such
keys to the least privilege and spend limit the workflow needs. The container
is the boundary, as above.

## If you want the allowlist to be a real boundary

Drop `--dangerously-skip-permissions` and rely on `--allowedTools` (verified
enforced in headless mode). This requires each profile's allowlist to be
*complete* for its job, or the agent is denied a needed tool mid-run — so it
must be validated end-to-end against each workflow before adopting.
