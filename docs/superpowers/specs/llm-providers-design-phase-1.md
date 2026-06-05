# LLM Providers — Phase 1 Design: OpenAI-compatible coverage (OpenAI + Ollama)

**Spec:** `docs/specs/llm-providers-spec.md`
**Phase:** 1 of 5
**Depends on:** nothing (extends existing `openrouter` OpenAI-compatible adapter)

## Goal

Make **OpenAI API**, **Ollama (local)**, and **Ollama (cloud / ollama.com)** selectable
on chat / non-tool-using nodes, reusing the existing OpenAI-compatible HTTP adapter
(`adapter/openrouter`). No new HTTP adapter is built in this phase — the work is
provider routing in the Factory, a localhost allowance for local Ollama, and
connection/config plumbing.

## Why these three fit the existing adapter

`adapter/openrouter` POSTs to `{base_url}/chat/completions` with `Authorization: Bearer
<key>` and an OpenAI-shaped body (`adapter/openrouter/provider.go`, `request.go`). It is
already generic over `BaseURL`/`Model`/`RevealKey`. Both OpenAI and Ollama expose
OpenAI-compatible `/v1/chat/completions` surfaces, so each is a connection config plus a
Factory routing entry:

| Connection | Provider string | BaseURL | Key |
|---|---|---|---|
| OpenAI | `openai` | `https://api.openai.com/v1` | API key |
| Ollama local | `ollama` | `http://localhost:11434/v1` | none |
| Ollama cloud | `ollama` (or `ollama-cloud`) | `https://ollama.com/v1` | API key |

Local vs cloud Ollama are distinguished by their connection's BaseURL and the
presence/absence of a key — not by separate provider strings (one `ollama` family).

## Components & changes

### 1. Factory routing (`adapter/provider/factory.go`)

- Add canonical provider constants `providerOpenAI = "openai"` and `providerOllama =
  "ollama"` (plus optional `ollama-cloud` alias normalized to `ollama`).
- In `ChatProviderFor`, route `openai` and `ollama` to `openrouter.New(...)` (the
  OpenAI-compatible adapter), identical to the `openrouter`/`litellm` arm.
- `AgentProviderFor` is untouched here (agent path is Phase 4); a chat-kind binding is
  the only path exercised in this phase.

### 2. Local-endpoint (localhost) allowance for Ollama

Local Ollama is `http://localhost:11434` — an internal host that the connection
write-path SSRF policy would otherwise reject. The policy lives in
`domain/provider/url_policy.go`: `StrictURLPolicy` (the production default applied by
`ConnectionRepo.Create`/`Update` via `provider.ValidateConnectionURL`) enforces
https-only and rejects loopback (`localhost`, `127.0.0.1`, `::1`), link-local, the
`169.254.169.254` metadata endpoint, and RFC1918 ranges.

Add a **narrow, explicit allowance** in `url_policy.go`: a connection whose provider is
`ollama` AND whose BaseURL host is a loopback address (`localhost`, `127.0.0.1`, `::1`)
may use `http` and that loopback host. `ValidateConnectionURL` already receives the full
`Connection` (including `Provider`), so the carve-out is provider-aware. The allowance is
scoped to the `ollama` provider AND loopback hosts only — it does NOT permit `ollama`
pointed at link-local/metadata/RFC1918 hosts, and does NOT widen the policy for any other
provider. The openrouter adapter's existing `checkScheme` (http|https) is unchanged.

### 3. Connection & profile plumbing

Connections are NOT declared in YAML — they are created at runtime (the existing
`connection_create` MCP tool, sealed in SQLite) and only *profiles* live in config.
Therefore this phase adds **no config schema** for `openai`/`ollama`:

- `ValidateConnection` (`domain/provider/connection.go`): no new required extras for
  `openai`/`ollama`. (Bedrock/Azure-style required-extras handling is in their phases.)
- An operator creates an `openai`/`ollama` connection via `connection_create` (provider
  string + base_url + optional key). A `chat`-kind profile in `~/.arbitron/config.yaml`
  references it via `connectionRef`; `ProfilesFromConfig` + `StaticProfileResolver`
  already map that to a chat-kind `ResolvedBinding` with the connection's
  `Provider`/`BaseURL`/`Model` and a lazy `RevealKey`.
- The keyless local-Ollama path resolves with a nil-safe `RevealKey` (empty key → no
  Authorization header, already handled in `openrouter.newRequest`).
- No new connection-CRUD surface is added here; provider routing (§1) + the url_policy
  allowance (§2) are the only code changes beyond tests.

## Data flow

`llm_call` node → `LLMCallExecutor` → `ProfileResolver.Resolve(alias)` →
`ResolvedBinding{Kind: chat, Provider: "openai"|"ollama", BaseURL, Model, RevealKey}` →
`Factory.ChatProviderFor` → `openrouter.Provider` → POST `{base}/chat/completions`.

## Error handling

- Unknown model / 4xx-5xx from upstream → existing `ErrUpstreamStatus` with a scrubbed,
  bounded snippet (no key leak).
- Keyless connection that the endpoint requires a key for → upstream 401 surfaced as a
  configuration-class error.
- Local Ollama unreachable (server not running) → transport error surfaced as a
  node-level error.

## Testing (strict TDD, 5 categories)

- **Happy-path:** OpenAI chat returns assistant text against an `httptest` OpenAI-shaped
  server; Ollama local chat (no key) returns text; Ollama cloud (keyed) sets the Bearer
  header.
- **Input-validation:** empty model rejected/handled per existing adapter contract;
  malformed BaseURL rejected by `checkScheme`.
- **Error-handling:** upstream 401/429/500 mapped to `ErrUpstreamStatus`; transport
  failure surfaced.
- **Edge-cases:** keyless local-Ollama emits no Authorization header; trailing slash in
  BaseURL normalized; `ollama-cloud` alias normalizes to `ollama`.
- **Security:** the localhost allowance applies ONLY to `ollama` (a non-ollama
  connection pointing at `localhost`/private host is still rejected by the SSRF policy);
  revealed key never appears in error snippets.

## Out of scope (this phase)

Anthropic API (Phase 2), Bedrock (Phase 3), the agent/opencode path (Phase 4),
connection validation CLI (Phase 5).
