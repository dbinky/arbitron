# Agent Tool-Execution Security Model

## TL;DR

Arbitron's agent nodes (`interactive_agent`, `agent_call`) run the underlying CLI
(claude-cli / opencode) with **full, unconfirmed tool access**. The per-profile
`AllowedTools` allowlists are **defense-in-depth / intent documentation only —
they are NOT a security boundary.** The actual isolation boundary is the
**OS/container** the agent runs in.

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

## If you want the allowlist to be a real boundary

Drop `--dangerously-skip-permissions` and rely on `--allowedTools` (verified
enforced in headless mode). This requires each profile's allowlist to be
*complete* for its job, or the agent is denied a needed tool mid-run — so it
must be validated end-to-end against each workflow before adopting.
