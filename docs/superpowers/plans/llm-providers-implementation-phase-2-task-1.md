# LLM Providers — Phase 2 / Task 1: `anthropicapi` request/response + non-streaming `Chat`

**Spec:** `docs/specs/llm-providers-spec.md`
**Design:** `docs/superpowers/specs/llm-providers-design-phase-2.md`
**Phase:** 2 of 5 — native Anthropic Messages API chat adapter
**Task:** 1 of 2 — package skeleton, request body builder, non-streaming response decode, `Provider.Chat`
**Mirror template:** `adapter/openrouter/{provider.go,request.go,response.go,provider_test.go}`

---

## Objective

Create the new package `adapter/anthropicapi` and implement the **non-streaming**
half of `engine.ChatProvider` against `{BaseURL}/v1/messages` (BaseURL default
`https://api.anthropic.com`). This task delivers:

- The injectable `Config{Binding, HTTPClient, Timeout, MaxResponseBytes}` seam and
  `Provider` struct, mirroring `adapter/openrouter` exactly (same `boundedReader` /
  `limitedReader` OOM cap, same `scrubControl` / `statusError` no-leak error path,
  same `ErrUpstreamStatus` sentinel).
- `buildBody(req, model, stream)` producing the Anthropic Messages body, including
  the forced-tool encoding of structured output (Anthropic has no `response_format`).
- `decodeMessages(raw)` → concatenated text blocks (or the forced tool-use input JSON)
  + `usage.input_tokens` / `usage.output_tokens`.
- `Provider.Chat` wiring the above end to end.
- The `x-api-key` + `anthropic-version: 2023-06-01` + `content-type` headers, with
  the revealed key used ONLY for `x-api-key` and never logged.
- A configuration-class error when the key is missing (nil/empty `RevealKey`) — the
  request must never reach the transport (Anthropic always needs a key).

`StreamChat` and factory/main wiring are **Task 2**. To keep this task compilable and
its own tests green, `StreamChat` ships as a minimal stub in this task (see below) and
is fully implemented + tested in Task 2.

## Dependencies

- None on other phases. Parallel to Phase 1. New package, no edits to existing files
  in this task (factory/main edits are Task 2).
- Reuses existing infra: `github.com/arbitron/arbitron/adapter/procenv` (`WithTimeout`),
  `github.com/arbitron/arbitron/domain/engine` (`ChatProvider`, `ChatRequest`,
  `ChatResponse`, `ChatChunk`), `github.com/arbitron/arbitron/domain/provider`
  (`ResolvedBinding`, `ProfileKindChat`).
- Test libs already in the module: `stretchr/testify` (`assert`, `require`), stdlib
  `net/http`, `net/http/httptest` / fake `http.RoundTripper`, `log/slog`.
- `make test` (== `go test ./...`) must stay hermetic — no live network.

---

## Files to create

### 1. `adapter/anthropicapi/provider.go`

Package doc comment mirroring openrouter's (explains the injectable HTTP seam, the
`x-api-key`/`anthropic-version` distinction vs OpenAI shape, no live network in tests).

```go
package anthropicapi

// Config configures an Anthropic Messages-API chat Provider.
// SECURITY: Binding.RevealKey is invoked lazily at call time and used ONLY to set
// the x-api-key header; the revealed key is never logged or persisted.
// MaxResponseBytes caps the per-request response body; 0 means no cap (test convenience).
type Config struct {
	Binding          provider.ResolvedBinding
	HTTPClient       *http.Client
	Timeout          time.Duration
	MaxResponseBytes int64
}

// Provider implements engine.ChatProvider by POSTing to {base-url}/v1/messages.
type Provider struct {
	binding          provider.ResolvedBinding
	httpClient       *http.Client
	timeout          time.Duration
	maxResponseBytes int64
}

var _ engine.ChatProvider = (*Provider)(nil)

func New(cfg Config) *Provider          // nil HTTPClient -> &http.Client{}; stores all four fields

func (p *Provider) boundedReader(r io.Reader) io.Reader   // identical to openrouter
// errResponseTooLarge + limitedReader: copied verbatim from openrouter (rename msg prefix to "anthropicapi:")

func (p *Provider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error)

func (p *Provider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error)
// TASK 1 STUB: implemented in Task 2. Stub returns (nil, errStreamNotImplemented) so the
// package compiles and the port assertion holds. Task 2 replaces it + adds streaming tests.

// statusError(status int, body io.Reader) error  // identical shape to openrouter; wraps ErrUpstreamStatus
// scrubControl(s string) string                  // copied verbatim from openrouter
```

