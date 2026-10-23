# Latest Model Profiles — Resolved Provenance Decisions

**Decided by:** the product owner, 2026-10-01.
**Resolves:** all seven release-blocking provenance gates listed in
`2026-09-29-latest-model-profiles-design-phase-3.md` ("Remaining dependency
provenance"). Each decision below is a rule an implementer follows without
asking again. Where a plan task's text conflicts with this record, this record
wins.

---

## D1 — P1-LEGACY: runs already underway at rollout finish unpinned

- Runs created **before** the pin migration have **no** pin record (`NULL`, not an
  empty map). That `NULL` is the legacy discriminator.
- A legacy run keeps **today's behavior**: every model alias resolves live, through
  the profile, for the whole life of the run — including interactive follow-up
  turns, retries and resumes.
- Runs created **after** the migration always get a pin record (possibly an empty
  map, for workflows with no model aliases) and follow the pinned contract: a
  missing or corrupt pin for such a run is an error, never a live fallback.
- No historical recovery is attempted. Nothing drains or blocks the upgrade.
- Accepted consequence: if an admin switches a profile that a legacy run uses to
  "follow latest", that legacy run may move with it. Legacy runs disappear as they
  finish.

## D2 — P3-IDENTITY: a curated pattern table decides tier membership and order

A profile follows a **(catalog provider, tier)** pair. Membership and ordering come
from an Arbitron-shipped, admin-editable table of anchored **full-match** patterns
over the catalog's model ID (after stripping a `provider/` prefix where the catalog
uses one, e.g. OpenRouter's `deepseek/`).

**Ordering:** the captured version is a dotted numeric tuple (`5-5` → 5.5,
`4.1` → 4.1, `k2.7` → 2.7) compared numerically; higher is newer. Equal versions
are ordered by the catalog's `release_date`. A model matching no pattern stays
catalogued and visible but is **never** followable.

**Excluded by construction** (the full-match patterns do not admit them): dated
snapshots (`-20251001`, `-0731`, `:0731`), and variants such as `-fast`, `-exp`,
`-vision-exp`, `-highspeed`, `-preview`, `-turbo`, `flashx`, and vision `…v`
models.

The default Claude patterns bound each numeric version segment to one through
three digits so four/eight-digit dates cannot be mistaken for versions. Larger
segments stay unknown unless an admin explicitly extends the policy.

**Initial table (followable at launch):**

| Family | Catalog providers | Tier | Pattern (full match) |
|---|---|---|---|
| Anthropic | `anthropic` | opus | `claude-opus-(\d{1,3})(?:-(\d{1,3}))?` |
| | | sonnet | `claude-sonnet-(\d{1,3})(?:-(\d{1,3}))?` |
| | | haiku | `claude-haiku-(\d{1,3})(?:-(\d{1,3}))?` |
| | | fable | `claude-fable-(\d{1,3})(?:-(\d{1,3}))?` |
| OpenAI | `openai` | sol | `gpt-(\d+(?:\.\d+)?)-sol` |
| | | astra | `gpt-(\d+(?:\.\d+)?)-astra` |
| | | luna | `gpt-(\d+(?:\.\d+)?)-luna` |
| | | terra | `gpt-(\d+(?:\.\d+)?)-terra` |
| DeepSeek | `deepseek`, `ollama-cloud`, `openrouter` | flash | `deepseek-v(\d+(?:\.\d+)?)-flash` |
| | | pro | `deepseek-v(\d+(?:\.\d+)?)-pro` |
| GLM | `zhipuai`, `ollama-cloud` | glm | `glm-(\d+(?:\.\d+)?)` |
| | | glm-flash | `glm-(\d+(?:\.\d+)?)-flash` |
| Kimi | `moonshotai`, `ollama-cloud` | kimi | `kimi-k(\d+(?:\.\d+)?)` |
| | | kimi-code | `kimi-k(\d+(?:\.\d+)?)-code` |

Worked examples from the 2026-09-30 catalog: `claude-opus-5-5` > `claude-opus-5`
(opus); `gpt-6.1-sol` > `gpt-6-sol` (sol); on `ollama-cloud`, `deepseek-v4.1-flash`
> `deepseek-v4-flash` (flash), `glm-5.3` > `glm-5.2` (glm), and `kimi-k3` is the
newest `kimi` while `kimi-k2.7-code` remains the newest `kimi-code`.
`claude-haiku-4-5-20251001`, `deepseek-v4-flash:0731` and `claude-opus-5-5-fast` are
excluded.

The catalog source remains models.dev (`https://models.dev/api.json`, admin
configurable), as already designed. No LLM classifies tiers.

## D3–D6 — P3-USABILITY: a model is usable on a path only after a real probe

**Probe policy (all harnesses):** probe a model once per execution path (harness ×
connection) when it first appears as a candidate; re-probe a failed or unknown
result at later refreshes; do not re-probe a model already confirmed usable. A probe
is one tiny prompt (e.g. "Reply with exactly: ok"), run through the **same launch
path and flags Arbitron uses for real runs** of that harness, with the requested
model X. Anything other than the success rule below records the model as not
usable on that path, with the reason. Unknown is never treated as usable.

- **D3 — Claude Code (P3-USABILITY-CLAUDE):** run `claude -p --model X
  --output-format json` with **no** `--fallback-model`. Usable only if the call
  succeeds **and** X appears as a key of `modelUsage` (the model actually billed).
  Additional helper models in `modelUsage` are recorded but tolerated; X being
  absent means a fallback happened → not usable.
- **D4 — Codex (P3-USABILITY-CODEX):** probe only models present in Codex's
  account model list (`~/.codex/models_cache.json`, `.models[].slug`). Run the probe
  with `-m X` and Arbitron's normal Codex flags. Usable only if the turn completes
  **and** the session rollout's `turn_context.model` is X.
- **D5 — pi (P3-USABILITY-PI):** run the probe in Arbitron's real launch mode
  (prompt on stdin, `--print --mode json --no-session --model <provider>/X`). Usable
  only if the assistant `message_end` event's `provider` and `model` equal the
  requested ones. pi's own model catalog is **not** required (pi runs uncatalogued
  IDs).
