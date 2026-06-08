# Org-Readiness Phase 1 — Task 2: Edge JWT verification adapter (`adapter/edge`)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans, and superpowers:test-driven-development for every code change. Steps use checkbox (`- [ ]`) syntax.

**Objective:** Implement `identity.EdgeVerifier` in a new driven adapter `adapter/edge` that independently verifies a Cloudflare Access JWT (signature against the team-domain JWKS, issuer, audience, expiry/not-before) and extracts the verified `email` claim — reusing the `github.com/coreos/go-oidc/v3` verifier pattern from `adapter/oidc`, with no hand-rolled JWT crypto.

**Spec anchor:** Phase 1 — "The edge token is independently verified, not merely trusted"; "Identity is the verified email address".
**Design anchor:** §Adapter (`adapter/edge/verifier.go`), §Error handling/security, §Testing (verifier bullet).

**Dependencies:** Task 1 (`config.EdgeConfig`, `identity.EdgeVerifier`). No dependency on Tasks 3–4.

**Tech stack:** Go; `github.com/coreos/go-oidc/v3/oidc` (already in `go.mod` v3.18.0, used by `adapter/oidc`). Tests use `net/http/httptest` + a locally generated RSA key signed with `github.com/go-jose/go-jose/v4` (the exact stack `adapter/oidc/provider_test.go` already uses) so signatures are real and deterministic.

---

## Files to create / modify

- Create: `adapter/edge/verifier.go` — `Verifier` implementing `identity.EdgeVerifier`, plus a `Config` and `New`.
- Create: `adapter/edge/verifier_test.go` — httptest JWKS harness + scenario tests.

---

## Design of the adapter (read before coding)

Cloudflare Access is OIDC-discoverable: the team domain serves a discovery document and a JWKS. The issuer (`iss`) of an Access JWT is the team domain (`https://<team>.cloudflareaccess.com`); the JWKS lives at `<teamdomain>/cdn-cgi/access/certs`.

Two construction strategies, in preference order — try discovery first (matches `adapter/oidc.New` exactly), fall back to an explicit remote key set if discovery is unavailable:

1. **Preferred (mirror `adapter/oidc`):** `prov, err := coreoidc.NewProvider(ctx, cfg.TeamDomain)` then `prov.Verifier(&coreoidc.Config{ClientIDs: cfg.Audiences})` (or `ClientID` with a single aud; use the multi-aud config when the library exposes it — verify against the vendored v3.18.0 API and pick the field that checks `aud ∈ Audiences`). This makes the boot-time network call and fails fast on an unreachable/non-compliant team domain, exactly like the OIDC provider.
2. **Fallback (explicit JWKS):** if the team domain does not serve standard discovery, build `keySet := coreoidc.NewRemoteKeySet(ctx, cfg.TeamDomain + "/cdn-cgi/access/certs")` and `coreoidc.NewVerifier(cfg.TeamDomain /*issuer*/, keySet, &coreoidc.Config{ClientIDs: cfg.Audiences})`.

The test harness (below) must serve BOTH `/.well-known/openid-configuration` and the JWKS endpoint so whichever strategy is implemented is exercised by real signatures. Decide the strategy during implementation; the tests assert behavior, not the construction path.

The verifier enforces `iss`, `aud`, signature, and `exp` via `verifier.Verify`. **`nbf` (not-before):** go-oidc validates `exp` and (when present) `iat`/`nbf` skew per its `Config`; if the vendored version does not enforce `nbf` by default, add an explicit `nbf` check after decoding claims so the not-yet-valid scenario passes (the test asserts rejection regardless). Keep the default leeway tight; do not widen clock skew.

---

## TDD plan

### Step group A — harness + happy path

- [ ] **A1. Build the JWKS harness.** In `adapter/edge/verifier_test.go` (package `edge_test`), adapt the `idpHarness` from `adapter/oidc/provider_test.go`:
  - Generate `rsa.GenerateKey(rand.Reader, 2048)` once per harness; serve a JWKS at the path the chosen strategy fetches (serve BOTH `/.well-known/openid-configuration` — with `issuer`, `jwks_uri` pointing at the cert path — AND the cert endpoint `/cdn-cgi/access/certs`).
  - Set `h.issuer = srv.URL` (so issuer == team domain == `srv.URL`).
  - Provide `signToken(t, signWith *rsa.PrivateKey, claims map[string]any) string` using `jose.NewSigner(RS256, kid)` + `jwt.Signed(...).Serialize()` (copy from the oidc test).
  - `baseClaims(issuer, aud)` helper: `iss`, `aud`, `sub`, `email`, `exp = now+1h`, `iat = now`.
- [ ] **A2. Failing happy-path test.** `TestVerify_ValidToken_ReturnsEmail`:
  - `v, err := edge.New(ctx, edge.Config{TeamDomain: h.issuer, Audiences: []string{"app-aud"}})`; `require.NoError`.
  - Sign `baseClaims(h.issuer, "app-aud")` with `email = "alice@corp.example"`.
  - `email, err := v.Verify(ctx, raw)` → `require.NoError`, `assert.Equal("alice@corp.example", email)`.
  Run `go test ./adapter/edge/ -v` → FAIL (`adapter/edge` does not exist).
