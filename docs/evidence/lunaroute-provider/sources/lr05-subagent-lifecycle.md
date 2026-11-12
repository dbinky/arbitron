# LR-05 — Claude synchronous subagent lifecycle collection (partial)

Accessed/reviewed 2026-11-12. This is source/collector provenance, **not** an
installed-helper observation, gateway-routing proof or verified LR-05.

## Sources and reproducibility

- References: <https://code.claude.com/docs/en/sub-agents> and
  <https://code.claude.com/docs/en/cli-reference>. These identify the public
  references; they were not fetched in this pass.
- Inspected local source: native Claude Code **2.1.322** at
  `~/.local/share/claude/versions/2.1.322`, 323702464 bytes,
  SHA-256 `a9d237949db816a0b7afd88a9a8e1df36b3a597a30f5a41d1c0539eb180a3450`.
  Build/startup provenance is in [lr05-startup.md](lr05-startup.md).
- Exact inspected bundled-JavaScript distribution-byte ranges:

| Producer/schema | Offset | Length | SHA-256 |
|---|---:|---:|---|
| Agent/Task names | 265542251 | 1700 | `e035d35fbd03f781960a9a07a0fd51f58c84c5dac04b68e3d9c560609892815c` |
| `task_started` / `task_updated` schemas | 263353582 | 2050 | `b83b5008c149ff4ed9f2cacd18e305873f6906dbd81c096ae1e8295e95260acf` |
| Registered-task start producer | 269229935 | 2050 | `8f4c0894b6f739389dc0f20ac2c6bc7a1a433ac19ab18fae6cf2c0216a06a480` |
| Agent background/isolation input | 279832722 | 1700 | `d32e11bec23b0c547d2d625cdce769e097467e56ef32448bbc4258dcb95146ab` |
| Explicit Agent model input and precedence | 279831400 | 1700 | `671777c5db4e061b25847b2ef362bd3e5f6f049e5d58f5611c1a82d09966fdcc` |
| Forced-model schema removal | 279834744 | 1700 | `47e0a4a061186b19ebb46774cce96f84389e4852df8e6635b4280e0fc5a9be0f` |
| `Wt` / `Y$` Haiku alias resolution | 264405992 | 1300 | `69efc89e79a6ed95c1a3a3301a5c0a47c623d6a302a69a6f52be0f97d28cba9f` |
| `DH` Haiku default env resolution | 264379850 | 1500 | `6dc83f2df205affc4bd3a97f414ca7a1885964081f0aacca323c5eb8c1340a92` |
| Completed versus async output schemas | 279835446 | 1700 | `1cc86534c64359a7517e68a7b6222da7e8c32b4d4de13e26948393e7b86fd2da` |
| Completed Agent output fields | 279686900 | 1800 | `6e04c3772b7355094521341a2cc72861055c5d9fdebaca250a74e9c0b3e47487` |
| Agent report/model/totals producer | 279693548 | 1700 | `96650d3047e8d7d3d1eab1a9b5c714408e29686550bbb5e93ad0d50d83ab3823` |
| SDK user `tool_use_result` projection | 263141414 | 1450 | `fc157bab47fd04c818b1abec981d1eaa98f6e8015ba84d2d3c727c9a9946ae30` |
| Task notification schema | 263351134 | 2050 | `f8cab96a23b74bb320da1bee63eb86a540ea252b32de4655ed466c4a24e415f0` |

The installed tool name is `Agent` (`Task` is also represented in the bundle,
but this collector does not assume the two names have equivalent semantics).
Its schema says agents default to background execution, so merely asking for a
subagent is insufficient: this experiment explicitly requests
`run_in_background=false`. Registration exposes `task_id`, per-run `run_id`,
`tool_use_id`, `task_type`, `subagent_type`, `is_backgrounded` and spawn depth.
The SDK user event's structured `tool_use_result` carries the tool's completed
output, independently of the model-directed text trailer. Completed Agent
output has `status:"completed"`, `agentId`, `resolvedModel`, optional
`modelsUsed`, and usage/tool/duration totals; async launch is a different
status. These are **harness-reported** models, not observed gateway models.

## Composition, feeds and sink

