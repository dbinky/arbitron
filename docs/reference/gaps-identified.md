# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

### GAP-FA1-01: `OIDCConfigured` in `Redact` ignored whitespace-only secret
**Area**: Focus Area 1 — Auth config & mode
**Fix**: Changed `cfg.Auth.OIDC.ClientSecret != ""` to `strings.TrimSpace(cfg.Auth.OIDC.ClientSecret) != ""` in `domain/config/redact.go`, aligning `Redact` with `validateAuth`'s definition of "configured." Added `TestRedact_WhitespaceClientSecret_NotConfigured`.

### GAP-FA1-02: BootstrapAdmins from YAML had no test
**Area**: Focus Area 1 — Auth config & mode
**Fix**: Added `TestLoad_AuthBootstrapAdmins_FromYAML` in `adapter/config/loader_test.go` verifying YAML native-list loading for the security-critical bootstrap admins field.

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
