# LR-05 — Same-launch configured selector projection (partial)

Reviewed 2026-11-12. This note records source and offline collection provenance,
not an installed-helper observation or verified contract.

## Sources

Public reference URLs: <https://code.claude.com/docs/en/model-config> and
<https://code.claude.com/docs/en/sub-agents> (not fetched in this pass).
Inspected the local Claude Code **2.1.322** distribution at
`~/.local/share/claude/versions/2.1.322`. Distribution identity and
SHA-256 are recorded in [startup](lr05-startup.md).

Inspected bundled-JavaScript distribution-byte ranges:

| Producer | Offset | Length | SHA-256 |
|---|---:|---:|---|
| Small-fast selection and denied-model remapping | 264368945 | 1700 | `393552ddc73f5cc27aef090142a27cd26170576ef22741eace9972c1b0954b9f` |
| Opus/Sonnet/Haiku default selection | 264379730 | 900 | `27d6486f7bd80cec1367cd7648461172d99986a3ff9258893c295b4715f4ae0c` |
| Explicit Agent model input precedence | 279831400 | 1700 | `671777c5db4e061b25847b2ef362bd3e5f6f049e5d58f5611c1a82d09966fdcc` |

`uk()` first reads `ANTHROPIC_SMALL_FAST_MODEL`; without it, selection can
use the Haiku default or the main model. `tme()` can remap denied models.
`SS()`, `sy()` and `DH()` read the Opus, Sonnet and Haiku default env values.
Agent's explicit model input takes precedence over the configured subagent
default; see [lifecycle](lr05-subagent-lifecycle.md). The existing startup
collector verifies the main model and bounded same-executable version query.
These sources explain why emitted selector agreement alone cannot prove that
every installed helper stayed selected.

## Producer, composition and sink

The production LunaRoute emitter already constructs all six decided selectors.
The existing isolated experiment Factory now reads the **actual adapter-emitted
RunSpec**, requiring exactly one of each with the same canonical selected model:

- `ANTHROPIC_MODEL`
- `ANTHROPIC_DEFAULT_HAIKU_MODEL`
- `ANTHROPIC_SMALL_FAST_MODEL`
- `ANTHROPIC_DEFAULT_SONNET_MODEL`
- `ANTHROPIC_DEFAULT_OPUS_MODEL`
- `CLAUDE_CODE_SUBAGENT_MODEL`

Missing, empty, duplicate, substituted or unsafe values refuse before the version
query/mission. The collector never rebuilds env from ambient state or changes
the launch. After successful startup, lifecycle, controls and workspace proofs,
it binds the projection to the same accepted session/version/model.

`TestLiveClaudeCodeIsolation` feeds that projection to the existing private
publication path. The final collector rejects missing/drifting selector feeds
or source references even with completed authenticated inference and
command-created files. Cleanup still precedes publication. JSON labels this
projection `configured_selectors` with `kind: configured_intent`; it never calls
these effective selectors. Only source-defined lifecycle/header/response joins
can prove helper routing. Raw env, credentials, paths and transcripts are not
serialized.

Offline protocol processes and private publication tests prove this data path
only. Small-fast/title/summary/compaction forcing, installed default-selector
behavior, full control/isolation verification and genuine operator observations
remain pending. No supported capability, register status or backend readiness
changes here.
