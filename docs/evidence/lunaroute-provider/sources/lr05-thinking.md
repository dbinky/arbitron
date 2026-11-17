# LR-05 — Claude main thinking request collection (partial)

Reviewed 2026-11-12. This is source/collector provenance, **not** installed
inference evidence, a Claude effort-support fact or verified LR-05.

## Sources and reproducibility

- Public references: <https://code.claude.com/docs/en/settings> and
  <https://code.claude.com/docs/en/cli-reference>. These URLs identify the
  references; they were not fetched in this pass.
- Local source accessed 2026-11-12: installed native Claude Code **2.1.322**,
  `~/.local/share/claude/versions/2.1.322`, 323702464 bytes,
  SHA-256 `a9d237949db816a0b7afd88a9a8e1df36b3a597a30f5a41d1c0539eb180a3450`.
  Build/startup identity is documented in [lr05-startup.md](lr05-startup.md).
- Inspected bundled JavaScript ranges (distribution-byte offsets):

| Producer | Offset | Length | SHA-256 |
|---|---:|---:|---|
| `TNs` / `tne` env/settings thinking selection | 264422344 | 400 | `b8e3b5d83f8b8a2ff6aa76f02bd9c315d979f1ef286eb826a298d1da3a6895fe` |
| Main CLI thinking resolution | 281765870 | 750 | `b5950b33fdc26636ae397189d894a3dc77f2f0f48ed240649d3645615830ccc9` |
| Main wire thinking selection, budget clamp, disabled predicate and effort clamp | 273799950 | 2100 | `2675e061e9b8438af875acb5beaa1af956188ba44adf23c0d8c9c548ae4839ce` |
| Messages request insertion/extra-body precedence | 273806650 | 700 | `5a53bd9af89b3a47441fc3e0ab2331116767b6f9c60e9a490896b743683a01c1` |

`MAX_THINKING_TOKENS=0` selects the CLI's disabled state unless an explicit
CLI thinking override wins. It does **not** guarantee a disabled wire field:
the request producer emits `thinking: {type:"disabled"}` only under its
first-party/provider, model-support, reject-disabled and feature predicates.
Enabled budgets are clamped to at least 1024 and below `max_tokens`; adaptive
requests carry no budget. Extra-body fields can override prepared fields.
Therefore neither an emitted zero budget nor absence of thinking/effort on the
wire establishes that the gateway was instructed to disable reasoning.

## Producer, composition and sink

The existing isolated experiment Factory consumes the selected binding's
actual effort, same loaded rules and actual adapter-emitted launch. Explicit
`none` requires an exact known backend effort fact, `MAX_THINKING_TOKENS=0`,
no graded-effort env, and no CLI effort/thinking override. Production Claude
facts remain unknown; offline fixtures use a distinctive table with `none`.
No new live selection flag, production behavior or supported fact is added.

The real CONNECT/TLS proxy parses each bounded actual Messages body into a
nonnil safe projection of thinking type and budget. A nonnil empty type means
**observed absence**, not disabled. Malformed/duplicate/null/unrecognized types,
invalid budgets and per-message thinking overrides refuse capture before
upstream forwarding. Raw bodies, display text and other arbitrary fields are
not serialized. Snapshots clone the projection so consumers cannot mutate the
private captured record.

The final isolation collector joins that actual feed with the already accepted
same-session/version/model effort selection, authenticated terminal inference,
request budget, first-request memory and command-created workspace/tool proofs.
Its private `main_thinking` projection names source/version/session/model,
selected effort, exact request ids and observed type/budget per request.
Explicit `none` additionally requires **every** observed request to contain
`type:"disabled"`; absent/adaptive/enabled fields refuse publication even when
all files and inference complete. Default and graded selections only record
observed wire controls, never infer thinking support from absence. Current
request/source joins do not establish the gateway's internal reasoning state.

`TestLiveClaudeCodeIsolation` constructs and consumes this collector now,
using the same actual capture and cleanup-before-writing boundary. Offline
`TestAgentEvidenceClaudeThinking*` fixtures exercise Factory → real runner →
subprocess → CONNECT/TLS → command-created files → exclusive private JSON,
including missing-feed and unsafe-field refusal. These are collection fixtures,
not installed-binary observations. If this installed version omits the disabled
field for a selected canonical model, collection must fail; no workaround,
verified label or supported fact is inferred.

Full helper/selector/thinking behavior, complete isolation, installed memory
preservation and the complete Claude agent/contract entry point remain pending
under P3-T5. No register status, capability support or genuine artifact changes.
