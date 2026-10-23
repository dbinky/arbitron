# Paired Integration Reviews — Latest Model Profiles

Phase 2 of the loop; it runs only once every single-system box in
`docs/reference/focus-areas.md` is ticked. A row exists only when all three hold:

1. The two behaviours share persisted state, a wire contract, a flag or an ordering constraint.
2. They were built by different tasks, phases or commits.
3. The failure would be silent, with each side green alone.

Each row is one seam, reviewed in both directions, with one approval.

Applying that test to the four shipped behaviours produced **no seam**. The
live cross-task hand-offs this branch contains sit inside one behaviour and are
reviewed as checks of that area. Remaining candidates either lack shared state,
have no silent combined failure, or need an unbuilt consumer (the blocked
refresh, pinning and announcement phases). Blocked consumers are recorded below and in
`docs/reference/gaps-identified.md` `## Won't Fix`, to be promoted when the
consumers land.

## Paired Integration Reviews

| # | Paired Review | Seam | Risk | Status |
|---|---|---|---|---|

## No seam

### Inside one behaviour

- Catalog URL setting (P1T1, 3854785) ↔ catalog fetcher and guarded client (P3T2, 52212c2): no production call hands the URL from `Service.ReadInternal` to `Fetcher.Fetch`; this is pending infrastructure folded into Area 3, not a present shared-wire seam. It is covered by Area 3 review 1 check 5 (the unfed hop, W1/W2) and review 2 check 7 (validation asymmetry).
- Settings service (P1T1) ↔ HTTP, MCP and dashboard boundaries (P1T2): Area 3 review 1 checks 1–3 and review 2 checks 1–4.
- `domainStack.httpDeps` refactor (P1T2) ↔ every pre-existing HTTP route: Area 3 review 1 check 2.
- Following selection fields (P2T1) ↔ the pre-existing `SeedProfiles` one-time seed gate: Area 2 review 1 check 4 and review 2 check 1 (G3).
- Following selection fields (P2T1) ↔ the pre-existing `ProfileService.UpdatePatch` and MCP `profile_update`: Area 2 review 1 check 2 and review 2 check 6.
- Following selection fields (P2T1) ↔ the pre-existing `ProfileCache` and `DynamicProfileResolver` read path: Area 2 review 1 check 3 and review 2 checks 2 and 5.
- Model-state key file (P3T3) ↔ the `model_state_meta.secret_initialized` flag (migration 023, same commit): Area 1 review 1 check 2 and review 2 check 2.
- Connection config revision (P3T3) ↔ pre-existing MCP `connection_update` and `ConnectionService`: Area 4 review 1 check 2.

### Genuinely unrelated

- Area 1 ↔ Area 2 (model-state key ↔ following profiles): no shared state or contract. The only link is boot order (key init precedes profile seeding), and a key failure stops boot with an error, which is not silent.
- Area 1 ↔ Area 3 (model-state key ↔ model settings): the settings code never reads the key. Boot order again, and a key failure is a loud boot error.
- Area 1 ↔ Area 4 (model-state key ↔ connection revision): same migration 023 and same commit 40026e8, so the two sides were not built independently. They share no rows (`model_state_meta` vs `connections`).
- Area 2 ↔ Area 3 (following profiles ↔ model settings): no shared table, flag or contract.
- Area 3 ↔ Area 4 (model settings ↔ connection revision): no shared state.
- Area 3 ↔ the pre-existing run-notification Teams setting (`domain/config/settings.go:SettingsStore`, `system_update_teams_config`): the phase 1 design keeps these as separate keys and stores, shown in separate dashboard cards. No state is shared.
- Area 3's `adapter/outbound/guarded_client.go` ↔ the pre-existing `adapter/notification/ssrf_guard.go`: independent implementations with no shared code or state; they are compared in Area 3 review 2 check 5.

### Unbuilt consumer

- Area 2 ↔ Area 4 (profile revision ↔ connection revision): `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve` does read connection metadata, but ignores both revision fields; `ConnectionService.Delete` reads profiles only to refuse referenced deletes, and a profile read error aborts loudly. Both revisions are inputs to a future execution path revision (P3T3 inventory / `ExecutionGate`), but no shipped code composes them (W4, W5), so a combined failure cannot occur yet. Promote this to a row when that consumer lands.

### Deferred (budget)

None: 2 × 4 + 0 = 8 ≤ 175. Pending consumers above are unbuilt, not budget deferrals.
