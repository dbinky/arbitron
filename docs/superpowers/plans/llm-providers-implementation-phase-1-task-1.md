# LLM Providers — Phase 1, Task 1: Factory routing for OpenAI + Ollama

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route the `openai` and `ollama` chat providers (plus the `ollama-cloud` alias → `ollama`) to the existing OpenAI-compatible `adapter/openrouter` adapter inside the provider Factory, so both become selectable on chat / non-tool-using nodes with no new HTTP adapter.

**Architecture:** `adapter/provider/factory.go` already maps `openrouter`/`litellm` → `openrouter.New(...)` and `azure-*` → `azurefoundry.New(...)` in `ChatProviderFor`, after normalizing the provider string (trim + lowercase). This task adds two canonical constants and one alias, and extends the existing `switch` so `openai` and `ollama` reuse the exact same `openrouter.New(openrouter.Config{...})` construction arm. `AgentProviderFor` is untouched (agent path is Phase 4).

**Tech Stack:** Go, `testing` + `testify` (`assert`/`require`), table-free per-case tests mirroring `adapter/provider/factory_test.go`. No network: selection-only tests construct adapters but never call them.

---

## Dependencies

- **None.** Phase 1 depends on nothing. This task depends on no other task. (Task 2 — the url_policy loopback allowance — is independent and can proceed in parallel.)

## Spec / design references

- Spec `docs/specs/llm-providers-spec.md` — Provider Catalog (OpenAI API, Ollama cloud, Ollama local); "Universal chat" success criterion #2.
- Design `docs/superpowers/specs/llm-providers-design-phase-1.md` §1 "Factory routing" and the provider→BaseURL→Key table.

## Shared file: `adapter/provider/factory.go` (edit ordering)

`adapter/provider/factory.go` is edited by **four** tasks across the feature, each touching
a DISTINCT switch arm / struct field — apply them in **phase order (1→4)** to avoid churn:

- **P1 T1 (this task):** adds the `openai`/`ollama`/`ollama-cloud` strings to the existing
  openrouter `ChatProviderFor` arm.
- **P2 T2:** adds a NEW `anthropic-api` case to `ChatProviderFor`.
- **P3 T2:** adds a NEW `bedrock`/`amazon-bedrock` case to `ChatProviderFor`.
- **P4 T2:** sets `AllowAllTools` in the `AgentProviderFor` opencode arm + adds
  `Deps.OpencodeAllowAllTools`.

These do not conflict (distinct arms/fields); just rebase in order. This task only touches
the openrouter `ChatProviderFor` arm and the provider-const block.

## Files

- **Modify:** `adapter/provider/factory.go`
  - Add to the canonical-provider `const` block (currently lines 24-30):
    ```go
    providerOpenAI = "openai"
    providerOllama = "ollama"
    // providerOllamaCloud is an accepted alias for the ollama family; it is
    // normalized to providerOllama in ChatProviderFor. Local vs cloud Ollama are
    // distinguished by the connection's BaseURL + presence/absence of a key, not
    // by the provider string.
    providerOllamaCloud = "ollama-cloud"
    ```
  - In `ChatProviderFor` (currently lines 96-120), extend the existing openrouter arm's `case` list so `openai`, `ollama`, and `ollama-cloud` route to the SAME `openrouter.New(openrouter.Config{...})` already used for `openrouter`/`litellm`:
    ```go
    case providerOpenRouter, providerLiteLLM, providerOpenAI, providerOllama, providerOllamaCloud:
        return openrouter.New(openrouter.Config{
            Binding:          b,
            HTTPClient:       f.deps.HTTPClient,
            Timeout:          f.deps.HTTPTimeout,
            MaxResponseBytes: f.deps.MaxResponseBytes,
        }), nil
    ```
    (No separate arm — adding the strings to the existing `case` keeps a single construction site so `MaxResponseBytes`/`HTTPClient`/`HTTPTimeout` threading stays DRY and FACTORY-001-safe.)
  - Update the `ChatProviderFor` doc comment (lines 91-95) to mention that `openai` and `ollama`/`ollama-cloud` also map to `adapter/openrouter`.
- **Test:** `adapter/provider/factory_test.go` (append; the package is `provider_test` and already imports `openrouter`, `assert`, `require`, and the `chatBinding(name)`/`newTestFactory(t)` helpers).

## TDD test list (five categories)

All tests use the existing `newTestFactory(t)` (claude-cli adapters pre-wired, no HTTP/opencode I/O) and `chatBinding(providerName)` helpers from `factory_test.go`. **Seam:** none — these are construction/selection tests that assert the concrete returned type with `assert.IsType`. No `httptest`, no transport, no network is needed because the adapter is never invoked.