`Chat` control flow (mirror `openrouter.Provider.Chat`):

1. `ctx, cancel := procenv.WithTimeout(ctx, p.timeout)`; `defer cancel()`.
2. `body, err := buildBody(req, p.binding.Model, false)`.
3. `httpReq, err := p.newRequest(ctx, body)` — this is where the missing-key check
   fires (see `request.go`); on missing key it returns `ErrMissingAPIKey` BEFORE any
   transport call.
4. `resp, err := p.httpClient.Do(httpReq)`; wrap transport error as
   `fmt.Errorf("anthropicapi: chat call: %w", err)`; `defer resp.Body.Close()`.
5. `reader := p.boundedReader(resp.Body)`; non-2xx → `return nil, p.statusError(...)`.
6. `raw, err := io.ReadAll(reader)`; wrap as `anthropicapi: read response`.
7. `msg, err := decodeMessages(raw)`; on error return it.
8. `content, err := msg.output()` — concatenated text OR forced tool-use input JSON
   string (see `response.go`).
9. Return `&engine.ChatResponse{Output: content, Model: msg.Model, TokensIn:
   msg.Usage.InputTokens, TokensOut: msg.Usage.OutputTokens}`.
   IMPORTANT: `Output` is always a **string** pre-parse — the domain executor's
   `processLLMOutput` is the sole structured-output parser (same contract as openrouter).

### 2. `adapter/anthropicapi/request.go`

```go
package anthropicapi

// anthropicVersion is the pinned Anthropic API version header value. Bumping it is a
// deliberate, reviewed change — never derived from config or upstream.
const anthropicVersion = "2023-06-01"

// defaultBaseURL is used when the binding omits a BaseURL.
const defaultBaseURL = "https://api.anthropic.com"

// messagesPath is appended to the (trimmed) base-url.
const messagesPath = "/v1/messages"

// defaultMaxTokens is supplied when ChatRequest.MaxTokens == 0, because Anthropic
// REQUIRES max_tokens. Sane mid-range default.
const defaultMaxTokens = 4096

// ErrMissingAPIKey is the configuration-class sentinel returned (before any network
// call) when the binding has no usable API key. Anthropic always requires a key.
var ErrMissingAPIKey = errors.New("anthropicapi: missing API key (x-api-key required)")

// ErrUnsupportedScheme — defensive belt-and-suspenders, identical to openrouter.
var ErrUnsupportedScheme = errors.New("anthropicapi: base_url scheme must be http or https")

// buildBody assembles the Anthropic Messages JSON body.
func buildBody(req engine.ChatRequest, model string, stream bool) ([]byte, error)

// newRequest builds the authenticated POST to {base-url}/v1/messages, sets headers,
// and enforces the missing-key check.
func (p *Provider) newRequest(ctx context.Context, body []byte) (*http.Request, error)

// resolveBaseURL returns binding.BaseURL trimmed of trailing slashes, or defaultBaseURL.
func resolveBaseURL(raw string) string

// checkScheme — copied from openrouter (rejects non-http(s)).
func checkScheme(rawURL string) error

// revealKey — copied from openrouter (nil-safe; returns "" when RevealKey == nil).
func (p *Provider) revealKey(ctx context.Context) (string, error)
```

`buildBody` rules (the Anthropic shape):

- `model`: the resolved `model` arg.
- `max_tokens`: `req.MaxTokens` when `> 0`, else `defaultMaxTokens`. ALWAYS present.
- `messages`: `[]any{ {"role":"user","content": req.Prompt} }`. (Single user message;
  multi-turn is out of scope per `ports.go` history note.)
