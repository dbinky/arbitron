# LLM Providers — Phase 2 / Task 2: `anthropicapi` SSE `StreamChat` + factory/main routing

**Spec:** `docs/specs/llm-providers-spec.md`
**Design:** `docs/superpowers/specs/llm-providers-design-phase-2.md`
**Phase:** 2 of 5 — native Anthropic Messages API chat adapter
**Task:** 2 of 2 — SSE `StreamChat`, factory routing (`anthropic-api`), composition-root wiring
**Mirror template:** `adapter/openrouter/{provider.go,response.go,provider_test.go}`,
`adapter/provider/{factory.go,factory_test.go}`
**Depends on:** Task 1 (the `adapter/anthropicapi` package: `Provider`, `Config`, `New`,
`buildBody`, `decodeMessages`, `boundedReader`, `statusError`, `scrubControl`,
`ErrUpstreamStatus`, `anthropicVersion`, `defaultBaseURL`, `messagesPath`,
`ErrMissingAPIKey`).

---

## Objective

Complete the Anthropic Messages adapter and make it selectable:

1. Replace the Task-1 `StreamChat` stub with a real SSE implementation that POSTs the
   streaming Messages body and maps the Anthropic event stream to `engine.ChatChunk`s:
   `content_block_delta` (text_delta / input_json_delta) → token chunks;
   `message_delta` / `message_stop` → terminal `Done` chunk carrying
   `usage.input_tokens` / `usage.output_tokens`. Same goroutine/channel contract as
   `openrouter.StreamChat`: bounded body read, mid-stream failure delivered as a
   `ChatChunk{Error}` before the channel closes, context-aware `send`, always drains +
   closes.
2. Add the `providerAnthropicAPI = "anthropic-api"` route to
   `adapter/provider/factory.go` `ChatProviderFor`, constructing `anthropicapi.New(...)`
   with the threaded `HTTPClient` / `HTTPTimeout` / `MaxResponseBytes`. Must NOT collide
   with the empty/`claude-cli` subscription path or any existing provider string.
3. Confirm composition-root wiring needs no change (the Factory already receives
   `HTTPClient`, `HTTPTimeout`, `MaxResponseBytes` from `cmd/arbitron/main.go`); add a
   factory test proving the new route threads those deps.

## Dependencies

- Task 1 complete and green.
- `adapter/provider/factory.go`, `adapter/provider/factory_test.go` (existing).
- `cmd/arbitron/main.go` ~464-476 (existing `NewFactory(Deps{...})` call — inspected; no
  edit expected because Anthropic reuses the same HTTP deps).
- Same test infra (`fakeTransport`, `sseResponse`, testify).

---

## Files to modify / extend

### 1. `adapter/anthropicapi/provider.go` — implement `StreamChat`

Replace the Task-1 stub. Structure mirrors `openrouter.Provider.StreamChat`:

```go
func (p *Provider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	streamCtx, cancel := procenv.WithTimeout(ctx, p.timeout)

	body, err := buildBody(req, p.binding.Model, true) // stream:true
	if err != nil { cancel(); return nil, err }

	httpReq, err := p.newRequest(streamCtx, body) // missing-key check fires here, pre-goroutine
	if err != nil { cancel(); return nil, err }   // ErrMissingAPIKey returned synchronously, no channel

	resp, err := p.httpClient.Do(httpReq)
	if err != nil { cancel(); return nil, fmt.Errorf("anthropicapi: stream call: %w", err) }

	out := make(chan engine.ChatChunk)
	go func() {
		defer cancel()
		defer close(out)
		defer func() { _ = resp.Body.Close() }()

		reader := p.boundedReader(resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			send(streamCtx, out, engine.ChatChunk{Error: p.statusError(resp.StatusCode, reader)})
			return
		}

		var usage *usageBlock
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var curEvent string
		for scanner.Scan() {
			tok, u, done, perr := parseSSEEvent(scanner.Bytes(), &curEvent)
			if perr != nil { send(streamCtx, out, engine.ChatChunk{Error: perr}); return }
			if u != nil { usage = u }
			if done { break }
			if tok != "" {
				if !send(streamCtx, out, engine.ChatChunk{Token: tok}) { return }
			}
		}
		if serr := scanner.Err(); serr != nil {
			send(streamCtx, out, engine.ChatChunk{Error: fmt.Errorf("anthropicapi: read stream: %w", serr)})
			return
		}
		final := engine.ChatChunk{Done: true}
		if usage != nil { final.TokensIn = usage.InputTokens; final.TokensOut = usage.OutputTokens }
		send(streamCtx, out, final)
	}()
	return out, nil
}
```

