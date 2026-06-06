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

### BEDROCK-002: Untested paths in bedrock provider
**Area**: bedrock chat adapter (`adapter/bedrock/provider.go`)
**Detail**: Two error paths in `provider.go` are not directly tested:
1. `StreamChat` non-2xx status — the goroutine sends `ChatChunk{Error: statusError(...)}` via channel, but no test exercises this path (only `Chat` has the 4xx test).
2. `signedRequest` RevealKey-returns-error — the `fmt.Errorf("bedrock: reveal key: %w", err)` path is unexercised; only the missing-region and missing-access-key-id guards are tested.
Both are fixable with httptest stubs.

## Fixed Previously

- **BEDROCK-parseConverseEvent-coverage** (Area 4 Pass 1): `parseConverseEvent` was missing direct tests for the blank/whitespace-only input (edge) and malformed JSON (error) paths. Added `TestParseConverseEvent_BlankLine` and `TestParseConverseEvent_MalformedJSON` to `response_test.go`.

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Candidates the reviewer should NOT implement (log here if tempted): an 8th provider family; Amazon Bedrock ambient AWS credential-chain auth (spec chose stored keys); exposing connection validation as an MCP tool (CLI-only by design); model-name validation at workflow-author time; auto-refresh/caching of model lists.
-->