- [ ] **A3. Implement `Config`, `New`, `Verify` (minimal).** Create `adapter/edge/verifier.go`:
  - `type Config struct { TeamDomain string; Audiences []string }`.
  - `type Verifier struct { verifier *coreoidc.IDTokenVerifier; issuer string }` with `var _ identity.EdgeVerifier = (*Verifier)(nil)`.
  - `New(ctx, cfg)` builds the verifier per the chosen strategy above; returns an error on discovery/JWKS failure (fail-fast at boot).
  - `Verify(ctx, raw)`:
    ```go
    if strings.TrimSpace(raw) == "" {
        return "", fmt.Errorf("edge: empty token")
    }
    tok, err := v.verifier.Verify(ctx, raw)
    if err != nil {
        return "", fmt.Errorf("edge: token verification: %w", err)
    }
    var c struct {
        Email string `json:"email"`
    }
    if err := tok.Claims(&c); err != nil {
        return "", fmt.Errorf("edge: decoding claims: %w", err)
    }
    if strings.TrimSpace(c.Email) == "" {
        return "", fmt.Errorf("edge: missing email claim")
    }
    return c.Email, nil
    ```
  Run the test → PASS.

### Step group B — error / failure scenarios (each its own sub-test)

Add `TestVerify_Rejections` with a `t.Run` per case; every case asserts `err != nil` **and** `email == ""` (fail-closed: never return a partial identity):

- [ ] **B1. Expired token.** `exp = now - 1h` → reject.
- [ ] **B2. Not-yet-valid (`nbf`).** `nbf = now + 1h`, `exp = now + 2h` → reject. (If go-oidc does not enforce `nbf`, add the explicit check in `Verify` to make this pass.)
- [ ] **B3. Wrong audience.** `aud = "someone-else"` while verifier accepts `["app-aud"]` → reject.
- [ ] **B4. Wrong issuer.** `iss = "https://evil.example"` (token still signed by the harness key) → reject (issuer mismatch).
- [ ] **B5. Bad signature.** Sign with a DIFFERENT freshly generated RSA key (the `signWith` override), keeping the harness JWKS unchanged → reject (no matching key / signature invalid).
- [ ] **B6. Missing email claim.** Valid signed token with `email` omitted (or empty string) → reject with the missing-email error.
- [ ] **B7. Malformed JWT.** `v.Verify(ctx, "not.a.jwt")` and `v.Verify(ctx, "")` → reject.

After B1–B7, run `go test ./adapter/edge/ -v` → all PASS (implementing B2's explicit `nbf` check if needed).

### Step group C — multi-audience boundary + state

- [ ] **C1. Multiple configured audiences.** Construct with `Audiences: []string{"aud-a","aud-b"}`; a token with `aud = "aud-b"` → accepted; `aud = "aud-c"` → rejected. (Confirms the verifier checks membership, not equality to a single value — the spec allows a list.)
- [ ] **C2. JWKS caching / second request.** Two sequential `Verify` calls with valid tokens succeed without a second discovery; assert the harness JWKS handler is hit at most the expected number of times (go-oidc caches the remote key set). This documents the design's "JWKS caching/refresh is handled by the go-oidc remote key set".

### Step group D — verify + vet

- [ ] **D1.** `go test ./adapter/edge/ -v` → all PASS. `go vet ./adapter/edge/` clean.
- [ ] **D2.** `go build ./...`. Do NOT commit.

---

## Acceptance criteria (this task)

- A valid Cloudflare-style token (correct iss/aud, in-date, signed by the harness key) yields the verified email.
- Expired, not-yet-valid, wrong-audience, wrong-issuer, bad-signature, missing-email, and malformed tokens are each rejected with a non-nil error and an empty email — directly satisfying the spec's "A request with a missing, expired, wrong-audience, wrong-issuer, or signature-invalid token is rejected".
- Multiple audiences are supported (membership check).
- `New` fails fast when the team domain JWKS/discovery is unreachable.

## Notes — security & reuse

- **Independent verification, not header trust:** the bad-signature and wrong-issuer cases prove a forged token (e.g. a direct/SSRF request that fabricates the header) is rejected because it lacks a valid Cloudflare signature — this is the core Phase 1 security property.
- **Key never logged:** `Verify` returns wrapped errors that never include the raw token or any key material; do not add a `slog` line that logs `rawToken`. The returned value is only the verified email.
- **No hand-rolled crypto / no new dependency:** reuse `coreos/go-oidc/v3` exactly as `adapter/oidc/provider.go` does (`NewProvider`/`Verifier` or `NewRemoteKeySet`/`NewVerifier`). The library is already vendored at v3.18.0.
- **Mirror the oidc test harness verbatim** (`adapter/oidc/provider_test.go`): same `jose` signer, same RSA-key-per-harness, same `httptest.Server`. This keeps signatures real (no `InsecureSkipSignatureCheck` anywhere).

## Test scenario coverage

| Category | Scenario | Step |
|---|---|---|
| Happy | valid token → verified email | A2 |
| Error/failure | expired / wrong-aud / wrong-iss / bad-sig / malformed | B1,B3,B4,B5,B7 |
| Input validation | missing/empty email claim; empty raw token | B6,B7 |
| Boundary | `nbf` not-yet-valid; multi-aud membership | B2,C1 |
| Integration/state | JWKS cached across calls; fail-fast on unreachable JWKS | C2,A3(New) |