- `system`: top-level field, set ONLY when `req.SystemPrompt != ""` (NOT a message).
- `temperature`: set ONLY when `req.Temperature != 0`.
- `stream: true`: set ONLY when `stream` arg is true.
- **Structured output** (`req.OutputSchema != nil`): NO `response_format`. Instead emit:
  ```
  "tools":       [ {"name": forcedToolName, "description": "...", "input_schema": req.OutputSchema} ],
  "tool_choice": {"type": "tool", "name": forcedToolName}
  ```
  where `const forcedToolName = "structured_output"`. This forces the model to call the
  one tool whose input is the schema-shaped JSON; the response decoder extracts that
  tool-use input as the raw string.
- Marshal via `json.Marshal`; wrap error as `anthropicapi: marshal request body`.

`newRequest` rules:

1. `target := resolveBaseURL(p.binding.BaseURL) + messagesPath`.
2. `checkScheme(target)` — reject non-http(s) before building the request.
3. `key, err := p.revealKey(ctx)`; wrap reveal error as `anthropicapi: reveal key`.
4. **Missing-key guard:** `if strings.TrimSpace(key) == "" { return nil, ErrMissingAPIKey }`.
   This must run BEFORE `http.NewRequestWithContext`'s side effects matter, and the
   transport must NOT be called (asserted in tests).
5. Build `http.NewRequestWithContext(ctx, POST, target, bytes.NewReader(body))`.
6. Headers (exact casing — `http.Header.Set` canonicalizes, assert via `.Get`):
   - `Content-Type: application/json`
   - `x-api-key: <key>` (the ONLY place the key is used)
   - `anthropic-version: 2023-06-01` (the pinned const; ALWAYS set)

### 3. `adapter/anthropicapi/response.go`

```go
package anthropicapi

// ErrUpstreamStatus is the sentinel wrapping a non-2xx Messages response. Callers
// match with errors.Is. The wrapped message includes status + a bounded, scrubbed
// snippet but NEVER the x-api-key value.
var ErrUpstreamStatus = errors.New("anthropicapi: upstream returned non-2xx status")

// usageBlock mirrors Anthropic's usage object.
type usageBlock struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// contentBlock is one element of the Messages `content` array. text blocks carry
// Text; tool_use blocks carry Name + Input (raw JSON for the forced-tool path).
type contentBlock struct {
	Type  string          `json:"type"`            // "text" | "tool_use"
	Text  string          `json:"text"`            // when Type == "text"
	Name  string          `json:"name"`            // when Type == "tool_use"
	Input json.RawMessage `json:"input"`           // when Type == "tool_use"
}

// messagesResponse is the non-streaming /v1/messages response.
type messagesResponse struct {
	Model   string         `json:"model"`
	Content []contentBlock `json:"content"`
	Usage   usageBlock     `json:"usage"`
	// StopReason etc. ignored for now.
}

// decodeMessages parses a non-streaming Messages body; rejects empty body / no content.
func decodeMessages(body []byte) (*messagesResponse, error)

// output returns the assistant output string: if any tool_use block is present
// (forced structured output), return its Input as a string (raw JSON, pre-parse);
// otherwise concatenate all text blocks in order.
func (m *messagesResponse) output() (string, error)
```

`decodeMessages` rules:
- Reject `len(bytes.TrimSpace(body)) == 0` → `errors.New("anthropicapi: empty response body")`.
- `json.Unmarshal`; wrap error as `anthropicapi: decode response`.
- Reject `len(m.Content) == 0` → `errors.New("anthropicapi: response has no content")`.

`output()` rules:
- Scan `m.Content`: if a block has `Type == "tool_use"` and non-empty `Input`, return
  `string(block.Input)` (the forced tool-use input JSON, verbatim). Prefer the first
  tool_use block.
- Else concatenate `block.Text` for every `Type == "text"` block in order.
- If neither produced any content (all blocks unknown/empty), return an error
  `anthropicapi: response has no text or tool_use content`.

---

## TDD test list — `adapter/anthropicapi/provider_test.go`