Reuse the `send(ctx, out, chunk) bool` helper copied verbatim from openrouter (add it in
Task 1 if not already present, or here — it is shared by Chat-less streaming only, so it
may land in Task 2).

### 2. `adapter/anthropicapi/response.go` — Anthropic SSE parsing

Anthropic SSE differs from OpenAI: each event has BOTH an `event:` line and a `data:`
line, and tokens arrive inside `content_block_delta`. We track the current event name
across lines.

```go
// streamEvent is the JSON payload on an Anthropic SSE `data:` line.
type streamEvent struct {
	Type  string `json:"type"` // e.g. "content_block_delta","message_delta","message_stop","ping","error"
	Delta struct {
		Type        string `json:"type"`         // "text_delta" | "input_json_delta"
		Text        string `json:"text"`         // text_delta payload
		PartialJSON string `json:"partial_json"` // input_json_delta payload (forced-tool streaming)
		StopReason  string `json:"stop_reason"`  // on message_delta
	} `json:"delta"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`        // present on message_start (input) and message_delta (output)
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`        // present on an SSE `error` event
}

// parseSSEEvent interprets ONE line of an Anthropic SSE stream, threading the current
// event name through curEvent across the event:/data: line pair. Returns the token
// delta (text_delta.Text or input_json_delta.PartialJSON), a usage block when the data
// frame carries one, done=true on message_stop, and an error for a malformed data
// frame or an upstream `error` event. Blank lines, `:` comments, and `event:` lines
// (which only update *curEvent) return all-zero.
func parseSSEEvent(line []byte, curEvent *string) (token string, usage *usageBlock, done bool, err error)
```

`parseSSEEvent` rules:
- Trim the line. Blank or `:`-prefixed (comment / keep-alive) → all-zero.
- `event:` prefix → set `*curEvent = strings.TrimSpace(rest)`; all-zero (no token yet).
- `data:` prefix → unmarshal into `streamEvent`; on unmarshal error return
  `anthropicapi: decode stream frame` error.
  - `Type == "error"` (or non-nil `.Error`) → return error
    `anthropicapi: upstream stream error: <scrubControl(message)>` (NEVER the key; scrub
    control chars on the upstream-shaped message).
  - `Type == "content_block_delta"`: token = `Delta.Text` if `Delta.Type=="text_delta"`,
    else `Delta.PartialJSON` if `Delta.Type=="input_json_delta"` (forced-tool streaming).
  - `Type == "message_delta"`: capture `usage` if present (output_tokens).
  - `Type == "message_start"`: capture `usage` if present (input_tokens).
  - `Type == "message_stop"`: `done = true`.
  - `Type == "ping"` / anything else: all-zero (ignored).
- Decision: prefer parsing on `data:` payload `.Type` (self-describing) over relying on
  `*curEvent`; keep `curEvent` only as a fallback/debug aid. (Anthropic sets both; the
  data `type` is authoritative.)

### 3. `adapter/provider/factory.go` — add the `anthropic-api` route

> **Shared-file ordering:** `adapter/provider/factory.go` is edited by P1 T1 (openai/ollama arm), this task (anthropic-api arm), P3 T2 (bedrock arm), and P4 T2 (`AgentProviderFor` AllowAllTools + `Deps.OpencodeAllowAllTools`). Each touches a distinct `ChatProviderFor` switch arm / struct field — no real conflict; apply in phase order (1→4). This task adds ONE new `ChatProviderFor` case + one const, leaving the openrouter arm (P1) untouched.

- Add a const beside the others:
  ```go
  providerAnthropicAPI = "anthropic-api"
  ```
- In `ChatProviderFor`, add a case BEFORE `default`:
  ```go
  case providerAnthropicAPI:
      return anthropicapi.New(anthropicapi.Config{
          Binding:          b,
          HTTPClient:       f.deps.HTTPClient,
          Timeout:          f.deps.HTTPTimeout,
          MaxResponseBytes: f.deps.MaxResponseBytes,
      }), nil
  ```
- Add the import `"github.com/arbitron/arbitron/adapter/anthropicapi"`.
- Update the `ChatProviderFor` doc comment to mention `anthropic-api → adapter/anthropicapi`.
- Collision check: `anthropic-api` ≠ `""` ≠ `claude-cli` ≠ `openrouter` ≠ `litellm` ≠
  `azure-openai` ≠ `azure-foundry`. No overlap. The Claude CLI **subscription** path stays
  the empty/`claude-cli` branch; `anthropic-api` is the keyed HTTP path.

### 4. `cmd/arbitron/main.go` — verify (likely no change)

The `NewFactory(Deps{...})` call at ~464-476 already supplies `HTTPClient`,
`HTTPTimeout`, and `MaxResponseBytes`, which is everything `anthropicapi.New` needs. No
edit expected. If a smoke/wiring check reveals a gap, note it; otherwise leave main.go
untouched and record "no change required" in the task report.

---

## TDD test list

### A) `adapter/anthropicapi/provider_test.go` — streaming (add to Task-1 file)

Add an `sseResponse(status, body)` helper (copy from openrouter: `Content-Type:
text/event-stream`).

#### Category: Happy-path

- `TestStreamChat_SSEChunking` — seam: `sseResponse(200, …)` with an Anthropic event
  sequence:
  ```
  event: message_start
  data: {"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":0}}}

  event: content_block_delta
  data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}

  event: content_block_delta
  data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}

  event: content_block_delta
  data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"!"}}

  event: message_delta
  data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

  event: message_stop
  data: {"type":"message_stop"}
  ```
  Assert ordered tokens `["Hel","lo","!"]`, exactly one terminal `Done` chunk,
  `done.TokensIn == 9`, `done.TokensOut == 3`, no `Error` chunks. Also assert request
  body had `stream == true` and the request carried `x-api-key == secretKey` and
  `anthropic-version == 2023-06-01`.

#### Category: Input-validation

- `TestStreamChat_StructuredOutput_InputJSONDelta` — schema set; stream emits
  `content_block_delta` with `delta.type == "input_json_delta"` and `partial_json`
  fragments `"{\"answer\":"`, `"42}"`. Assert concatenated tokens reconstruct
  `{"answer":42}` (forced-tool streaming yields the JSON as tokens). Assert the request
  body carried the forced `tools`/`tool_choice` (reuse Task-1 invariants).

#### Category: Error-handling

- `TestStreamChat_UpstreamNon2xx` — `sseResponse(500, `{"type":"error",...}`)`; assert
  `StreamChat` returns `err == nil` (transport ok) but the channel yields a
  `ChatChunk{Error}` with `errors.Is(c.Error, ErrUpstreamStatus)` and no `secretKey`,
  then closes.
- `TestStreamChat_MidStreamErrorEvent` — a valid prefix then
  `data: {"type":"error","error":{"message":"overloaded"}}`; assert an `Error` chunk is
  delivered (message contains "overloaded", control-scrubbed, no key) before close.
- `TestStreamChat_MalformedDataFrame` — `data: {nope`; assert an `Error` chunk before close.
- `TestStreamChat_RevealKeyError` — `RevealKey` errors; accept either a synchronous
  returned error or an `Error` chunk (mirror openrouter `TestStreamChat_RevealKeyError`);
  assert message contains the reveal failure and no key leaks.

#### Category: Edge-cases

- `TestStreamChat_StopOnly` — only `message_stop` (no deltas); assert exactly one `Done`
  chunk, empty token, zero usage.
- `TestStreamChat_IgnoresPingAndComments` — interleave `event: ping` / `data: {"type":
  "ping"}` and `: keep-alive` lines among deltas; assert they produce no tokens and the
  token sequence is unaffected.
- `TestStreamChat_AnthropicVersionHeaderPresent` — assert the streaming request also
  sets `anthropic-version: 2023-06-01` (always present on both paths).

#### Category: Security

- `TestStreamChat_XApiKeyOnly_NoAuthHeader` — assert the streaming request sets
  `x-api-key == secretKey` and `Authorization == ""`.
- `TestStreamChat_MissingKey_FailsClosedBeforeCall` — `RevealKey: nil`; assert
  `errors.Is(err, ErrMissingAPIKey)`, returned channel is nil, and `ft.gotReq == nil`
  (request never reaches transport).
- `TestStreamChat_MaxResponseBytesExceeded` — stream body far larger than a 32-byte cap;
  assert an `Error` chunk is delivered (bounded read trips) and the channel closes.
- `TestStreamChat_KeyNeverLogged` — drive a streaming run (incl. an error-event variant
  whose body embeds `secretKey+"-ish"`) inside `captureLogs`; assert `secretKey` never
  appears.
- `TestStreamChat_ScrubsControlCharsInErrorEvent` — an `error` event message containing
  `\n\r\x1b\x00`; assert the surfaced `Error` chunk string is scrubbed.

#### Unit table (parser)

- `TestParseSSEEvent` — table mirroring openrouter's `TestParseSSELine`:
  rows for text_delta (token), input_json_delta (partial_json token), message_delta
  (usage, output_tokens), message_start (usage, input_tokens), message_stop (done),
  ping (zero), `event:` line (zero, updates curEvent), blank/comment (zero),
  error event (err), malformed json (err). Assert `(token, usage!=nil, done, err)` per row.

### B) `adapter/provider/factory_test.go` — routing

- `TestFactory_ChatProviderFor_AnthropicAPI` — `f.ChatProviderFor(chatBinding("anthropic-api"))`
  returns a non-nil `engine.ChatProvider`, no error, and (assert concrete type via a
  type switch or by behavior) is `*anthropicapi.Provider` — mirror
  `TestFactory_ChatProviderFor_OpenRouter`.
- `TestFactory_ChatProviderFor_AnthropicAPI_NotClaudeSubscription` — assert
  `chatBinding("anthropic-api")` does NOT return the injected `ClaudeChat` adapter
  (i.e. the keyed HTTP path is distinct from the empty/`claude-cli` subscription path).
  Cross-check `chatBinding("")` and `chatBinding("claude-cli")` still return `ClaudeChat`.
- `TestFactory_ChatProviderFor_AnthropicAPI_NormalizesCaseAndWhitespace` —
  `chatBinding("  Anthropic-API  ")` routes to anthropicapi (normalization parity with
  the openrouter case test).
- Extend `TestFactory_ProviderFor_ThreadsMaxResponseBytes` (or add
  `TestFactory_AnthropicAPI_ThreadsDeps`) to assert the `anthropic-api` route threads
  `HTTPClient` / `HTTPTimeout` / `MaxResponseBytes` from `Deps` into the constructed
  provider (assert via injected `fakeTransport` round-trip + the provider's
  `maxResponseBytes` field, mirroring the existing thread test).
- `TestFactory_Selection_DoesNotLogRevealedKey` — already exists; if practical, add an
  `anthropic-api` binding variant so selection of the new route is covered by the
  no-leak assertion too.

---

## Acceptance criteria

- `go test ./adapter/anthropicapi/... ./adapter/provider/...` passes; the five categories
  are present for `StreamChat` and the factory routing tests are green.
- `StreamChat` maps Anthropic SSE correctly: `content_block_delta` text_delta /
  input_json_delta → ordered token chunks; `message_start`/`message_delta` usage captured;
  `message_stop` → a single terminal `Done` chunk with `TokensIn`/`TokensOut`.
- Mid-stream failures (non-2xx, `error` event, malformed frame, `MaxResponseBytes` cap,
  reveal failure) surface as a `ChatChunk{Error}` (or synchronous error for reveal/missing
  key) and the channel always closes; the goroutine always drains + closes.
- Missing key fails closed BEFORE the transport on the streaming path too
  (`ErrMissingAPIKey`, nil channel, `gotReq == nil`).
- `x-api-key` carries the key and is the ONLY key use; `Authorization` is never set;
  `anthropic-version: 2023-06-01` is present on the streaming request; the key never
  appears in any token, error chunk, returned error, or captured log; upstream error
  text is control-scrubbed.
- `ChatProviderFor` routes normalized `anthropic-api` to `anthropicapi.New(...)` with the
  threaded `HTTPClient` / `HTTPTimeout` / `MaxResponseBytes`, without colliding with the
  empty/`claude-cli` subscription path or any existing provider string; unknown providers
  still return `ErrUnknownProvider`.
- `cmd/arbitron/main.go` requires no change (or the change, if any, is minimal and noted);
  the composition root already supplies the deps the new route consumes.
- `make test` (`go test ./...`) is green and hermetic.
