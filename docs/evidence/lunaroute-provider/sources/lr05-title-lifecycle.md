# LR-05 — First-user session-title request correlation (partial)

Reviewed 2026-11-12. This is source/offline collector provenance, not an
installed-helper observation, title-adoption proof or verified contract.

## Sources

Reference: <https://code.claude.com/docs/en/model-config> (not fetched here).
Inspected the local Claude Code **2.1.322** distribution
`~/.local/share/claude/versions/2.1.322`; full distribution identity
and hash are in [startup](lr05-startup.md). Bundled JavaScript byte ranges:

| Producer | Offset | Length | SHA-256 |
|---|---:|---:|---|
| System identity/template block preparation | 268996022 | 3400 | `a05f9bdd70f66d49950fc6a0c09d6c65713217e36796bb6e2bc4a599eec2af70` |
| Query-source/request-class mapping | 264319900 | 1800 | `037e36ef1789649d1b29bbe79858b19c9a1ffeee7e733fb7230aef1e5575e7bf` |
| Small-fast helper selection (`g2`, `JGr`) | 273903715 | 1700 | `2edec7e59e77c6288e01c370394eb3014722cf40f77c83c4188fd0bd01b07e4e` |
| Exact title system template (JS literal bytes) | 280268201 | 3094 | `e734c004ebc67a70361c53be9d9e39e45652cbcb98f664711c4a482c98571ab5` |
| Title prompt/request/result producer | 280271080 | 2300 | `6f7283da8e11dd7382d8456ad742f794aec3e813cec4eff7dcee282703736a36` |
| First-user title lifecycle | 301427087 | 2000 | `28b78e040134fafdd9501dec5ba1dd8c23990359843c82deb90681cbd45d8abf` |
| Headless title-state construction | 302236500 | 900 | `625ed15ca7568316ea0c593bbb6240877b8193b74452601f4bcb925f594e8fe8` |
| Resume/disable gate | 302286400 | 800 | `2d42ef69c68a6034606ab443afe7172d9d3ef5cbbeea30361eda8d689cbdff5f` |
| Actual user-ingress trigger | 302386100 | 1100 | `23ef2fc29adada619c1a38a9c37f817e4c91ed385830bf60b04ca615c90653a1` |

`dQs` consumes real nonsynthetic first-user ingress. `Qt`/`Gt` start the
asynchronous title operation once, using its prose. Resumed conversation or
`hhi()` disables that automatic path; absence is not coverage. The existing
experiment starts a fresh non-command mission, with no reconstructed input or
new helper flag. If the installed launch suppresses/does not finish this
operation, publication refuses; other main/helper traffic cannot replace it.

The title producer uses the exact system template and a `<session>` frame
containing the mission, followed by the default language instruction. `g2`
selects `tme(uk())`; `uk` reads `ANTHROPIC_SMALL_FAST_MODEL` (see
[selectors](lr05-selectors.md)). The source query is `generate_session_title`,
whose request class is `auxiliary`, **not** `main` or `subagent`. A returned
nonempty JSON `title` is required. JSON and ordered Messages SSE are supported
by the capture tooling. The actual selected request/response identities remain
mandatory; a configured selector does not establish gateway execution.

## Composition and final sink

The actual capture proxy recognizes the exact versioned system block, mission
frame and default-language suffix, refuses tools/foreign history, and validates
terminal response plus bounded safe JSON title text in memory. Only a boolean
signature/result projection survives. No prompt, title, key or raw body is
persisted. The first **non-title** request supplies the existing first-request
project-memory check; a title arriving first cannot consume that slot.

The existing `TestLiveClaudeCodeIsolation` Factory/runner supplies same-launch
startup, version, session, selector, lifecycle, control and workspace proofs.
Its cleanup-before-publication finalizer requires exactly one title exchange
with that session, auxiliary class, no child-agent identity, current interval,
selected canonical requested/reported model and successful terminal response.
Only its actual request id is labelled `title`; main controls exclude it.
The same selected exact-effort/`none` predicates still apply to its own actual
wire controls; exclusion from main proofs does not bypass explicit constraints.
The title source must be readable before cleanup/publication. Missing,
duplicate, substituted, malformed, unsafe or lost feeds write no observation.

This establishes collection/request correlation only. It does not prove the
installed process adopted/persisted the generated title, every small-fast use,
summary/compaction/default-subagent behavior, full isolation/control enforcement
or memory preservation. Genuine operator observations, full Claude agent/contract
entry point and LR-05 readiness remain pending. No supported fact or register
status follows from fixtures or skips.
