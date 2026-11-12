# Gaps identified — LunaRoute provider

> **Rule:**
> - Each record is at most ten lines.
> - When an item is closed, move it under `## Closed` with a one-line note.
> - The open list is what a pass reads, so keep it short.
> - An unfed or inert production path is never "acceptable". Fix it, or escalate it under `Won't Fix` with the ruling that
>   covers it.

## Open

- [ ] **G1 (Area 1, high): switching a connection *away* from lunaroute keeps the sealed `lr_` key.**
  `adapter/mcp/tools_connection.go:connectionUpdateHandler` requires `provider` and treats `api_key` as optional.
  `domain/provider/lunaroute_connection.go:PrepareLunaRouteUpdate` returns early when the new provider is not lunaroute, so
  `adapter/sqlite/connection_repo.go:Update` takes the `UpdateConnectionMetadata` path and keeps the `lr_` ciphertext. The
  next call sends the LunaRoute credential to the new provider's host, which breaks the spec's "credentials remain
  protected from disclosure".
  Fix: when the stored provider is lunaroute and the new one is not, require a new key, mirroring
  `ErrLunaRouteKeyRequired` and using the conditional-update guard. Add the failing repo and MCP tests first.
- [ ] **G2 (Area 2, high): inverting `BackendExplicit` in the dynamic resolver turns no test red.**
  The mutation run kept `harden:adapter/provider/dynamic.go`: negating `strings.TrimSpace(p.Backend) != ""` at
  `adapter/provider/dynamic.go:89` and `:124` survived. No test drives a real `DynamicProfileResolver.Resolve` binding to
  the launch gate.
  Add root tests: an explicit-pi profile reaches the adapter, and a backend-less profile is refused, both via the real
  resolver rather than a hand-built `ResolvedBinding`.
- [ ] **G3 (Area 7): classifier boundaries are untested.**
  The `harden:domain/provider/lunaroute_classify.go` survivors sit at lines 118, 137, 141, 189, 217, 225, 229, 232, 234 and
  236. They cover:
  - the redaction pair sizing;
  - the 256-byte snippet boundary;
  - the Retry-After digit test;
  - transport-before-code precedence;
  - verified-code-before-status precedence.

  Add a table test per boundary that fails under each mutant.
- [ ] **G4 (Area 7): StreamChat may not classify SSE error frames.**
  The `harden:adapter/openrouter/provider.go` survivors are at lines 239–240: negating
  `p.classify != nil && len(frame.Choices) == 0 && frame.Usage == nil` survives, and so does the scanner buffer at line 209.
  A structured LunaRoute error frame mid-stream may produce empty content instead of a classified failure. Prove it is
  caught, or fix it.
- [ ] **G5 (Area 9): diagnosis branches are not discriminated.**
  The `harden:adapter/provider/lunaroute_agent.go` survivors are at lines 83 and 85, and the
  `harden:domain/provider/lunaroute_diagnosis.go` survivors at lines 17 and 23. Classified-probe-error, local-probe-error
  and nil-original paths are not told apart by any assertion. Add tests on `Checked`, `Observation`, the error text and
  `errors.Is` for each branch.
- [ ] **G6 (Area 8): a 3xx listing status may count as success.**
  `harden:adapter/probe/lunaroute.go` survives at line 76 (`resp.StatusCode >= 300`). Add a 300 response at the fake
  gateway: it must classify as a failure, never as a listing.
- [ ] **G7 (Area 3): loader and save boundaries are untested.**
  The survivors are:
  - `harden:adapter/config/capabilities.go` (`len(...) > 0` guards at lines 186, 200 and 220);
  - `harden:domain/provider/profile_service.go` (line 316, the mixed-route agent-backend axis);
  - `harden:domain/provider/lunaroute_validate.go` (line 135, the `MaxOutputTokens > 0` boundary);
  - `harden:domain/provider/lunaroute_capability.go` (line 109, sort order).

  Add tests for: empty `tasks`, `backends` and `efforts` maps; mixed routes on two backends; `MaxOutputTokens` equal to the
  limit; and sorted ids.
- [ ] **G8 (Area 4): vendor-prefix consistency is under-tested.**
  The survivors are `harden:domain/modelidentity/family.go` (negations at lines 89, 98 and 110) and
  `harden:domain/modelidentity/version.go` (line 192, the lunaroute case).
  Add cases where a vendor prefix contradicts the id, where a bedrock qualifier contradicts the prefix, and where an alias
  hint conflicts. Add an id that only the non-lunaroute grammar accepts.
- [ ] **G9 (Area 1): key-shape and meta-read errors are untested.**
  `harden:domain/provider/lunaroute_connection.go` survives at line 18 (the whitespace/control scan in `ValidLunaRouteKey`).
  `harden:adapter/sqlite/generated/connections.sql.go` survives at line 204 (a negated error check on a generated query).
  Add an `lr_ab cd` key case, and decide whether the generated-query error path is reachable and tested.
- [ ] **G10 (Areas 5, 6): executed-identity safety checks are untested.**
  The survivors are `harden:adapter/codex/lunaroute.go` (line 97, `safeLunaRouteIdentity`) and
  `harden:adapter/pi/model_probe.go` (lines 60 and 62). Reported model strings with quotes, whitespace, control characters,
  `lr_` text or more than 256 bytes must be rejected as evidence. Add a case per clause.
- [ ] **G11 (Areas 3, 8): text survivors need a decision.**
  The survivors `harden:adapter/mcp/tools_connection_models.go`, `harden:adapter/mcp/lunaroute_backend_hint.go` and
  `harden:domain/provider/model_discovery.go` are string-concatenation mutants in LLM-facing text. Pin the load-bearing
  phrases in tests (unknown is not a guarantee; codex or pi only; pinned only), or record each mutant as equivalent with a
  reason.

## Closed

(none yet)

## Won't Fix

- **Dormant claude-code and opencode LunaRoute emitters. Escalated and decided by operator ruling R7 (2026-11-12).**
  `adapter/claudecli/lunaroute_env.go` and the LunaRoute branches of `adapter/opencode/*` stay in the tree but are refused
  at `domain/provider/lunaroute_validate.go:lunaRouteEnabledBackends`, and at `Factory.AgentProviderFor` and the
  interactive selector. Their emission is not refined. Area 2 verifies only that they are unreachable. Re-enabling one
  takes a gate change plus a live operator pass (LR-05/LR-06 are `deferred`).
  The survivors left unrefined are `harden:adapter/claudecli/streamjson.go`, `harden:adapter/claudecli/stream_agent_adapter.go`,
  `harden:adapter/opencode/config.go` and `harden:adapter/opencode/model_probe.go`.
- **Live verification and the full MCP acceptance matrix.** Ruling R6 makes these operator-run, and R7 descopes them:
  P4-T7's matrix, P2-T8's manual acceptance, the P3-T5 harness, and the forced-helper collectors from P2-T9 step 3 and
  P4-T1 steps 2–3. Agents hold no `lr_` key, so a skipped live test is expected and never counts as readiness.
- **Codex warns "Model metadata for glm-5.3 not found".** R7 records this as a known, non-fatal degradation.
- **Repository content outside the LunaRoute scope.** The squashed single-commit branch makes the whole repo look changed;
  see `RALPH.md` → Scope. Layout, dashboard, spawn, queue, teams and other features are not this branch's work.
