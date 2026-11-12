# LR-05 — Claude main context and request output-budget collection (partial)

Reviewed 2026-11-12. Source/collector provenance only, **not** a live control
verification, gateway maximum-token experiment, helper proof or support fact.

## Sources and reproducibility

- Claude Code settings / CLI references:
  <https://code.claude.com/docs/en/settings> and
  <https://code.claude.com/docs/en/cli-reference>.
- Local source access/review: 2026-11-12. The URLs identify documentation;
  they were not fetched in this pass. The facts below were read from the
  installed native Claude Code **2.1.322** distribution. Its exact executable,
  build identity and full SHA-256 are recorded in [lr05-startup.md](lr05-startup.md).
- Inspected bundled JavaScript byte ranges in that distribution:

| Producer | Offset | Length | SHA-256 |
|---|---:|---:|---|
| SDK per-model usage schema | 263076300 | 1400 | `f5de39158cbc9bf4e6bfb55132a1e8b2c659d5898f85ae1fa532fd56af25049e` |
| `og` resolved context window | 264414578 | 1200 | `0ae81a649eff0a59ce06a3cdf6b50fa2e8ebd97dd4c72b43028e44f85806735e` |
| `lT` gateway/custom context env | 264415000 | 1300 | `869c57e6751f40d4ba05907c806ca668189ebccc9c3550994f842736938e19c7` |
| `awe` default vs `j2n` env-adjusted output | 264416663 | 1100 | `6cd443ae20c4c2f6b24cdb714abeab2a231f688161a3f5c604924836d0ae6f71` |
| `qft` request usage projection | 271927800 | 1100 | `09c391d74ebcda40fd0b4659228db36fc36dfe1bba840f6c23861b2f97e18ce5` |
| engine final result with `modelUsage` | 280805409 | 1600 | `704accb3a55198223f00dbdc69f26be7b028705b72787959f965ce21adfa9c8c` |

The usage schema has integer `contextWindow` and `maxOutputTokens`. The
request-usage producer sets context from the resolved `og(model, settings)`
window, but output from **`awe(model).default`**. The latter is not the
`CLAUDE_CODE_MAX_OUTPUT_TOKENS`-adjusted `j2n(model)` result. Thus comparing the
usage output field to the env would be a false proof. Context resolution also
has catalog/served-window precedence and clamps; the collector requires the
actual reported value rather than assuming the env won.

Output evidence instead comes from the actual authenticated `/v1/messages`
request's integer `max_tokens`. The existing bounded CONNECT/TLS capture reads
that numeric field while forwarding the request. It never stores the request
body, prompt, other settings or credential. A missing budget remains zero;
there is no fallback from env or result usage.

## Composition, application and scope

`liveClaudeIsolationFactoryWithClient` constructs the collector with the **same
loaded capability rules** as the real Factory/Claude adapter. It consumes the
actual emitted executable/env/cwd and bounded JSONL, requiring the reviewed
startup/version/final session plus exactly the selected model's nonempty usage
context. Both emitted limit values must equal the loaded source facts. Before
private publication, `collectClaudeIsolationEvidence` requires each current
captured Messages request's actual output budget to equal that fact and records
its exact request id. Missing context, budget, rules or feeds and drifting,
malformed or secret-bearing records refuse despite successful files/inference.

`TestLiveClaudeCodeIsolation` reads this source alongside the existing isolation
source before collection. Its narrow `lr05-isolation.json` adds `main_limits`
with version/session/model, resolved context, requested output budget and actual
request ids. Successful private experiment/proxy/CA cleanup still precedes the
writer. No raw usage/config/path/body is serialized.

The fixture sink tests deliberately report a different catalog output default
(8192) while requesting the distinctive table budget (32768). They drive the
real Factory, subprocess, authenticated CONNECT/TLS, command-created workspace
proofs and private JSON sink. These are tooling tests, not installed inference
observations. A short accepted request does not establish gateway maxima or
all limit enforcement. Six-selector/effort behavior, helper-specific budgets,
memory, complete isolation and the full Claude contract/agent entry point still
need their source-backed producers and genuine operator observations. No
register label or capability support is changed here.
