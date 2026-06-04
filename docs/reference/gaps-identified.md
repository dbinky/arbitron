# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

## Fixed Previously

### GAP-FA6-P2-02: No test for ID token without nonce claim — empty nonce must be rejected, not echoed
**Area**: Focus Area 6 — OIDC adapter (Pass 2)
**Fix**: Added `TestProvider_Exchange_EmptyNonce_Passthrough` in `adapter/oidc/provider_test.go`: deletes `nonce` from the ID token claims map, calls `Exchange`, and asserts `claims.Nonce == ""` — documenting that Exchange never injects a nonce and the empty value propagates to the caller. Added `omitNonce bool` field to `fakeOIDCProvider` and `TestAuthHandler_Callback_EmptyNonce_Rejected` in `adapter/http/auth_handler_test.go`: forces Exchange to return `Nonce:""` (as a real nonce-less IdP would) and asserts the callback returns 400 + no session cookie. An absent nonce is indistinguishable from a replay attempt and must be rejected.

### GAP-FA6-P2-01: OIDC nonce check deferred — ID token nonce never verified against expected nonce
**Area**: Focus Area 6 — OIDC adapter (Pass 1)
**Fix**: Added `Nonce string` to `identity.Claims` in `domain/identity/ports.go`. Updated `adapter/oidc/provider.go` `Exchange` to return `c.Nonce`. Replaced `_ = wantNonce` in `adapter/http/auth_handler.go` `Callback` with `subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(wantNonce)) != 1` → 400 Bad Request. Updated `fakeOIDCProvider` in `adapter/http/auth_handler_test.go` to capture the minted nonce in `AuthCodeURL` and echo it back from `Exchange` (so existing callback tests pass). Added `TestAuthHandler_Callback_NonceMismatch` which seeds `claims.Nonce = "WRONG_NONCE"` and asserts 400 + no session cookie. Added `TestProvider_Exchange_Success` assertion that `claims.Nonce == "nc"`. The replay guard is now active end-to-end.

### GAP-FA6-P1-01: OIDC adapter verifier checks for wrong-issuer, expired-token, and username fallback not tested
**Area**: Focus Area 6 — OIDC adapter (Pass 1)
**Fix**: Added `TestProvider_Exchange_WrongIssuer` (token with mismatched `iss` but valid signature must be rejected), `TestProvider_Exchange_ExpiredToken` (expired ID token must be rejected), and `TestProvider_Exchange_UsernameFallbackChain` (table test covering all four fallback levels: `preferred_username` → `name` → `email` → `subject`). These complete the security verifier check coverage alongside the existing bad-signature and wrong-audience tests.

### GAP-FA5-P2-01: No adapter-level test that `SessionRepo.Get` never repopulates `Session.ID`
**Area**: Focus Area 5 — Session store (Pass 2)
**Fix**: Added `TestSessionRepo_Get_DoesNotRepopulateID` in `adapter/sqlite/session_repo_test.go`. Stores a session with a known sentinel raw token, calls `Get`, and asserts `got.ID == ""` and that the sentinel string does not appear in the returned `Session.ID`. Guards against any future regression where someone adds `ID: row.IDHash` (which would expose the SHA-256 hash) or attempts to reconstruct the raw token.

### GAP-FA5-P1-01: `SweepExpiredSessions` had no test coverage
**Area**: Focus Area 5 — Session store (Pass 1)
**Fix**: Added `sweepFakeRepo` (in-memory `identity.SessionRepository` with per-call error injection), `TestSweepExpiredSessions_TicksAndStops` (verifies DeleteExpired is called on each tick and goroutine exits cleanly on context cancel — no goroutine leak), and `TestSweepExpiredSessions_ErrorIsLoggedNotFatal` (verifies a transient DeleteExpired error is logged but the loop continues). Tests use 5 ms ticks with `require.Eventually` to remain non-flaky.

### GAP-FA4-P2-01: `User.Subject` and `User.Email` had no `json:"-"` tag; MCP had no IdP-field non-leakage assertions
**Area**: Focus Area 4 — Users schema & repo (Pass 2)
**Fix**: Added `json:"-"` to `User.Subject` and `User.Email` in `domain/instance/user.go`. Added `TestUser_JSONMarshal_IdPFieldsNeverLeak` in `domain/instance/user_test.go`. Strengthened `TestUserHandler_HappyPath` (HTTP) to seed Subject/Email values and assert both values and field names are absent from the list response. Strengthened `TestUserTools_HappyPath` (MCP) with the same assertions for `user_list`. Pattern mirrors the FA-3 `Token.TokenHash`/`DeviceCode.HashedCode` fix.

