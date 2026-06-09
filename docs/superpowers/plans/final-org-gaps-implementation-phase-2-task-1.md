# Final Org Gaps — Phase 2, Task 1: Edge Verifier Groups + Port Change + Middleware Threading

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Change the `identity.EdgeVerifier` port to return `EdgeIdentity{Email, Groups}`, make `edge.Verifier` extract a configured group claim (string OR array, missing ⇒ nil groups, never a 401), and thread the groups through the auth middleware into the provisioner.

**Architecture:** The domain port `EdgeVerifier.Verify` stops returning a bare `email string` and instead returns a new domain value type `identity.EdgeIdentity{Email string; Groups []string}`. The Cloudflare-Access adapter (`adapter/edge`) gains a `GroupClaim` config field and decodes that claim from the *same verified token* it already validated. The HTTP auth middleware (`adapter/http/auth_middleware.go`) consumes `EdgeIdentity.Groups` and passes them to `ResolveByEmail` (whose signature change lands in Task 2 — this task wires the call site to pass groups). All verification failure semantics are unchanged: any signature/issuer/audience/expiry/missing-email failure still returns a zero `EdgeIdentity` plus error ⇒ 401. A missing/malformed group claim is **not** fatal — it yields `Groups: nil`.

**Tech Stack:** Go, `github.com/coreos/go-oidc/v3` (already used by the verifier), `github.com/go-jose/go-jose/v4` (test token signing), `testify`.

---

## Context the implementer needs

- **Spec:** `docs/specs/final-org-gaps-spec.md` — Capability B (the group claim is read only from the same verified token; a missing group claim falls back to default-everything, never a 401).
- **Design:** `docs/superpowers/specs/final-org-gaps-design-phase-2-sso-group-mapping.md` — sections 1 & 2.
- **Current port:** `domain/identity/edge.go` declares `Verify(ctx, rawToken) (email string, err error)`.
- **Current adapter:** `adapter/edge/verifier.go` — `Verifier.Verify` returns `(string, error)`. The `Config` struct has `TeamDomain` + `Audiences`. After audience check it decodes a `struct{ Email string }` from `tok.Claims`.
- **Current middleware:** `adapter/http/auth_middleware.go` — in `resolve`, the edge branch does:
  ```go
  email, verr := m.edge.verifier.Verify(r.Context(), raw)
  ...
  user, perr := m.edge.provisioner.ResolveByEmail(r.Context(), email)
  ```
  and the `emailProvisioner` interface (lines 34–39) declares `ResolveByEmail(ctx, email) (*instance.User, error)`.
- **Test harness:** `adapter/edge/verifier_test.go` already has `jwksHarness`, `signToken`, `baseClaims`, `newVerifier`. Group claims are added by setting a key on the `map[string]any` claims (e.g. `c["groups"] = []any{"eng","platform"}`).

> **IMPORTANT — coordination with Task 2:** This task changes `emailProvisioner.ResolveByEmail` to take `(ctx, email, groups)` and updates the middleware call site to pass `id.Groups`. The concrete `*instance.Provisioner.ResolveByEmail` signature is changed in **Task 2**. To keep the build green within this task, this task updates the `emailProvisioner` interface AND the concrete provisioner signature together (the concrete change is small and mechanical; Task 2 then fills in the *behavior* — passing groups to `ResolveDepartments` — and its tests). Implementers running tasks out of order: the interface in this file and the concrete method in `domain/instance/provisioner.go` must always agree.

---

### Task 1a: Introduce `identity.EdgeIdentity` and change the port

**Files:**
- Modify: `domain/identity/edge.go`
- Test: `domain/identity/edge_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

Create/append `domain/identity/edge_test.go`:

```go
package identity

import "testing"

func TestEdgeIdentity_ZeroValue(t *testing.T) {
	var id EdgeIdentity
	if id.Email != "" {
		t.Fatalf("zero EdgeIdentity.Email = %q, want empty", id.Email)
	}
	if id.Groups != nil {
		t.Fatalf("zero EdgeIdentity.Groups = %v, want nil", id.Groups)
	}
}

