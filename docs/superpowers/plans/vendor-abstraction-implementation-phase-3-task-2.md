# Phase 3, Task 2: `adapter/azurefoundry` Native Chat Adapter (both Azure topologies)

## Goal

Implement a native `engine.ChatProvider` for Azure that serves BOTH Azure topologies
**with no special-casing in code** — the difference is entirely in the connection rows
it consumes:

- **Azure OpenAI resource (GPT-family):** one resource endpoint + key shared across
  many deployments. Request path
  `{base-url}/openai/deployments/{deployment}/chat/completions?api-version=<v>`, header
  `api-key: <revealed key>`, where `{deployment}` is the binding `Model` (the
  deployment name) and `<v>` is the `api-version` from the connection `Extras`.
- **Azure AI Foundry serverless / open-weight catalog (Llama, Mistral, …):** each
  serverless deployment has its own endpoint + key → one connection per model.

Streaming (`StreamChat` SSE → `ChatChunk`), structured output via `OutputSchema`, and
token usage from the response `usage` block are all native and match the
`adapter/openrouter` contract from task 1.

**This is task 2 of 5. It depends on the HTTP/transport seam pattern established in
task 1 (`adapter/openrouter`)** — mirror that package's structure, error handling, and
test seam. It does not depend on tasks 3–5.

### Dependencies consumed (do NOT redefine — import them)

- Phase 1 `domain/engine.ChatProvider` + `ChatRequest` / `ChatResponse` / `ChatChunk`.
- Phase 2 `domain/provider.ResolvedBinding` (`BaseURL`, `Extras`, `Model`, `Params`,
  lazy `RevealKey(ctx)`). The `api-version` is read from `Extras` (Phase 2 validates it
  is present for Azure providers at connection-create time — this adapter does not
  re-validate it but must error clearly if it is absent at call time).

### Hex placement

Driven adapter under `adapter/`; implements the `engine.ChatProvider` domain port and
consumes `domain/provider.ResolvedBinding`. Domain never imports this package.

## Files to Create/Modify

### Create: `adapter/azurefoundry/provider.go`

- `Config` mirroring task 1's openrouter `Config` (injectable HTTP seam, per-call
  limits):
  ```go
  type Config struct {
      Binding          provider.ResolvedBinding // BaseURL, Extras (api-version), Model (deployment), RevealKey
      HTTPClient       *http.Client             // nil → default; transport is the test seam
      Timeout          time.Duration
      MaxResponseBytes int64
  }
  ```
- `Provider` + `New(cfg Config) *Provider`, with
  `var _ engine.ChatProvider = (*Provider)(nil)`.
- `Chat` and `StreamChat` with the same signatures and stream semantics as the
  openrouter adapter (full response vs. SSE token chunks + terminal `Done` with usage).
  ```go
  func (p *Provider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error)
  func (p *Provider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error)
  ```
- The request body is OpenAI-compatible (Azure OpenAI uses the same
  `messages`/`max_tokens`/`temperature`/`response_format` schema), so the body builder
  can be shared in shape with task 1 but is reimplemented locally to keep the adapters
  independent.

### Create: `adapter/azurefoundry/url.go`

- The single place topology selection lives — driven purely by the binding fields, NOT
  by branching on a provider name:
  ```go
  // endpointURL returns the chat-completions URL for the binding. When Extras
  // carries an "api-version", the Azure OpenAI resource deployment path is used:
  //   {base-url}/openai/deployments/{model}/chat/completions?api-version=<v>
  // Otherwise the binding BaseURL is treated as a complete serverless endpoint
  // and the chat-completions suffix is appended once.
  func endpointURL(b provider.ResolvedBinding) (string, error)
  ```
  - Resource topology: requires a non-empty `Extras["api-version"]` and a non-empty
    `Model` (deployment name); returns `ErrMissingAPIVersion` / `ErrMissingDeployment`
    if absent.
  - Serverless topology: the connection's `base-url` IS the deployment endpoint;
    append `/chat/completions` only if not already present.
- Table-test this helper directly for both topologies.

### Create: `adapter/azurefoundry/request.go`

- `func (p *Provider) newRequest(ctx, url string, body []byte) (*http.Request, error)`:
  sets `Content-Type: application/json` and the `api-key: <RevealKey(ctx)>` header
  (Azure uses `api-key`, NOT `Authorization: Bearer`). Key fetched from the lazy
  `RevealKey` at call time only; never logged.
- `func buildBody(req engine.ChatRequest, model string, stream bool) ([]byte, error)`
  — same OpenAI-compatible body as task 1, with `response_format` json_schema when
  `OutputSchema` is set. For the resource topology the `model` field in the body is the
  deployment name (same value used in the path).

### Create: `adapter/azurefoundry/response.go`

- Response + SSE-frame structs and decode helpers (parallel to task 1's
  `response.go`), a `parseSSELine` helper, and a non-2xx handler returning a typed
  error that never includes the `api-key` value.

## Testing

Strict TDD. `httptest.Server` / fake `http.RoundTripper` via `Config.HTTPClient` — no
live network. All FIVE scenario categories, exercising BOTH topologies from different
binding rows:

- **Happy path** — resource topology: a binding with `BaseURL` = resource endpoint,
  `Model` = deployment name, `Extras["api-version"]` set → asserts the request URL is
  `{base-url}/openai/deployments/{deployment}/chat/completions?api-version=<v>`, the
  `api-key` header carries the revealed key, and `Output` is the assistant content.
- **Success (variants)** — serverless topology: a binding with `BaseURL` = serverless
  endpoint and NO `api-version` → asserts the URL is the serverless endpoint +
  `/chat/completions` and the same `api-key` header; structured output via
  `OutputSchema`; `StreamChat` SSE chunking → ordered `Token` chunks + terminal `Done`;
  `usage` → `TokensIn` / `TokensOut` on both `Chat` and the terminal chunk.
- **Failure** — upstream non-2xx (401/404 deployment-not-found/429/500) → typed error;
  mid-stream upstream error → `ChatChunk{Error}` then channel close.
- **Error** — transport error; `RevealKey` error; request-build error all propagate.
- **Edge case** — resource topology with `Extras["api-version"]` missing →
  `ErrMissingAPIVersion`; resource topology with empty `Model` →
  `ErrMissingDeployment`; serverless `BaseURL` that already ends in `/chat/completions`
  is not double-suffixed; empty/malformed JSON body → decode error; `[DONE]`-only
  stream → single terminal `Done`; `MaxResponseBytes` exceeded → bounded-read error.
- **Security assertion** — capture logs and returned errors across all branches and
  assert the revealed key never appears; assert the `api-key` header IS set (verified
  via the fake transport).

## Acceptance Criteria

- `adapter/azurefoundry` implements `engine.ChatProvider` (compile-time assertion).
- Both topologies are served by ONE adapter with no provider-name special-casing —
  topology selection is a pure function of the binding's `Extras`/`BaseURL`/`Model`.
- Resource path is
  `{base-url}/openai/deployments/{deployment}/chat/completions?api-version=<v>`; the
  `api-key` header carries the revealed key; `api-version` comes from `Extras`.
- Serverless path uses the connection's own endpoint per the binding row.
- Streaming, structured output (`response_format`), and `usage` token accounting are
  native and match the openrouter adapter's contract.
- Missing `api-version` (resource topology) or missing deployment yields a clear typed
  error.
- The revealed key never appears in any log line or returned error.
- All five scenario categories pass for both topologies with no live network.
- All existing tests continue to pass.
