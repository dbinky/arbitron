# Latest Model Profiles — identity source evidence

Provenance for `domain/modelidentity` (P3T1) and `adapter/modelcatalog/testdata/identity-evidence.json`.

- **Primary source:** owner decision D2, `docs/designs/2026-09-29-latest-model-profiles-decisions.md` ("Initial table" and "Worked examples from the 2026-09-30 catalog"), which is itself drawn from the `https://models.dev/api.json` catalog.
- **Fixture rows:** only the exact IDs and orderings the decision records. The tier, version tuple and "newest per (provider, tier)" expectations are the decision's own.
- **Not in the identity fixture:** `release_date` values. The decision records none, so none are invented. P3T2 now separately captures actual `release_date`/`canonical_model_id` values in `adapter/modelcatalog/testdata/models.dev.json`; its `models.dev-provenance.md` records source URLs, hashes, reduction and the complete captured schema. `TestNormalizeCapturedReleaseDateOrdersEqualVersions` feeds the captured dates through normalization and the real mapper/comparator, under an explicit major-only test policy. The older `TestIdentityReleaseDateIsFed` remains a synthetic algorithm test.
- **Catalog retrieval:** the D2 decision records the 2026-09-30 catalog. P3T2's separate live capture was retrieved on 2026-10-16 and does not claim to reproduce those historical bytes.
- **Version documentation retrieved 2026-10-16:** [Claude Code model configuration](https://code.claude.com/docs/en/model-config) (full model IDs, versioned shorthand, `[1m]`, and the exact Bedrock spelling `anthropic.claude-sonnet-4-5-20250929-v1:0`); [OpenAI GPT-4.1](https://developers.openai.com/api/docs/models/gpt-4.1) (explicit snapshot `gpt-4.1-2025-04-14`); [Bedrock inference profiles](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-profiles-support.html) (geographic inference-profile IDs). Retrieved over HTTPS; these are unversioned documentation pages, not binary/probe evidence. No harness binary was invoked to establish usability here.

## Version grammar (`domain/modelidentity/version.go`)

Spelling is never rewritten; the model is returned as given or rejected.

| Route | Accepted | Rejected (`ErrSpecificVersionUnavailable`) |
|---|---|---|
| No connection, Claude backend | `[claude-]{opus,sonnet,haiku,fable}-N[.N\|-N][-YYYYMMDD][[1m]]` (the seeded profile spelling `opus-4.8` is covered) | `sonnet`, `opus`, `latest`, `default`, `claude-sonnet` |
| `claude-cli` | same versioned Claude CLI spelling as the no-connection route | unversioned tier aliases |
| `anthropic`, `anthropic-api` | full `claude-{opus,sonnet,haiku,fable}-N[-N][-YYYYMMDD][[1m]]`, optionally prefixed `anthropic/` | arbitrary numbered aliases and unproved suffixes such as `-fast` |
| `openai` | D2 `gpt-N[.N]-{sol,astra,luna,terra}`; documented explicit snapshot `gpt-4.1-2025-04-14`; optional `openai/` prefix | arbitrary numbered aliases and mutable suffixes |
| No connection, `codex` backend | same fixed OpenAI spellings, without a routing prefix | unproved numeric aliases |
| `deepseek` | D2 `deepseek-vN[.N]-{flash,pro}[:MMDD]`, optionally prefixed `deepseek/` | arbitrary or repeated slash prefixes |
| `openrouter` | bare D2 DeepSeek spelling, or exactly one recognized vendor prefix with that vendor's fixed grammar: `anthropic/claude-{opus,sonnet,haiku,fable}-N[.N\|-N][-YYYYMMDD]`, `openai/` + the OpenAI grammar, `deepseek/` + DeepSeek, `z-ai/` + GLM, `moonshotai/` + Kimi | unknown vendors, repeated prefixes, floating `~vendor/x-latest` aliases, unversioned tiers |
| `zhipuai` | D2 `glm-N[.N][-flash]`, optionally prefixed `zhipuai/` | other families or unproved variants |
| `moonshotai` | D2 `kimi-kN[.N][-code]`, optionally prefixed `moonshotai/` | other families or unproved variants |
| `ollama-cloud` | the D2 DeepSeek/GLM/Kimi spellings above, optionally prefixed `ollama-cloud/` | arbitrary proxy names or multi-slash IDs |
| `bedrock`, `amazon-bedrock` | documented Anthropic `anthropic.claude-{opus,sonnet,haiku}-N[-N][-YYYYMMDD]-vN:M`, optionally prefixed `us.`, `eu.`, `apac.`, `global.` | arbitrary IDs merely ending `-vN:M`, unknown geographic prefixes, application-inference-profile ARNs |
| `azure-openai`, `azure-foundry`, `litellm` | nothing (deployment or proxy aliases; no mapping producer exists) | every model, even one spelled like a fixed native ID |
| Unknown provider, missing provider on a named connection, local `ollama` aliases | nothing | every model; having a digit is not immutable-version evidence |

Every refusal names the route's accepted spelling pattern (`accepted spellings: …`). LiteLLM and local Ollama are retired for pinned workflow execution in this release (decision D13, Option B).

`anthropic-api` is the existing connection-provider alias for the native Anthropic adapter (`domain/provider.Canonicalize`, consumed by `adapter/provider.Factory.ChatProviderFor` and the interactive selector). It uses the same fixed-version grammar as `anthropic`; this routing alias does not add a catalog provider, follow tier or model equivalence.

Canonical identity is a static family table (`families` in `identity.go`), not the editable policy: `lab/<id>` for the D2 families on their listed catalog providers, the source `canonical_model_id` for other models, else `provider/native`.

Identity and tier matching remove at most one routing prefix: the offering's catalog provider itself (including the already-supported `ollama-cloud/` executable spelling), or a recorded lab prefix for a family that catalog provider carries. An unrelated provider/lab prefix, repeated prefix, or a provider merely added to another editable rule cannot authorize stripping. Native and executable spelling remain unchanged. Routing-prefix test variants are explicitly generated from D2 examples; they do not claim a new upstream capture or new model equivalence.

## Fixed-version boundary (2026-10-16)

The generic digit fallback has been removed. D2 establishes the chosen version semantics for its families, including its dated snapshot examples; it does not certify arbitrary suffixes. The fixture's `claude-opus-5-5-fast` remains cataloged/excluded, but no longer claims a proved fixed executable version. A dated snapshot can still be pinned without belonging to a follow tier. Admin regex edits cannot add immutable-version grammars; the version resolver never reads policy or the catalog.

`provider.VersionRequestSource` is the route-only port owned by P3T1; P3T3 supplies actual connection/default/override requests, P1T3/P1T4 consume them, and P3T6 delegates them for prospective selection. Existing root snapshot wiring already feeds the version resolver persisted connection metadata. `TestVersionRejectsNumericAliasesAtTrigger` exercises that real root through a successful HTTP model sink, then verifies an unproved numeric alias or opaque routing provider creates no instance and makes no provider call. It does not certify the pending all-route inventory contract.

P3T2's raw metadata capture and adapter normalization tests are implemented; its historical persistence and root acceptance remain pending P3T5–P3T7. P3T4 owns actual exact-model execution/session evidence; P3T7 owns policy-edit → fresh catalog → durable advancement → new execution integration. No fixture or version grammar alone proves usability.

## Alignment boundary (2026-10-18)

The version grammar above remains evidence for ordinary exact pins; D8 allows native Azure/Bedrock pinned configured-ID execution without treating opaque IDs as immutable. P1T3 persists its route exception in migration 031 and P1T4 enforces its original family at real execution. This is not a new deployment identity mapping or a confirmation source. Production probe/router/refresh/candidate/root implementations now exist; historical “pending” statements above describe the earlier slice only. The Phase 3 current closure map and task follow-through sections own full-source discovery, cold durable bootstrap, capability validation, effective config fingerprints and next-cycle publication recovery. No implementation completion or new external evidence is asserted by this alignment review.

## Announcement operations and root acceptance (2026-10-18)

The webhook remains empty by default. Administrators edit `teams_webhook_url` with `system_settings_update` (or the settings HTTP/dashboard boundary); public reads redact it. Never paste a real destination into fixtures or logs. `newModelRefresh` constructs the durable announcement repository, synchronous Teams sender and announcement service before `domainStack.Start` performs seeding or discovery. Serve and MCP share this assembly and its shutdown join.

Every newly confirmed canonical model can generate a card, not just a newer replacement: older models, other tiers, unknown-tier native IDs and no-profile default harness discoveries are included. Only actual committed profile changes appear; otherwise cards explicitly report no profiles changed. Native Azure/Bedrock paths remain pinned-only under D8 and cannot generate confirmed-usability announcements.

Pending events retain their original payload across restart and failed discovery. Empty-at-discovery events are suppressed permanently; clearing the destination pauses pending work. Each part reads the current raw setting, so reconfiguration redirects only subsequent attempts/parts. A failed attempt retries after four hours, doubling up to 24 hours. A lost response or delivery-marker failure can produce a duplicate (D7); do not rely on receiver deduplication. Confirmed multipart indexes survive restart and are not resent. Operators can inspect `model_announcement_events` state, attempts, next_attempt_at, last_code, part_count and confirmed_parts without exposing the raw destination.

`cmd/arbitron/model_announcement_acceptance_test.go` uses the real root, migrated SQLite, registered admin tools, four-hour ticker and an actual non-deduplicating HTTP receiver; only network/process I/O and the shared clock are substituted. It verifies source-wide discovery, immutable original cards, old/new run execution, retry/backoff, response loss, marker failure, oversized 2xx confirmation, per-part destination changes and multipart union after reopen. Existing root runner/publication/D8 tests add default-harness delivery, recovery and opaque-route refusal sinks. These are synthetic I/O acceptance fixtures, **not** new upstream harness evidence or paid model calls. No harness was installed/upgraded and no real webhook was configured. Full release remains gated by P3T4's unfinished non-Codex inherited tool/MCP/plugin isolation acceptance; fresh P4T3 Go/dashboard/build verification is recorded below and in its task file.

### Restricted Codex receiver acceptance (2026-10-20)

`cmd/arbitron/model_announcement_codex_d10_test.go:TestModelAnnouncementCodexD10DiscoveryRoot` separately requires batch and interactive restricted probes to produce the first receiver card, durable announced payload and reopened exact proof, then executes a fresh pinned run to its batch output or interactive assistant transcript. The other mode always reports fallback, so it cannot hide a missing mode-specific restriction. A hostile inherited MCP/plugin config, actual account auth/cache files and correlated rollout files feed the existing root-built factory/selector/readers; only process and HTTP I/O are substituted. Wrong/missing turn proof, absent cache and rejected restrictions cannot announce, and later real ticks/manual refresh recover after those faults clear.

Independently removing network/plugin/MCP overrides, either sandbox, rollout comparison, raw destination or the final announcer fails the receiver/durable sink. This verifies wiring to D10's already-recorded recipe (`adapter/codex/testdata/probe-restrictions.md`); it is not additional upstream evidence or a live model invocation. Full Go passes both in the worktree and at committed production revision `acefac8e` plus this test alone, independently of inherited P3T6 edits. TypeScript, Vitest (50 files/526 tests) and `make ui` pass with unchanged embedded assets. No real webhook or harness install/update was used. The still-unfinished non-Codex isolation contract prevents full release certification.