func TestEdgeIdentity_CarriesEmailAndGroups(t *testing.T) {
	id := EdgeIdentity{Email: "alice@corp.example", Groups: []string{"eng", "platform"}}
	if id.Email != "alice@corp.example" {
		t.Fatalf("Email = %q", id.Email)
	}
	if len(id.Groups) != 2 || id.Groups[0] != "eng" || id.Groups[1] != "platform" {
		t.Fatalf("Groups = %v", id.Groups)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./domain/identity/ -run TestEdgeIdentity -v`
Expected: FAIL — `undefined: EdgeIdentity`.

- [ ] **Step 3: Change the port**

Replace the entire body of `domain/identity/edge.go` with:

```go
package identity

import "context"

// EdgeIdentity is the verified identity an EdgeVerifier extracts from a single
// edge-issued token. Email is the verified email claim (never empty on success).
// Groups carries the RAW, un-normalized values of the configured group claim used
// downstream to derive department membership; it is nil when no group claim is
// configured or the token omits the claim (=> the user falls back to the default
// group, NOT a 401). Department normalization/mapping happens later in
// identity.ResolveDepartments — the verifier returns claim values verbatim.
type EdgeIdentity struct {
	Email  string
	Groups []string
}

// EdgeVerifier verifies a single edge-issued identity token (e.g. a Cloudflare
// Access JWT) and returns the authenticated identity. It is the domain port the
// auth middleware depends on in edge mode so the verification adapter is
// swappable and testable. Verification is STATELESS (per request). On ANY
// failure — malformed, bad signature, wrong issuer/audience, expired/not-yet-
// valid, or a missing/empty email claim — it returns (EdgeIdentity{}, err); the
// caller maps a non-nil error to a fail-closed 401. A missing/malformed GROUP
// claim is NOT a failure: it yields Groups: nil. The raw token is never logged
// or returned.
type EdgeVerifier interface {
	Verify(ctx context.Context, rawToken string) (EdgeIdentity, error)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./domain/identity/ -run TestEdgeIdentity -v`
Expected: PASS. (The rest of the repo will not compile yet — that is expected until Tasks 1b/1c land. Do not run a full build here.)

- [ ] **Step 5: Commit**

```bash
git add domain/identity/edge.go domain/identity/edge_test.go
git commit -m "feat(identity): EdgeVerifier port returns EdgeIdentity{Email,Groups}"
```

---

### Task 1b: Extract the configured group claim in `edge.Verifier`

**Files:**
- Modify: `adapter/edge/verifier.go`
- Test: `adapter/edge/verifier_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `adapter/edge/verifier_test.go`. (`newVerifier` currently builds a `Config` with only `TeamDomain`/`Audiences`; add a group-claim-aware constructor and the scenario tests.)

```go
// newVerifierWithGroupClaim builds a verifier that also extracts the named group
// claim. Mirrors newVerifier but threads GroupClaim through Config.
func newVerifierWithGroupClaim(t *testing.T, h *jwksHarness, groupClaim string, audiences ...string) *adaptedge.Verifier {
	t.Helper()
	v, err := adaptedge.New(context.Background(), adaptedge.Config{
		TeamDomain: h.issuer,
		Audiences:  audiences,
		GroupClaim: groupClaim,
	})
	require.NoError(t, err)
	return v
}

func TestVerify_GroupClaimArray_ReturnsGroups(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
	c := baseClaims(h.issuer, "app-aud")
	c["groups"] = []any{"eng", "platform"}
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, "alice@corp.example", id.Email)
	assert.Equal(t, []string{"eng", "platform"}, id.Groups)
}

func TestVerify_GroupClaimSingleString_ReturnsOneGroup(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
	c := baseClaims(h.issuer, "app-aud")
	c["groups"] = "eng" // a single JSON string, not an array
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, "alice@corp.example", id.Email)
	assert.Equal(t, []string{"eng"}, id.Groups)
}

func TestVerify_GroupClaimAbsent_ReturnsNilGroupsNot401(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
	c := baseClaims(h.issuer, "app-aud") // no "groups" key
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err, "a missing group claim must NOT fail verification")
	assert.Equal(t, "alice@corp.example", id.Email)
	assert.Nil(t, id.Groups)
}

func TestVerify_NoGroupClaimConfigured_IgnoresClaim(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "", "app-aud") // GroupClaim == "" => no extraction
	c := baseClaims(h.issuer, "app-aud")
	c["groups"] = []any{"eng"} // present, but not configured to be read
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, "alice@corp.example", id.Email)
	assert.Nil(t, id.Groups, "no configured claim => groups not extracted")
}

func TestVerify_GroupClaimMalformedType_DefaultsToNilGroups(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
	c := baseClaims(h.issuer, "app-aud")
	c["groups"] = map[string]any{"unexpected": "object"} // neither string nor array-of-strings
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err, "a malformed group claim must NOT lock out a valid user")
	assert.Equal(t, "alice@corp.example", id.Email)
	assert.Nil(t, id.Groups)
}

func TestVerify_GroupClaimMixedArray_KeepsOnlyStrings(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
	c := baseClaims(h.issuer, "app-aud")
	c["groups"] = []any{"eng", 42, "platform", ""} // non-strings and blanks dropped
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, []string{"eng", "platform"}, id.Groups)
}
```

Also update **every existing** assertion in this file that consumed the old `(string, error)` return. The pre-existing happy-path test `TestVerify_ValidToken_ReturnsEmail` and the failure tests must now read `id.Email` instead of a bare `email`. Concretely, for each existing `email, err := v.Verify(...)`:
- rename `email` to `id`,
- change `assert.Equal(t, "...", email)` to `assert.Equal(t, "...", id.Email)`,
- for failure cases that asserted `assert.Empty(t, email)`/`assert.Equal(t, "", email)`, change to `assert.Empty(t, id.Email)`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/edge/ -run 'TestVerify_GroupClaim|TestVerify_NoGroupClaimConfigured' -v`
Expected: FAIL to compile — `Config` has no `GroupClaim` field, and `Verify` returns `string` not `EdgeIdentity`.

- [ ] **Step 3: Implement group extraction**

Edit `adapter/edge/verifier.go`.

First, add the config field. In the `Config` struct (after `Audiences`):

```go
	// GroupClaim is the token claim name carrying group membership. "" disables
	// group extraction entirely (every user falls back to the default group). The
	// claim value may be a JSON string or an array of strings; both normalize to
	// []string. A missing or malformed claim yields nil groups — it is NEVER a
	// verification failure, so a user without group info is not locked out.
	GroupClaim string
```

Add a `groupClaim` field to `Verifier`:

```go
type Verifier struct {
	verifier   *coreoidc.IDTokenVerifier
	audiences  []string
	groupClaim string
}
```

In `New`, set it when constructing the verifier (trim it):

```go
	return &Verifier{
		verifier:   verifier,
		audiences:  audiences,
		groupClaim: strings.TrimSpace(cfg.GroupClaim),
	}, nil
```

Replace `Verify` with the `EdgeIdentity`-returning version:

```go
// Verify checks the token signature, issuer, expiry/not-before, and audience
// membership, then returns the verified EdgeIdentity. On ANY of those failures
// (or a missing email claim) it returns (EdgeIdentity{}, err) so the caller fails
// closed (401). A missing/malformed GROUP claim is NOT a failure: it yields
// Groups: nil. The raw token is never logged.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (identity.EdgeIdentity, error) {
	if strings.TrimSpace(rawToken) == "" {
		return identity.EdgeIdentity{}, fmt.Errorf("edge: empty token")
	}
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return identity.EdgeIdentity{}, fmt.Errorf("edge: token verification: %w", err)
	}
	if !audienceMatches(tok.Audience, v.audiences) {
		return identity.EdgeIdentity{}, fmt.Errorf("edge: token audience %v not in configured audiences", tok.Audience)
	}
	var c struct {
		Email string `json:"email"`
	}
	if err := tok.Claims(&c); err != nil {
		return identity.EdgeIdentity{}, fmt.Errorf("edge: decoding claims: %w", err)
	}
	email := strings.TrimSpace(c.Email)
	if email == "" {
		return identity.EdgeIdentity{}, fmt.Errorf("edge: missing email claim")
	}
	return identity.EdgeIdentity{Email: email, Groups: v.extractGroups(tok)}, nil
}

// extractGroups decodes the configured group claim from the already-verified
// token. It tolerates the claim being a single JSON string OR an array of
// strings; any other shape (object, number, absent) yields nil. Non-string array
// elements and blank strings are dropped. Returns nil when no group claim is
// configured. The raw token is never logged.
func (v *Verifier) extractGroups(tok *coreoidc.IDToken) []string {
	if v.groupClaim == "" {
		return nil
	}
	// Decode all claims into a generic map so an arbitrary claim name can be read.
	// tok.Claims into a map cannot error here for a well-formed JWT body; on the
	// off chance it does, treat it as "no groups" (fail open to default group, not
	// a lockout — a missing group must never 401 a valid user).
	var all map[string]any
	if err := tok.Claims(&all); err != nil {
		return nil
	}
	raw, ok := all[v.groupClaim]
	if !ok {
		return nil
	}
	switch val := raw.(type) {
	case string:
		if s := strings.TrimSpace(val); s != "" {
			return []string{s}
		}
		return nil
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			s, isStr := item.(string)
			if !isStr {
				continue
			}
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}
```

(`coreoidc` and `identity` are already imported; no import changes are needed. `fmt` and `strings` remain in use.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./adapter/edge/ -v`
Expected: PASS for all `TestVerify_*` (including the updated pre-existing ones).

- [ ] **Step 5: Commit**

```bash
git add adapter/edge/verifier.go adapter/edge/verifier_test.go
git commit -m "feat(edge): extract configured group claim (string|array), missing => nil groups"
```

---

### Task 1c: Thread groups through the auth middleware

**Files:**
- Modify: `adapter/http/auth_middleware.go`
- Modify: `domain/instance/provisioner.go` (signature only — behavior lands in Task 2)
- Test: `adapter/http/auth_middleware_edge_test.go` (create) — or extend the existing edge middleware test file if one exists (search `adapter/http` for `EdgeVerifier` fakes first).

- [ ] **Step 1: Find the existing edge middleware test & fake**

Run: `grep -rln "EdgeVerifier\|ResolveByEmail\|ModeEdge" adapter/http/*_test.go`
Expected: locate the file holding the fake `EdgeVerifier` and fake provisioner used by edge-mode middleware tests. Reuse that file; if none exists, create `adapter/http/auth_middleware_edge_test.go`.

- [ ] **Step 2: Write the failing test**

The fake verifier must now return `identity.EdgeIdentity`, and the fake provisioner must now record the groups it received. Add (or adapt the existing fakes to) this shape:

```go
// fakeEdgeVerifier returns a fixed EdgeIdentity (or error) for any token.
type fakeEdgeVerifier struct {
	id  identity.EdgeIdentity
	err error
}

func (f fakeEdgeVerifier) Verify(_ context.Context, _ string) (identity.EdgeIdentity, error) {
	return f.id, f.err
}

// recordingProvisioner captures the groups passed to ResolveByEmail.
type recordingProvisioner struct {
	user       *instance.User
	err        error
	gotEmail   string
	gotGroups  []string
}

func (p *recordingProvisioner) ResolveByEmail(_ context.Context, email string, groups []string) (*instance.User, error) {
	p.gotEmail = email
	p.gotGroups = groups
	return p.user, p.err
}
```

Then the threading test:

```go
func TestEdgeMiddleware_ThreadsVerifiedGroupsToProvisioner(t *testing.T) {
	uid := uuid.New()
	prov := &recordingProvisioner{user: &instance.User{ID: uid, Username: "alice", Role: identity.RoleUser}}
	verifier := fakeEdgeVerifier{id: identity.EdgeIdentity{Email: "alice@corp.example", Groups: []string{"eng", "platform"}}}
	users := newFakeUserRepo(t, uid) // existing helper that returns departments via ListDepartments; adapt to whatever the file uses
	mw := NewEdgeAuthMiddleware(CfAccessJWTHeader, verifier, prov, users, identity.GroupConfig{DefaultGroup: "default"})

	var seen identity.Identity
	h := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = identity.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	req.Header.Set(CfAccessJWTHeader, "any-non-empty-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "alice@corp.example", prov.gotEmail)
	assert.Equal(t, []string{"eng", "platform"}, prov.gotGroups, "verified groups must reach the provisioner")
	_ = seen
}
```

(Match the imports, the `newFakeUserRepo` helper, and the `identity.FromContext` usage to the conventions already present in the file you found in Step 1. If the existing fake provisioner/verifier already exist, MODIFY them to the new signatures rather than adding duplicates.)

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./adapter/http/ -run TestEdgeMiddleware_ThreadsVerifiedGroups -v`
Expected: FAIL to compile — `emailProvisioner.ResolveByEmail` still takes `(ctx, email)`; the verifier fake signature and middleware call site mismatch.

- [ ] **Step 4: Update the `emailProvisioner` port and the call site**

In `adapter/http/auth_middleware.go`, change the `emailProvisioner` interface:

```go
type emailProvisioner interface {
	ResolveByEmail(ctx context.Context, email string, groups []string) (*instance.User, error)
}
```

In `resolve`, the edge branch: rename `email` to the verified identity and pass groups through. Replace:

```go
		email, verr := m.edge.verifier.Verify(r.Context(), raw)
		if verr != nil {
			return identity.Identity{}, errNoCredential
		}
		if email == "" {
			return identity.Identity{}, errNoCredential
		}
		user, perr := m.edge.provisioner.ResolveByEmail(r.Context(), email)
		if perr != nil {
			return identity.Identity{}, fmt.Errorf("edge: provisioning %q: %w", email, perr)
		}
```

with:

```go
		edgeID, verr := m.edge.verifier.Verify(r.Context(), raw)
		if verr != nil {
			// Invalid/forged/expired token: fail closed. NOT a server error — do
			// not log the raw token; the verifier already wrapped a safe error.
			return identity.Identity{}, errNoCredential
		}
		if edgeID.Email == "" {
			// Defense-in-depth: a conforming EdgeVerifier never returns an empty
			// email with a nil error, but if a broken one does, the empty email is a
			// credential failure (401), not a server fault (500).
			return identity.Identity{}, errNoCredential
		}
		// Thread the verified group claim into provisioning so the user is scoped to
		// the department(s) their SSO group resolves to. A nil/empty Groups (no claim)
		// preserves the default-group fallback.
		user, perr := m.edge.provisioner.ResolveByEmail(r.Context(), edgeID.Email, edgeID.Groups)
		if perr != nil {
			// Provisioning/DB failure is a SERVER fault, not an auth denial: surface a
			// non-errNoCredential error so Handler maps it to 500.
			return identity.Identity{}, fmt.Errorf("edge: provisioning %q: %w", edgeID.Email, perr)
		}
```

- [ ] **Step 5: Update the concrete provisioner signature (behavior in Task 2)**

In `domain/instance/provisioner.go`, change the `ResolveByEmail` method signature to accept `groups`, but keep the body otherwise identical for now (Task 2 wires `groups` into `ResolveDepartments`). Change:

```go
func (p *Provisioner) ResolveByEmail(ctx context.Context, email string) (*User, error) {
```

to:

```go
func (p *Provisioner) ResolveByEmail(ctx context.Context, email string, groups []string) (*User, error) {
```

Leave the body's `depts := identity.ResolveDepartments(nil, p.groups)` UNCHANGED in this task. (Task 2 changes `nil` to `groups` and adds the behavior tests. To avoid an "unused parameter" compile error in the interim, Go does not flag unused function parameters, so this compiles cleanly.)

- [ ] **Step 6: Run the affected packages to verify green**

Run: `go build ./... && go test ./adapter/http/ ./adapter/edge/ ./domain/identity/ ./domain/instance/ -v`
Expected: build succeeds; all listed package tests PASS (including the new threading test). Any OTHER call site of `ResolveByEmail` across the repo must also be updated — find them first:

Run: `grep -rn "ResolveByEmail" --include='*.go' . | grep -v '_test.go'`
Expected: only `domain/instance/provisioner.go` (definition) and `adapter/http/auth_middleware.go` (call). If a test fake elsewhere implements the old signature, update it to `(ctx, email, groups)`.

- [ ] **Step 7: Commit**

```bash
git add adapter/http/auth_middleware.go domain/instance/provisioner.go adapter/http/*_test.go
git commit -m "feat(auth): thread verified edge groups into ResolveByEmail"
```

---

## Self-review checklist (run before handing off)

- [ ] `EdgeVerifier.Verify` returns `identity.EdgeIdentity` everywhere (port, adapter, every fake).
- [ ] `edge.Verifier` reads `cfg.GroupClaim` dynamically; string and array both normalize; missing/malformed/empty ⇒ `nil` groups and no error.
- [ ] `GroupClaim == ""` ⇒ groups never extracted (today's behavior).
- [ ] Raw token never logged (no new log lines reference `rawToken`).
- [ ] Middleware passes `edgeID.Groups` to `ResolveByEmail`; nil groups still allowed (no 401 on missing claim).
- [ ] `emailProvisioner` interface and concrete `*instance.Provisioner.ResolveByEmail` signatures agree: `(ctx, email, groups)`.
- [ ] `go build ./...` is green; the `nil` ⇒ `groups` behavioral change is deferred to Task 2 (this task only changes the signature).