- **D6 — opencode (P3-USABILITY-OPENCODE):** run `opencode run --format json -m
  <provider>/X` with Arbitron's real injected connection config, then
  `opencode export <sessionID>`. Usable only if the assistant message's
  `providerID` and `modelID` equal the requested ones.

These four contracts also satisfy run pin enforcement: a pinned run's harness must
be invoked with the pinned exact model and no fallback flag.

## D7 — P4-DELIVERY: Teams announcements are at-least-once

- A Teams POST is marked delivered on any 2xx. Failures and ambiguous outcomes
  (timeouts, connection resets) are retried with backoff at later refreshes until
  delivered.
- A rare duplicate announcement after an ambiguous send is accepted. No receiver
  deduplication or receipt reconciliation is required.
- The spec outcome "Each newly usable model produces one announcement" is revised
  to "…at least one announcement (a duplicate is possible after an ambiguous
  send)".

## D8 — Azure and Bedrock are pinned-only (decided 2026-10-18)

Neither provider gives Arbitron an evidenced immutable model identity. An Azure
deployment name is opaque and nothing persisted maps it to a model version.
Bedrock's Converse responses carry text and usage, not the model that ran.
Therefore:

- Azure and Bedrock profiles are **pinned-only**. They can never follow a
  (provider, tier); D2 has no rows for them, and none may be added without new
  evidence producers.
- A pinned Azure/Bedrock profile keeps today's behavior: it calls the configured
  deployment or model ID as given. Arbitron makes no exact-version claim for it.
- Exact-model **acceptance** (D3–D6 identity proof) does not apply to Azure or
  Bedrock. Every plan step or test that requires positive exact-model evidence
  for them is satisfied instead by **fail-closed** behavior: creating a
  following profile on, or confirming catalog usability for, an Azure/Bedrock
  path is rejected with a validation error, and a test proves that.
- Copying a request's model field into a response as "proof" remains forbidden.

## D9 — Task order: P3T4 builds the probe; durable acceptance moves downstream (decided 2026-10-18)

The 2026-10-16 run stopped with P3T4, P3T6, P3T7 and P4T3 each waiting on
another: P3T4 withheld its own probe/router because its durable-discovery
acceptance needed P3T6's pipeline, and P3T6 waited on P3T4's probe/router.
That is a planning cycle, not a missing producer. Resolution:

- **P3T4 owns and builds** the tool-disabled `UsabilityProbe` and its per-path
  router now, constructed at the composition root and fed the real adapters.
  P3T4 is complete when they exist and pass P3T4's Happy, Success, Failure and
  Error checks against real adapters and fixture harness output.
- P3T4's **refresh/restart-dependent** acceptance moves out of P3T4: "unchanged
  confirmed evidence survives refresh/restart without re-probe", no-follower
  durable discovery, and batch-versus-interactive durable eligibility move to
  **P3T6** (which consumes the probe through `DiscoveryPipeline`) and their
  end-to-end forms to **P3T7**.
- **Order:** P3T4 probe/router → P3T6 → P3T7 → P4T3. P3T6 never waits on P3T4's
  durable acceptance. P3T7 and P4T3 consume what P3T4 and P3T6 build.
- Every existing `BLOCKED` note that cites the P3T4↔P3T6 dependency, or Azure or
  Bedrock exact-model evidence, is **void**. Implement instead of re-recording it.

## D10 — Codex usability probes use a restricted, not tool-free, request (decided 2026-10-19)

Codex 0.163 has no verified exec switch that removes every tool (see
`adapter/codex/testdata/probe-restrictions.md`): sandbox permissions and tool
registration are independent. A Codex probe therefore counts as acceptable for
D3–D6 when ALL of the following hold, each asserted by a test on the real argv:

- **Read-only sandbox**: `-s read-only` on a fresh probe, or the equivalent `-c`
  sandbox key on any resume form that rejects `-s`.
