# LunaRoute model identity evidence

The reviewed executable set is exactly `glm-5.3`, `glm-5.3-flash` and
`deepseek-4.1-flash`. [Operator ruling R3](../../designs/2026-11-10-lunaroute-provider-decisions.md#r3--lr-03-release-set-and-claude-shaped-aliases)
(2026-11-11) supersedes the earlier four-candidate decision and the
Claude-shaped alias mapping. Fixed-id authorization comes from that explicit
review, not numeric syntax or listing membership alone.

## Sources

- Source title: **Authenticated LunaRoute release-model snapshot**.
- URL: https://gw.lunaroute.com/v1/models
- Retrieved: 2026-11-11, authenticated operator run.
- Committed snapshot: [sources/lr02-live-models-2026-11-11.json](sources/lr02-live-models-2026-11-11.json).
- Source title: **Operator rulings — R3 release set and Claude-shaped aliases**.
- Recorded: 2026-11-11 in the linked decisions document. The operator completed
  a live chat on each canonical id with a `PONG` response. No credential is
  present in this note or the snapshot.

The snapshot confirms exact executable spellings; its `owned_by: lunaroute`
field is not a vendor-family assertion. The families below use the existing
`domain/modelidentity/identity.go:families` prefix table, confirmed by R3.
This identity review does not establish support for every task/backend, nor
make any backend release-ready.

## glm-5.3

- Exact executable id: `glm-5.3` (snapshot `models[].id`, R3).
- Vendor: `zhipuai` (`glm-` family).
- Reviewed fixed target: `glm-5.3`.
- Snapshot weights source: https://huggingface.co/lunaroute/GLM-5.3-Vision-NVFP4

The earlier **Chat completions** example at
https://docs.lunaroute.com/api/chat-completions.md (accessed 2026-11-11 UTC)
also names `glm-5.3`; R3 now supplies the reviewed live confirmation.

## glm-5.3-flash

- Exact executable id: `glm-5.3-flash` (snapshot `models[].id`, R3).
- Vendor: `zhipuai` (`glm-` family).
- Reviewed fixed target: `glm-5.3-flash`.
- Snapshot weights source: https://huggingface.co/nvidia/GLM-5.3-Flash-NVFP4

## deepseek-4.1-flash

- Exact executable id: `deepseek-4.1-flash` (snapshot `models[].id`, R3).
- Vendor: `deepseek` (`deepseek-` family).
- Reviewed fixed target: `deepseek-4.1-flash`.
- Snapshot weights source: https://huggingface.co/deepseek-ai/DeepSeek-V4.1-Flash-1017

`deepseek-4.1-flash-1017` is the upstream weights name, **not** a served
executable id. It is refused; no automatic suffix removal or model rewriting
is permitted.

## Revoked Claude-shaped aliases

`claude-*-lr*` aliases are excluded from execution and classify as `unresolved`,
never `anthropic`, including samples without route metadata and names with
routing/vendor prefixes. R3 reports that the gateway's Claude-client listing
maps `claude-opus-lr29` to GLM 5.3 and `claude-haiku-lr37` to DeepSeek 4.1
Flash; numeric suffixes are not stable. `claude-sonnet-5-lr1` returned
`404 INFERENCE_MODEL_NOT_FOUND`.

The earlier Claude Code documentation passage claiming that
`claude-sonnet-5-lr1` resolves to Claude is superseded by R3's live findings.
Its former `anthropic` vendor mapping and `alias:` fixed target are revoked.
The spelling may remain only in negative/unavailable-model fixtures, not as
an authorized proprietary alias. Canonical `glm-5.3` works directly on
`/v1/messages` (R1); a Claude-shaped selector is unnecessary.

The generic reviewed-alias evidence mechanism remains fixture-tested, but no
production alias is authorized and no proprietary-model execution is claimed.
Register/readiness changes belong to their evidence and backend tasks.
