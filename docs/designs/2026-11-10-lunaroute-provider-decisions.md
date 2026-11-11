# LunaRoute provider — dependency decisions LR-01 to LR-08

**Decided:** 2026-11-10, by the product owner in an interactive plan-dependency
review. These decisions close the `DECISION:` gaps the planning gate raised
against `docs/plans/lunaroute-provider/`. They replace every earlier
`UNRESOLVED DEPENDENCY LR-0x` and `ESCALATION LR-0x` marker in the LunaRoute
designs and plans.

Each decision fixes a **contract** that implementers build to now. Live
evidence tasks (P1-T11, P2-T8, P3-T5, P4-T1) **verify** a contract. They do not
create it. A verification task that contradicts a contract follows the
decision's **On failure** rule. It never edits production constants
automatically, and it never claims readiness from a skip, mock or empty
observation.

Evidence-register status vocabulary (P1-T11 `register.yaml`):
`decided` (contract fixed here, live verification pending), `verified` (contract
plus committed live/source evidence plus `applied_to`), `escalated` (a
verification contradicted the contract and a human must decide again). The
release gate accepts only `verified`.

---

## LR-01 — Hosted destination, dialect paths and authentication

**Source:** LunaRoute documentation, checked by the product owner on
2026-11-10: `https://docs.lunaroute.com/api/`, `/api/models/`,
`/getting-started/authentication/`. P1-T11 records this as the source note
`docs/evidence/lunaroute-provider/sources/lr01-gateway-docs.md`.

**Contract:**

- Root `https://gw.lunaroute.com` (`domain/provider/lunaroute_endpoint.go:LunaRouteHostedRoot`).
- `GET /v1/models`, `POST /v1/chat/completions`, `POST /v1/messages`,
  `POST /v1/responses`. The bare `/responses` alias is not used.
- Keys start with `lr_`. The gateway accepts them in `LUNAROUTE-API-KEY`
  (vendor-preferred), `Authorization: Bearer` or `x-api-key`. A value without
  the `lr_` prefix in `Authorization` or `x-api-key` is passed upstream as a
  bring-your-own provider key. Rejecting non-`lr_` keys at write time (P1-T3)
  and at reveal (P1-T5) is therefore a security requirement.
- **Arbitron sends exactly one auth header on every sink: `Authorization: Bearer lr_…`.**
  - Probe (P2-T1) and chat (P2-T4): Bearer.
  - Claude Code (P3-T1): `ANTHROPIC_AUTH_TOKEN` (Claude Code sends Bearer).
    `ANTHROPIC_API_KEY` is stripped from the child env and never set for a
    LunaRoute binding.
  - OpenCode (P3-T2), Codex (P4-T2) and Pi (P4-T4, `authHeader: true`): Bearer.
  - No sink sends `x-api-key` or `LUNAROUTE-API-KEY` for LunaRoute.
- Optional header `LUNAROUTE-PROJECT-ID`: free-form, at most 128 characters.
  It remains **not sent** in this release (spec non-goal: usage attribution).

**Still unverified (owned by live tasks, not by this decision):** tool-call
support per model (LR-02 completion evidence) and whether `/v1/models` works
without a key (irrelevant, because Arbitron always sends the key).

**On failure:** if P1-T11's live gateway test shows a path or Bearer auth
rejected on any dialect, release mode fails, LR-01 becomes `escalated`, and
constants stay unchanged until a human decides again.

**Register:** LR-01 `verified` once the source note exists and P1-T1/P2-T1/
P2-T4/P3-T1/P3-T2/P4-T2/P4-T4 apply it (`applied_to`). The P1-T11 live
gateway test is a regression check run in release mode.

## LR-02 — Per-model capability facts and the release model set

**Release model set:**

