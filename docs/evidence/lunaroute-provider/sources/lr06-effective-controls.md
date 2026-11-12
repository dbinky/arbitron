# LR-06 OpenCode effective-control collection (partial)

Access date: 2026-11-11. Installed binary queried: OpenCode 1.19.5.
This is schema/precedence evidence, not a completed agent or helper-routing observation.
LR-06 remains decided.

## Versioned upstream sources

Repository: anomalyco/opencode, tag `v1.19.5`.

| Source title / URL | SHA-256 of retrieved source |
| --- | --- |
| Resolved configuration command — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/cli/cmd/debug/config.ts | `5bd3c345d3b4037ff6c081223d5e349b96844d16afb7cbaf144f71131fea9659` |
| Debug-only configuration redaction — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/cli/cmd/debug/redact.ts | `7ff1f8aeedd4631f78b1169ea5b29f52060325960be000dd48fdf05bc05e7ce5` |
| Configuration loading / merge — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/config/config.ts | `839c934f014c29a40f06cafd8bf2da1f0b2976ecd9e3e0f71b859792837ab14d` |

The debug command serializes `Config.Service.get()` through `redactConfig`.
The latter replaces string `apiKey` values with `***` in debug output only;
it does not replace the provider's usable resolved configuration. Thus debug
output cannot establish the actual credential value or Bearer authentication.
Those still require the inference capture's in-memory credential comparison.

The loader merges global and project configuration before
`OPENCODE_CONFIG_CONTENT`. It substitutes variables when loading configuration.
It may insert `$schema` into a config missing that property; the isolated
precedence experiment seeds that property explicitly and checks unchanged bytes.

## Implemented experiment seam

`test/lunaroute/agent_live_test.go:liveFactoryWithClient` constructs an
`openCodeControlsRunner` around the real subprocess runner. For each emitted
LunaRoute OpenCode mission it requires the selected `-m` and `--pure`, then
queries `debug config --pure` using that exact executable, env and cwd. Only
argv/stdin and the debug-specific deadline/output cap change for the query.
No ambient environment or reconstructed overlay is used.

The collector validates the emitted env-key reference and compares the resolved
main/small model, enabled provider, npm package, canonical `/v1` destination,
registered model name, tool-call flag and both limits. Duplicate/malformed,
oversized or drifting output, process failure and cancellation refuse before
the mission starts. Raw debug output stays in bounded memory. The returned
projection contains only canonical model, the actual resolved `tool_call` boolean
and token limits; it carries no raw config, auth value or claim of successful
helper traffic.

## Reproduction and scope

```
go test ./test/lunaroute -run 'TestAgentEvidenceOpenCode' -count=1
```

`TestAgentEvidenceOpenCodeInstalledDebugSchema` invokes the installed debug
command in a throwaway HOME/XDG tree with a synthetic credential and no
inference operation. Its control query without the overlay requires project
model/helper values and independently identifiable global/project permission
entries, proving both hostile layers actually loaded. The overlay query then
requires all selected controls and checks seeded config bytes unchanged.
If the binary is absent this schema-only check explicitly skips; that cannot
verify LR-06.

`TestAgentEvidenceOpenCodeControlsFactorySink` additionally drives the real
Factory/adapter/runner through protocol fixtures, authenticated CONNECT/TLS
inference and command-created workspace proofs. Bad effective controls must
leave both inference and mission workspace output absent. These executables
are fixtures, not installed-backend completion evidence.

## Same-run supporting-artifact feed

`TestLiveOpencodeCompaction` now constructs an `openCodeControlJournal` in the
actual experiment Factory. `openCodeControlsRunner` retains only the validated
model/tool-call/context/output projection and query interval for its warm-up and
same-session continuation. Executable and cwd remain private join keys. Before
cleanup, `collectOpenCodeSupportingEvidence` requires two ordered checks in that
workspace from the same executable, the exact resumed session, matching model
and limits, main traffic after the initial query and completed compaction after
the continuation query. It validates all four correlated traffic categories and
real command-created workspace/tool/model proofs. Only the safe projections,
source reference and intervals enter the supporting artifact after cleanup.

`TestAgentEvidenceOpenCodeControlsToSupportingObservation` exercises this actual
Factory/runner/TLS/file sink and zero-write refusals. Its executables and exports
are synthetic; it is not installed-backend verification. Source-backed title,
subagent and first automatic-compaction joins/forcing now exist separately.
Claude's controls/helpers, full prescribed contract/agent entry points and
genuine operator observations remain incomplete. No supported capability,
contract/register verification or release readiness follows from these checks.

## Source-bound tool metadata publication

Reviewed 2026-11-12. R2's recorded
[`lr02-live-models-2026-11-11.json`](lr02-live-models-2026-11-11.json)
provides each canonical model's independent `capabilities.tools` fact. The
completed-agent finalizer now requires that selected record's explicit boolean
`true` to agree with the actual emitted/resolved `tool_call` proof. Missing,
null, false, nonboolean or duplicate source fields cannot authorize publication;
a different model's true fact cannot supply the selected model's missing fact.
The supporting collector also refuses a lost tool-call projection, even when
both warm-up/continuation controls otherwise agree and inference/files completed.
Both final artifacts retain the same-run resolved boolean, not a value inserted
by the publisher. This does not infer installed tool support or a supported
capability from schema checks or protocol fixtures.

`TestAgentEvidenceOpenCodeToolMetadataSource`,
`TestAgentEvidenceOpenCodeCompletedAgentPublication` and
`TestAgentEvidenceOpenCodeControlsToSupportingObservation` cover source rejection
and the real Factory/subprocess/CONNECT/TLS/workspace-to-private-JSON sinks.
The existing operator entry points consume these collectors/finalizer; no new
runtime option, observation, support fact or register status is introduced.