The shared Claude mission now explicitly permits the Agent tool and asks a
synchronous general-purpose subagent to read and verify the two command-created
proof files. It does not add any production flag or broaden production tool
policy. The existing isolation experiment Factory constructs the lifecycle
collector around its actual same-emitted-env runner. It checks the emitted
subagent selector against the selected canonical model, then consumes the real
bounded JSONL output already validated for startup/version/session and main
context. It never reconstructs configuration under ambient environment values.

The narrow collector requires one ordered main-thread Agent call, foreground
local-agent registration, and matching successful structured tool result.
Task/run/tool/session identities, general-purpose type, top-level depth and
selected model must agree. Background/remote/resumed/replayed/nested calls,
missing/duplicate/reordered frames, failed/withheld/asynchronous results,
foreign model histories, unsafe ids, malformed/duplicate/oversized JSON and
contradictory task updates/notifications refuse collection. Completion is sticky
for the accepted task/run: a `running` update after a completed patch,
notification or structured tool result refuses, even if another successful result
follows it. Same-run completed confirmations and status-free metadata updates
remain valid. The source-defined `task_updated.patch` must be an object:
missing/null/nonobject patches cannot become empty updates through Go's JSON
zero values. When present, `status` must be an accepted nonempty state and
`is_backgrounded` must be a nonnull boolean; null is not absence or foreground.
An empty object and metadata-only patch still leave the accepted state intact.
Factory/subprocess/CONNECT/TLS fixtures require zero publication
on such regressions despite completed main/helper/title exchanges and both
command-created files; the positive control reaches the cleanup-before-write
private JSON sink. This is bounded collector validation, not an installed
lifecycle observation. Missing optional reported models are never filled from
the selected binding.

The final isolation publisher additionally requires current authenticated main
inference, actual main controls/memory and command-created workspace/tool
proofs. Its `subagent_lifecycle` projection contains only source/version,
session/task/run/tool identities and `harness_reported_model`. Raw prompt,
report, paths, transcript, usage object and credentials are not serialized.
The existing operator entry point reads this source and publishes only after
successful private experiment/capture cleanup.

Offline Factory/subprocess/CONNECT/TLS fixtures deliberately produce a
synthetic lifecycle and **only main gateway traffic**. They prove lifecycle
collection and sink refusal, not installed subagent behavior or helper
routing. Records stay classified `main`; no operation/category is invented.
The follow-up [routing collector](lr05-subagent-routing.md) now adds distinct
Factory/CONNECT/TLS fixtures and constructs the same-launch header/task join in
the operator isolation entry point. That narrow publisher requires actual helper
traffic; the main-only lifecycle fixtures above remain separate refusal controls.
Installed subagent observations are still pending.

## Explicit Haiku model-path forcing and collection

The source ranges above were inspected on 2026-11-12. Agent's optional `model`
input takes precedence over its definition and configured default subagent model
(except fork/coordinator/forced-model cases). `Wt` / `Y$` resolve `haiku` through
`DH`, which reads `ANTHROPIC_DEFAULT_HAIKU_MODEL`. A forced-model setting can
remove the input from the schema; this collector does not invent a replacement
or accept an omitted input as proof of the requested path.

The existing Claude experiment mission now explicitly requests `model=haiku`
on its synchronous general-purpose file verifier. The actual emitted launch
must contain exactly the selected canonical model in both the subagent and
Haiku selectors. The real JSONL collector requires the Agent call's exact
`model:"haiku"` and records it as `subagent_lifecycle.requested_model`, separate
from `harness_reported_model`. Missing, null, malformed, duplicate, substituted
or unsafe input refuses collection. The existing finalizer additionally rejects
a lost requested-model feed, even with completed inference/files, and still
requires the exact lifecycle/header/terminal-response join before publication.
Only that actual joined exchange can establish the executed canonical model;
`haiku` is a harness selector, not a new LunaRoute id or an Anthropic-family fact.

Factory/subprocess/CONNECT/TLS fixtures feed distinct main/helper traffic and
command-created files into the private JSON sink, including cleanup before
writing. They prove this forcing/collection seam only. They do **not** prove
installed alias precedence, independent configured-default subagent or small-fast routing, other helper
categories or complete LR-05 verification; those need genuine operator
observations. No capability/support fact or register status changes.

Other Claude
helper categories, full controls/isolation, installed memory behavior and the
complete Claude agent entry point remain pending under P3-T5. No backend
support, register status or genuine live observation is changed by this work.
