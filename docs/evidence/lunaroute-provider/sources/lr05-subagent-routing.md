# LR-05 — Synchronous Claude subagent request attribution (partial)

Reviewed 2026-11-12. This is source and offline collector provenance, **not** an
installed-helper observation or verified LR-05 contract.

## Sources

Public references: <https://code.claude.com/docs/en/sub-agents> and
<https://code.claude.com/docs/en/cli-reference> (reference URLs, not fetched in
this pass). Inspected local distribution: Claude Code **2.1.322**,
`~/.local/share/claude/versions/2.1.322`, 323702464 bytes,
SHA-256 `a9d237949db816a0b7afd88a9a8e1df36b3a597a30f5a41d1c0539eb180a3450`.
See [startup](lr05-startup.md) and [lifecycle](lr05-subagent-lifecycle.md)
for the same-executable version query and structured task producers.

Exact inspected bundled-JavaScript distribution-byte ranges:

| Producer | Offset | Length | SHA-256 |
|---|---:|---:|---|
| Request-class mapping, built-in type, routing header names | 264319783 | 1600 | `40fb3fd042f93dbbb25c5dabe236b66eceafc216d4bb5ab25aaa2b88da55e05a` |
| Main versus subagent context and async-local context | 264816318 | 1600 | `7444e3edbac3cf10818d3fc457e3a03fd9c4d31de32f00a67035302699db192f` |
| Hint-header enablement | 268252373 | 1450 | `7e6e14a7af0c0d5a965029f9df3a81d942debb16774d69ffdfb775838b50489a` |
| Common session header | 268323500 | 400 | `a3fea7ca9845f91d33ede29aac9ff142809e2309bb0d1432f27399b436f83f3a` |
| Actual API-client header construction | 268330600 | 2100 | `be58fe4ae54527625855d0c7f0f6f8cf0f02deacaa0ad9283fd83cc7b4d603e7` |
| Agent-specific context propagation into query | 279649532 | 1600 | `b2e073283d6127feaa8cfcdba4d530b2b3ec315570639fccdea40b1e3f46a513` |
| Built-in/custom query-source construction | 279740930 | 1450 | `860ebf916392de2842fa2a9777a1426e1c033120fe9c56c6fe78f97bfecffa08` |

The common header is `X-Claude-Code-Session-Id: G()` (the root session).
Main context is excluded from the agent-specific headers. Subagent context
supplies `x-claude-code-agent-id` and, for nested agents, optional
`x-claude-code-parent-agent-id`. With the decided gateway hints enabled,
`repl_main_thread*`/`sdk` map to request class `main`; `agent:*` maps to
`subagent` (workflow contexts differ). Built-in query source
`agent:builtin:general-purpose` supplies agent type `general-purpose`.
A class label alone cannot establish an Agent invocation.

## Collection and final sink

The real capture proxy projects only those five bounded safe ASCII routing
fields. Repeated, combined, percent-encoded, empty, oversized and secret-bearing
values fail before upstream I/O. Other header values and `metadata.user_id`
are not persisted. Missing headers remain missing; selection never fills them.

`TestLiveClaudeCodeIsolation` constructs the existing isolated production
Factory/runner and now consumes `collectClaudeSubagentRoutingEvidence` before
cleanup/publication. That collector first requires the existing actual-launch
startup/version, ordered successful synchronous Agent lifecycle, emitted
controls, captured Messages budgets/thinking/effort/memory, authenticated
terminal exchanges and command-created workspace/tool proofs. It then requires
current root-session main traffic without agent ids and nonempty distinct
subagent traffic with the lifecycle's exact task id and general-purpose type.
Top-level foreground registration has no parent task; foreign/nested parent
headers are refused. Every exchange must lie inside this launch's interval and
report the selected canonical model. Unknown categories are refused, not
silently marked covered.

The operation records exact observed request ids, the source/version, root
session, Agent tool-use id and observed request interval. This interval is
**capture time**, not a claimed harness task-start/end timestamp. The separate
`subagent_lifecycle` retains its task/run/tool identities and explicitly labelled
harness-reported model. Only exchanges joined to that same-launch lifecycle are
marked `subagent`; main traffic is never relabelled merely to supply a helper.

Offline tests run the real Factory, adapter, runner, executable wrapper and
CONNECT/TLS proxy. Protocol executables simulate the documented headers and
structured lifecycle, receive the actual mission/key/model/limits/tool policy,
issue separate main and helper requests and create the command-derived files.
Assertions read the final exclusive private JSON sink. Missing feeds/files,
foreign identities, model substitution and stale capture write no observation.
These fixtures are tooling proof, not installed-binary evidence.

## Attributed request controls

The same operator publication path now records `subagent_controls` only after
that lifecycle/header/response join succeeds. Its exact task/run/tool/session,
selected model/effort and reviewed version come from the same accepted launch.
Each request id, `max_tokens`, effort and thinking type/budget comes from the
actual bounded Messages capture, never from selectors or a copied main request.
The existing all-request validator still requires output-budget equality with
the same loaded/emitted limit, exact explicit effort and an actual disabled
thinking field for `none`. Default records observed presence/absence only.

The projection cites this note and the already versioned
[output-budget](lr05-limits.md), [effort](lr05-effort.md) and
[thinking](lr05-thinking.md) producers. This pass re-read their exact installed
2.1.322 byte ranges (264416663/1100, 273738292/240, 273799950/2100,
273806650/700) and the agent-context propagation range (279649532/1600);
the SHA-256 values still match the tables in those notes and above. The common
request producer can clamp/remove controls or accept extra-body overrides;
observing each attributed request, not assuming inheritance, is essential.

Only main-attributed ids remain in `main_limits`, `main_effort` and
`main_thinking`. In particular the main reported context is **not** claimed as
a resolved helper context window. Offline protocol tests deliberately give the
helper different default effort/thinking fields, then inspect the final private
JSON (including cleanup-before-publication). Missing/unsafe projections, lost
routing and explicit-control drift refuse with no write, despite completed
workspace and inference proofs. This is collector provenance only; installed
helper behavior, internal reasoning state and gateway limit enforcement remain
unverified. No support/status or genuine observation changes.

The operator entry point still writes only the narrow `lr05-isolation.json`,
after successful private experiment/proxy/CA cleanup. Remaining helper
categories, full isolation/controls, installed memory behavior and the complete
Claude agent/contract entry point remain pending. No support fact, register
status, production routing policy or genuine live artifact changes here.
