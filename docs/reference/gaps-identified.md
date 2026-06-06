# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

### OPENCODE-001: Modern-opencode contract details are unverified placeholders
**Area**: opencode args + version (`adapter/opencode/args.go`, `adapter/opencode/version.go`)
**Detail**: The flag spellings `permissionAllowAllFlag="--allow-all-tools"`, `toolScopeFlag="--allowed-tools"`, `runSubcommand="run"`, and `minOpencodeVersion="0.4.0"` are placeholders chosen from documentation, not confirmed against an installed modern opencode binary. They are deliberately localized so a correction is a one-line edit each. Verify against `opencode run --help` and `opencode --version`, correct the consts, and update the unit tests' expected argv accordingly. Until confirmed, agent runs against a real binary may use the wrong flags.

### OPENCODE-002: Agent-path multi-provider routing only fake-tested
**Area**: opencode env + run (`adapter/opencode/{env,provider}.go`)
**Detail**: The `opencode run -m <provider>/<model>` argv + per-provider auth-env contract is asserted hermetically via a fake `CommandRunner` only. No live smoke test exercises a real opencode binary per provider (anthropic/openai/openrouter/ollama/amazon-bedrock/azure). Once OPENCODE-001 is confirmed, add a documented manual/integration smoke check (kept out of `make test`) or a build-tagged live test, and note any provider the installed binary cannot actually drive (fail-closed path).

### BEDROCK-001: Converse + ListFoundationModels shapes not verified against live AWS
**Area**: bedrock adapter + probe (`adapter/bedrock/{request,response}.go`, `adapter/probe/bedrock.go`)
**Detail**: The Converse request/response body and the `ListFoundationModels` control-plane shape are validated only against fixture servers. Confirm against live AWS: the `/model/{id}/converse` (and `/converse-stream`) runtime host vs the `bedrock.{region}.amazonaws.com/foundation-models` control-plane host, the `inferenceConfig`/`usage` field names, and the streaming event framing. Adjust fixtures and parsing if reality differs.

### ANTHROPIC-001: Forced-tool structured output + SSE not verified against live API
**Area**: anthropicapi adapter (`adapter/anthropicapi/{request,response}.go`)
**Detail**: The structured-output mapping (single forced tool via `tool_choice`, returning the tool-use `input` as a raw pre-parse string) and the SSE event handling (`content_block_delta` text_delta/input_json_delta, usage split across `message_start`/`message_delta`, `message_stop`) are tested only against fixtures. Confirm against the live Anthropic `/v1/messages` API, including the pinned `anthropic-version: 2023-06-01`, and reconcile any divergence.

## Fixed Previously

