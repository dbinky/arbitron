# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

## Fixed Previously

### GAP-FA24-P1-01: `authorizeIntervention` missing no-identity fail-closed test
**Area**: Focus Area 24 — Intervention authz & attribution (Pass 1)
**Fix**: Added `TestIntervention_NoIdentity_FailClosed` to `domain/engine/engine_intervention_authz_test.go`. The test calls all six intervention operations (Pause, Resume, Cancel, SetPriority, Retry, Rerun) with `context.Background()` (no identity in context) and asserts each returns `ErrNotFound` and leaves instance state unchanged. This pins the fail-closed invariant in `authorizeIntervention → canView`: `identity.FromContext` returning `!ok` causes `canView` to return `false`, which maps to `ErrNotFound` — byte-for-byte identical to a non-owner request (no existence or state leak). Mirrors the no-identity fail-closed tests added for the WS handler (FA-22 P1), the per-instance WS stream (FA-22 P2), and the conversationHandler (FA-23 P2).

### GAP-FA23-P2-01: `conversationHandler` missing no-identity fail-closed test
**Area**: Focus Area 23 — Interactive participation gate (Pass 2)
**Fix**: Added `TestConversationAuthz_NoIdentity_FailClosed` to `domain/engine/conversation_authz_test.go`. The test calls `HandleMessage` and `EndSession` with `context.Background()` (no identity in context) and asserts: (1) both return `ErrNotFound`, (2) neither returns `ErrNotInteractiveWait` (state not revealed), (3) the LLM stream was never invoked, (4) no interactive events were published, (5) instance state is untouched. This pins the fail-closed invariant in `canView`: `identity.FromContext` returning `!ok` → `canView` returns `false` → `loadAndValidate` wraps `ErrNotFound`. The behavior is identical to a non-owner (no existence leak), which is the expected defense-in-depth response to an unauthenticated caller that somehow bypasses the auth middleware. Mirrors the `TestStreamHandler_ConnectAuthorization` no-identity sub-test added in FA-22 P1 for the WS handler.

### GAP-FA23-P1-01: `interactive_end_session` MCP tool missing non-owner test
**Area**: Focus Area 23 — Interactive participation gate (Pass 1)
**Fix**: Added `"non-owner gets the not-found tool error (gate enforced in the service)"` sub-test to `TestInteractiveEndSession` in `adapter/mcp/tools_interactive_test.go`. The test injects `instance.ErrNotFound` from the mock `ConversationHandler.EndSession` (the error the ownership gate in `loadAndValidate` returns for a non-owner) and asserts the tool result is an error containing "not found" — byte-for-byte identical to a missing instance, no existence leak. This mirrors the equivalent test already present in `TestInteractiveSendMessage`, closing the asymmetric coverage gap. The domain-level gate (`conversation_authz_test.go`) and the HTTP adapter (`TestInteractiveHandler_EndSession_NonOwner`) both already had non-owner tests; the MCP `end_session` surface was the only one that did not.

