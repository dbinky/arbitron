# Phase 1, Task 8: Wire Auth Across REST + /mcp + WS in main.go + Session Sweep + Surface-Coverage Tests

## Goal

Wire everything from Tasks 1–7 into the running server in `cmd/arbitron/main.go`: construct the `SessionRepository`, the (org-mode) `OIDCProvider`, the `AuthMiddleware`, and the `AuthHandler`; pass them into `RouterDeps` so the middleware covers REST, the `/mcp` mount, and the WebSocket upgrade handlers, and the `/auth/*` routes are mounted outside it; start the background session-expiry sweep in org mode; and update the non-loopback bind warning. Add the cross-surface coverage tests proving every surface rejects anonymous requests in org mode and passes through in local mode.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-1.md` §4, §7, §8.

## Dependencies

- All of Tasks 1–7. This is the integration/wiring task and is last in the sequence.

## Background (current state)

- `cmd/arbitron/main.go` `initDomain` builds the whole stack into a `domainStack` and `runServe` constructs the router via `adapthttp.NewRouterFromDeps(RouterDeps{...})`. `userRepo := adaptsqlite.NewUserRepo(db)` already exists (step 5); the session repo and auth components are added alongside.
- `domainStack` is the shared dependency carrier; both `runServe` and `runMCP` call `initDomain`. The auth middleware must protect the HTTP `/mcp` mount in `runServe`; the standalone `runMCP` (stdio) is a separate transport — Phase 1 scope is the HTTP surfaces, so stdio MCP auth is out of scope here (note it).
- `initDomain` already surfaces construction errors fail-fast (closing db/logCloser). OIDC `New` (Task 5) makes a network call and can fail — in ORG mode this is a fatal boot error (consistent with the existing fail-fast gates like `RequireMasterKeyIfConnections`). In LOCAL mode, no OIDC provider is constructed.
- The non-loopback warning in `runServe` ("the API has no authentication") becomes conditional: in org mode the API IS authenticated, so the warning should only fire in local mode (where binding non-loopback genuinely exposes an unauthenticated admin surface).
- The session sweep (Task 4 `SweepExpiredSessions`) needs a cancellable context + goroutine; `initDomain` already manages a `schedulerCtx/schedulerCancel`. Add a similar lifecycle hook on `domainStack` (or reuse the scheduler context) and stop it in `Close`.

## Files to Create/Modify

### Modify: `cmd/arbitron/main.go` — `domainStack`

Add fields:

```go
sessionRepo   identity.SessionRepository
authMW        *adapthttp.AuthMiddleware
authHandler   *adapthttp.AuthHandler // nil in local mode
sweepCancel   context.CancelFunc     // org-mode session sweep; nil in local mode
```

(Import `"github.com/arbitron/arbitron/domain/identity"` and `adaptoidc "github.com/arbitron/arbitron/adapter/oidc"`.)

### Modify: `initDomain` — construct auth components

After the user repo is created (step 5) and before building the router deps, add:

```go
// Auth (Phase 6). The session store is always constructed (migration 003
// created its table); the middleware and OIDC provider differ by mode.
sessionRepo := adaptsqlite.NewSessionRepo(db)
mode := identity.ModeFromString(cfg.Auth.Mode)

var oidcProvider identity.OIDCProvider
var authHandler *adapthttp.AuthHandler
if mode == identity.ModeOrg {
	oidcProvider, err = adaptoidc.New(context.Background(), adaptoidc.Config{
		IssuerURL:    cfg.Auth.OIDC.IssuerURL,
		ClientID:     cfg.Auth.OIDC.ClientID,
		ClientSecret: cfg.Auth.OIDC.ClientSecret,
		RedirectURL:  cfg.Auth.OIDC.RedirectURL,
		Scopes:       cfg.Auth.OIDC.Scopes,
	})
	if err != nil {
		db.Close(); logCloser.Close()
		return nil, fmt.Errorf("initialising OIDC provider: %w", err)
	}
	authHandler = adapthttp.NewAuthHandler(oidcProvider, sessionRepo, userRepo, cfg.Auth.Session)
}