- **BEDROCK-parseConverseEvent-coverage** (Area 4 Pass 1): `parseConverseEvent` was missing direct tests for the blank/whitespace-only input (edge) and malformed JSON (error) paths. Added `TestParseConverseEvent_BlankLine` and `TestParseConverseEvent_MalformedJSON` to `response_test.go`.
- **BEDROCK-002** (Area 4 Pass 1): Two untested error paths in `provider.go` — `StreamChat` non-2xx status (error delivered via channel) and `signedRequest` RevealKey-returns-error. Fixed by adding `TestStreamChat_NonOKStatusDeliveredViaChannel` and `TestChat_RevealKeyError` to `provider_test.go`.
- **BEDROCK-scrubControl** (Area 4 Pass 2): `scrubControl` security function had no direct unit test. Added `TestScrubControl` to `provider_test.go` with 13 cases covering empty, printable ASCII, all control-char variants (0x00–0x1F, 0x7F), and valid UTF-8 multibyte preservation.
- **FACTORY-MaxResponseBytes-threading** (Area 5 Pass 1): `TestFactory_ProviderFor_ThreadsMaxResponseBytes` covered only 3 of 5 credential-bearing backends (openrouter, azurefoundry, opencode) and omitted `anthropic-api` and `bedrock`. Extended the test to include all five and updated the comment accordingly.
- **FACTORY-ErrUnknownProvider-quoting** (Area 5 Pass 2): `ErrUnknownProvider` uses `%q` to quote the provider string, preventing control-char injection into logs. This property was untested. Added `TestFactory_ChatProviderFor_UnknownProvider_ControlCharsSafelyQuoted` to verify a `\n`-containing provider string is escaped rather than emitted raw.
- **OPENCODE-ToolScope-precedence** (Area 6 Pass 1): `mission.ToolScope` wins over `posture.AllowedTools` when both are non-empty, but no test covered the case where both were simultaneously non-empty. Added `TestBuildArgs_MissionToolScopeWinsOverProfileAllowlist` to `args_test.go` — verifies a 3-tool mission scope beats a 1-tool profile allowlist.
- **OPENCODE-buildArgs-quoting** (Area 6 Pass 2): `buildArgs` error path (`args.go:52`) uses `%q` to quote the provider string but this was untested. Added `TestBuildArgs_UnknownProviderControlCharsSafelyQuoted` to verify a `\n`-containing provider string is escaped in the error message (same property verified for the factory in Area 5).
- **OPENCODE-env-error-paths** (Area 7 Pass 1): Three error-path guards in `env.go` were untested: (1) `bedrockEnv` nil RevealKey (line 107–109), (2) keyless ollama with empty BaseURL (line 59–62), (3) non-keyless provider whose RevealKey returns `("", nil)` (line 79–81). Added `TestOpencodeEnv_BedrockNilRevealKey`, `TestOpencodeEnv_KeylessOllamaEmptyBaseURL`, and `TestOpencodeEnv_EmptyKeyFromRevealRejectsNonKeyless`.
- **OPENCODE-env-quoting** (Area 7 Pass 2): `opencodeEnv` error path (`env.go:39`) uses `%q` to quote the provider string — same property verified for `buildArgs` (Area 6 Pass 2). Added `TestOpencodeEnv_UnknownProviderControlCharsSafelyQuoted`.
- **PROBE-anthropic-coverage** (Area 8 Pass 1): `anthropic_test.go` was missing 5 paths compared with the openai_compat coverage: RevealKey error, transport → `ErrUnreachable`, 5xx → `ErrUpstreamStatus`, empty base URL uses default endpoint, and bounded response. Added corresponding tests.
- **PROBE-factory-quoting + scrubControl** (Area 8 Pass 2): `factory.go:83` uses `%q` on the provider string in `ErrNoProbeForProvider` — untested with control chars. Added `TestProbeFactory_ProbeFor_UnknownProvider_ControlCharsSafelyQuoted`. Also added `TestProbeScrubControl` direct unit test for `httpshared.go`'s `scrubControl` (mirrors bedrock/openrouter pattern).
- **DOMAIN-probe-failure-path** (Area 9 Pass 1): `probe_test.go` had no test for `ConnectionProbe.Probe` returning an error (failure category empty). Added `TestConnectionProbe_ProbeError` using `fakeProbe{err: ...}`.
- **CLI-connection-ErrUpstreamStatus** (Area 10 Pass 1): `connection_test.go` covered `ErrCredentialsRejected`, `ErrUnreachable`, and `ErrRegionConfig` probe errors but not `ErrUpstreamStatus` (4xx/5xx upstream). Added `TestRunConnectionValidate_UpstreamStatus`.
- **CLI-connection-RevealKey-error** (Area 10 Pass 2): `bindingFromConnection` creates a `RevealKey` closure but no test verified that `store.Reveal` errors propagate without leaking the stored secret. Added `TestBindingFromConnection_RevealKeyError_SecretFree`.
- **DOMAIN-bedrock-amazon-bedrock-alias** (Area 11 Pass 1): `ValidateConnection` used `c.Provider == "bedrock"` (exact match) and skipped the region+access-key-id requirement for `"amazon-bedrock"` (the canonical alias used in adapter maps). Fixed by introducing a `bedrockProviders` set (mirrors `azureProviders` pattern) and changing the check to `bedrockProviders[c.Provider]`. Added `TestValidateConnection_AmazonBedrockAlias_AlsoRequiresCredentials`.
- **DOMAIN-allowany-whitespace-url** (Area 11 Pass 2): `TestAllowAnyURLPolicy_RejectsEmpty` only tested `""` but not whitespace-only strings (`"   "`, `"\t"`). Extended to cover all empty-after-trim variants.

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Candidates the reviewer should NOT implement (log here if tempted): an 8th provider family; Amazon Bedrock ambient AWS credential-chain auth (spec chose stored keys); exposing connection validation as an MCP tool (CLI-only by design); model-name validation at workflow-author time; auto-refresh/caching of model lists.
-->