### Happy-path

- [ ] `TestFactory_ChatProviderFor_OpenAI_MapsToOpenRouter`
  - `cp, err := f.ChatProviderFor(chatBinding("openai"))`
  - Assert `require.NoError(err)` and `assert.IsType((*openrouter.Provider)(nil), cp)`.
- [ ] `TestFactory_ChatProviderFor_Ollama_MapsToOpenRouter`
  - `chatBinding("ollama")` → `require.NoError`, `assert.IsType((*openrouter.Provider)(nil), cp)`.

### Input-validation

- [ ] `TestFactory_ChatProviderFor_OllamaCloudAlias_NormalizesToOllama`
  - `chatBinding("ollama-cloud")` selects the openrouter adapter: `require.NoError`, `assert.IsType((*openrouter.Provider)(nil), cp)`. This proves the alias is accepted (validated as a known provider, not rejected as unknown).
- [ ] `TestFactory_ChatProviderFor_OpenAI_CaseAndWhitespaceNormalized`
  - `chatBinding("  OpenAI  ")` → `require.NoError`, `assert.IsType((*openrouter.Provider)(nil), cp)` (mirrors the existing `NormalizesCaseAndWhitespace` test for openrouter; proves `normalizeProvider` covers the new strings).

### Error-handling

- [ ] `TestFactory_ChatProviderFor_UnknownProviderStillErrors_Ollamish`
  - A near-miss string that is NOT a recognized provider, e.g. `chatBinding("ollamaa")` (extra `a`) and `chatBinding("open-ai")` (hyphen), must each return `ErrUnknownProvider`:
    `require.Error(err)`, `assert.ErrorIs(err, adapterprovider.ErrUnknownProvider)`, and `assert.Contains(err.Error(), "ollamaa")` / `"open-ai"` respectively. Confirms the new arm did not over-match.

### Edge-cases

- [ ] `TestFactory_ChatProviderFor_OllamaCloud_CaseAndWhitespaceNormalized`
  - `chatBinding("  Ollama-Cloud ")` → `require.NoError`, `assert.IsType((*openrouter.Provider)(nil), cp)` (alias survives normalization).
- [ ] `TestFactory_AgentProviderFor_OpenAI_RoutesToOpencode_NotChanged`
  - `ap, err := f.AgentProviderFor(agentBinding("openai"))` → `require.NoError`, `assert.IsType((*opencode.Provider)(nil), ap)`. Proves Task 1 did NOT accidentally special-case `openai`/`ollama` on the agent path (the agent path is Phase 4; any non-claude provider still falls through to opencode). Requires importing `opencode` and using the existing `agentBinding` helper — both already present in `factory_test.go`.

### Security

- [ ] `TestFactory_ChatProviderFor_OpenAI_DoesNotLogRevealedKey`
  - Mirror the existing `TestFactory_Selection_DoesNotLogRevealedKey`: install a buffer-backed `slog` default, build `b := chatBinding("openai")` with `b.RevealKey = func(context.Context) (string, error) { return "sk-OPENAI-SENTINEL", nil }`, call `f.ChatProviderFor(b)`, then assert `require.NoError`, `require.NotNil(cp)`, and `assert.False(strings.Contains(buf.String(), "sk-OPENAI-SENTINEL"))`. Confirms selection never logs the key for the new provider. (`bytes`, `log/slog`, `strings`, `context` are already imported in `factory_test.go`.)

## Acceptance criteria (what `go test ./...` proves)

- `go test ./adapter/provider/...` passes with the new tests, proving:
  - `openai`, `ollama`, and `ollama-cloud` (case/whitespace-insensitive) each select the `openrouter.Provider` chat adapter.
  - `ollama-cloud` is normalized to the same adapter as `ollama` (single family).
  - Unknown near-miss strings still return `ErrUnknownProvider` (no over-matching).
  - The agent path for `openai` is unchanged (routes to opencode), proving Task 1 scoped its change to `ChatProviderFor`.
  - Selection of the new providers never leaks a revealed key into logs.
- `go test ./...` (`make test`) stays green — no other package depends on these provider strings, and the change is additive to one `switch` arm.

## "No secret in logs/errors" security checks

- The Factory never reveals or logs the key: `ChatProviderFor` only carries `b` (with its lazy `RevealKey`) into `openrouter.Config.Binding`. The security test above asserts the revealed key does not appear in `slog` output during selection.
- The actual Authorization-header construction and its scrubbing (`statusError`/`scrubControl`, `ErrUpstreamStatus`) live in `adapter/openrouter` and are exercised by that package's own tests; this task adds no new key-handling code, so it introduces no new leak surface.
