# LR-05 — First-user session-title request correlation (partial)

Reviewed 2026-11-12. This is source/offline collector provenance, not an
installed-helper observation or verified contract. The offline collector now
also joins persisted AI-title adoption, without claiming an operator run.

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

## Persisted AI-title adoption (offline collector)

Additional inspected 2.1.322 byte ranges, accessed 2026-11-12, from the same
local distribution and reference URL above:

| Producer | Offset | Length | SHA-256 |
|---|---:|---:|---|
| SDK custom-name-only event schema | 263358450 | 1600 | `72180d6048061c7b9bacfed68e7c025fd7599a206f0eabdd84ec3994a017afac` |
| AI-title adoption and subscription | 301427087 | 3250 | `66def09bfb19eabc8812c9e7f922156c8dd131a3af18f38a3c279acf7cd49567` |
| AI-title transcript writer | 271387331 | 2550 | `e62a5323b43a81f61f94217b95fef1a2afa8d539ea930b424511170bc7a6a705` |
| Custom versus AI-title getters | 271397839 | 500 | `328e0684bade90734efb0e2bbdaa9ec4ac9a241739ae297e62c06929700020d4` |
| Local transcript paths | 265969708 | 1900 | `153e81576d623dbce41096365cd9d6830a4334b9b48c55ab31e81b7d01f2180e` |
| Short project key | 261819200 | 1200 | `d4ca06ef11fca0c4bb49c345b38e075fd09e99337ea34ac4f8b482d9ba2f079c` |
| Title sanitizer | 263719480 | 430 | `26e1c8a254811f25624c7a091bf865e65c86b2808b758b1f66e4b2c99ed63a9f` |

`Gt` adopts the sanitized title into the AI-title state. With a session
transcript, `zXe` appends exactly `{type:"ai-title", aiTitle, sessionId}`;
`wy` and `mse` distinguish custom names from AI titles. **The SDK
`session_title_changed` event explicitly reports custom names, not AI-generated
titles.** It is not an adoption producer, despite the shared change subscription.
A custom rename or that SDK event cannot satisfy this collector.

The existing cleanup-before-publication finalizer reads the actual private
launch's local transcript at
`HOME/.claude/projects/<project-key>/<accepted-session>.jsonl`. The ordinary
short ASCII project key replaces every non-alphanumeric cwd character with `-`.
Long/hashed, relocated and storage-backed paths are deliberately refused, not
searched or guessed. The root/session/workspace come from the already accepted
actual emitted launch and startup, not an ambient home or caller source ref.
Noncanonical roots, symlink ancestors/files, stale/unreadable/oversized inputs
and malformed/duplicate JSON refuse safely. The bounded transcript must include
a current root conversation record with that session/cwd, exactly one matching
`ai-title` row and no custom rename. Title-before-conversation persistence order
is allowed because the writer is asynchronous. Missing adoption still refuses.

The proxy retains the actual generated JSON/SSE title privately, indexed by its
actual request id. The finalizer requires exact safe agreement with the
persisted AI title as well as the existing current request/response, session,
version, controls, lifecycle and command-created workspace proofs. Only
`adopted:true` and `adoption_source:"ai-title"` are added to the safe title proof;
raw title, transcript, prompt and filesystem paths never serialize. Capture
cleanup clears the private comparison map before publication. A transformed or
renamed title refuses rather than approximating sanitation/uniqueness rules.

This establishes offline collection/routing/adoption joins, not an installed
process observation, every small-fast use, summary/compaction/default-subagent
behavior, full isolation/control enforcement or memory preservation. Genuine
operator observations, full Claude agent/contract entry point and LR-05 readiness
remain pending. No supported fact or register status follows from fixtures or
skips.
