# Native Anthropic exact-model response evidence

Primary source inspected for this execution slice (2026-10-16): Anthropic's
Go SDK at commit `543d266ce81a4487281e084b013fef807dbb5f18`,
[`message.go`](https://github.com/anthropics/anthropic-sdk-go/blob/543d266ce81a4487281e084b013fef807dbb5f18/message.go).

- `Message.Model` is required and documented as "The model that will complete
  your prompt." `Message.Role` is always `assistant`; `Message.Type` is always
  `message`. Text and tool-use output are content blocks, not model evidence.
- `MessageStartEvent` contains the response `message: Message`. The stream
  reports identity there, **not on every content delta**. Usage is split between
  message_start and message_delta. Ping/comment frames carry no identity.
- `MessageStopEvent` (`type: message_stop`) terminates a message. A pinned stream
  must observe the parsed data event, not just EOF or an SSE event-name line.

The existing adapter uses `/v1/messages`, `x-api-key` and
`anthropic-version: 2023-06-01`. Verification consumes the existing bounded JSON
and SSE parsers in production Chat/StreamChat. A pinned call requires an
assistant message with exactly the binding's executable model spelling. A pinned
stream must start with that identity before emitting text/tool-input tokens,
must not start a second message, and must finish with message_stop. Legacy
unpinned calls retain their existing behavior.

No alias, provider prefix, dated snapshot or deployment equivalence is inferred.
This source establishes the response identity fields, not a claim that an
arbitrary configured proxy is truthful. Missing/contradictory evidence fails
closed. These tests do not establish usability discovery or confirmation reuse.

Representative schema-based fixtures (not recordings of a paid model call):

```json
{"type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"verified output"}]}
```

```text
event: message_start
data: {"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-sonnet-4-6"}}

event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"verified output"}}

event: message_stop
data: {"type":"message_stop"}
```

The root test uses the existing `anthropic` connection-provider spelling, routed
by the real factory to this native adapter. It checks persisted pins, request
model/auth/path/mode, durable node output/transcripts and continuation rejection.
The canonical `anthropic-api` spelling currently fails upstream fixed-version
resolution before HTTP execution; that separate blocker is recorded in P3T4.
