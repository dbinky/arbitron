# LLM Providers — Phase 3 Design: Amazon Bedrock chat adapter

**Spec:** `docs/specs/llm-providers-spec.md`
**Phase:** 3 of 5
**Depends on:** nothing functionally; shares connection/credential patterns with P1/P2

## Goal

Add an **Amazon Bedrock** chat adapter using the **Converse API** with **SigV4** request
signing and **stored AWS credentials**. Bedrock becomes selectable on chat /
non-tool-using nodes.

## Why Converse + SigV4

The Bedrock **Converse API** (`POST /model/{modelId}/converse`) is a single, unified
request/response shape across all Bedrock model families — one code path instead of
per-model `InvokeModel` body branching. Bedrock authenticates with **AWS SigV4** request
signing (service `bedrock`, the connection's region), not a bearer token, so the adapter
needs a SigV4 signer rather than the openrouter Bearer path.

## Credential model (two secrets, sealed)

The existing `ConnectionStore.Reveal` returns a single sealed string. Bedrock needs an
access-key-id, a secret-access-key, and (optionally) a session token. Mapping:

- **Non-secret Extras** (on `Connection.Extras`): `region` (required), `access-key-id`.
  (`access-key-id` is an identifier, not a secret; keeping it in Extras keeps the sealed
  payload to true secrets.)
- **Sealed payload** (via `ConnectionStore.Create/Reveal`): the **secret-access-key**.
  When a **session token** is supplied, seal a small JSON composite
  `{"secret":"...","session_token":"..."}` so both secrets stay encrypted; absent a
  session token, seal the bare secret string. The adapter's `RevealKey` returns this
  payload and the adapter parses it (tolerating both the bare-string and JSON forms).
- `ValidateConnection`: a `bedrock` connection requires a non-empty `region` and
  `access-key-id` in Extras (new rule alongside the existing Azure `api-version` rule).

## Components

New package `adapter/bedrock`:

- `provider.go` — `Provider` implementing `engine.ChatProvider`. Same injectable
  `Config{Binding, HTTPClient, Timeout, MaxResponseBytes}` seam and bounded-reader /
  no-leak error handling as the other HTTP adapters.
- `endpoint.go` — derive the regional endpoint
  `https://bedrock-runtime.{region}.amazonaws.com/model/{modelId}/converse`
  (and `.../converse-stream` for streaming) from `Extras["region"]` + `Model`.
- `sigv4.go` — a self-contained SigV4 signer (service `bedrock`, region from Extras):
  canonical request, string-to-sign, signing key derivation, `Authorization` header +
  `X-Amz-Date` (+ `X-Amz-Security-Token` when a session token is present). Time is
  injectable for deterministic test signatures.
- `request.go` / `response.go` — build the Converse body (`messages`, `system` blocks,
  `inferenceConfig` for max_tokens/temperature) and decode the Converse response
  (`output.message.content[].text`, `usage.inputTokens`/`outputTokens`). Streaming maps
  the Converse event stream to `engine.ChatChunk`s.

## Factory routing

Add `providerBedrock = "bedrock"` (also accept `amazon-bedrock` → normalize to
`bedrock`) to `ChatProviderFor`, constructing `bedrock.New(...)`. Because Bedrock signs
rather than bearer-auths and derives its own endpoint from region, it does not reuse the
openrouter arm.

## Data flow

`llm_call` node → executor → `ResolvedBinding{Kind: chat, Provider: "bedrock",
Extras{region, access-key-id}, Model, RevealKey→sealed-secret(+session)}` →
`Factory.ChatProviderFor` → `bedrock.Provider` → SigV4-signed POST to the regional
Converse endpoint.

## Error handling

- Missing region/access-key-id → configuration-class error from `ValidateConnection` at
  connection create/update time, and a guard in the adapter before signing.
- SigV4 rejection (bad keys/clock skew) → Bedrock 403 surfaced as a configuration-class
  `ErrUpstreamStatus`; the secret is never included in the error.
- Unknown model / throttling → 4xx/5xx mapped to `ErrUpstreamStatus`.

## Testing (strict TDD, 5 categories)

- **Happy-path:** Converse call against an `httptest` server returns assistant text +
  token usage; streaming yields ordered tokens then Done with usage.
- **Input-validation:** missing region or access-key-id rejected; empty model rejected;
  composite vs bare-string sealed payload both parsed.
- **Error-handling:** 403 (auth), 400 (bad model), 429/500 mapped; malformed Converse
  JSON handled.
- **Edge-cases:** session-token present → `X-Amz-Security-Token` header set and composite
  payload parsed; region drives the endpoint host; trailing-slash handling.
- **Security:** SigV4 signature is correct for a fixed key+time+request (golden test);
  secret-access-key and session token never appear in headers other than the signature /
  in logs/error snippets; response capped by `MaxResponseBytes`. The signer is
  hand-rolled and signature-tested rather than pulling the full AWS SDK, keeping the
  dependency surface (and its supply-chain) small — a deliberate, tested trade-off.

## Out of scope (this phase)

Ambient AWS credential chain (spec chose stored keys), Bedrock on the agent path
(Phase 4 via opencode), Bedrock model listing (Phase 5).
