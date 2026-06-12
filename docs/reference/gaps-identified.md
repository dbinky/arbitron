# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

### EMIT-001: chat-kind profiles do not yet emit context/effort (wired-but-no-op seam)
**Area**: claude-cli emission (`adapter/claudecli/env.go`, `shared.go`, `chat_adapter.go`, `stream_agent_adapter.go`) + Runtime mapping (#7, #8, #21)
**Detail**: Design Phase 3 specifies that context-window + effort take effect for the claude backend on **both** chat and agent paths (e.g. a `kind: chat` profile pinned to `200k` should run the standard window). The implementation routes the chat/stream env build through the shared `applyCapabilityEnv` seam, but the chat path constructs it from a **zero `ResolvedBinding`** because `engine.ChatRequest` does not carry the profile's `ContextWindow`/`Effort` — so chat-kind emission is currently a no-op. Agent-kind profiles emit correctly. **Action for the loop**: verify whether chat-kind (`llm_call`) profiles genuinely need the axes at runtime; if so, plumb `ContextWindow`/`Effort` from the resolved profile through the chat resolution path onto the chat env build (the seam already exists), with tests proving a `chat`+`200k` profile sets `CLAUDE_CODE_DISABLE_1M_CONTEXT`/`AUTO_COMPACT_WINDOW` and a `chat`+effort profile sets `CLAUDE_CODE_EFFORT_LEVEL`. If the team decides chat-tier context/effort is out of scope for v1, move this to Won't Fix with that rationale (only the user may do so).

## Fixed Previously

_(none yet — as issues are fixed during iterations, move them here with a summary of what was wrong and how it was fixed)_

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Candidate out-of-scope items per the spec's Non-Goals (do NOT build these; log here if encountered):
- Per-node context/effort override (node > profile precedence).
- opencode + ollama.com (DeepSeek) effort/thinking runtime wiring.
- CLI commands for profile/connection mutation (MCP is the only mutation surface).
- New model backends or adapters beyond claude-code/codex/opencode.
- REST endpoints for profile management (services are surface-agnostic, but only MCP is wired this feature).
-->