| Model id | Role in release acceptance |
|---|---|
| `glm-5.3` | Open-weight agent model, candidate for all four backends |
| `glm-5.3-flash` | Open-weight chat model |
| `deepseek-4.1-flash-1017` | Open-weight model (DeepSeek v4.1 Flash, 1017 snapshot), candidate for chat and all four backends |
| `claude-sonnet-5-lr1` | Proprietary alias selection (see LR-03) |

The exact spelling of `deepseek-4.1-flash-1017` and `claude-sonnet-5-lr1` must
be confirmed against LunaRoute's model pages in P1-T11 Step 1. If LunaRoute
spells either differently, every literal in the plans and fixtures is replaced
together. The rule does not change.

**Contract:**

- **Static facts** (context limit, output limit, reasoning support, efforts,
  `temperature`/`output_schema` support) come **only** from LunaRoute's model
  documentation pages (`https://docs.lunaroute.com/models/` and per-model
  pages). P1-T11 Step 1 records them in
  `docs/evidence/lunaroute-provider/model-capabilities.md` with URL and access
  date. P1-T8 writes them into the embedded
  `adapter/config/capabilities.default.yaml:lunaroute_models`, with each fact's
  `evidence` pointing at that note. The embedded section is **not empty** in
  the release: it carries the release set.
- **Task/backend support** (`tasks.*`, `backends.*`, including tool calling) is
  written as `supported` **only** from completed live runs: chat from P2-T8,
  claude-code/opencode from P3-T5, codex/pi from P4-T1. Until then it stays
  `unknown`.
- No placeholder or "conservative" values. A static fact the docs do not
  publish stays `unknown`.

**On failure:** a release model whose docs omit a limit or reasoning fact
cannot be used for claude-code, opencode or pi (each refuses unknown limits;
pi also refuses unknown reasoning). It stays usable for chat. If no release
model has the facts a backend requires, that backend's readiness is blocked and
LR-02 becomes `escalated`. Placeholders are never substituted.

## LR-03 — Fixed executable ids and alias handling under pinning

**Decision (option A — D8-style reviewed-alias exception):**

- `domain/modelidentity/version.go:lunaRouteReviewedIDs` ships these production
  entries (vendor / fixed target / evidence):
  - `glm-5.3` → `zhipuai` / `glm-5.3` / `model-identities.md#glm-5.3`
  - `glm-5.3-flash` → `zhipuai` / `glm-5.3-flash` / `model-identities.md#glm-5.3-flash`
  - `deepseek-4.1-flash-1017` → `deepseek` / `deepseek-4.1-flash-1017` / `model-identities.md#deepseek-4.1-flash-1017`
  - `claude-sonnet-5-lr1` → `anthropic` / `alias:claude-sonnet-5-lr1` / `model-identities.md#claude-sonnet-5-lr1`

  Vendor labels equal the lab names in `domain/modelidentity/identity.go:families`
  (`zhipuai` for `glm-`, `deepseek` for `deepseek-`, `anthropic` for `claude-`).
  `TestLunaRouteProductionIdentityEvidence` asserts that they agree.
- **Open-weight ids** are fixed executable ids. Pinning works as for any fixed id.
- **Reviewed aliases** (fixed target `alias:<id>`) are a pinned-only exception
  in the style of D8 (`docs/designs/2026-09-29-latest-model-profiles-decisions.md`):
  - Arbitron keeps configured-ID continuity and makes **no exact-version claim**.
  - The vendor family comes from the reviewed table (`anthropic`), never from
    listing membership. Doer/approver cross-family rules treat the alias as
    Anthropic, never as open-weight.
  - Execution evidence for an alias run must report a model that
    `ClassifyFamily` puts in the reviewed vendor's family. A different or
    unresolved family fails the run with an execution-evidence error. Version
    drift inside that vendor is allowed and recorded in the run's evidence.
  - An unreviewed alias (`claude-sonnet-5-lr999`, any unlisted `-lrN`) is
    refused at save, pin and launch with `ErrSpecificVersionUnavailable`.
