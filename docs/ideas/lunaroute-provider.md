# LunaRoute Provider — flat-rate open-weight inference as a first-class connection

**Status:** idea / not started.
**Written:** 2026-11-10
**Scope:** a first-class `lunaroute` provider. That means:
- a canonical provider id
- connection validation (the `lr_` key and a fixed endpoint)
- chat nodes on LunaRoute
- agent nodes on LunaRoute through the `claude-code` and `opencode` backends, with `pi` as a stretch
- the capability rules and tests that go with them

**Explicitly out of scope:**
- **System One** (LunaRoute's decision models, `POST /v1/systemone`). It belongs with [decision-node.md](decision-node.md), as a candidate `DecisionProvider` backend.
- Embeddings, rerank, images, audio, OCR and document conversion.
- LunaRoute's MCP server and web search.
- The `codex` backend (§7).
- The `@lunaroute/cli` setup tool (§3).

---

## 1. Why this exists

[LunaRoute](https://www.lunaroute.com/) sells **flat-rate inference on open-weight models**. You pay monthly for a number of concurrent requests ("lanes"), from 1 to 10 depending on plan, instead of paying per token. The catalog includes GLM 5.3, GLM 5.3 Flash, DeepSeek 4.1 Flash and Muse Spark Open, and every model is available on every plan.

That pricing suits Arbitron's workload. Ralph loops, implementation loops, reviewer fan-outs and long agent missions burn tokens in volume, and they currently run on metered routes such as OpenRouter and ollama-cloud, or on subscriptions with usage caps. A flat-rate open-weight backend is a cheap home for the "doer" side of the doer ≠ approver split (GPT or open models implement, Claude approves).

It is also a good, bounded first test for the composable `feature-pipeline`. It touches the domain, several adapters, validation and tests, but needs nothing external beyond one `lr_` key.

## 2. What exists today (grounding)

**LunaRoute itself (from the docs, 2026-11-07):**
- **One hosted endpoint, `https://gw.lunaroute.com`, speaks three API dialects:**
  - OpenAI chat completions: `POST /v1/chat/completions`
  - Anthropic Messages: `POST /v1/messages` and `/v1/messages/count_tokens`, which require `anthropic-version`
  - OpenAI Responses: `POST /v1/responses`
  - `GET /v1/models` lists the catalog.
- **Auth is an `lr_…` key** in `LUNAROUTE-API-KEY`, `Authorization: Bearer lr_…`, or `x-api-key: lr_…`, checked in that order. The gateway **strips** a standard auth header carrying an `lr_` key before forwarding upstream. A value *without* the `lr_` prefix (e.g. `sk-…`) is **forwarded upstream as a bring-your-own provider key**.
- **Optional `LUNAROUTE-PROJECT-ID` header** (free-form, at most 128 chars) attributes usage to a project.
- **Background priority** is chosen by model id suffix: `glm-5.3-background` gets lower fair-share priority, and the default is realtime.
- **`@lunaroute/cli`** (`npx @lunaroute/cli setup <harness>`) only writes config. It sets environment variables for Claude Code, writes `~/.codex/lunaroute.config.toml`, or adds the opencode plugin. **It is not a local gateway process.** Inference always goes straight to `gw.lunaroute.com`.
- **Claude Code on LunaRoute** (their docs) uses:
  - `ANTHROPIC_BASE_URL=https://gw.lunaroute.com` (root; Claude Code appends `/v1/messages`) and `ANTHROPIC_AUTH_TOKEN=<lr_ key>`
  - `ANTHROPIC_MODEL=<catalog id>`, and `ANTHROPIC_DEFAULT_HAIKU_MODEL=<same id>` for background calls (titles, summaries, compaction)
  - `CLAUDE_CODE_MAX_CONTEXT_TOKENS` and `CLAUDE_CODE_MAX_OUTPUT_TOKENS` from the catalog
  - `CLAUDE_CODE_GATEWAY_HINT_HEADERS=1`
- **opencode on LunaRoute** (their docs, static path): `@ai-sdk/openai-compatible`, `options.baseURL = https://gw.lunaroute.com/v1`, and `apiKey` from `LUNAROUTE_API_KEY`. Their default path is an npm plugin (`@lunaroute/opencode-extension`) with interactive `/connect` login, which Arbitron should not use (§5).

**Arbitron:**
- **Provider vocabulary.** `domain/provider/canonical.go` holds the canonical ids and alias collapse. Every selector dispatches on `Canonicalize`. There is no `lunaroute` entry.
- **Connection validation.** `ValidateConnection` (`domain/provider/connection.go:92`) carries provider-specific rules: the Azure `api-version` and the Bedrock region and key. Other providers pass with no checks. `ValidateConnectionURL` (`domain/provider/url_policy.go:150`) applies the SSRF policy to `BaseURL`.
- **Chat dispatch.** `Factory.ChatProviderFor` (`adapter/provider/factory.go:187`):
  - OpenAI-compatible ids (`openrouter`, `litellm`, `openai`, `ollama`, `ollama-cloud`) go to `adapter/openrouter` (`:207`). It POSTs to `{BaseURL}/chat/completions` with `Authorization: Bearer` (`adapter/openrouter/request.go:87,104`).
  - `anthropic-api` goes to `adapter/anthropicapi`, which POSTs to `{BaseURL}/v1/messages` with `x-api-key` (`adapter/anthropicapi/request.go:109,128`).
  - Either would likely reach LunaRoute today with a hand-built connection, but nothing names it, validates it, or documents the base-URL convention.
- **Agent dispatch is on the backend axis** (`factory.go:258`):
  - **`claude-code`** (`:268`) injects `ANTHROPIC_BASE_URL` and `ANTHROPIC_API_KEY` per call through `procenv.CredentialEnv` (`adapter/claudecli/env.go:35`). Capability env such as the context tier is appended by `agentEnvWithRules` (`:55`). Nothing sets `ANTHROPIC_DEFAULT_HAIKU_MODEL` or the gateway context/output variables. So on a non-Anthropic endpoint, Claude Code's background calls would ask for a Haiku model that LunaRoute does not serve.
  - **`opencode`** (`:282`) uses `providerDescriptors` (`adapter/opencode/providers.go:35`). Each row gives the `-m` prefix, the credential env var, the npm package and whether a config block is always emitted. A custom `BaseURL` becomes `provider.<id>.options.baseURL` after an SSRF re-check (`adapter/opencode/config.go:55-67`). There is no `lunaroute` row, so a `lunaroute` binding fails closed with `ErrUnknownProvider`.
  - **`pi`** (`:313`) uses a deliberately short credential allowlist (`adapter/pi/env.go:58`). The pi adapter does not use `binding.BaseURL`, so a custom endpoint would need a pi custom-provider definition, not just a row.
  - **`codex`** (`:296`) is subscription-only: no key is revealed, and the binding's provider is ignored.
- **Capability rules** (`adapter/config/capabilities.default.yaml`) are keyed by **backend**, not provider. The `claude-code` context tiers (`200k`, `1m`) emit Claude-specific env vars (`emitClaudeContext`, `domain/provider/capability.go:214`), which mean nothing on a GLM model.
- **Model catalog.** Discovery reads one global `CatalogURL` (`domain/modelcatalog/refresh.go:158`, `domain/settings/settings.go:14`) through an unauthenticated fetcher. LunaRoute's `/v1/models` needs the key, so it doesn't fit that path as-is.
- **Known opencode trap.** opencode's model id rules differ per provider, and the opencode adapter reports a failed launch as a bare `exit status 1` (see the ollama-cloud model-id memory). A wrong LunaRoute model prefix will fail the same opaque way.

## 3. The idea

Add **`lunaroute` as a named provider**. It is configured like any other connection (name, base URL, encrypted key) and selected by profiles on the usual chat and agent axes:

```
connection  lunaroute-main   provider=lunaroute   base_url=https://gw.lunaroute.com   key=lr_…
profile     coder-glm53      kind=agent  backend=opencode     connection=lunaroute-main  model=glm-5.3
profile     coder-glm53-cc   kind=agent  backend=claude-code  connection=lunaroute-main  model=glm-5.3
profile     chat-glm53-flash kind=chat                        connection=lunaroute-main  model=glm-5.3-flash
```

Arbitron talks to the hosted gateway directly and builds each harness's config itself, exactly as it already does for OpenRouter, LiteLLM and Azure. **No `@lunaroute/cli`, no extension plugin, no interactive login.** The CLI is a convenience for humans configuring their own shells. Arbitron's per-call env and config injection already does the same job, and keeps the key out of dotfiles.

## 4. Proposal

### 4a. Canonical id and connection rules
- Add `CanonicalLunaRoute = "lunaroute"` to `domain/provider/canonical.go`.
- In `ValidateConnection`, add a LunaRoute rule:
  - **The key must start with `lr_`.** This is a security rule, not cosmetic: the gateway forwards a non-`lr_` key upstream as a BYOK credential. A mis-pasted OpenAI or OpenRouter key would quietly leave LunaRoute for a third party. This must fail at write time.
  - **`BaseURL` defaults to `https://gw.lunaroute.com` when empty**, and must be HTTPS under the normal SSRF policy.
- **Store the base URL as the root, without `/v1`.** Each adapter appends what its dialect needs:
  - chat completions: `/v1/chat/completions`
  - Claude Code: root (it appends `/v1/messages`)
  - opencode: `/v1`

  Keep that normalization in one place and test it, because a doubled or missing `/v1` is the most likely failure.
- Optional `Extras["project-id"]` becomes the `LUNAROUTE-PROJECT-ID` header, capped at 128 chars. One natural use is stamping the Arbitron workflow or instance so LunaRoute's usage view lines up with ours.

### 4b. Chat nodes
- Route `lunaroute` to the existing OpenAI-compatible adapter (`adapter/openrouter`) with the base URL plus `/v1`. Add the project header from Extras, and send no OpenRouter-specific headers.
- Reuse the adapter rather than writing a new one: the request and response contract is identical.
- The Anthropic-dialect route (`adapter/anthropicapi`) stays possible but is not the default.

### 4c. `claude-code` backend
When the binding's provider is `lunaroute`, extend the claude-cli agent env to also emit:
- `ANTHROPIC_DEFAULT_HAIKU_MODEL=<binding model>` (and the small/fast equivalent), so background calls stay on a model LunaRoute serves
- `CLAUDE_CODE_GATEWAY_HINT_HEADERS=1`
- `CLAUDE_CODE_MAX_CONTEXT_TOKENS` and `CLAUDE_CODE_MAX_OUTPUT_TOKENS` from the model's known window, when known (see the §8 question)

Notes:
- `ANTHROPIC_API_KEY` (sent as `x-api-key`) is accepted by LunaRoute, so the existing credential skeleton can stay. Switching to `ANTHROPIC_AUTH_TOKEN` (`Authorization: Bearer`) to match their docs is a choice, not a requirement.
- The Claude-specific context tiers (`200k`, `1m`) must not emit on a LunaRoute binding (§5).

### 4d. `opencode` backend
Add one descriptor row:

```go
provider.CanonicalLunaRoute: {opencodeID: "lunaroute", credEnvVar: "LUNAROUTE_API_KEY",
    npmPackage: "@ai-sdk/openai-compatible", alwaysConfig: true},
```

The config block carries `baseURL = <root>/v1`. Model argv is `-m lunaroute/<catalog id>`, verified against a live run.

### 4e. `pi` backend (stretch)
pi has no notion of a custom base URL in the current adapter. Supporting LunaRoute means emitting a pi custom-provider definition for an OpenAI-compatible endpoint, not just adding a credential row. If pi support is cut, the factory must still **fail closed** on a `lunaroute` + `pi` binding.

### 4f. Probe and backend health
Add a model probe so `connection_create`, `profile_update` and backend health can tell "bad key" from "unknown model" from "gateway down":
- `GET /v1/models` with the key
- `401 INVALID_API_KEY` is distinct from `503 SERVICE_UNAVAILABLE`

Follow the existing `model_probe.go` pattern in `adapter/openrouter` and `adapter/anthropicapi`.

## 5. Design tensions and gotchas

- **Context tiers are keyed by backend, not provider.** `claude-code` + `200k` sets `CLAUDE_CODE_DISABLE_1M_CONTEXT` and the auto-compact window. On GLM those are meaningless at best, and they can conflict with `CLAUDE_CODE_MAX_CONTEXT_TOKENS`. Either validation rejects non-default tiers on a LunaRoute binding, or emission skips them. Rejecting is more honest.
- **Lanes vs Arbitron's queue.** A plan has 1–10 concurrent lanes. Arbitron allows 20 running instances globally and 5 per user, and a single parallel group can launch several agents at once. Requests over the lane count go to overflow (spare capacity, no guarantee) rather than failing, so a burst slows down instead of breaking. But a reviewer fan-out on a 2-lane plan will mostly run in overflow. The 429 codes (`*_busy` + `Retry-After`) need the same retry treatment as other providers.
- **Realtime vs background priority** is a model-id suffix, not a header. Profiles can choose it today by naming `glm-5.3-background`. Deriving it from Arbitron's instance priority is tempting but couples two schedulers; leave it to the profile (§7).
- **Don't use the opencode plugin.** `@lunaroute/opencode-extension` installs at opencode startup, logs in interactively, and syncs models into opencode's own auth store. All three break Arbitron's model: every launch is reproducible, configured per call, and keeps the key out of on-disk stores. Use the static `@ai-sdk/openai-compatible` block.
- **Tool calling is unverified.** Agent backends depend on tool calls through the OpenAI dialect (opencode) and the Messages dialect (claude-code). The docs don't state tool-call support per model. Prove this on a live key before the build relies on it.
- **Claude-shaped aliases.** LunaRoute's `/model` picker shows aliases like `claude-sonnet-5-lr1` that "resolve to real Claude models". Arbitron should use **plain catalog ids only**. An alias would put Claude traffic on a route our cross-family doer/approver rules think is open-weight. Family is the model vendor, so validation should treat these aliases as Anthropic family or reject them.
- **Catalog discovery needs auth.** The global catalog fetch is unauthenticated (`refresh.go:158`). Per-connection discovery from `/v1/models` is a separate, larger change (§7).

## 6. Decisions locked

- **Direct access to the hosted gateway.** No `@lunaroute/cli`, no plugin, no local process.
- **System One is out of this doc.** It belongs to `decision-node.md`.
- **One canonical id, `lunaroute`**, reusing existing adapters (OpenAI-compatible chat, claude-cli env, opencode descriptor). No new LunaRoute adapter package unless a dialect forces one.
- **Reject non-`lr_` keys at write time** (§4a).
- **No new node types, no vendor-specific node kinds.** LunaRoute is reached only through profiles and connections.

## 7. Non-goals (for now)

- **The `codex` backend.** In Arbitron it is subscription-only and ignores the binding's provider (`factory.go:296`). LunaRoute's codex setup needs a named provider and key through `-c` overrides, which is a change to codex's credential model. It's worth doing later, as a separate idea.
- **Per-connection model catalog discovery** from `/v1/models` into the model catalog and dashboard pickers.
- **Mapping Arbitron instance priority to the `-background` model suffix.**
- **BYOK passthrough** (non-`lr_` keys through LunaRoute).
- System One, embeddings, images, audio, MCP and web search.

## 8. Open questions

1. **Tool calls.** Do `glm-5.3` and `deepseek-4.1-flash` support tool calls on `/v1/chat/completions` and `/v1/messages`? This is the gate for both agent backends. Answer it with a live key in the first task.
2. **Context and output limits.** Where does `CLAUDE_CODE_MAX_CONTEXT_TOKENS` come from? Options: a per-model value in the capability YAML, a field on the profile, or a probe of `/v1/models`. Leaning towards the YAML, as `model_overrides` already does per model.
3. **Credential header for claude-code.** Keep `ANTHROPIC_API_KEY` (`x-api-key`, already wired), or switch to `ANTHROPIC_AUTH_TOKEN` (Bearer) as LunaRoute documents? Both are accepted. Keeping it is less change.
4. **Background-model ids in validation.** Should `glm-5.3-background` be a valid model on any LunaRoute profile, or only when a profile opts in?
5. **pi in or out of v1?** It depends on how much custom-provider plumbing pi needs (§4e).

## 9. Suggested sequencing

1. **Live smoke test (throwaway).** With a real `lr_` key, `curl` the chat, messages and models endpoints. Confirm tool-call support, the exact model ids, and the opencode `-m` form. This settles open questions 1 and 4.
2. **Domain.** The canonical id, the `ValidateConnection` rules (the `lr_` prefix, the default base URL), and base-URL normalization helpers, with unit tests including the BYOK-leak case.
3. **Chat.** The `ChatProviderFor` route, the project-id header, and the chat probe.
4. **opencode.** The descriptor row, the config block, argv, and env tests (key redaction, no key in argv or config).
5. **claude-code.** LunaRoute-specific env (Haiku override, gateway hints, context and output tokens), tier suppression or rejection, and env-construction tests.
6. **Profiles and samples.** Add documented example profiles. Make the first real use a ralph-loop doer profile on `glm-5.3` against a small repo, and compare cost and outcome with the current doer.
7. **(Stretch) pi.**

## Appendix — related

- [decision-node.md](decision-node.md): System One (`/v1/systemone`, `kev-4b` / `djev`) is a hosted candidate for the `DecisionProvider` port.
- [fp-next.md](fp-next.md): this doc is the first feature intended to run through the composable `feature-pipeline`.
- LunaRoute docs: [getting started](https://docs.lunaroute.com/getting-started/), [authentication](https://docs.lunaroute.com/getting-started/authentication/), [Claude Code](https://docs.lunaroute.com/getting-started/claude-code/), [OpenCode](https://docs.lunaroute.com/getting-started/opencode/), [harnesses](https://docs.lunaroute.com/mcp/harnesses/), [models](https://docs.lunaroute.com/models/).
