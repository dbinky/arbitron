# Phase 2 / Task 1 — Codex streaming agent adapter

**Design:** `docs/superpowers/specs/codex-backend-design-phase-2.md`
**Depends on:** Phase 1 Task 2 (`adapter/codex` args/env/parse).

## Goal
A `StreamProvider` in `adapter/codex` implementing `engine.ChatProvider`
(`Chat` + `StreamChat`) that drives `codex exec` in streaming-JSON mode and
emits incremental `engine.ChatChunk`s — the first non-Claude streaming agent
adapter. Parallel to `claudecli.StreamAgentAdapter`.

## Files (under `adapter/codex/`)
- `stream_adapter.go` + `stream_adapter_test.go`
- refactor `parse.go` so the JSONL event decoder is shared between the batch
  `RunAgent` fold and the incremental stream (one event vocabulary, no
  divergence — mirror claudecli's `streamjson.go`/`streamstate.go` sharing).

## Contracts
- `NewStreamProvider(Config) *StreamProvider` (same `Config` as the batch
  Provider; reuses `args.go` + `env.go`).
- `Chat(ctx, ChatRequest) (*ChatResponse, error)` — run to completion, return
  terminal result.
- `StreamChat(ctx, ChatRequest) (<-chan ChatChunk, error)` — incremental
  assistant text.
- `ToolScope` precedence: non-empty `ChatRequest.ToolScope` overrides the
  adapter's global scope (matches claudecli); under `--yolo` this narrows the
  tool set, absent a scope the posture stays fully open.

## TDD steps
1. **RED** — `StreamChat` over a fake runner emitting an incremental JSONL event
   sequence yields ordered `ChatChunk`s ending in the terminal result.
2. **GREEN** — implement using the shared decoder + `args.go`/`env.go`.
3. **error handling:** empty WorkDir/model rejected pre-launch; a mid-stream
   runner error / malformed event closes the channel with a typed error (no
   silent partial success).
4. **security:** assert the stream env carries no `OPENAI_API_KEY` and preserves
   `HOME`.
5. **shared-decoder refactor:** a regression test confirms batch `RunAgent` and
   `StreamChat` produce consistent summaries from the same event fixture.

## Acceptance
- `go test ./adapter/codex/...` passes (batch + stream).
- `StreamChat` relays incremental output and a terminal result suitable for an
  interactive node's `outputSchema` extraction.