- `domain/modelidentity/family.go:reviewedAliasVendors["lunaroute"]` ships
  `{"claude-sonnet-5-lr1": "anthropic"}`. Under P1-T6's alias boundary, a
  `-lrN` gateway alias classifies only through this reviewed table, so an
  unreviewed `claude-…-lrN` stays `unresolved`. Open-weight release ids
  classify through the prefix rules (`glm-` → `zhipuai`, `deepseek-` →
  `deepseek`) and need no entry.

**On failure:** if P1-T11's identity note cannot confirm a release id's
spelling or vendor, that id is removed from both tables and from the release
set (with the same change made in every plan literal). If no reviewed
proprietary alias remains, LR-03 becomes `escalated`.

## LR-04 — Listing envelope and gateway error codes

**Decision (option B — documented codes):**

- Listing: parse `GET /v1/models` as `{"data":[{"id":…}, …]}` and read only
  `data[].id`. The listing request sends Bearer auth and **neither**
  `anthropic-version` **nor** `X-LunaRoute-Client`. The P2-T8 live run must
  confirm the `data` wrapper. A listing without it is a decode failure, which
  maps to `ErrLunaRouteCauseUnknown`.
- Error body: accept the error object both bare (`{"error":{"code":…}}` /
  top-level `code`) and wrapped (`{"type":"error","error":{…}}`). Read only
  `error.code` (and `error.type` for observations). Never surface messages.
- `domain/provider/lunaroute_classify.go:lunaRouteVerifiedCodes` ships with
  exactly these documented codes, for **every** operation (listing, chat,
  agent):

  | Code | Outcome |
  |---|---|
  | `INVALID_API_KEY` | `ErrLunaRouteCredentialsRejected` |
  | `CONCURRENT_REQUEST_LIMIT_EXCEEDED` | `ErrLunaRouteBusy` |
  | `INFERENCE_UNAVAILABLE`, `NO_HEALTHY_PROVIDER`, `PROVIDER_KEY_UNAVAILABLE`, `BYOK_SPEND_GATE_UNAVAILABLE` | `ErrLunaRouteServiceFailure` |
  | `BYOK_SPEND_CAP_REACHED`, `MODEL_UNPRICED` | **new** `ErrLunaRouteRequestRefused` (non-retryable) |

- A recognised body code overrides the status mapping. In particular, a 429
  carrying `BYOK_SPEND_CAP_REACHED` or `MODEL_UNPRICED` is
  `ErrLunaRouteRequestRefused`, never busy, and is not retried.
- Do **not** add `MODEL_NOT_FOUND` or any `*_busy` code. Model availability is
  decided only by the `/v1/models` re-list check (P2-T5 diagnosis). Any other
  code falls back to the status mapping (401 → credentials, 429 → busy with
  `Retry-After`, 5xx → service failure, else cause unknown).
- New sentinel `ErrLunaRouteRequestRefused`: "LunaRoute refused this request
  for account or pricing reasons (spend cap reached or model not priced); it
  will not succeed on retry". It is a member of `LunaRouteGatewaySentinels()`.

**Source:** LunaRoute API error documentation (product owner, 2026-11-10).
P1-T11 records it as `docs/evidence/lunaroute-provider/gateway-contract.md`.

**On failure:** if P2-T8 observes a different envelope or code spelling, that
code is removed from the table (status mapping still applies) and LR-04
becomes `escalated`. Codes are refinements, so readiness does not otherwise
depend on them.

## LR-05 — Claude Code controls, precedence and helper routing

**Decision (option A — settings isolation for LunaRoute launches):**

- Env contract for a LunaRoute binding (`adapter/claudecli/lunaroute_env.go`):
  `ANTHROPIC_BASE_URL=<root>`, `ANTHROPIC_AUTH_TOKEN=<lr_ key>` (LR-01;
  `ANTHROPIC_API_KEY` stripped), all of `lunaRouteModelSelectorVars`
  (`ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_HAIKU_MODEL`,
  `ANTHROPIC_SMALL_FAST_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`,
  `ANTHROPIC_DEFAULT_OPUS_MODEL`, `CLAUDE_CODE_SUBAGENT_MODEL`) set to the
  binding model, `CLAUDE_CODE_GATEWAY_HINT_HEADERS=1`,
  `CLAUDE_CODE_MAX_CONTEXT_TOKENS`/`CLAUDE_CODE_MAX_OUTPUT_TOKENS` from LR-02.
