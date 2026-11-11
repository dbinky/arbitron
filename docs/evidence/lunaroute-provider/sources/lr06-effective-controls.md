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
projection contains only canonical model and token limits; it carries no raw
config, auth value or claim of successful helper traffic.

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

Still missing: source-backed forced title/summary/compaction/subagent lifecycle
collectors, their correlated actual traffic, Claude's effective-control
collector, and the complete installed-binary live entry points/observations.
No supported capability, contract/register verification or release readiness
is inferred from these partial checks.
