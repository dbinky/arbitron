# Phase 3, Task 1: `adapter/openrouter` Native Chat Adapter

## Goal

Implement a native `engine.ChatProvider` for OpenRouter (and any OpenAI-compatible
router, including a self-hosted LiteLLM endpoint) that POSTs to
`{base-url}/chat/completions` with `Authorization: Bearer <revealed key>`. It must
support full `Chat` responses, streamed `StreamChat` (SSE → `ChatChunk`), structured
output via the request's `OutputSchema`, and token-usage extraction from the response
`usage` block. The adapter consumes a Phase-2 `ResolvedBinding` and never touches
`workflow.json`, the connection store, or `claude` machinery.

**This is task 1 of 5; it has no dependencies on the other Phase-3 tasks.** It is the
first native chat adapter and establishes the HTTP contract (`net/http` + injectable
transport) that task 2 (`adapter/azurefoundry`) mirrors.

### Dependencies consumed (do NOT redefine — import them)

- Phase 1 `domain/engine` port `ChatProvider` and the `ChatRequest` / `ChatResponse` /
  `ChatChunk` shapes (`Model`, `SystemPrompt`, `Prompt`, `MaxTokens`, `Temperature`,
  `OutputSchema`, `WorkDir`, `SessionID` → response `Output`, `Model`, `TokensIn`,
  `TokensOut`, `SessionID`; chunk `Token`, `Done`, `Error`, `TokensIn`, `TokensOut`).
- Phase 2 `domain/provider.ResolvedBinding` (`BaseURL`, `Extras`, `Model`, `Params`,
  and the lazy `RevealKey(ctx) (string, error)`). The revealed key is fetched at call
  time only and never logged or persisted.

### Hex placement

`adapter/openrouter` is a driven adapter under `adapter/`. It implements the
`engine.ChatProvider` domain port and consumes `domain/provider.ResolvedBinding`. The
domain never imports this package.

## Files to Create/Modify

### Create: `adapter/openrouter/provider.go`

- `Config` carrying the injectable HTTP seam and per-request limits:
  ```go
  type Config struct {
      Binding          provider.ResolvedBinding // BaseURL, Extras, Model, RevealKey
      HTTPClient       *http.Client             // nil → a default *http.Client
      Timeout          time.Duration            // 0 → no per-call timeout
      MaxResponseBytes int64                    // 0 → no cap (test convenience only)
  }
  ```
  The `HTTPClient` (with a swappable `http.RoundTripper` transport) is the unit-test
  seam — no live network in tests.
- `Provider` struct + constructor `New(cfg Config) *Provider`, with a compile-time
  assertion `var _ engine.ChatProvider = (*Provider)(nil)`.
- `Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error)`:
  build the request body (below), set headers, POST to
  `{base-url}/chat/completions`, decode the JSON response, map the first choice's
  message content → `ChatResponse.Output`, copy `Model`, and read
  `usage.prompt_tokens` / `usage.completion_tokens` → `TokensIn` / `TokensOut`.
  ```go
  func (p *Provider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error)
  ```
- `StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error)`:
  set `stream: true`, parse the SSE response (`data: {...}` lines, terminated by
  `data: [DONE]`), emit one `ChatChunk{Token: delta}` per streamed delta, and a final
  `ChatChunk{Done: true, TokensIn, TokensOut}` carrying usage when the provider
  includes a trailing usage frame. Any mid-stream failure is delivered as a
  `ChatChunk{Error: ...}` before the channel closes; the goroutine always drains the
  body and closes the channel.
  ```go
  func (p *Provider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error)
  ```

### Create: `adapter/openrouter/request.go`

- Helper that builds the OpenAI-compatible chat-completions JSON body from a
  `ChatRequest` + binding:
  ```go
  func buildBody(req engine.ChatRequest, model string, stream bool) ([]byte, error)
  ```
  - `model` = binding `Model` (resolved model-id).
  - `messages` = `[{role: "system", content: SystemPrompt}, {role: "user", content: Prompt}]`
    (omit the system message when `SystemPrompt` is empty).
  - `max_tokens` and `temperature` from the request (omit zero values).
  - When `req.OutputSchema != nil`, set `response_format` to a
    `{"type":"json_schema","json_schema":{...}}` block derived from the schema
    (structured output).
- Helper to construct the authenticated `*http.Request`:
  ```go
  func (p *Provider) newRequest(ctx context.Context, body []byte) (*http.Request, error)
  ```
  Sets `Content-Type: application/json` and
  `Authorization: Bearer <RevealKey(ctx)>`. The key is fetched here from the binding's
  lazy `RevealKey` and used only to populate the header; it is never logged. URL is
  `strings.TrimRight(BaseURL, "/") + "/chat/completions"`.

### Create: `adapter/openrouter/response.go`

- Typed structs + decode helpers for the full chat-completions response and the SSE
  stream delta frames, plus a `usage` struct. A `parseSSELine([]byte) (delta string,
  usage *usageBlock, done bool, err error)` helper for table-testable stream parsing.
- A non-2xx handler that reads the (bounded) error body and returns a typed error
  (e.g. `ErrUpstreamStatus`) WITHOUT including any `Authorization` header value.

## Testing

Strict TDD. Tests use `httptest.Server` (or a fake `http.RoundTripper` injected via
`Config.HTTPClient`) — no live network. All FIVE scenario categories:

- **Happy path** — `Chat` against a fake returning a well-formed completion: asserts
  POST to `{base-url}/chat/completions`, request body has the right `model` and
  `messages`, and `ChatResponse.Output` is the assistant content.
- **Success (variants of the working path)** — structured output: `OutputSchema` set
  produces a `response_format` json_schema block and the parsed structured `Output`;
  `StreamChat` SSE chunking yields the expected ordered `Token` chunks then a terminal
  `Done` chunk; `usage` block maps to `TokensIn` / `TokensOut` on both `Chat` and the
  terminal stream chunk.
- **Failure** — upstream non-2xx (401/429/500): returns a typed error; for
  `StreamChat`, a mid-stream upstream error surfaces as a `ChatChunk{Error}` and the
  channel still closes.
- **Error** — transport error (round-tripper returns an error), request-build error,
  and `RevealKey` returning an error (credential unavailable) all propagate as a
  returned error / error chunk.
- **Edge case** — empty/`[DONE]`-only SSE stream → a single terminal `Done` chunk with
  no tokens; empty/malformed JSON body → decode error; missing `choices` array;
  `MaxResponseBytes` exceeded → bounded read returns an error.
- **Security assertion** — capture logs (and the returned error string) for every
  branch above and assert the revealed key never appears; assert the
  `Authorization: Bearer <key>` header IS set on the outbound request (verified via the
  fake transport, not via logs).

## Acceptance Criteria

- `adapter/openrouter` implements `engine.ChatProvider` (compile-time assertion present).
- `Chat` POSTs to `{base-url}/chat/completions` with `Authorization: Bearer <revealed key>`
  and returns the assistant content as `ChatResponse.Output`.
- `StreamChat` maps the SSE token stream to ordered `ChatChunk`s with a terminal `Done`.
- Structured output is requested via `response_format` when `OutputSchema` is set, and
  parsed back into `Output`.
- Token usage from the response `usage` block populates `TokensIn` / `TokensOut`.
- A LiteLLM router needs NO code: pointing a connection's `base-url` at the router
  resolves through this same adapter (verified by an httptest "router" base-url test).
- The revealed key never appears in any log line or returned error.
- All five scenario categories pass with no live network.
- All existing tests continue to pass.
