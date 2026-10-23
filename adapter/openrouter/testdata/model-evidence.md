# OpenAI-compatible exact-model response evidence

Primary-source schemas inspected for this execution slice (2026-10-16):
OpenAI's generated Python SDK at commit
`230a923faba5574ebdc5b45a505a26b6bad79f61`:

- [ChatCompletion](https://github.com/openai/openai-python/blob/230a923faba5574ebdc5b45a505a26b6bad79f61/src/openai/types/chat/chat_completion.py):
  top-level `model: str`, documented as "The model used for the chat completion."
  Assistant output is in `choices[].message.content`; request `messages[]` is
  not response evidence.
- [ChatCompletionChunk](https://github.com/openai/openai-python/blob/230a923faba5574ebdc5b45a505a26b6bad79f61/src/openai/types/chat/chat_completion_chunk.py):
  top-level `model: str`, `choices[].delta`, optional `usage`. The model field
  is required even on the final usage-only chunk with empty choices.
- [stream reader](https://github.com/openai/openai-python/blob/230a923faba5574ebdc5b45a505a26b6bad79f61/src/openai/_streaming.py):
  SSE `data: [DONE]` terminates the response. This adapter requires that marker
  for pinned execution; EOF alone cannot complete a pinned turn.

The existing shared adapter serves OpenAI-compatible connections, including
OpenRouter, LiteLLM and Ollama's compatibility endpoint. These schemas establish
where actual response identity is reported, **not** a universal model-name
translation or a guarantee that a third-party proxy reports truthfully. Only
exact equality with the executable binding is accepted. Different snapshots,
router/provider prefixes, case or deployment spellings are not inferred equal.
Nonconforming/missing identity fails closed for pinned calls. Native Anthropic,
Azure and Bedrock protocols are separate, not certified by these schemas.

Representative protocol shapes exercised by `model_probe_test.go`:

```json
{"model":"deepseek/deepseek-v4-flash","choices":[{"message":{"role":"assistant","content":"verified output"}}]}
```

```text
data: {"model":"deepseek/deepseek-v4-flash","choices":[{"delta":{"content":"verified output"}}]}

data: [DONE]
```

These are schema-based test fixtures, not recordings of a live paid model call.
The root test substitutes only upstream HTTP I/O, validates outgoing model,
authorization, path and stream mode, and observes persisted run/node output and
initial/resumed conversation acceptance. This does not certify smoke probes,
durable discovery, model equivalences or refresh eligibility.