authMW := adapthttp.NewAuthMiddleware(mode, cfg.Auth.Session.CookieName, sessionRepo, userRepo)
```

Store `sessionRepo`, `authMW`, `authHandler` on the returned `domainStack`.

### Modify: `initDomain` — start the org-mode session sweep

After the scheduler is started (or near the end of `initDomain`), in org mode only:

```go
var sweepCancel context.CancelFunc
if mode == identity.ModeOrg {
	var sweepCtx context.Context
	sweepCtx, sweepCancel = context.WithCancel(context.Background())
	go adaptsqlite.SweepExpiredSessions(sweepCtx, sessionRepo,
		time.Duration(cfg.Auth.Session.TTLSeconds)*time.Second, logger)
}
```

Store `sweepCancel` on the stack and call it in `(*domainStack).Close` (before `db.Close()`):

```go
if ds.sweepCancel != nil {
	ds.sweepCancel()
}
```

### Modify: `runServe` — pass auth into the router

Add to the `RouterDeps` literal:

```go
Auth:        ds.authMW,
AuthHandler: ds.authHandler,
```

The middleware/handler wiring inside `NewRouterFromDeps` (Task 6 + Task 7) then covers REST + `/mcp` + WS and mounts `/auth/*` outside the guard.

### Modify: `runServe` — conditional non-loopback warning

```go
if mode := ds.cfg.Auth.Mode; mode == config.AuthModeLocal &&
	ds.cfg.Server.Host != "127.0.0.1" && ds.cfg.Server.Host != "localhost" && ds.cfg.Server.Host != "::1" {
	ds.logger.Warn("local mode is binding to a non-loopback address -- local mode has no authentication and should not be exposed on the network",
		"host", ds.cfg.Server.Host)
}
```

(In org mode, binding non-loopback is the intended deployment, so suppress the warning.)

### Note (out of scope, document only)

`runMCP` (stdio MCP) is not behind HTTP auth; Phase 1 scope is the HTTP surfaces. Add a one-line comment in `runMCP` noting stdio-MCP identity is deferred (the stdio transport is operator-local).

## Testing (strict TDD — five scenario categories)

A new cross-surface integration test `adapter/http/auth_surface_test.go` builds a real router via `NewRouterFromDeps` with fake repos/handlers, exercising every surface. Plus a wiring smoke check. Write first.

- **Happy path (local)** — `TestSurface_LocalMode_AllPassThrough`: with `Auth` = local-mode middleware, table-drive `GET /api/v1/health`, `GET /api/v1/instances`, the `/mcp` mount, and the WS upgrade `GET /api/v1/instances/{id}/ws` — each reaches its handler (200 / 101 / handler-specific) with NO credential, and the handler observes an admin identity.
- **Happy path (org, authed)** — `TestSurface_OrgMode_ValidSession_AllPassThrough`: with a valid session cookie, the same surfaces all pass.
- **Input validation** — `TestSurface_AuthRoutesOutsideGuard`: in org mode, `GET /auth/login`, `GET /auth/callback`, `POST /auth/logout` are reachable WITHOUT a session (they are mounted outside `d.Auth`); a 302/handler response, never a 401 from the middleware.
- **Error/failure** — `TestSurface_OrgMode_Anonymous_AllRejected`: with org-mode middleware and NO cookie, table-drive REST (`GET /api/v1/instances` → 401), `/mcp` (POST → 401), and the WS upgrade (`GET .../ws` → 401, no upgrade). This is success-criterion #2 ("org mode authenticates every surface").
- **Boundary** — `TestSurface_OrgMode_BrowserNavRedirects`: anonymous `GET /` (`Accept: text/html`) → 302 `/auth/login`; anonymous `GET /api/v1/...` → 401 (API never redirects). Confirms the redirect/401 split is wired through the real router.
- **Isolation/security** — `TestSurface_MCP_DestructiveTools_RequireAuth`: an anonymous POST to `/mcp` (the surface exposing `workflow_delete`, `instance_cancel`, etc.) is rejected at the middleware before reaching any tool — closes the documented `/mcp` auth gap. `TestInitDomain_OrgMode_RequiresOIDC` (or a `runServe`-level smoke test with a fake): org mode with an unreachable issuer fails `initDomain` fast (no half-initialised server); local mode initialises with no OIDC provider and a nil `authHandler`.

## Acceptance Criteria

- `main.go` constructs `SessionRepo`, (org-mode) `OIDCProvider` + `AuthHandler`, and `AuthMiddleware`, and passes `Auth`/`AuthHandler` into `RouterDeps`.
- In org mode, REST, `/mcp`, and WS all reject anonymous requests (401 / redirect); `/auth/*` is reachable unauthenticated; the session sweep runs and stops cleanly on shutdown.
- In local mode, every surface passes through with an injected admin identity and no OIDC provider is constructed; the non-loopback warning fires only in local mode.
- Org mode with an unreachable OIDC issuer fails `initDomain` fast.
- `go build ./...` and `go test ./...` pass.
