# LR-05 — Claude Code main startup identity (partial collector source)

Reviewed 2026-11-12. This is source provenance, **not** a completed installed
binary experiment, effective-limit/selector proof, helper proof, or LR-05
verification.

## Source and reproducibility

- Title: Claude Code 2.1.322 bundled SDK system/init schema and producer.
- Distribution/documentation: <https://code.claude.com/docs/en/overview>;
  <https://code.claude.com/docs/en/cli-reference>.
- Reviewed local native distribution: `claude` version `2.1.322`, build time
  `2026-11-10T00:15:08Z`, embedded git SHA
  `660928a5946898d0c8d3488f20dd9b0b2da4ec09`.
- Whole native executable SHA-256:
  `a9d237949db816a0b7afd88a9a8e1df36b3a597a30f5a41d1c0539eb180a3450`.
- Its readable bundled JavaScript at byte offset 263289385, length 1800,
  SHA-256 `c595abbb00633311833a43abd0ed5bd8ce8b4a6a8c4cee4bdc3c76ecfed4e50e`,
  declares the `system` / `init` schema, including string `model`, `cwd` and
  `claude_code_version` fields. Schema metadata explicitly says the init version
  identifies the process running the session's turns.
- The producer `lAt` at byte offset 280242760, length 1440,
  SHA-256 `2139cc69156809f097e8d3788359fc214e570971dbd273e864de5b61ef4fc559`,
  constructs `type:"system", subtype:"init"`, copying `cwd`, `sessionId` (as
  `session_id`) and `model` from the initialized session, and the version from
  the bundled build's `VERSION`.

Offsets/hashes identify inspected source bytes, not user/config/credential
files. No proprietary source bundle, raw transcript or credential is copied
into this note. A different distribution needs source review before this
experiment's version-specific validator is extended; production has no new
runtime version gate.

## Applied collector and scope

`test/lunaroute/claude_startup_helpers_test.go` is constructed around the real
runner in `newLiveAgentFactory`. It queries `--version` with the **same emitted
binary/env/cwd**, empty stdin and a short bounded query, then consumes that
launch's actual bounded JSONL stream. Before the Factory mission can be
accepted, it requires one init with the selected canonical model, the reviewed
version, actual workspace and safe nonempty session; init must precede tool or
assistant output and the successful final result must identify the same
session. Duplicate JSON fields, missing/late/duplicate init, changed identities,
failed/incomplete/extra terminal events and malformed streams are refused.

The emitted `--model`, `ANTHROPIC_MODEL`, hosted base/Bearer-token input and empty
`--setting-sources` must agree at the launch seam. This establishes **emitted
intent plus main startup identity only**. The separate capture validates the
actual hosted path, selected Bearer credential, requested/reported model and
terminal response. A startup model or argv alone is not gateway evidence.

The Factory tests require authenticated CONNECT/TLS inference and actual
command-created workspace proofs, including in post-launch refusal cases.
Protocol executables are fixtures, not installed Claude helper observations.
The collector does not prove the six selectors, hints, effective context/output
limits, effort behavior, `CLAUDE.md` loading, settings precedence or unchanged
user/project files. Those remaining source-backed controls/isolation and forced
helper producers still must be implemented before the complete Claude live
entry point can publish an agent/contract observation. No register or capability
support fact is changed by this partial source/collector work.
