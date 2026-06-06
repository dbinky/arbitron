# LLM Providers — Phase 2 Design: Anthropic API chat adapter

**Spec:** `docs/specs/llm-providers-spec.md`
**Phase:** 2 of 5
**Depends on:** nothing (parallel to Phase 1; new `adapter/anthropicapi` package)

## Goal

Add a **native Anthropic Messages API** chat adapter so the Anthropic API (key-based,
hosted) is selectable on chat / non-tool-using nodes. This is distinct from the Claude
CLI subscription path (`adapter/claudecli`) — it is the direct HTTP API with a stored
API key.

## Why a new adapter (not the OpenAI-compatible one)

Anthropic's Messages API is not OpenAI-shaped: it POSTs to `/v1/messages`, authenticates
with an `x-api-key` header (not `Authorization: Bearer`), requires an
`anthropic-version` header, carries the system prompt as a top-level `system` field
(not a message), and returns `content` as a block array. We build a dedicated adapter for
fidelity and future features (prompt caching, etc.), mirroring the structure of
`adapter/openrouter`.

## Components

New package `adapter/anthropicapi`, modeled on `adapter/openrouter`:

- `provider.go` — `Provider` implementing `engine.ChatProvider` (`Chat` + `StreamChat`),
  with the same injectable `Config{Binding, HTTPClient, Timeout, MaxResponseBytes}`
  seam, the same `boundedReader`/`limitedReader` OOM cap, and the same
  `scrubControl`/`statusError` no-leak error handling.
- `request.go` — `buildBody(req, model, stream)` producing the Messages body:
  - `model`, `max_tokens` (Anthropic **requires** max_tokens — supply a sane default
    when `req.MaxTokens == 0`), `messages: [{role: "user", content: ...}]`,
    top-level `system` when `req.SystemPrompt != ""`, `temperature` when non-zero,
    `stream: true` when streaming.
  - Structured output: Anthropic has no `response_format`; map `req.OutputSchema` to a
    single **tool** with `input_schema = OutputSchema` plus `tool_choice` forcing that
    tool, and extract the tool-use input as the JSON output. (The domain executor remains
    the sole structured-output parser — the adapter returns the raw JSON string.)
- `response.go` — decode the non-streaming Messages response (concatenate `text` blocks,
  or extract the forced tool-use input for structured output) and map `usage.input_tokens`/
  `usage.output_tokens`. Parse the SSE event stream (`content_block_delta` → token,
  `message_delta`/`message_stop` → done + usage) into `engine.ChatChunk`s.

### Request specifics

- Endpoint: `{base_url}/v1/messages`, base_url defaulting to `https://api.anthropic.com`
  when the connection omits it.
- Headers: `x-api-key: <revealed key>`, `anthropic-version: 2023-06-01` (pinned
  constant), `content-type: application/json`. The revealed key sets `x-api-key` only and
  is never logged.

## Factory routing

Add `providerAnthropicAPI = "anthropic-api"` (normalized) to
`adapter/provider/factory.go` `ChatProviderFor`, constructing `anthropicapi.New(...)`.
Chosen string `anthropic-api` to avoid collision with the Claude CLI subscription path
(empty / `claude-cli`).

## Data flow

`llm_call` node → executor → `ResolvedBinding{Kind: chat, Provider: "anthropic-api",
BaseURL, Model, RevealKey}` → `Factory.ChatProviderFor` → `anthropicapi.Provider` → POST
`/v1/messages`.

## Error handling

- Missing key (nil/empty RevealKey) → adapter returns a configuration-class error before
  the call (Anthropic always requires a key; unlike the keyless local-Ollama path).
- 4xx/5xx → `ErrUpstreamStatus` with a bounded, control-scrubbed snippet, no key leak.
- Streaming mid-failure delivered as a `ChatChunk{Error}` before channel close (same
  contract as openrouter).

## Testing (strict TDD, 5 categories)

- **Happy-path:** non-streaming Messages call returns concatenated text + token counts;
  streaming yields ordered tokens then a Done chunk with usage.
- **Input-validation:** empty `max_tokens` gets the default; empty system omits the
  `system` field; structured-output request emits the forced-tool body.
- **Error-handling:** 401/429/500 mapped to `ErrUpstreamStatus`; malformed JSON handled;
  streaming error surfaced as a `ChatChunk{Error}`.
- **Edge-cases:** multi-block content concatenation; base_url default + trailing-slash
  normalization; `anthropic-version` header always present.
- **Security:** key sets `x-api-key` only, never appears in logs/error snippets; response
  body capped by `MaxResponseBytes`; control chars scrubbed from upstream error text.

## Out of scope (this phase)

Anthropic on the agent path (Phase 4 via opencode), Bedrock-hosted Claude (Phase 3).