- **No network**: network access is disabled for the probe.
- **No MCP servers or plugins**: the probe overrides inherited MCP server and plugin
  configuration via `-c`, so user or installation config cannot add tools.
  `--ignore-user-config` may be used too, provided auth still resolves.
- **Identity-only prompt**: the probe asks only for the one-line model identity
  and does not ask the model to act.
- **Evidence unchanged**: model identity is still proven from the Codex rollout
  (D3–D6). Restriction is defense in depth, not the evidence.

The exact flag spellings are the implementer's to verify against the installed
CLI and record in `probe-restrictions.md`, with the help/schema hashes. Do not
upgrade or install a harness to satisfy this. Codex profiles stay followable;
this is not D8's pinned-only rule. The P3T4 BLOCKED note about the tool-free
source contract is void.

## D11 — The branch's red suite is this feature's work (decided 2026-10-19)

The `go test ./...` failures recorded under P3T7 and P4T3 (adapter/config,
adapter/mcp, adapter/provider, cmd/arbitron, domain/provider) reproduce on this
feature's own branch, not on main. They are stale fixtures and proofs left by
earlier passes of THIS feature: announcement fixtures, probe ordering/counts,
dated-candidate rejection, identity prefixes, admin-edit proofs, and
profile-service fixtures missing pin/selection/gate inputs. They are **in scope**:

- A failure that is present on the feature branch but absent on `main` is never
  baseline. Fix it in the code or the fixture, whichever is wrong, with the
  feature's contracts as the reference.
- That work is owned by the new task **P4T4** so it never blocks another task.
  P3T7 and P4T3 consume a green suite rather than waiting on one; their
  full-suite boxes close once P4T4 is done.

## D12 — OpenCode usability probes disable every resolved MCP server (decided 2026-10-21)

OpenCode 1.19.3 has no switch that isolates a run from its configuration. It
deep-merges the global (`$XDG_CONFIG_HOME/opencode`), `$HOME/.opencode`, account,
managed and project layers. It starts every configured MCP server unless that
entry is `enabled:false`, and it concatenates plugin lists. A deny-all permission
block or an empty `mcp` map therefore isolates nothing. In the same spirit as
D10, an OpenCode probe counts as acceptable for D3–D6 when ALL of the following
hold:

- **Resolved, not guessed:** the probe runs `opencode debug config --pure` with
  the probe's own cwd, credential env and inline config, and reads every MCP
  server OpenCode resolves across all layers.
- **Every server disabled:** the probe's `OPENCODE_CONFIG_CONTENT` overlay sets
  `enabled:false` on every server found. OpenCode deep-merges it over the
  inherited definitions.
- **Verified before launch:** a second `debug config` under that overlay must
  show every server with `enabled:false`. An unreadable or oversized resolution,
  a server that stays enabled, or one that appears only under the overlay
  refuses the probe before any run (`ErrProbeIsolationUnverified`).
- **No external plugins:** the run and its export use `--pure`.
- **Deny-all tools and an identity-only prompt**, as before, whatever the
  profile's tools or the allow-all knob say.
- **Evidence unchanged:** model identity is still proven from the correlated
  `opencode export` (D3–D6). The restriction is defense in depth, not the
  evidence.

The probe requires OpenCode >= 1.19.3, the version the recipe was verified on.
Ordinary missions keep their own posture and the 1.17.0 floor. OpenCode stays
followable; this is not D8's pinned-only rule.

**Residual, accepted:** when a config directory contains plugins, OpenCode
installs that directory's plugin dependencies on first use (network). `--pure`
stops the plugins from *loading* but not that install. D12 covers MCP servers
and plugin execution, not package installation.

The P3T4 BLOCKED notes about OpenCode isolation are void. The mechanism and
hashes are in `adapter/opencode/testdata/probe-restrictions.md`, and
`adapter/opencode/testdata/probe-isolation.sh` reproduces them against an
installed binary.

## D13 — LiteLLM and local Ollama are retired for pinned workflow execution (decided 2026-10-22, PR #84 review)

Every run pins a fixed model version at trigger time. A LiteLLM proxy alias or a
local Ollama tag has no evidenced immutable identity, and an unchanged
`connection_ref` freezes neither (the proxy or host can repoint it). Extending
D8's configured-ID continuity to them would weaken the immutable-pin promise and
needs sink-level enforcement. For this release (Option B):

- Workflows whose profiles route through `litellm` or local `ollama` refuse
  triggers with `ErrSpecificVersionUnavailable`. The refusal states that the
  route accepts no spelling.
- OpenRouter accepts recognized `vendor/model` exact IDs (`anthropic/`,
  `openai/`, `deepseek/`, `z-ai/`, `moonshotai/` with that vendor's grammar).
- Every refusal names the route's accepted spelling pattern.
- A boot preflight logs and notifies each persisted fixed-mode profile that
  will refuse triggers, with the reason.

**Deferred (Option A):** labeled, connection-bound configured-ID continuity for
LiteLLM/local Ollama, reusing `OpaqueRoutes`/`OpaqueConnections`, pinned-only and
enforced at the execution sink. It needs explicit owner approval.