### GAP-FA22-P2-01: `instance_subscribe` MCP tool had no authorization check
**Area**: Focus Area 22 — WebSocket scoping (Pass 2)
**Fix**: Changed `instanceSubscribeHandler` in `adapter/mcp/tools_streaming.go` to use `ctx context.Context` (was discarding `_`) and call `srv.deps.Engine.GetInstance(ctx, id)` before accepting the subscription. `ErrNotFound` → tool error `"instance not found"` (same text for both a genuinely absent instance and a non-owner's instance — no existence leak). Other errors → generic tool error. Added `TestMCPAuthzE2E_InstanceSubscribe` to `adapter/mcp/authz_e2e_test.go`: wires the real engine, asserts owner can subscribe, non-owner gets tool error containing "not found", and admin can subscribe to any instance. This mirrors the HTTP WS handler's `GetInstance` check before `websocket.Accept`. A nil guard (`srv.deps.Engine == nil`) preserves backward compatibility for existing streaming tests that omit the engine.

Note: `system_subscribe` (MCP) currently delivers no events because `OnEvent` is never wired in production — this is a latent delivery gap, not a security gap, and is out of scope for the current review.

### GAP-FA22-P1-01: Per-instance WS missing no-identity fail-closed test
**Area**: Focus Area 22 — WebSocket scoping (Pass 1)
**Fix**: Added `"no identity in context: 404 before upgrade, same as non-owner (fail-closed, no existence leak)"` sub-test to `TestStreamHandler_ConnectAuthorization` in `adapter/http/stream_handler_test.go`. Wires a getter that returns `ErrNotFound` when `identity.FromContext` reports no identity (replicating the engine's `ownerConstraint` rule) and mounts the handler without the auth middleware. Asserts HTTP 404 before the WebSocket upgrade. This pins the fail-closed invariant: an unauthenticated caller that somehow bypasses the auth middleware is rejected at the service boundary — response is byte-for-byte identical to a non-owner (no existence leak). The system stream handler already had `TestSystemStreamHandler_NoIdentity_Unauthorized` (returns 401); the per-instance stream relies on the service, not a direct identity check, so only the 404 path needed pinning.

### GAP-FA21-P2-01: HTTP `Get` handler missing explicit non-owner isolation test
**Area**: Focus Area 21 — Instance read scoping (Pass 2)
**Fix**: Added `"non-owner gets 404 identical to a missing instance (no existence leak)"` to `TestInstanceHandler_Get` in `adapter/http/instance_handler_test.go`. Seeds a mock service whose `GetInstance` returns `ErrNotFound` (the same signal the scoped engine sends for non-owner access), then asserts HTTP 404 with `NOT_FOUND` code. This mirrors the equivalent test already present in `TestInstanceHandler_GetTrace`, documenting the security invariant: a non-owner cannot confirm an instance exists via the GET endpoint — the response is byte-for-byte identical to a genuinely missing instance. The domain-level engine already has `TestRead_NonOwner_NotFoundNoSubReads`; this test pins that the HTTP adapter correctly threads the error through.

### GAP-FA21-P1-01: `trace_get_node` and `variables_history` had no non-owner isolation test on MCP surface
**Area**: Focus Area 21 — Instance read scoping (Pass 1)
**Fix**: Added `"non-owner gets the same not-found error as a missing instance (no existence leak)"` to `TestTraceGetNode` and `TestVariablesHistory` in `adapter/mcp/tools_trace_test.go`. Each test wires a stub engine whose relevant read method returns `ErrNotFound` (the signal the engine sends for non-owner access), then asserts: (1) the tool result is an error, (2) the error text contains "not found", and (3) the underlying repo (node-exec / snapshot) was **never queried** — proving the authz gate fires before sub-resource access. `TestTraceGet` already had an equivalent test; now all three node-level MCP reads are consistently covered. The `authz_e2e_test.go` covers `instance_get`, `trace_get`, and `variables_get` with a real engine; `trace_get_node` and `variables_history` are now also pinned at the handler level.

### GAP-FA20-P2-01: HTTP `List` no-identity defence-in-depth path had no test
**Area**: Focus Area 20 — Instance list scoping (Pass 2)
**Fix**: Added `"no-identity defence-in-depth: ErrNotFound from service returns 200 empty array"` subtest to `TestInstanceHandler_List` in `adapter/http/instance_handler_test.go`. Seeds a mock service that returns `instance.ErrNotFound` from `ListInstances` (the signal the engine sends when no identity is present in context), calls `h.List`, and asserts HTTP 200 with an empty JSON array. This pins the unusual-but-intentional behavior: the handler does NOT return 404 or 500 when the engine refuses to list due to a missing identity — it returns an empty list so a probe cannot distinguish "no instances" from "access denied". The auth middleware in org mode would have already rejected such a request before reaching the handler; this test documents the defense-in-depth fallback.

### GAP-FA20-P1-01: MCP `TestInstanceList` missing context-forwarding assertion — identity not verified on `ListInstances` path
**Area**: Focus Area 20 — Instance list scoping (Pass 1)
**Fix**: Added `capturedListCtx context.Context` field to `stubEngine` in `adapter/mcp/tools_instance_test.go`. Updated `ListInstances` to always capture `ctx` before delegating to `listFn`. Added `"caller identity context is forwarded to engine"` subtest to `TestInstanceList`: calls `instance_list` via `callTool` (which injects `mcpTestUserID`), then asserts `identity.FromContext(eng.capturedListCtx)` returns the expected test user ID. A regression where the handler passed `context.Background()` instead of the call context would immediately fail, breaking the `ownerConstraint` scoping in the domain engine.

### GAP-FA19-P2-01: HTTP and MCP Trigger stubs discarded context — identity forwarding not verified on adapter surfaces
**Area**: Focus Area 19 — Flat-store migration & run-shared-directly (Pass 2)
**Fix**: Added `capturedTriggerCtx context.Context` field to `mockWorkflowService` (HTTP) and `stubEngine` (MCP). Both stubs now capture the context passed to their `Trigger` method. Added `"caller identity context is forwarded to service"` subtest to `TestWorkflowHandler_Trigger` and `"caller identity context is forwarded to engine"` subtest to `TestInstanceTrigger`. Both assert that `identity.FromContext(capturedCtx)` returns the expected test user ID. A regression where the handler accidentally passed `context.Background()` instead of the request context would now be caught immediately on both surfaces.

### GAP-FA19-P1-01: `Trigger` missing error-category test — no-identity + default-user unavailable
**Area**: Focus Area 19 — Flat-store migration & run-shared-directly (Pass 1)
**Fix**: Added `defaultUserErr error` field to `mockUserRepo` in `domain/engine/engine_test.go`. Added `TestTrigger_DefaultUserUnavailable_FailsClosed` in `domain/engine/engine_trigger_scope_test.go`: seeds `defaultUserErr` to simulate a DB failure, calls `Trigger` with no identity in context, asserts error containing "failed to resolve triggering user" and confirms no instance was created. Documents the fail-closed invariant: a system trigger with an unavailable default user must not create an instance with a nil OwnerID, which would break all visibility scoping.

### GAP-FA18-P2-01: MCP `safeWorkflowName` accepted `"."` — HTTP correctly rejected it
**Area**: Focus Area 18 — Publish/copy & publisher store (Pass 2)
**Fix**: Added `&& name != "."` to `safeWorkflowName` in `adapter/mcp/helpers.go` to match HTTP's implementation. `"."` resolves via `filepath.Join(scopeRoot, ".")` to the scope root itself, which `resolveWorkflowDir` catches with `ErrInvalidName` — but that falls to the `default:` handler case and returns an opaque error. The fix catches it at the adapter boundary, consistent with how HTTP handles it. Added `TestWorkflowTool_Publish_DotName` and `TestWorkflowTool_Copy_DotName` (4 subtests) to pin the behavior on both source and destination names.

### GAP-FA17-P2-01: HTTP and MCP surfaces had no test for `ErrSharedReadOnly` → 409/tool-error on `Update`/`Delete`
**Area**: Focus Area 17 — Workflow service authz (Pass 2)
**Fix**: Added `"returns 409 when workflow is shared-only"` to `TestWorkflowHandler_Update` and `TestWorkflowHandler_Delete` in `adapter/http/workflow_handler_test.go`, and `"shared-only workflow returns tool error"` to `TestWorkflowUpdate` and `TestWorkflowDelete` in `adapter/mcp/tools_workflow_test.go`. Both surfaces now pin that `ErrSharedReadOnly` from the domain service is correctly mapped to HTTP 409 Conflict / MCP tool error with "shared and read-only" text. Also confirmed that `withChiURLParam` automatically injects `testIdentity` into all HTTP handler tests, so the service's `caller()` check is always satisfied.

### GAP-FA17-P1-01: `Delete`, `WriteFile`, `DeleteFile`, `SaveNodePositions`, and `ListFiles` had zero service-level tests
**Area**: Focus Area 17 — Workflow service authz (Pass 1)
**Fix**: Added 25 tests to `domain/workflow/service_test.go` covering all five previously untested public methods, with 5 scenario categories each. Key invariants pinned: (a) `Delete`/`WriteFile`/`DeleteFile`/`SaveNodePositions` on a shared-only workflow returns `ErrSharedReadOnly`, not `ErrNotFound` — via `translateWrite`; (b) each write method only targets the caller's own private scope, never another user's; (c) no-identity context is rejected with `ErrInvalidName` on every write path; (d) `ListFiles` falls back to shared and does not expose another user's private files.

### GAP-FA16-P2-01: HTTP and MCP `ReadFile` tests never verified owner threading from context to FileReader
**Area**: Focus Area 16 — Scope resolution at call sites (Pass 2)
**Fix**: Added `capturedOwner uuid.UUID` field to `contentFileReader` (HTTP) and `stubFileReader` (MCP); each `ReadFile` method now records the owner it receives before dispatching to the configurable `readFileFn`. Added `TestWorkflowHandler_ReadFile/"caller identity UserID is forwarded to file reader as owner"` and `TestWorkflowReadFile/"caller identity UserID is forwarded to file reader as owner"` — both assert that the captured owner equals the known test identity UserID (`testUserID` / `mcpTestUserID`). A regression in `callerOwner(ctx)` wiring (e.g., passing `uuid.Nil` or the wrong user) is now caught immediately on both surfaces.

### GAP-FA16-P1-01: `LoadVisible`, `ListFilesVisible`, `ReadScopes`, and `OwnerFromContext` had zero test coverage
**Area**: Focus Area 16 — Scope resolution at call sites (Pass 1)
**Fix**: Created `domain/workflow/resolve_test.go` with 12 tests covering `ReadScopes` (valid owner, ordering), `LoadVisible` (private hit, shared fallback, neither found, non-ErrNotFound aborts, private-precedes-shared), and `ListFilesVisible` (same five categories). Created `domain/engine/runcontext_test.go` with 5 tests covering `OwnerFromContext` (set+get, absent returns uuid.Nil, child context inherits, shadow override, explicit uuid.Nil propagation). Also added `baseRepo`, `fakeFilesRepo`, and error-injection stubs for the resolve tests so the abort-on-non-ErrNotFound invariant can be pinned at the domain level.

### GAP-FA15-P2-01: `WriteFile`/`DeleteFile` did not protect `workflow.json` from direct targeting
**Area**: Focus Area 15 — Workflow scope & repo (Pass 2)
**Fix**: Added a manifest-protection guard to both `WriteFile` and `DeleteFile` in `adapter/filesystem/workflow_repo.go`. After the traversal check resolves `targetPath`, both methods now compare it to `filepath.Join(wfDir, "workflow.json")` and return `ErrInvalidFilePath` if they match. The manifest is a structured domain object; allowing raw bytes through the file API would bypass schema validation and could leave the workflow directory in an unloadable state. Both HTTP (`PUT /workflows/{name}/files/*`) and MCP (`workflow_write_file`/`workflow_delete_file`) already handle `ErrInvalidFilePath` with a 400/tool-error response, so no adapter changes were needed. Added `TestWorkflowRepo_WriteFile_ManifestProtected` and `TestWorkflowRepo_DeleteFile_ManifestProtected` to pin the new guard.

### GAP-FA15-P1-01: `SaveNodePositions` had zero test coverage
**Area**: Focus Area 15 — Workflow scope & repo (Pass 1)
**Fix**: Added five tests to `adapter/filesystem/workflow_repo_test.go`: `TestWorkflowRepo_SaveNodePositions_HappyPath` (positions persisted atomically, no .tmp artifacts remain, node Position fields set on reload), `TestWorkflowRepo_SaveNodePositions_NotFound` (ErrNotFound for missing workflow), `TestWorkflowRepo_SaveNodePositions_PathTraversalRejected` (traversal name mapped to ErrNotFound, consistent with Load), `TestWorkflowRepo_SaveNodePositions_EmptyPositionsMap` (no-op, manifest intact), and `TestWorkflowRepo_SaveNodePositions_UnknownNodeIDsIgnored` (positions whose IDs don't match any node are silently skipped, manifest remains valid). Also added the `strings` import required by the atomic-write artifact assertion in the happy-path test.

### GAP-FA14-P2-01: Successful demotion (admin → user) never tested on REST or MCP surfaces
**Area**: Focus Area 14 — Role management (Pass 2)
**Fix**: Extended `TestUserHandler_HappyPath` in `adapter/http/user_handler_test.go` and `TestUserTools_HappyPath` in `adapter/mcp/tools_user_test.go` to include a successful demotion after promotion. Both tests seed a second admin to satisfy the last-admin floor, then demote the original target and assert 200/non-error + role is now `user`. This proves the full promote→demote round-trip works end-to-end on both surfaces and that the domain's authorization is wired correctly.

### GAP-FA14-P1-02: MCP `user_set_role` invalid UUID path untested
**Area**: Focus Area 14 — Role management (Pass 1)
**Fix**: Added `"id": "not-a-uuid"` case to `TestUserTools_Validation` in `adapter/mcp/tools_user_test.go`. The `userSetRoleHandler` does `uuid.Parse(idStr)` and returns "invalid id" — this validation path was never exercised despite the other missing-id and bad-role cases being tested.

### GAP-FA14-P1-01: `UserService.List` error scenario untested; `SetRole` unauthenticated path untested on REST; MCP unauthenticated untested
**Area**: Focus Area 14 — Role management (Pass 1)
**Fix**: Added `List` error propagation to `TestUserService_Errors` in `domain/instance/user_service_test.go` — the `listErr` field on `fakeUserSvcRepo` was never exercised. Added unauthenticated `SetRole` → 401 to `TestUserHandler_Security` in `adapter/http/user_handler_test.go` (only `List` unauthenticated was tested). Added `context.Background()` calls to `TestUserTools_Security` in `adapter/mcp/tools_user_test.go` for both `user_list` and `user_set_role` → "authentication required". All three gaps document that the admin gate fails closed with no identity in context.

### GAP-FA12-P2-01: `/auth/device/token` response had no `token_hash` non-leakage assertion; Start had no `hashed_code` pin
**Area**: Focus Area 12 — Device-code login (Pass 2)
**Fix**: Extended `TestDeviceHandler_HappyPath` in `adapter/http/device_handler_test.go`. Added `assert.NotContains(wt.Body.String(), "token_hash", ...)` to the Token endpoint response — the device token endpoint returns a newly minted arb_ access_token (highest-risk surface) and must never echo the internal token hash. Added `assert.NotContains(w.Body.String(), "hashed_code", ...)` and `assert.NotContains(w.Body.String(), "device_code_hash", ...)` to the Start response — the Start response returns the plaintext device_code (correct, once) but must never include the sha256 hash. Mirrors the FA-9 secrecy pin pattern applied to token_create and token_list.

### GAP-FA12-P1-01: `Deny` HTTP handler completely untested; `expired_token` RFC path uncovered at handler level
**Area**: Focus Area 12 — Device-code login (Pass 1)
**Fix**: Added three tests for the `Deny` handler in `adapter/http/device_handler_test.go`: `TestDeviceHandler_Deny_HappyPath` (happy path → 200 `{"status":"denied"}`, repo row status confirmed Denied), `TestDeviceHandler_Deny_RequiresIdentity` (no identity → 401, row stays Pending), `TestDeviceHandler_Deny_NotFound` (unknown user_code → 404). Also expanded `TestDeviceHandler_PollStates` into subtests and added a third: "expired → expired_token" — backdates the stored row's ExpiresAt via the memDeviceRepo and asserts the handler returns 400 with body `"expired_token"`. Previously `writeDeviceGrantError`'s `ErrExpired` branch was dead from a test perspective at the HTTP layer.

### GAP-FA11-P2-01: REST/MCP/WS surface-parity test checked status codes but not resolved identity
**Area**: Focus Area 11 — Bearer-token auth path (Pass 2)
**Fix**: Strengthened `TestAuthMiddleware_Bearer_Security` "REST/MCP/WS identical under bearer" in `adapter/http/auth_middleware_test.go`. The existing test loop used `_, plaintext := seedBearer(...)` (discarding the UID) and only asserted `rec.Code == 200`, proving access was granted but not that it was granted to the correct user. Changed to capture `uid` from `seedBearer`, added `assert.Equal(t, uid, probe.id.UserID, ...)` and `assert.Equal(t, "carol", probe.id.Username, ...)` for each surface. This pins the cross-surface identity parity guarantee: a bearer token authenticates as its exact owner on REST, /mcp, and /ws — any divergence in the identity derivation path would be caught immediately.

### GAP-FA11-P1-01: `TouchLastUsed` channel drain discarded the touched token ID — correct token not pinned
**Area**: Focus Area 11 — Bearer-token auth path (Pass 1)
**Fix**: Strengthened `TestAuthMiddleware_Bearer_HappyPath` in `adapter/http/auth_middleware_test.go`. The existing `select { case <-tokens.touchedIDs: }` verified the goroutine fired but discarded the ID, proving only that *some* touch happened, not that the *correct* token was named. Replaced with `case touchedID := <-tokens.touchedIDs: assert.Equal(t, tokens.byHash[hash].ID, touchedID, ...)`. Now pins the invariant that the async `go func(tid uuid.UUID)` closure captures `tok.ID` from the hash-lookup result, not an ambient variable or a different token. A future refactor that accidentally shadowed the captured ID would be caught immediately.

## Fixed Previously

### GAP-FA13-P2-01: OIDC callback CSRF guard has no explicit edge-case tests for absent state credentials
**Area**: Focus Area 13 — JIT provisioning & bootstrap admin (Pass 2)
**Fix**: Added `TestAuthHandler_Callback_MissingStateCredentials_Rejected` in `adapter/http/auth_handler_test.go` as a table test covering two sub-cases: (1) no state cookie — an attacker-initiated OAuth flow where the server has no record of starting a login round-trip (direct CSRF attack); (2) no `state` query param — an incomplete or tampered callback URL. Both must return 400 and create no session. The code already handles these via `!ok` and `gotState == ""` guards, but neither case had an explicit test pinning the behavior.

### GAP-FA13-P1-01: `UpdateUsername` collision tolerance and `pickUsername` subject-fallback untested
**Area**: Focus Area 13 — JIT provisioning & bootstrap admin (Pass 1)
**Fix**: Added `TestProvisioner_Resolve_UpdateUsernameCollisionTolerated` in `domain/instance/provisioner_test.go`. Sets `updateNameErr` on the fake repo and verifies that `Resolve` still returns a user with the updated in-memory username and does NOT create a new user — documenting the explicitly-designed best-effort refresh behavior (the code does `_ = p.users.UpdateUsername(...)` with a comment explaining username collision must not abort login). Also added `TestProvisioner_Resolve_UsernameSubjectFallback` verifying the third tier of `pickUsername`: when both `Username` and `Email` are empty, the subject is used as the display name.

### GAP-FA10-P2-01: MCP cross-user revoke was untested — no existence-oracle check on MCP surface
**Area**: Focus Area 10 — Token service & surface (Pass 2)
**Fix**: Extended `TestTokenTools_Isolation` in `adapter/mcp/tools_token_test.go`. After asserting B's list is empty, added: B calls `token_revoke` with A's token ID → `revokeRes.IsError == true`, body contains "token not found", `repo.toks` still has 1 entry. This pins the isolation guarantee on the MCP surface: the error message is the same as "not found" (not "not yours"), so neither the existence of the token nor A's userID is revealed. The REST surface had this covered in `TestTokenHandler_Isolation`; now both surfaces are symmetric.

### GAP-FA10-P1-01: REST `Revoke` happy path (204, token removed, list empty) was untested
**Area**: Focus Area 10 — Token service & surface (Pass 1)
**Fix**: Added `TestTokenHandler_Revoke_HappyPath` in `adapter/http/token_handler_test.go`. Creates a token, revokes it with the correct owner identity, then asserts 204 response, empty `repo.toks`, and an empty subsequent list. The MCP surface (`TestTokenTools_HappyPath`) already covered the successful revoke path; the REST surface was missing this scenario. Previously only the failure paths (not-found → 404, cross-user → 404, bad UUID → 400) were tested.

### GAP-FA9-P2-01: Create response was not checked for `token_hash` absence on REST and MCP
**Area**: Focus Area 9 — API tokens store & format (Pass 2)
**Fix**: Added `assert.NotContains(t, w.Body.String(), "token_hash", ...)` to `TestTokenHandler_CreateThenList_HappyPath` in `adapter/http/token_handler_test.go`, and `assert.NotContains(t, body, "token_hash", ...)` to `TestTokenTools_HappyPath` in `adapter/mcp/tools_token_test.go`. The create endpoint is the highest-risk surface because it already carries the plaintext — accidentally including the hash here is maximally dangerous. The list response was already checked; now both create and list are pinned on both surfaces. The implementation is architecturally safe (`tokenView`/`tokenMeta` DTOs have no `TokenHash` field), but the test documents the security intent.

### GAP-FA9-P1-01: `TokenRepo.Delete` happy path (own-token revocation) was untested
**Area**: Focus Area 9 — API tokens store & format (Pass 1)
**Fix**: Added `TestTokenRepo_Delete_OwnToken` in `adapter/sqlite/token_repo_test.go`. Mints a token, deletes it with the correct owner ID, then asserts `GetByHash` returns `ErrNotFound` and `ListByUser` returns an empty slice. Documents that revocation = row delete and that the deleted token is immediately unresolvable by hash. Previously only error paths (non-existent ID, cross-user delete) were tested; the success path — the most common operational path — was absent.

### GAP-FA8-P2-01: State cookie security attributes had no test — HttpOnly/SameSite/MaxAge regression undetected
**Area**: Focus Area 8 — `/auth` handlers (Pass 2)
**Fix**: Added `TestAuthHandler_StateCookie_Attributes` in `adapter/http/auth_handler_test.go`. Calls `Login`, reads the `arbitron_oauth_state` cookie, and asserts `HttpOnly=true`, `SameSite=Lax`, `MaxAge=600`, `Secure=true`, `Path="/"`, and non-empty value. The state cookie carries the HMAC-signed OIDC state+nonce payload; if any of these attributes were accidentally removed, the CSRF protection for the login round-trip would be silently weakened. The `TestAuthHandler_SessionCookie_Attributes` test already covered the session cookie — now both cookies are pinned.

### GAP-FA8-P1-01: Provisioner Create failure had no test — session cookie could theoretically appear on partial state
**Area**: Focus Area 8 — `/auth` handlers (Pass 1)
**Fix**: Added `failingCreateUserRepo` (wraps `fakeUserRepo`, returns `errors.New("db write error")` from `Create`) and `TestAuthHandler_Callback_ProvisionError_500` in `adapter/http/auth_handler_test.go`. Seeds `fakeUserRepo` with no pre-existing user for subject `"new-sub"` so `GetBySubject` returns `ErrNotFound`, then `Create` fails. Asserts: 500 response, `sessions.byToken` empty, no `arbitron_session` cookie. Documents the "error" scenario category for JIT provisioning — a DB failure during `resolveOrProvision` must never produce a session or session cookie.

### GAP-FA7-P2-01: Browser-nav redirect did not have a test for URLs with query parameters
**Area**: Focus Area 7 — Auth middleware (Pass 2)
**Fix**: Added `t.Run("deep-link with query params fully encoded in next")` inside `TestAuthMiddleware_Org_BrowserNav_Redirects`. Sends an unauthenticated GET to `/instances/abc?tab=logs&page=2` with `Accept: text/html` and asserts the redirect Location is `/auth/login?next=%2Finstances%2Fabc%3Ftab%3Dlogs%26page%3D2`. Documents that `url.QueryEscape(r.URL.RequestURI())` correctly encodes both the path separator and the query string delimiters, so the user lands back at the exact deep-linked URL after login.

### GAP-FA7-P1-01: Local-mode error path (GetDefaultUser failure) had no test
**Area**: Focus Area 7 — Auth middleware (Pass 1)
**Fix**: Added `errUserRepo` wrapper (returns `errors.New("db unavailable")` from `GetDefaultUser`) and `TestAuthMiddleware_Local_DefaultUserError_FailsClosed`. The test sends three requests and asserts all get 401 — documenting that the `sync.Once`-cached error is permanent (fail-closed) and the handler is never invoked. Covers the "error" scenario category for local mode that was previously missing.

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
