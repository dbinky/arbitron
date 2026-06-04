# Phase 2, Task 2: `TokenService` + REST Token Endpoints + MCP Token Tools

## Goal

Add the domain `TokenService` (`Mint`/`List`/`Revoke`, all scoped to the **context identity's** user) and expose it on two surfaces: REST (`POST /api/v1/tokens`, `GET /api/v1/tokens`, `DELETE /api/v1/tokens/{id}`) and MCP (`token_create`, `token_list`, `token_revoke`). The plaintext token is returned exactly once — on Mint — and never echoed by `List` or any other read. A user can only manage their **own** tokens (isolation enforced in the domain service, not just the surface).

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-2.md` §1 (Port & service; Surfaces).

## Dependencies

- **Phase 2, Task 1** — `identity.TokenRepository`, `identity.Token`, and `identity.GenerateToken`/`HashToken` must exist; `adapter/sqlite.TokenRepo` is wired into `main.go`'s `domainStack`.
- **Phase 1** — `identity.Identity`, `identity.FromContext(ctx) (Identity, bool)`, `Identity.IsAdmin()`, and the auth middleware that injects an `Identity` on every request (so `FromContext` is always populated for REST/MCP handlers). In local mode that identity is the default admin user, so token management works locally too.
- **Phase 2, Task 3** (bearer middleware) is a *consumer* of the tokens minted here but is not a dependency of this task; Mint must work as soon as any credential exists (session in org mode, default identity in local mode).

## Background (current state)

- There is no `TokenService` yet. The closest existing "domain service over a repo with identity-scoping" pattern does not exist pre-Phase-6; model the service after the thin domain services already in `domain/` (constructor takes the port; methods take `ctx` and read identity from it).
- REST handlers live in `adapter/http/*_handler.go` and are mounted in `adapter/http/server.go` `NewRouterFromDeps` inside the `r.Route("/api/v1", ...)` group. Handlers use `WriteJSON(w, status, v)` and `WriteError(w, status, code, msg)` (`adapter/http/errors.go`), `http.MaxBytesReader` for bodies, and `json.NewDecoder`. `RouterDeps` is the injection struct; add the service there.
- MCP tools live in `adapter/mcp/tools_*.go`, registered via `RegisterAllTools` (`register.go`) which calls `RegisterXTools(srv)`. Handlers are `server.ToolHandlerFunc` closures over `*Server`; deps come from `srv.deps` (`adapter/mcp/deps.go`). `MarshalResult`, `gomcp.NewToolResultError`, and `gomcp.NewTool(name, WithDescription, WithString(...))` are the helpers (`helpers.go`, `tools_connection.go` is the canonical CRUD model — note its `connectionMeta` response type deliberately omits the secret field).
- **Identity in MCP:** the MCP handler context must carry the `Identity` the same way REST does. The MCP HTTP transport is mounted under the auth middleware (Phase 1 §4 / Task 3), so `srv.deps`-based handlers read identity via `identity.FromContext(ctx)` using the `ctx` passed to the `ToolHandlerFunc`. If the stdio transport path cannot carry an HTTP-injected identity, the stdio server runs in local mode only (default admin), which is the existing single-user assumption — note this explicitly in the tool handlers' nil-identity guard.

## Files to Create/Modify

### Create: `domain/identity/token_service.go`

```go
// TokenService mints, lists, and revokes personal API tokens for the caller
// identified by identity.FromContext. Every method is scoped to that user: a
// caller can never see or revoke another user's token. Admin is irrelevant
// here — token management is always "my own tokens" (admins manage their own
// tokens like anyone else; cross-user token admin is out of scope for Phase 6).
type TokenService struct {
    repo TokenRepository
    now  func() time.Time // injectable clock for tests
}

func NewTokenService(repo TokenRepository) *TokenService

// Mint creates a new token for the caller and returns the plaintext ONCE.
// The returned Token never carries the plaintext; the plaintext string is the
// only secret-bearing value and is the caller's responsibility to surface once.
func (s *TokenService) Mint(ctx context.Context, label string) (plaintext string, t *Token, err error) {
    id, ok := FromContext(ctx)
    if !ok { return "", nil, ErrUnauthenticated }
    if strings.TrimSpace(label) == "" { return "", nil, ErrInvalidLabel }
    plaintext, prefix, hash, err := GenerateToken()
    if err != nil { return "", nil, err }
    tok := &Token{ID: uuid.New(), UserID: id.UserID, Label: label, Prefix: prefix, TokenHash: hash}
    if err := s.repo.Create(ctx, tok); err != nil { return "", nil, err }
    return plaintext, tok, nil
}

func (s *TokenService) List(ctx context.Context) ([]Token, error) {
    id, ok := FromContext(ctx)
    if !ok { return nil, ErrUnauthenticated }
    return s.repo.ListByUser(ctx, id.UserID) // never includes plaintext; hash stripped at surface
}

func (s *TokenService) Revoke(ctx context.Context, tokenID uuid.UUID) error {
    id, ok := FromContext(ctx)
    if !ok { return ErrUnauthenticated }
    return s.repo.Delete(ctx, tokenID, id.UserID) // ErrNotFound if not the caller's token
}
```

Add sentinels in `domain/identity/errors.go` (or the package's existing errors file): `ErrUnauthenticated`, `ErrInvalidLabel`. Reuse `ErrNotFound` from Task 1 for revoke-of-other's-token.

SECURITY: `Mint` is the **only** code path that ever holds the plaintext. It must not log it. The returned `*Token` is the stored view (no `TokenHash` on serialization — see surface DTOs below).

### Create: `adapter/http/token_handler.go`

```go
type TokenHandler struct { svc *identity.TokenService }

func NewTokenHandler(svc *identity.TokenService) *TokenHandler

// tokenView is the read DTO: NO token_hash, NO plaintext.
type tokenView struct {
    ID         string  `json:"id"`
    Label      string  `json:"label"`
    Prefix     string  `json:"prefix"`
    CreatedAt  string  `json:"created_at"`
    LastUsedAt *string `json:"last_used_at,omitempty"`
}

// createTokenResponse is the ONLY response that carries the plaintext, returned
// once at creation. Document loudly that this is the sole opportunity to copy it.
type createTokenResponse struct {
    Token tokenView `json:"token"`
    Plaintext string `json:"plaintext"` // arb_..., shown ONCE
}
```

- `Create` (`POST /tokens`): `MaxBytesReader` 1<<20, decode `{ "label": "..." }`, call `svc.Mint(r.Context(), label)`. Map `identity.ErrInvalidLabel` → 400, `ErrUnauthenticated` → 401 (defense-in-depth; middleware should already block). Return 201 with `createTokenResponse`.
- `List` (`GET /tokens`): `svc.List`, map to `[]tokenView` (strip hash), 200.
- `Revoke` (`DELETE /tokens/{id}`): parse `{id}` via `uuid.Parse`, 400 on bad UUID; `svc.Revoke`; map `ErrNotFound` → 404 (do not distinguish "not found" from "not yours" — both are 404, so a user cannot probe for another's token id); 204 on success.

### Modify: `adapter/http/server.go`

- Add `Tokens *identity.TokenService` to `RouterDeps`.
- Inside `r.Route("/api/v1", ...)`, after the instance routes, add (guarded by `d.Tokens != nil`):

```go
if d.Tokens != nil {
    th := NewTokenHandler(d.Tokens)
    r.Route("/tokens", func(r chi.Router) {
        r.Post("/", th.Create)
        r.Get("/", th.List)
        r.Delete("/{id}", th.Revoke)
    })
}
```

These sit **inside** the authenticated `/api/v1` group, so the auth middleware (Phase 1 / Task 3) has already injected the identity that `TokenService` reads.

### Create: `adapter/mcp/tools_token.go`

Mirror `tools_connection.go`. Add `Tokens *identity.TokenService` to `adapter/mcp/deps.go` `Deps` (optional; nil → "token service not available").

```go
func RegisterTokenTools(srv *Server) {
    srv.AddTool(gomcp.NewTool("token_create",
        gomcp.WithDescription("Mint a personal API token for the calling user. The plaintext arb_... is returned ONCE and never recoverable."),
        gomcp.WithString("label", gomcp.Required(), gomcp.Description("Human label to tell tokens apart"))),
        tokenCreateHandler(srv))
    srv.AddTool(gomcp.NewTool("token_list",
        gomcp.WithDescription("List the calling user's tokens (metadata only; never returns the secret or hash).")),
        tokenListHandler(srv))
    srv.AddTool(gomcp.NewTool("token_revoke",
        gomcp.WithDescription("Revoke (delete) one of the calling user's tokens by id."),
        gomcp.WithString("id", gomcp.Required(), gomcp.Description("Token id"))),
        tokenRevokeHandler(srv))
}
```

Handlers read identity from the tool `ctx` (the same `ctx` the middleware populated for the `/mcp` mount). The `token_create` result includes the plaintext exactly once (the MCP read tools `token_list` use a hash-free meta type identical to `tokenView`). Map `ErrNotFound`→ tool error "token not found", `ErrInvalidLabel`→ "label is required".

### Modify: `adapter/mcp/register.go`

Add `RegisterTokenTools(srv)` to `RegisterAllTools`.

### Modify: `cmd/arbitron/main.go`

- In `initDomain`, build `tokenSvc := identity.NewTokenService(tokenRepo)` and store on `domainStack` (`tokenSvc *identity.TokenService`).
- In `mcpDeps()` set `Tokens: ds.tokenSvc`.
- In `runServe`'s `NewRouterFromDeps(...)` call set `Tokens: ds.tokenSvc`.

## Testing (strict TDD — five scenario categories)

Tests first. New: `domain/identity/token_service_test.go`, `adapter/http/token_handler_test.go`, `adapter/mcp/tools_token_test.go`. Use a fake `TokenRepository` for the service test; the HTTP/MCP tests inject the real service over a fake or in-memory repo and seed identity into the request context via `identity.WithIdentity`.

**`token_service_test.go` (fake repo + `identity.WithIdentity` ctx):**
- **Happy path:** `Mint(ctx, "laptop")` returns a non-empty plaintext, a `*Token` with the caller's `UserID`, the right `Label`/`Prefix`, and the repo received a `Create` whose `TokenHash == HashToken(plaintext)`. `List` returns it; `Revoke` deletes it.
- **Input validation:** `Mint(ctx, "")` and `Mint(ctx, "   ")` → `ErrInvalidLabel`, no `Create` call; `Revoke(ctx, uuid.Nil)` → maps through repo to `ErrNotFound`.
- **Error/failure:** repo `Create` error propagates from `Mint` and **no plaintext is returned** (assert empty string); repo `Delete` returning `ErrNotFound` surfaces from `Revoke`.
- **Boundary:** `List` with zero tokens returns empty non-nil slice; `Mint` then immediate `List` reflects exactly one token; the returned `Token.LastUsedAt` is nil.
- **Isolation/security:** with `ctx` carrying user A, `List` calls `repo.ListByUser(A)` only; `Revoke` calls `repo.Delete(id, A)` (assert the userID arg is A, proving a caller cannot revoke by id alone). With **no** identity in ctx, every method returns `ErrUnauthenticated` and touches the repo zero times. Assert via a log buffer that the plaintext never appears in any log line emitted during `Mint`.

**`token_handler_test.go` (httptest + real service over fake repo):**
- **Happy path:** `POST /tokens {"label":"ci"}` → 201, body has `plaintext` matching `arb_..` and a `token.prefix`; `GET /tokens` → 200 list whose items have **no** `plaintext` and **no** `token_hash` JSON key (assert the raw body string does not contain `"token_hash"` and does not contain the minted plaintext).
- **Input validation:** empty label → 400; malformed JSON → 400; `DELETE /tokens/not-a-uuid` → 400.
- **Error/failure:** `DELETE /tokens/{random-uuid}` (not owned / absent) → 404; service error → 500 path returns a clean error envelope.
- **Boundary:** `GET /tokens` with none → 200 `[]`.
- **Isolation/security:** request context seeded with user B cannot revoke user A's token id → 404 (same shape as absent — no existence oracle). The plaintext appears in exactly one response (create) and never in list.

**`tools_token_test.go` (mirror `tools_connection_test.go`):**
- **Happy path:** `token_create` returns plaintext once; `token_list` returns metadata with no secret/hash; `token_revoke` deletes.
- **Input validation:** missing `label` → tool error; missing/invalid `id` → tool error.
- **Error/failure:** revoke unknown id → "token not found" tool error.
- **Boundary:** `token_list` empty → `[]`.
- **Isolation/security:** nil `Tokens` dep → "token service not available"; the `token_list` JSON never contains `token_hash` or the plaintext.

Run `go build ./...` and `go test ./domain/identity/... ./adapter/http/... ./adapter/mcp/...` green before handoff.
