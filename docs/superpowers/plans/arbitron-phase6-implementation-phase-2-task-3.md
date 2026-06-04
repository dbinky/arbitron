# Phase 2, Task 3: Middleware Bearer-Token Branch

## Goal

Extend the Phase 1 auth middleware (`adapter/http/auth_middleware.go`) with the `Authorization: Bearer arb_...` branch: hash the presented token, look it up via `TokenRepository.GetByHash`, load the owning user, inject their `Identity`, and `TouchLastUsed` best-effort (async, non-blocking, error-swallowing). An invalid/revoked/garbage bearer token yields 401. Bearer tokens are accepted on **REST**, **`/mcp`**, and **WebSocket** surfaces; session cookies remain browser-only. This is what lets Claude Code / CLI / scripts authenticate as a real user.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-2.md` §1 (Middleware integration).

## Dependencies

- **Phase 1, Task — auth middleware** (`adapter/http/auth_middleware.go`) must exist with the mode-aware org-mode session branch already in place (read cookie → `SessionRepository` → load user → inject `Identity`; else 401 / login redirect). This task adds the bearer branch **before** the "no valid credential" rejection. The middleware is already mounted at the top of `/api/v1`, wraps the `/mcp` mount, and wraps the WS upgrade (Phase 1 §4) — so adding the branch here automatically covers all three surfaces.
- **Phase 2, Task 1** — `identity.TokenRepository`, `identity.HashToken`, `identity.ParsePrefix`, `identity.Token`.
- **Phase 1** — `instance.UserRepository.GetByID(ctx, uuid) (*User, error)` (added in Phase 1) to map a token's `user_id` → a full `User` → `Identity{UserID, Username, Role}`. `instance.User{ID, Subject, Username, Role}`.

## Background (current state)

- Today there is **no** auth middleware; `adapter/http/server.go` `NewRouterFromDeps` has a `TODO(phase-6)` comment where it belongs, and the `/mcp` mount carries a "NO authentication" warning. Phase 1 replaces both with `auth_middleware.go`. This task assumes that file exists and only adds a branch.
- The middleware is constructed with the configured `Mode`, the `SessionRepository`, the `UserRepository`, and (org mode) the `OIDCProvider` — see Phase 1 §7 and `cmd/arbitron/main.go` wiring. This task adds the `TokenRepository` to that constructor's dependency set.
- `instance.UserRepository` lives in `domain/instance/ports.go` and is already imported by the engine (`main.go` builds `userRepo := adaptsqlite.NewUserRepo(db)` and passes it to `engine.NewEngine`). Phase 1 expands it with `GetByID`/`GetBySubject`/`Create`. The middleware maps a persisted `User` to an `identity.Identity` (per Phase 1 §3: `domain/identity` imports nothing from `domain/instance`; the mapping happens in the adapter middleware).
- `identity.WithIdentity(ctx, Identity)` is the injection helper; downstream handlers/services read `identity.FromContext`.

## Files to Create/Modify

### Modify: `adapter/http/auth_middleware.go`

Add a `TokenRepository` (and the `UserRepository` already present from Phase 1) to the middleware struct/constructor:

```go
type AuthMiddleware struct {
    mode     identity.Mode
    sessions identity.SessionRepository
    tokens   identity.TokenRepository   // NEW (Phase 2)
    users    instance.UserRepository
    oidc     identity.OIDCProvider      // org-mode only; may be nil in local mode
    logger   *slog.Logger
    // defaultIdentity cache for local mode (Phase 1)
}
```

Insert the bearer branch in the **org-mode** path, after the session-cookie attempt and before the final 401/redirect. Order: a present-and-valid session wins; otherwise try bearer; otherwise reject. (A request may carry either; never require both.)

```go
// --- Bearer token branch (Phase 2) ---
if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
    raw := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
    // Cheap pre-filter: only arb_<prefix>_<secret> shapes are ours.
    if _, ok := identity.ParsePrefix(raw); ok {
        hash := identity.HashToken(raw)
        tok, err := m.tokens.GetByHash(r.Context(), hash)
        if err == nil {
            user, uerr := m.users.GetByID(r.Context(), tok.UserID)
            if uerr == nil {
                id := identity.Identity{UserID: user.ID, Username: user.Username, Role: identity.Role(user.Role)}
                // best-effort, async, non-blocking; never delays or fails the request.
                go func(tid uuid.UUID) {
                    _ = m.tokens.TouchLastUsed(context.Background(), tid)
                }(tok.ID)
                next.ServeHTTP(w, r.WithContext(identity.WithIdentity(r.Context(), id)))
                return
            }
        }
        // A malformed/revoked/garbage bearer that LOOKED like ours is a hard 401:
        // fall through to the rejection below rather than silently continuing,
        // so a revoked token can never be treated as anonymous-but-allowed.
    }
    // An Authorization header that isn't a recognizable arb_ token also falls
    // through to the standard rejection (no Basic/other schemes accepted here).
}
// ... existing "no valid credential" → 401 (REST/MCP/WS) / redirect (nav) ...
```

Notes / invariants:
- **Constant-work-ish:** `GetByHash` on a sha256 hex string is an indexed unique lookup; a non-matching token returns `ErrNotFound` → 401. We do not leak timing about prefix existence beyond the cheap `ParsePrefix` shape check.
- **TouchLastUsed is best-effort:** detached `context.Background()` goroutine, error ignored, never on the request's critical path. Its failure must never 401 a valid request.
- **Local mode is untouched:** the bearer branch lives only in the org-mode path. In local mode the middleware still injects the default admin identity with no credential — an `Authorization` header in local mode is simply ignored.
- **Surface coverage falls out for free:** because Phase 1 already wraps `/api/v1`, `/mcp`, and the WS upgrade with this one middleware, adding the branch here makes bearer auth work on all three with no per-surface code.

### Modify: `cmd/arbitron/main.go`

Pass the `tokenRepo` (built in Task 1) into the auth-middleware constructor alongside the session repo, user repo, and OIDC provider. (The exact constructor signature is Phase 1's; this task adds the `tokens` argument.)

## Testing (strict TDD — five scenario categories)

Tests first. Extend `adapter/http/auth_middleware_test.go` (created in Phase 1). Build the middleware in **org mode** over fake `SessionRepository`, fake `TokenRepository`, and fake `UserRepository`; assert on the `Identity` reaching a sentinel next-handler (capture `identity.FromContext`) and on the HTTP status.

- **Happy path:** seed a user U and a token T (hash = `HashToken(plaintext)`) owned by U. A request with `Authorization: Bearer <plaintext>` reaches the next handler with `Identity{UserID: U}` and the right `Role`; the handler returns 200.
- **Input validation:** `Authorization: Bearer not-an-arb-token`, `Authorization: Basic xxx`, and a missing header all → 401 (org mode), and `GetByHash` is **not** called for the non-`arb_` shapes (assert the fake repo's call count is 0, proving the `ParsePrefix` pre-filter).
- **Error/failure:** a syntactically valid `arb_..` token whose hash is absent (`GetByHash → ErrNotFound`) → 401; a token whose user was deleted (`GetByID → ErrNotFound`) → 401; never 500.
- **Boundary:** a request carrying **both** a valid session cookie and a garbage bearer → 200 via the session (session wins, evaluated first); a request with a valid bearer and **no** cookie → 200 via the token; `TouchLastUsed` is invoked exactly once for the matched token id (use a synchronization seam — e.g. a channel/`WaitGroup` in the fake — since it runs in a goroutine) and a `TouchLastUsed` that returns an error still yields 200.
- **Isolation/security:** a **revoked** token (deleted from the repo so `GetByHash → ErrNotFound`) → 401, confirming revocation is immediate; the bearer branch never injects an identity for a token that doesn't resolve; in **local mode**, a request with a bogus `Authorization: Bearer` header still gets the default admin identity and 200 (header ignored, no repo call). Add a table test asserting REST, a simulated `/mcp` request, and a WS-upgrade request all behave identically under bearer (valid → pass, revoked → 401) since they share the one middleware.

Run `go build ./...` and `go test ./adapter/http/...` green before handoff.