Reuse the openrouter test harness verbatim where possible: `secretKey` const,
`fakeTransport` (records `gotReq` + `gotBody`, returns canned `resp`/`err`),
`jsonResponse`, `staticBinding` (RevealKey returns `secretKey`, Provider
`"anthropic-api"`, `Kind: ProfileKindChat`), `newProvider`, `captureLogs`. Write the
test FIRST in each case (red), then implement to green. Tests that target Task-2
behavior (streaming) live in Task 2; do not add them here.

### Category: Happy-path

- `TestProviderImplementsPort` — compile-time `var _ engine.ChatProvider = (*Provider)(nil)`.
- `TestChat_HappyPath` — seam: `fakeTransport` returns a 200 Messages body
  `{"model":"claude-3-5-sonnet","content":[{"type":"text","text":"hello world"}],
  "usage":{"input_tokens":11,"output_tokens":5}}`. Assert: `resp.Output == "hello world"`,
  `resp.Model == "claude-3-5-sonnet"`, `TokensIn == 11`, `TokensOut == 5`. Request
  assertions: `POST`, URL == `https://api.anthropic.com/v1/messages` when BaseURL
  empty (or the configured base + `/v1/messages`), body has `model`, `messages[0]`
  is `{role:user,content:"say hi"}`, `max_tokens == 256`, `temperature == 0.5`,
  top-level `system == "be terse"`.
- `TestChat_MultiTextBlockConcatenation` — content is two text blocks
  `["foo","bar"]`; assert `resp.Output == "foobar"` (ordered concat). (Also satisfies
  the edge "multi-block" requirement.)

### Category: Input-validation

- `TestChat_DefaultMaxTokensWhenZero` — `ChatRequest{MaxTokens: 0}`; assert body
  `max_tokens == defaultMaxTokens` (4096) and `max_tokens` is ALWAYS present (never omitted).
- `TestChat_OmitsSystemWhenEmpty` — empty `SystemPrompt`; assert body has no `system`
  key; also assert no `temperature` key when `Temperature == 0`.
- `TestChat_StructuredOutput_EmitsForcedTool` — `OutputSchema = {"type":"object",
  "properties":{"answer":{"type":"integer"}}}`. Assert body has NO `response_format`;
  has `tools` (len 1) whose `[0].name == "structured_output"` and
  `[0].input_schema == schema`; has `tool_choice == {"type":"tool","name":"structured_output"}`.
  Upstream returns a tool_use block `{"type":"tool_use","name":"structured_output",
  "input":{"answer":42}}`; assert `resp.Output` is a string and `assert.JSONEq("{\"answer\":42}", s)`.
