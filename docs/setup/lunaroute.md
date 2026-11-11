# LunaRoute provider

LunaRoute is a hosted gateway used through ordinary Arbitron connections and
profiles: no new node types, LunaRoute CLI, plugin or separate login. Every
LunaRoute call uses `https://gw.lunaroute.com` and the selected connection's
`lr_` key, sent as `Authorization: Bearer`.

## 1. Create the connection (MCP `connection_create`, admin)

```json
{"name": "lunaroute-main", "provider": "lunaroute", "base_url": "", "api_key": "lr_…"}
```

- Keys must start with `lr_`. Other providers' credentials are rejected, never
  forwarded as upstream bring-your-own keys.
- `base_url` may be empty, `https://gw.lunaroute.com`, or
  `https://gw.lunaroute.com/v1` (also with a trailing slash); it is stored as
  the hosted root. Other destinations are rejected.
- Switching an existing connection to `lunaroute` requires a new `api_key`.
- Keys are encrypted at rest. Never put a real key in workflow JSON, examples,
  logs or evidence artifacts.

## 2. Inspect models (MCP `connection_models`)

Call `connection_models` with `{"name": "lunaroute-main"}`. It lists currently
available ids, vendor family, task/backend facts, context/output limits and
effort support. An `unknown` field is not a guarantee either way. Listing
membership alone proves neither executable identity nor task/backend support.
The author explicitly selects the model and backend; Arbitron makes no
recommendations and substitutes nothing.

## 3. Create profiles (MCP `profile_create`, admin)

These are declarative examples, not shell commands. For MCP, use `alias` for
the profile name and `connection_ref` for `connection`; the other profile
fields retain their shown names. Omitted `selection_mode` means pinned.

```text
connection  lunaroute-main   provider=lunaroute base_url=https://gw.lunaroute.com api_key=lr_…
profile     chat-glm53-flash kind=chat connection=lunaroute-main model=glm-5.3-flash selection_mode=pinned
profile     coder-glm53-oc   kind=agent backend=opencode connection=lunaroute-main model=glm-5.3
profile     coder-glm53-cc   kind=agent backend=claude-code connection=lunaroute-main model=glm-5.3 context_window=default effort=default
profile     coder-ds41-oc    kind=agent backend=opencode connection=lunaroute-main model=deepseek-4.1-flash
```

Agent examples are conditional on the configured capability table:
**claude-code and opencode need known context and output limits** for the
selected model. An empty or incomplete override makes these examples fail at
save and launch, even though the executable spelling is reviewed. Known
unsupported task/backend facts also refuse a selection; unknown support is
not proof of a completed live task.

Rules enforced at save and before launch:

- LunaRoute profiles are **pinned-only**; `selection_mode: following` is
  refused. Agent profiles must name their backend explicitly; deployment
  defaults never choose it. Chat profiles have no backend.
- The reviewed executable set is `glm-5.3`, `glm-5.3-flash` and
  `deepseek-4.1-flash`. Selections retain that exact spelling. A numeric id,
  uppercase spelling, provider prefix or listing membership does not authorize
  another id. `latest` is refused. `-background` is never appended; such an id
  would need a separate review and explicit selection.
- `deepseek-4.1-flash-1017` is an upstream weights name, not an executable id.
  **claude-*-lr* aliases are refused and never classified as Anthropic**;
  this includes the revoked `claude-sonnet-5-lr1`. There is no authorized
  proprietary alias in the current reviewed set.
- Vendor-family safeguards still apply: `glm-*` is ZhipuAI, `deepseek-*` is
  DeepSeek. An unresolved family cannot bypass doer/judge separation.
- `context_window` must be omitted or `default`. Claude-native `200k`/`1m`
  tiers are not LunaRoute limits.
- Non-default `effort` needs per-model evidence for the selected chat/backend
  axis and an exact emission path. OpenCode has no non-default effort emission.
  Unsupported settings fail rather than being dropped or rounded.
- Enabled agent backends are `claude-code` and `opencode`;
  **codex and pi are not yet enabled** for LunaRoute.

Live backend/helper verification and whole-release readiness remain pending.
Offline enablement and accepted profile examples do not claim live completion;
see the [evidence register](../evidence/lunaroute-provider/README.md).

## 4. Capability facts (operator)

The shipped static facts for the three reviewed ids come from the
operator-reviewed gateway snapshot
[`sources/lr02-live-models-2026-11-11.json`](../evidence/lunaroute-provider/sources/lr02-live-models-2026-11-11.json),
as authorized by [ruling R2](../designs/2026-11-10-lunaroute-provider-decisions.md#operator-rulings--2026-11-11).
See [model-capabilities.md](../evidence/lunaroute-provider/model-capabilities.md)
for limits, independent reasoning metadata, supported effort mappings and
explicit unknowns. Static protocol/tool facts do not establish completed agent
work. Task/backend support is populated only for the model and path proven by
a completed live run; no success is generalized to another backend or model.
Temperature and unproven effort axes remain unknown.

Operators can override facts through `capabilitiespath` (default
`~/.arbitron/capabilities.yaml`). This is a YAML **shape**, not populated values:

```yaml
lunaroute_models:
  glm-5.3:
    context_tokens: {tokens: <from evidence>, evidence: "docs/evidence/lunaroute-provider/<file>#<anchor>"}
    output_tokens: {tokens: <from evidence>, evidence: "docs/evidence/lunaroute-provider/<file>#<anchor>"}
    backends:
      opencode: {support: supported, evidence: "<completed live observation>"}
```

Populate only evidence-backed facts; do not copy placeholders as values.
An override replaces the whole model entry, not individual facts. Omitted
facts are `unknown`, so retain every required limit and independently reviewed
fact. A short successful run never establishes maximum limits. Check the
actually deployed defaults/overrides before selecting a profile.

## 5. What errors mean

| Message starts with | Meaning / action |
|---|---|
| `LunaRoute rejected the credential` | Update the connection with a valid `lr_` key. |
| `the selected model is not available` | Re-run `connection_models` to inspect current availability; nothing is substituted. |
| `LunaRoute is busy` | All lanes are busy; retry later, respecting any retry guidance. |
| `LunaRoute service failure` | Gateway/transport failure; retry later. |
| `LunaRoute refused this request for account or pricing reasons` | Spend cap reached (`BYOK_SPEND_CAP_REACHED`) or model unpriced (`MODEL_UNPRICED`); retrying does not help. Resolve the account/pricing issue. |
| `LunaRoute request failed and the cause is unknown` | The original cause is uncertain. A later availability observation on an agent failure does not prove the earlier cause. |
| `… not yet supported` / `… needs known context and output limits` / `… use a reviewed fixed LunaRoute id` | Refused locally before inference. Correct the explicit selection or provide the required evidence-backed facts. |

Nothing falls back to another model, backend or provider. For a live chat smoke
call that needs visible content, allow at least **1024 output tokens**: these
reasoning models may spend a smaller budget entirely on reasoning. This is the
operator-observed smoke-test floor, not a new model limit or an automatic
request rewrite.