### GAP-FA4-P1-01: `UpdateUsername`, `List`, `UpdateRole`, `CountAdmins` had no test coverage
**Area**: Focus Area 4 — Users schema & repo (Pass 1)
**Fix**: Added 6 tests to `adapter/sqlite/user_repo_test.go`: `TestUserRepo_UpdateUsername_ChangesDisplayName`, `TestUserRepo_UpdateUsername_NonExistentUserNoError` (documents silent-no-op contract), `TestUserRepo_List_ReturnsAllUsers`, `TestUserRepo_UpdateRole_ChangesRole`, `TestUserRepo_UpdateRole_NonExistentUserNoError`, `TestUserRepo_CountAdmins_ReflectsCurrentAdmins` (covers initial state, add second admin, demote back to 1).

### GAP-FA3-P2-01: `Token.TokenHash` and `DeviceCode.HashedCode` had no `json:"-"` tag
**Area**: Focus Area 3 — Identity ports (Pass 2)
**Fix**: Added `json:"-"` to `Token.TokenHash` and `DeviceCode.HashedCode` in `domain/identity/ports.go`. Added `TestToken_JSONMarshal_HashNeverLeaks` and `TestDeviceCode_JSONMarshal_HashedCodeNeverLeaks` in `ports_test.go` — each marshals the struct directly and asserts neither the field value nor field name (`"TokenHash"`, `"token_hash"`, `"HashedCode"`, `"hashed_code"`) appears in the output. The tag makes the secrecy guarantee self-enforcing at the type level rather than relying solely on DTOs and documentation.

### GAP-FA1-01: `OIDCConfigured` in `Redact` ignored whitespace-only secret
**Area**: Focus Area 1 — Auth config & mode
**Fix**: Changed `cfg.Auth.OIDC.ClientSecret != ""` to `strings.TrimSpace(cfg.Auth.OIDC.ClientSecret) != ""` in `domain/config/redact.go`, aligning `Redact` with `validateAuth`'s definition of "configured." Added `TestRedact_WhitespaceClientSecret_NotConfigured`.

### GAP-FA1-02: BootstrapAdmins from YAML had no test
**Area**: Focus Area 1 — Auth config & mode
**Fix**: Added `TestLoad_AuthBootstrapAdmins_FromYAML` in `adapter/config/loader_test.go` verifying YAML native-list loading for the security-critical bootstrap admins field.

### GAP-FA1-P2-01: No surface-level secrecy test for OIDC secret on REST/MCP
**Area**: Focus Area 1 — Auth config & mode (Pass 2)
**Fix**: Added `TestRouter_GetConfig_AuthSection_NeverLeaksOIDCSecret` and `TestRouter_GetConfig_AuthSection_LocalMode` to `adapter/http/server_test.go`, and `TestSystemConfig_NeverLeaksOIDCSecret` to `adapter/mcp/tools_system_test.go`. Both surfaces now have a regression guard for the OIDC client-secret / field-value leak, mirroring the existing LLM API key and Teams webhook tests.

### GAP-FA1-03: Session CookieSecure=false via YAML had no test
**Area**: Focus Area 1 — Auth config & mode
**Fix**: Added `TestLoad_AuthSession_CookieSecure_Override` in `adapter/config/loader_test.go` verifying CookieSecure can be set to false via YAML for local HTTP dev.

## Won't Fix (Beyond Current Scope)

_(items logged here are accepted limitations or out-of-scope enhancements — only the user can add to or move items in this section. Pre-seeded from the Phase 6 spec's out-of-scope boundary so the loop does NOT spend iterations building deferred work.)_

### SCOPE-W01: Scheduled & webhook triggers
**Area**: Triggers
**Detail**: Scheduled (cron) and webhook triggers are **Phase 7**, explicitly out of scope for Phase 6 (spec "Overview"). Do not build trigger types here.

### SCOPE-W02: Parallel fan-out / fan-in execution
**Area**: Engine
**Detail**: Fan-out/fan-in parallel branch execution is **Phase 8**, out of scope for Phase 6. Do not build it here.

### SCOPE-W03: Granular per-token permission scopes
**Area**: Personal API tokens
**Detail**: A personal API token carries the **full permissions of its owning user**; per-token granular scopes are explicitly deferred (spec §2). Tokens are made safe by being individually revocable. Do not add scope/permission fields to tokens in this phase.

### SCOPE-W04: Resource-server / per-request IdP token validation
**Area**: Auth model
**Detail**: The credential model is deliberately Arbitron-native (sessions + tokens; IdP only at login). Validating IdP-issued JWTs on every request was considered and rejected. Do not introduce per-request IdP/JWKS validation.

### SCOPE-W05: Admin approval gate for publishing
**Area**: Publishing
**Detail**: Publishing is **self-service** by design (any User may publish; spec §4 "Defaults"). Do not add an admin approval workflow.
