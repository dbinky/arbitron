# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none — all identified gaps have been resolved)_

## Fixed Previously

### EMIT-001: chat-kind profiles did not emit context/effort (wired-but-no-op seam)
**Fixed in**: this iteration (commit following the 22-area review loop)
**What was wrong**: `ChatAdapter.spec()` called `adapterCore.chatEnv()` which always used a zero `provider.ResolvedBinding{}`. The binding's `ContextWindow` and `Effort` axes were never applied to the chat subprocess, so a `kind: chat` profile with `200k` or a non-default effort was silently a no-op. Agent-kind profiles emitted correctly via `agentEnvWithRules`.
**How it was fixed**:
- Renamed `adapterCore.chatEnv()` → `chatEnvWithBinding(binding)` so the method takes the actual binding.
- Added `binding provider.ResolvedBinding` field to `ChatAdapter` and a new `NewChatAdapterForBinding(cfg, binding)` constructor (mirroring the agent arm's `NewAgentAdapterForBinding`).
- Updated `ChatAdapter.spec()` to call `a.core.chatEnvWithBinding(a.binding)`.
- Updated `StreamAgentAdapter.spec()` (the other caller) to pass `provider.ResolvedBinding{}` (zero — unchanged behavior).
- Added `ClaudeChatConfig claudecli.Config` to `adapter/provider.Deps` and `claudeChatConfigSupplied()` helper. When supplied, `Factory.ChatProviderFor` builds a per-binding `ChatAdapter` instead of returning the pre-built singleton.
- Wired `ClaudeChatConfig: chatCfg` in `cmd/arbitron/main.go`.
- Tests: `TestChatAdapterForBinding_EmitsContextAndEffort`, `TestChatAdapterForBinding_DefaultAxesAddNoVars`, `TestChatAdapter_NoBindingNoCapabilityVars` in `adapter/claudecli/chat_adapter_test.go`; `TestFactory_ChatProviderFor_ClaudeChatConfigBuildsPerBindingAdapter`, `TestFactory_ChatProviderFor_ClaudeChatConfigExplicitProvider` in `adapter/provider/factory_test.go`.

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