- **Precedence:** every LunaRoute claude-code launch (`RunAgent`,
  `StreamAgentAdapter`, `ProbeExecutedModel`) passes `--setting-sources ""`,
  the same mechanism `adapter/claudecli/model_probe.go` already uses, so no
  user, project or local `settings.json` `env`/`model` block can override the
  env contract. Repo `CLAUDE.md` memory still loads. Non-LunaRoute launches
  are unchanged.
- Verified-on version: Claude Code 2.1.322 (installed 2026-11-10). P3-T5
  records the version it ran. There is no runtime version gate.

**Verification (P3-T5):** with conflicting user and project settings seeded
(another base URL, model and env), observe the main request and at least one
helper request (title/summary or subagent) on `gw.lunaroute.com/v1/messages`
with Bearer `lr_` auth and the binding model. Confirm `CLAUDE.md` still
loaded. Confirm user files are byte-identical.

**On failure:** a selector the binary ignores is removed from
`lunaRouteModelSelectorVars`. If any reachable helper category cannot be
routed to the binding model on LunaRoute, claude-code LunaRoute enablement
(P3-T3) stops, LR-05 becomes `escalated`, and the domain rule keeps
refusing claude-code + LunaRoute.

## LR-06 — OpenCode model metadata, helper routing and precedence

**Decision (option A — full restricted overlay):** The precedence mechanism is
the existing D12-verified per-run inline `OPENCODE_CONFIG_CONTENT` overlay,
which deep-merges over every inherited layer
(`adapter/opencode/testdata/probe-restrictions.md`). For a LunaRoute binding,
`adapter/opencode/config.go:buildConfig` emits:

- `provider.lunaroute = {npm: "@ai-sdk/openai-compatible", options: {baseURL: "<root>/v1", apiKey: "{env:LUNAROUTE_API_KEY}"}, models: {"<id>": {name: "<id>", tool_call: true, limit: {context: <LR-02>, output: <LR-02>}}}}`.
  OpenCode sends the key as Bearer (LR-01).
- `model: "lunaroute/<id>"` and `small_model: "lunaroute/<id>"`.
- `enabled_providers: ["lunaroute"]`.
- The run (and any export or `debug config` it performs) uses `--pure`, so
  no external plugin loads, including `@lunaroute/opencode-extension`.
- **Unknown context or output limit → refuse before reveal**, at save
  (domain rule, P1-T9 `lunaroute_validate.go`) and at launch, with the same
  rule as claude-code.
- Verified-on version: opencode 1.19.5 (installed 2026-11-10).

**Verification (P3-T5):** with conflicting global and project opencode config
seeded (another provider, `model`, `small_model` and plugin), `opencode debug config --pure`
under the overlay must report the contract values, and a title or summary
request must reach the gateway with the binding model and Bearer auth.

**On failure:** a key opencode ignores is dropped from the contract (if
`enabled_providers` is ignored, `model`/`small_model`/`-m` routing alone must
still pass verification). If `small_model` routing cannot be proven, opencode
LunaRoute enablement (P3-T3) stops and LR-06 becomes `escalated`.

## LR-07 — Codex Responses overrides, auth independence and helper routing

**Decision (option A — candidate contract plus explicit helper pinning):**

- Per-launch private `CODEX_HOME` (`os.MkdirTemp`, 0700, no `auth.json`, no
  `config.toml`), removed on every exit path **after** the pinned rollout check
  reads it. Operator `CODEX_HOME`, `OPENAI_API_KEY` and `OPENAI_BASE_URL` are
  not forwarded.
