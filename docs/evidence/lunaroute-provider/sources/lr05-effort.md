# LR-05 — Claude main graded-effort request collection (partial)

Reviewed 2026-11-12. This records source/collector provenance, **not** an
installed inference observation, gateway effort-support fact or verified LR-05.

## Sources and reproducibility

- Claude Code settings and CLI reference:
  <https://code.claude.com/docs/en/settings> and
  <https://code.claude.com/docs/en/cli-reference>.
- Local source access: 2026-11-12. The documentation URLs identify the public
  references; they were not fetched in this pass. Inspected the installed native
  Claude Code **2.1.322** distribution identified and fully hashed in
  [lr05-startup.md](lr05-startup.md).
- Inspected bundled JavaScript ranges (distribution-byte offsets, not line
  numbers):

| Producer | Offset | Length | SHA-256 |
|---|---:|---:|---|
| Wire effort vocabulary | 261429307 | 100 | `a5a095b767f2d3d31e274037f24c49dfc78749abcd97b20c12034850ae12a602` |
| `Hw` model effort-support resolution | 265656449 | 600 | `35d24b369f527b7feac0cf015145cd4cfdc1b0e7012593cf551028968e3bbade` |
| `Zq`/`Nw` env effort parsing | 265661243 | 650 | `e2f1c48be89b6857457e0f7574f2616c0fa664387363cbdf26a187145b2d9166` |
| `ry`/`nBn` effective effort and downgrade | 265666373 | 950 | `bd5e293bb4e2ff8c18a3d801af25c254b7104a6d7f5d171530c7ad1856bbe9dc` |
| `jlt` request effort insertion/support removal | 273738292 | 240 | `0a6927aa5a4dd110bc0458fb942f6199f1abb23ceef9113bae7e97f64fc8dc0c` |
| Main request output-config preparation | 273798300 | 700 | `85f00df3985c735d3fedd49d7a30d7b4df45b460f8f1d29ffbba5272acb67dd7` |
| Main thinking and effort clamping | 273799950 | 2100 | `2675e061e9b8438af875acb5beaa1af956188ba44adf23c0d8c9c548ae4839ce` |
| Main Messages wire request | 273806650 | 700 | `5a53bd9af89b3a47441fc3e0ab2331116767b6f9c60e9a490896b743683a01c1` |

The wire vocabulary is `low`, `medium`, `high`, `xhigh`, `max`. The env parser
also recognizes aliases, numeric budgets and auto/unset, but emitting one of
those is **not** Arbitron's exact graded-effort contract. Model support, caps
and thinking state can remove/downgrade the requested effort. `jlt` writes the
string to `output_config.effort` only when the resolved model supports effort.
Thus emitted `CLAUDE_CODE_EFFORT_LEVEL` alone proves nothing about the request.
Per-message output config is another protocol and can override request-wide
intent; this narrow collector refuses it rather than calling the top-level
field effective in its presence.

## Producer, composition and sink

`TestLiveClaudeCodeIsolation` passes the selected binding's **actual Effort**
into `liveClaudeIsolationFactoryWithClient`. Its guard consumes the real
adapter-emitted launch, the same capability rules and existing accepted
startup/version/final-session feed. Explicit graded effort requires a known
backend effort fact containing that exact level and an exact matching env
value; command-line effort/thinking overrides and thinking-budget env are
refused. This experiment does not change the emitted launch.

The real CONNECT/TLS proxy parses only the bounded actual Messages effort
field, retaining a nonnil safe projection even when that field is absent.
Malformed, duplicate, null, numeric, unrecognized or secret-bearing effort
values cannot enter the projection. No raw request/config content is stored.
At the final isolation publisher, every current authenticated terminal request
must supply this projection. Each explicit level must match exactly, with no
flooring, substitution or env fallback. The private `main_effort` JSON records
source/version/session/model, selected axis, exact request ids and observed
levels alongside the existing workspace/tool/memory/limit proofs. Successful
proxy/CA/private-experiment cleanup still precedes live publication.

For `default`, the experiment records the **observed** field (empty string means
observed absence), without assigning an exact effort to the default or claiming
backend support. Current production Claude effort facts remain unknown; the
live isolation entry point uses default, not a newly invented effort fact.
Offline explicit-level fixtures load a distinctive test-only table and drive
all five levels through the real Factory/runner/TLS/workspace/private-JSON sink.
They prove collection, not installed support. `none` needs an independent
source-backed effective-thinking proof; absence of an effort parameter cannot
prove thinking disabled. This narrow experiment refuses that selection.

No new flag/env vocabulary, production adapter behavior, capability fact,
register status or live artifact is introduced. Complete thinking/effort
verification, helper-specific controls, selectors, full isolation, installed
memory preservation and the complete Claude agent/contract entry point remain
pending under P3-T5. Missing inputs or contradictory installed behavior cannot
publish success or authorize readiness.
