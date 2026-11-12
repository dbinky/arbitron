# LR-05 — First-request project memory collection (partial)

Reviewed 2026-11-12. Source review and offline collector validation only;
**not** installed memory-loading verification or a completed LR-05 contract.

## Sources

- Memory reference: <https://code.claude.com/docs/en/memory>.
- Settings reference: <https://code.claude.com/docs/en/settings>.
- Local source access date: 2026-11-12. These URLs identify the documentation;
  they were not fetched in this pass. The following facts were inspected in
  the installed native Claude Code 2.1.322 distribution. Distribution/build
  identity and executable hash are in [lr05-startup.md](lr05-startup.md).
  Byte ranges in that same executable identify the reviewed bundled source:

| Producer | Offset | Length | SHA-256 |
|---|---:|---:|---|
| `SL` instruction-source selection | 272299732 | 400 | `3030fda962e799f572d9957b426e5b72ddec865130b942e3debdf2b6eca69cc5` |
| `Rsr` project instruction traversal | 272314000 | 2550 | `4e0c0a8a32abda4a708f6c9e1898ae8ac86e6c976b9ac106f380884251792d01` |
| `Za` / `aWn` / `Tet` instruction formatting | 265013297 | 1600 | `415f2861c6dbae410c0246ac630cd1d19670168b9803df4090e56d347a52b1ce` |
| `hki` / `TDt` announced instruction rendering | 270981904 | 1500 | `6f542e1719bc20be1b69bd093f881831cba0b41765e47350d6edd8b2af3d42e5` |
| `Bo` / `qt` enabled settings sources | 261234152 | 650 | `88acd2f449da2891cf15cffcca9d93dcf717c2e0d4a1678608e50e183b742346` |
| `Fko` setting-sources application | 277060431 | 450 | `1fcd23c0a3df03492e130bd3829c10e8824c1c0cc6a5b7762c4ef20671c9b273` |
| instruction attachment -> user message | 276262680 | 650 | `6c1a6e7339d33ecf5537ef8617282171ec6f8f65d76d37f6ade91fc55d1a44ce` |

`Rsr` considers the cwd/ancestor `CLAUDE.md`, `.claude/CLAUDE.md` and rules
when project instruction selection is enabled. The project formatter emits
`Contents of <absolute path> (project instructions, checked into the codebase):`
then a blank line and trimmed file contents. Files are separated by blank
lines. The first announced instruction attachment uses that same formatter
and becomes a user/meta message; inline rendering may wrap it in a system
reminder. A tool result or assistant echo is not this initial instruction feed.

**Unresolved source-level contract concern:** the reviewed instruction walker
uses `SL("projectSettings")`; `SL` consults enabled settings or a workspace
restriction that spares instruction files. `O6n("")` returns no sources, and
`Fko` applies the explicit empty `--setting-sources` selection. This source
review does **not** establish that project memory survives the decided empty
settings-sources launch. That needs an installed-process observation (and a
human decision if contradicted), not a fixture or an invented workaround.
No emitter/selector, readiness flag or register status is changed here.

## Producer, composition and sink

The existing `TestLiveClaudeCodeIsolation` experiment now exclusively seeds
`CLAUDE.md` with a fresh random 128-bit probe in the previously empty mission
workspace, alongside its conflicting settings/auth. Its bounded byte/mode
snapshot includes the memory file and its ancestors. The actual emitted
HOME/cwd is checked before the real Factory runner registers the private
source-formatted path/content expectation with its capture, before any
version query or inference. Registration after traffic, twice, on another
backend, or without unchanged source inputs is refused.

Only the **first** actual `/v1/messages` request can supply this proof. Its
user text must contain exactly one complete source-formatted memory frame
matching that private file. String and text-block forms are supported;
assistant/system text, tool results, missing/substituted/quoted/duplicate
frames, malformed/duplicate/oversized JSON and memory appearing only in a
later request do not count. The nonce is not put in the mission prompt or
argv. No later Read tool, configured filename, exported boolean, successful
model output or command-created proof substitutes for actual initial traffic.

The existing isolation collector requires unchanged inputs, accepted
startup/final session, current authenticated terminal model evidence,
reported context/request budget and command-created workspace/tool proofs.
It joins the actual first request id into `project_memory`, citing this note.
Only the safe loaded boolean/source/request id is serialized. Private paths,
nonce, file contents and raw request text never enter the artifact. Capture
cleanup clears its private comparison material; successful cleanup of the
proxy/CA and experiment still precedes live publication.

The protocol fixtures exercise real Factory/subprocess/CONNECT/TLS and final
private JSON sinks, but deliberately simulate instruction production. They
prove collection/refusal only. Missing memory in the installed binary will
fail the experiment, not silently yield a verified contract or supported
backend. Helpers, selectors/effort, complete isolation/limit enforcement and
genuine operator observations remain outside this slice.