- Env: `procenv.BaseEnv()` + `LUNAROUTE_API_KEY=<lr_ key>` + `CODEX_HOME=<private>`.
- Argv after `-m <id>`: `-c model_provider="lunaroute"`,
  `-c model_providers.lunaroute.name="LunaRoute"`,
  `-c model_providers.lunaroute.base_url="<root>/v1"`,
  `-c model_providers.lunaroute.env_key="LUNAROUTE_API_KEY"` (Codex sends it
  as Bearer), `-c model_providers.lunaroute.wire_api="responses"`,
  `-c review_model="<id>"`, then the optional exact effort override.
  Compaction uses the main model.
- A workspace `.codex/config.toml` does not apply: codex loads project config
  only for trusted projects, and the private home has no trust entries.
- Verified-on version: codex-cli 0.167.0 (installed 2026-11-10).

**Verification (P4-T1):** with a conflicting `~/.codex/auth.json`,
`~/.codex/config.toml` and project `.codex/config.toml` seeded, complete a tool
task. Every request must go to `gw.lunaroute.com/v1/responses` with Bearer
`lr_` auth and the binding model. `~/.codex` must not be read, and user files
must stay byte-identical.

**On failure:** an override the binary ignores is dropped. If auth requires a
login, or any request leaves the connection or model, P4-T3 enablement stops,
LR-07 becomes `escalated`, and codex + LunaRoute stays refused (the phase-1
domain rule).

## LR-08 — Pi provider schema, model metadata and relocation

**Local source facts (pi 1.5.1, installed docs,
`@earendil-works/pi-coding-agent/docs/configuration.md` and
`models-configuration.md`, read 2026-11-10):**

- `PI_CODING_AGENT_DIR` relocates the entire agent directory (`models.json`,
  `auth.json`, `settings.json`, extensions, `mcp.json`). Relocation **is
  supported**, so the earlier "pi outcome BLOCKED" escalation is void.
- Project `.pi/` configuration loads only after project trust, which is
  recorded in the agent directory. A fresh private directory is untrusted, so
  project settings, extensions and MCP do not load. `AGENTS.md`/`CLAUDE.md`
  context files still load.
- `models.json` `apiKey` is a literal unless written `"$NAME"`. The earlier
  candidate `"apiKey": "LUNAROUTE_API_KEY"` was a defect: it would send that
  literal string as the key.
- Compaction summaries use the active model unless an extension overrides
  them. No extensions load in the private, untrusted setup.

**Decision (option A — corrected contract):**

- Per-launch private `PI_CODING_AGENT_DIR` (0700) containing only
  `models.json` (0600), removed on every exit path.
- `models.json`:
  `{"providers":{"lunaroute":{"baseUrl":"<root>/v1","api":"openai-completions","apiKey":"$LUNAROUTE_API_KEY","authHeader":true,"models":[{"id":"<id>","name":"<id>","reasoning":<LR-02>,"input":["text"],"contextWindow":<LR-02>,"maxTokens":<LR-02>}]}}}`.
- `reasoning`, `contextWindow` and `maxTokens` come only from LR-02. Unknown →
  refuse before reveal (pi's own defaults of `false`/128000/16384 are never
  relied on).
- Env: `procenv.BaseEnv()` + `LUNAROUTE_API_KEY` + `PI_CODING_AGENT_DIR`.
  Argv `--model lunaroute/<id>`.

**Verification (P4-T1):** with conflicting `~/.pi/agent` files and a project
`.pi/settings.json` seeded, complete a tool task. Every request must go to
`gw.lunaroute.com/v1/chat/completions` with Bearer `lr_` auth and the binding
model, and user files must stay byte-identical.

**On failure:** if relocation or the schema does not behave as documented,
pi + LunaRoute stays refused, `~/.pi` is never written, and LR-08 becomes
`escalated` for a new human decision. Pi is never dropped from scope.
