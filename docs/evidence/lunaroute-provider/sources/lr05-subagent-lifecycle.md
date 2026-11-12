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
contradictory task updates/notifications refuse collection. Missing optional
reported models are never filled from the selected binding.

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
Actual subagent gateway requests still need a source-backed current-run join
before any completed-agent/contract artifact can be accepted. Other Claude
helper categories, full controls/isolation, installed memory behavior and the
complete Claude agent entry point remain pending under P3-T5. No backend
support, register status or genuine live observation is changed by this work.