- `TestChat_StructuredOutput_NonForcedTextReturnedRaw` — schema set but upstream
  returns a plain text block (no tool_use); assert adapter returns the raw text string
  (parsing is the executor's job — mirror openrouter `..._ContentNotJSON_ReturnedRaw`).

### Category: Error-handling

- `TestChat_UpstreamNon2xx` — table over `{401, 429, 500}`; `fakeTransport` returns
  that status with `{"type":"error","error":{"message":"nope"}}`. Assert `err` non-nil,
  `resp == nil`, `errors.Is(err, ErrUpstreamStatus)`, and `!strings.Contains(err.Error(), secretKey)`.
- `TestChat_TransportError` — `fakeTransport{err: errors.New("dial tcp: connection refused")}`;
  assert error propagates, `resp == nil`, key absent from message.
- `TestChat_MalformedBody` — 200 with `{not json`; assert decode error, `resp == nil`,
  key absent.
- `TestChat_EmptyBody` — 200 with empty body; assert decode error.
- `TestChat_MissingContent` — 200 with `{"model":"m","usage":{...}}` (no `content`);
  assert error (`response has no content`).
- `TestChat_RevealKeyError` — `RevealKey` returns `("", errors.New("credential unavailable"))`;
  assert error contains `"credential unavailable"`, `resp == nil`, and
  `ft.gotReq == nil` (transport NOT called).

### Category: Edge-cases

- `TestChat_BaseURLDefaultWhenEmpty` — `staticBinding("", "m")`; assert request URL ==
  `https://api.anthropic.com/v1/messages`.
- `TestChat_BaseURLTrailingSlashTrimmed` — BaseURL `"https://api.anthropic.com/"`;
  assert URL == `https://api.anthropic.com/v1/messages` (no doubled slash).
- `TestChat_AnthropicVersionHeaderAlwaysPresent` — after a successful `Chat`, assert
  `ft.gotReq.Header.Get("anthropic-version") == "2023-06-01"` (the pinned const).
  (Add a second assertion in the non-2xx and structured-output tests that the header is
  still set, or fold into a small table to prove "always".)
- `TestChat_ContentTypeJSON` — assert `Content-Type: application/json` on the request.

### Category: Security

- `TestChat_APIKeySetsXApiKeyOnly` — after `Chat`, assert
  `ft.gotReq.Header.Get("x-api-key") == secretKey` AND
  `ft.gotReq.Header.Get("Authorization") == ""` (Anthropic uses x-api-key, never Bearer).
- `TestChat_MissingKey_FailsClosedBeforeCall` — binding with `RevealKey: nil` (and a
  variant where `RevealKey` returns `("", nil)`); assert `errors.Is(err, ErrMissingAPIKey)`,
  `resp == nil`, and `ft.gotReq == nil` (request never reaches transport). This is the
  configuration-class fail-closed required by the design (Anthropic always needs a key).
- `TestSecurity_KeyNeverLogged` — table over branches {chat_success, chat_non2xx,
  chat_malformed}; one responder embeds `secretKey+"-ish"` in the error body; run each
  inside `captureLogs` and assert logs never contain `secretKey`.
- `TestStatusError_ScrubsControlCharacters` — 500 body
  `"bad\nrequest\r\twith\x1b[31mescape\x00null"`; assert `errors.Is(err, ErrUpstreamStatus)`
  and the error string contains no `\n`, `\r`, `\x1b`, `\x00`, while `"request"`/`"escape"`
  survive.
- `TestChat_MaxResponseBytesExceeded` — build a 200 body far larger than the cap;
  `Config{MaxResponseBytes: 32}`; assert `err` non-nil, `resp == nil` (bounded read trips).
- `TestNew_CarriesMaxResponseBytes` — `New(Config{MaxResponseBytes: 1<<20})`; assert
  `p.maxResponseBytes == 1<<20` (composition-root guard for the OOM cap wiring).
- `TestChat_RejectsNonHTTPScheme` — `staticBinding("file:///etc/passwd","m")`; assert
  `errors.Is(err, ErrUnsupportedScheme)`, `resp == nil`, `ft.gotReq == nil`.

### Unit table (decode/build helpers)

- `TestBuildBody_StructuredOutputShape` — pure `buildBody` call with a schema; unmarshal
  result and assert the `tools` / `tool_choice` / absent-`response_format` invariants
  (faster, transport-free).
- `TestMessagesOutput_PrefersToolUseOverText` — `messagesResponse.output()` with both a
  text block and a tool_use block returns the tool_use Input JSON, not the text.

---

## Acceptance criteria

- `go test ./adapter/anthropicapi/...` passes; all five categories present and green.
- `Provider` satisfies `engine.ChatProvider` (compile-time assertion).
- `Chat` produces the exact Anthropic Messages body: always-present `max_tokens`
  (default 4096 when zero), top-level `system` only when non-empty, `temperature` only
  when non-zero, single user message, and the forced-tool encoding (no `response_format`)
  when `OutputSchema != nil`.
- Headers: `x-api-key` carries the revealed key and nothing else uses it;
  `anthropic-version: 2023-06-01` is always present; `content-type: application/json`.
  `Authorization` is never set.
- Missing key (nil RevealKey or empty result) returns `ErrMissingAPIKey` BEFORE the
  transport is invoked.
- Non-2xx maps to `ErrUpstreamStatus` with a bounded, control-scrubbed, key-free snippet.
- `MaxResponseBytes` caps the response read; `0` means no cap.
- The revealed key never appears in any returned error or any captured log.
- `make test` stays hermetic (no live network; all paths exercised via `fakeTransport`).
- `StreamChat` is a compiling stub returning a not-implemented error (replaced in Task 2);
  no streaming test is asserted in this task.
