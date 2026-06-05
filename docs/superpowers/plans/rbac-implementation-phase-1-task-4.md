# RBAC Phase 1 — Task 4: Provisioner Live-Admin Re-eval, Department Snapshot & Claim Extraction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On every login, (1) re-evaluate the user's admin role from the bootstrap-admin config (live override — promote/demote), and (2) snapshot the user's departments from the verified group claim into `user_departments`. Add the `Groups` claim to both `identity.Claims` and `instance.Claims`, and have the OIDC adapter extract the configured multi-valued group claim.

**Architecture:** Extend `instance.Claims` with `Groups []string`. Give `Provisioner` a `GroupConfig` dependency. In `Provisioner.Resolve`, after resolving/creating the user: re-evaluate role against the bootstrap set (update if changed), then call `users.ReplaceDepartments(ctx, userID, ResolveDepartments(claims.Groups, cfg))`. Extend `identity.Claims` with `Groups []string`, extend the OIDC adapter to read the configured `groupclaim` (array-or-string tolerant), and thread the group claim name into the OIDC `Config` and `AuthHandler` claim mapping.

**Tech Stack:** Go, `domain/identity` (Task 1 helpers), sqlc-backed repo (Task 3 methods), coreos/go-oidc claim decoding.

> **Spec alignment — live admin re-evaluation.** Per the design doc §Provisioning(1): on *every* login set role to `admin` iff `IsBootstrapAdmin(subject, verifiedEmail)`, else demote to `user`. The runtime role API's last-admin / bootstrap-floor protections (`UserService.SetRole`) are unchanged and continue to guard the admin *API*; they do NOT block config-driven login demotion. The provisioner writes the role directly via `UserRepository.UpdateRole`, bypassing those API-only floors by design (config is authoritative at login).

---

### Task 4: provisioner role re-eval + department snapshot + claim extraction

**Files:**
- Modify: `domain/instance/provisioner.go` (`Claims.Groups`, `GroupConfig` dep, `NewProvisioner` signature, `Resolve` role re-eval + dept snapshot)
- Modify: `domain/identity/ports.go` (`Claims.Groups`)
- Modify: `adapter/oidc/provider.go` (extract configured group claim; `Config.GroupClaim`)
- Modify: `adapter/http/auth_handler.go` (map `claims.Groups` into `instance.Claims`)
- Modify: `cmd/arbitron/main.go` (pass `GroupConfig` to `NewProvisioner`; pass `GroupClaim` to `oidc.Config`)
- Test: `domain/instance/provisioner_test.go` (add cases; file may already exist)
- Test: `adapter/oidc/provider_groups_test.go`

**Dependencies:** Task 1 (`identity.GroupConfig`, `ResolveDepartments`), Task 2 (`config.RBACConfig`, `ToGroupConfig`), Task 3 (`UserRepository.ReplaceDepartments`, `UpdateRole` already exists). Do after Tasks 1–3.

- [ ] **Step 1: Add `Groups` to `identity.Claims`**

In `domain/identity/ports.go`, in the `Claims` struct, after the `Nonce` field, add:

```go
	// Groups carries the verified, raw (un-normalized) values of the configured
	// group claim, used to derive department membership. Nil when no group claim
	// is configured or the token omits it (=> default group). The OIDC adapter
	// tolerates both a JSON array and a single string value for the claim.
	Groups []string
```

- [ ] **Step 2: Add `Groups` to `instance.Claims`**

In `domain/instance/provisioner.go`, in the `Claims` struct, after `EmailVerified`, add:

```go
	// Groups is the raw (un-normalized) group-claim values from the verified ID
	// token; the provisioner normalizes them via identity.ResolveDepartments and
	// snapshots the result. Nil => the user falls back to the default group.
	Groups []string
```

- [ ] **Step 3: Write the failing provisioner tests**

Append to `domain/instance/provisioner_test.go`, which ALREADY EXISTS and defines `fakeProvUserRepo` (fields `bySubject map[string]*User`, `created []*User`, `updatedNames`; constructor `newFakeProvUserRepo()`). Two prerequisites before adding tests:

**(a) Extend `fakeProvUserRepo`.** Its `UpdateRole`/`CountAdmins` are inert no-ops and it lacks the Task-3 dept methods, so the package won't compile after Task 3 and the role re-eval tests need `UpdateRole` to mutate. Add:

```go
// add field to the fakeProvUserRepo struct:  depts map[uuid.UUID][]string
// init it in newFakeProvUserRepo(): ...,  depts: map[uuid.UUID][]string{}

// replace the inert UpdateRole no-op so it mutates the stored user (by ID,
// across both bySubject and created):
func (r *fakeProvUserRepo) UpdateRole(_ context.Context, id uuid.UUID, role string) error {
	for _, u := range r.bySubject {
		if u.ID == id {
			u.Role = identity.Role(role)
		}
	}
	for _, u := range r.created {
		if u.ID == id {
			u.Role = identity.Role(role)
		}
	}
	return nil
}

func (r *fakeProvUserRepo) ListDepartments(_ context.Context, id uuid.UUID) ([]string, error) {
	return append([]string(nil), r.depts[id]...), nil
}

func (r *fakeProvUserRepo) ReplaceDepartments(_ context.Context, id uuid.UUID, departments []string) error {
	r.depts[id] = append([]string(nil), departments...)
	return nil
}
```

**(b) Update the EXISTING `NewProvisioner(repo, nil)` / `NewProvisioner(repo, []string{...})` calls in this file** to the new 3-arg signature, appending `identity.GroupConfig{DefaultGroup: "default"}`.

Then add the new tests below. The fake is `newFakeProvUserRepo()`; seed existing users via `repo.bySubject[subj] = ...` and read the snapshot via `repo.depts[u.ID]`. Add `reflect` to the import block (the file already imports `context`, `testing`, `identity`, `uuid`, testify).

```go
func TestResolve_PromotesExistingUserToAdminWhenInBootstrapList(t *testing.T) {
	repo := newFakeProvUserRepo()
	// Seed an existing non-admin user whose subject is now in the bootstrap list.
	subj := "alice-subject"
	repo.bySubject[subj] = &User{ID: uuid.New(), Subject: &subj, Username: "alice", Role: identity.RoleUser}

	p := NewProvisioner(repo, []string{"alice-subject"}, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{Subject: subj, Username: "alice"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if u.Role != identity.RoleAdmin {
		t.Errorf("role = %q, want admin (live promotion)", u.Role)
	}
}

func TestResolve_DemotesExistingAdminRemovedFromBootstrapList(t *testing.T) {
	repo := newFakeProvUserRepo()
	subj := "bob-subject"
	repo.bySubject[subj] = &User{ID: uuid.New(), Subject: &subj, Username: "bob", Role: identity.RoleAdmin}

	// Bootstrap list NO LONGER contains bob -> demote at login.
	p := NewProvisioner(repo, []string{"someone-else"}, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{Subject: subj, Username: "bob"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if u.Role != identity.RoleUser {
		t.Errorf("role = %q, want user (live demotion)", u.Role)
	}
}

func TestResolve_UnverifiedEmailDoesNotGrantAdmin(t *testing.T) {
	repo := newFakeProvUserRepo()
	// New user; email is in bootstrap list but NOT verified -> must not be admin.
	p := NewProvisioner(repo, []string{"eve@corp.example"}, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{
		Subject: "eve-subject", Username: "eve", Email: "eve@corp.example", EmailVerified: false,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if u.Role != identity.RoleUser {
		t.Errorf("role = %q, want user (unverified email must not bootstrap admin)", u.Role)
	}
}

func TestResolve_SnapshotsDepartmentsFromGroups(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{
		Subject: "carol-subject", Username: "carol", Groups: []string{"Engineering", " engineering ", "Platform"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := repo.depts[u.ID]
	want := []string{"engineering", "platform"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
}

func TestResolve_NoGroupsSnapshotsDefaultGroup(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{Subject: "dan-subject", Username: "dan"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !reflect.DeepEqual(repo.depts[u.ID], []string{"default"}) {
		t.Errorf("snapshot = %v, want [default]", repo.depts[u.ID])
	}
}

func TestResolve_ReplacesDepartmentsOnEachLogin(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})
	ctx := context.Background()
	u1, _ := p.Resolve(ctx, Claims{Subject: "ed-subject", Username: "ed", Groups: []string{"sales"}})
	// Second login with a different group set fully replaces the first snapshot.
	_, _ = p.Resolve(ctx, Claims{Subject: "ed-subject", Username: "ed", Groups: []string{"eng"}})
	if !reflect.DeepEqual(repo.depts[u1.ID], []string{"eng"}) {
		t.Errorf("snapshot = %v, want [eng] after re-login", repo.depts[u1.ID])
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test ./domain/instance/ -run TestResolve -v`
Expected: FAIL — `NewProvisioner` arity mismatch (3 args vs 2) and snapshot/role assertions unmet.

- [ ] **Step 5: Change `NewProvisioner` to take `GroupConfig` and store it**

In `domain/instance/provisioner.go`, update the struct and constructor:

```go
type Provisioner struct {
	users     UserRepository
	bootstrap map[string]struct{} // lower-cased subjects AND emails
	groups    identity.GroupConfig
	now       func() time.Time
	newID     func() uuid.UUID
}

// NewProvisioner builds a Provisioner, normalizing the bootstrap list to a
// lower-cased set for O(1), case-insensitive membership. groups supplies the
// default-group fallback used when snapshotting departments at login.
func NewProvisioner(users UserRepository, bootstrapAdmins []string, groups identity.GroupConfig) *Provisioner {
	set := make(map[string]struct{}, len(bootstrapAdmins))
	for _, v := range bootstrapAdmins {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			set[v] = struct{}{}
		}
	}
	return &Provisioner{users: users, bootstrap: set, groups: groups, now: time.Now, newID: uuid.New}
}
```

- [ ] **Step 6: Add the role re-eval + department snapshot to `Resolve`**

Rewrite `Provisioner.Resolve` in `domain/instance/provisioner.go`. The existing-user branch must now re-evaluate role; both branches must snapshot departments before returning:

```go
// Resolve returns the existing user for claims.Subject, or creates a new one.
// On EVERY login it (1) re-evaluates the admin role from the live bootstrap-admin
// config (promote/demote so config is authoritative — spec §Roles), and
// (2) snapshots the user's departments from the verified group claim. Username is
// refreshed if the IdP changed it.
func (p *Provisioner) Resolve(ctx context.Context, c Claims) (*User, error) {
	if c.Subject == "" {
		return nil, ErrInvalidClaims
	}

	// SECURITY: only a VERIFIED email may drive bootstrap-admin matching (an
	// unverified email is attacker-controllable at the IdP). Subject is always
	// trusted (signature-verified, IdP-stable).
	matchEmail := ""
	if c.EmailVerified {
		matchEmail = c.Email
	}
	wantAdmin := p.IsBootstrapAdmin(c.Subject, matchEmail)
	wantRole := identity.RoleUser
	if wantAdmin {
		wantRole = identity.RoleAdmin
	}

	u, err := p.users.GetBySubject(ctx, c.Subject)
	switch {
	case err == nil:
		if c.Username != "" && c.Username != u.Username {
			u.Username = c.Username
			// Best-effort: a username collision must not fail login.
			_ = p.users.UpdateUsername(ctx, u.ID, c.Username)
		}
		// Live admin re-evaluation: config is authoritative at login. This
		// promotes a newly-listed user and DEMOTES one removed from the list,
		// bypassing the role-API demotion floors (which guard the runtime API,
		// not config-driven login).
		if u.Role != wantRole {
			if err := p.users.UpdateRole(ctx, u.ID, string(wantRole)); err != nil {
				return nil, fmt.Errorf("re-evaluating role at login: %w", err)
			}
			u.Role = wantRole
		}
	case errors.Is(err, ErrNotFound):
		subj := c.Subject
		var emailPtr *string
		if c.Email != "" {
			e := c.Email
			emailPtr = &e
		}
		nu := &User{
			ID:       p.newID(),
			Subject:  &subj,
			Email:    emailPtr,
			Username: pickUsername(c),
			Role:     wantRole,
		}
		if err := p.users.Create(ctx, nu); err != nil {
			return nil, err
		}
		u = nu
	default:
		return nil, err
	}

	// Department snapshot (RBAC Phase 1): replace the user's stored departments
	// with the normalized, default-group-fallback set derived from this login's
	// group claim. Non-interactive triggers later read this stored set.
	depts := identity.ResolveDepartments(c.Groups, p.groups)
	if err := p.users.ReplaceDepartments(ctx, u.ID, depts); err != nil {
		return nil, fmt.Errorf("snapshotting departments at login: %w", err)
	}
	return u, nil
}
```

Add `"fmt"` to the import block of `domain/instance/provisioner.go` (it is not currently imported).

- [ ] **Step 7: Run the provisioner tests to verify they pass**

Run: `go test ./domain/instance/ -run TestResolve -v`
Expected: PASS for all six new cases (and any pre-existing Resolve tests — update their `NewProvisioner(...)` calls to the 3-arg signature if they fail to compile; pass `identity.GroupConfig{DefaultGroup: "default"}`).

- [ ] **Step 8: Write the failing OIDC group-claim extraction test**

Create `adapter/oidc/provider_groups_test.go`:

```go
package oidc

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExtractGroups_ArrayClaim(t *testing.T) {
	raw := json.RawMessage(`["engineering","platform"]`)
	got := extractGroups(map[string]json.RawMessage{"groups": raw}, "groups")
	if !reflect.DeepEqual(got, []string{"engineering", "platform"}) {
		t.Errorf("got %v, want [engineering platform]", got)
	}
}

func TestExtractGroups_SingleStringClaim(t *testing.T) {
	raw := json.RawMessage(`"engineering"`)
	got := extractGroups(map[string]json.RawMessage{"groups": raw}, "groups")
	if !reflect.DeepEqual(got, []string{"engineering"}) {
		t.Errorf("got %v, want [engineering]", got)
	}
}

func TestExtractGroups_EmptyClaimNameReturnsNil(t *testing.T) {
	raw := json.RawMessage(`["x"]`)
	if got := extractGroups(map[string]json.RawMessage{"groups": raw}, ""); got != nil {
		t.Errorf("got %v, want nil when groupclaim is empty", got)
	}
}

func TestExtractGroups_MissingClaimReturnsNil(t *testing.T) {
	if got := extractGroups(map[string]json.RawMessage{}, "groups"); got != nil {
		t.Errorf("got %v, want nil when claim absent", got)
	}
}

func TestExtractGroups_NonStringElementsDropped(t *testing.T) {
	raw := json.RawMessage(`["eng", 42, "sales"]`)
	got := extractGroups(map[string]json.RawMessage{"groups": raw}, "groups")
	if !reflect.DeepEqual(got, []string{"eng", "sales"}) {
		t.Errorf("got %v, want [eng sales] (non-string dropped)", got)
	}
}
```

- [ ] **Step 9: Run the OIDC test to verify it fails**

Run: `go test ./adapter/oidc/ -run TestExtractGroups -v`
Expected: FAIL — `undefined: extractGroups`.

- [ ] **Step 10: Add `GroupClaim` to OIDC `Config`, extract the claim in `Exchange`, and add `extractGroups`**

In `adapter/oidc/provider.go`:

(a) Add to the `Config` struct (after `Scopes`):

```go
	// GroupClaim is the ID-token claim name carrying group membership. Empty
	// means no group claim is read (Groups is always nil => default group).
	GroupClaim string
```

(b) Store it on `Provider`. Add a field to the struct:

```go
	groupClaim string
```

and set it in `New` (in the returned `&Provider{...}`):

```go
		groupClaim: cfg.GroupClaim,
```

(c) In `Exchange`, change the claim decode to capture the full claim set so an arbitrary group claim name can be read. Replace the typed-struct decode block with a two-pass decode: first into the typed struct (for the known fields), then into a raw map (for the dynamic group claim). After the existing `if err := idTok.Claims(&c); err != nil { ... }`, add:

```go
	var rawClaims map[string]json.RawMessage
	if err := idTok.Claims(&rawClaims); err != nil {
		return identity.Claims{}, fmt.Errorf("oidc: decoding raw claims: %w", err)
	}
	groups := extractGroups(rawClaims, p.groupClaim)
```

and add `groups` to the returned `identity.Claims{...}`:

```go
	return identity.Claims{Subject: subject, Username: username, Email: c.Email, EmailVerified: c.EmailVerified, Nonce: c.Nonce, Groups: groups}, nil
```

(d) Add the helper and the `encoding/json` import:

```go
// extractGroups reads the configured group claim from the verified ID-token
// claim set, tolerating both a JSON array of strings and a single string value.
// Returns nil when claimName is empty, the claim is absent, or it is neither
// shape; non-string array elements are skipped.
func extractGroups(claims map[string]json.RawMessage, claimName string) []string {
	if claimName == "" {
		return nil
	}
	raw, ok := claims[claimName]
	if !ok {
		return nil
	}
	// Try array-of-any first (tolerate non-string elements by skipping them).
	var arr []any
	if err := json.Unmarshal(raw, &arr); err == nil {
		out := make([]string, 0, len(arr))
		for _, v := range arr {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	// Fall back to a single string value.
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && single != "" {
		return []string{single}
	}
	return nil
}
```

Add `"encoding/json"` to the import block of `adapter/oidc/provider.go`.

- [ ] **Step 11: Run the OIDC tests to verify they pass**

Run: `go test ./adapter/oidc/ -v`
Expected: PASS (new group tests + existing provider tests).

- [ ] **Step 12: Map `claims.Groups` through the auth handler**

In `adapter/http/auth_handler.go`, in `resolveOrProvision`, add `Groups` to the `instance.Claims` literal:

```go
	return h.provisioner.Resolve(ctx, instance.Claims{
		Subject:  claims.Subject,
		Username: claims.Username,
		Email:    claims.Email,
		// SECURITY: forward email_verified so the provisioner only trusts the
		// email for bootstrap-admin matching when the IdP verified it.
		EmailVerified: claims.EmailVerified,
		Groups:        claims.Groups,
	})
```

- [ ] **Step 13: Update the wiring in `main.go`**

In `cmd/arbitron/main.go`:

(a) Build the group config once (near where `provisioner` is created, ~line 281). Add before the `NewProvisioner` call:

```go
	groupCfg := adaptconfig.ToGroupConfig(cfg.Auth.RBAC)
```

(Confirm the import alias for `adapter/config` — `grep -n "adapter/config" cmd/arbitron/main.go`; use that alias. If it is imported as `config`, use `config.ToGroupConfig`.)

(b) Change the provisioner construction:

```go
	provisioner := instance.NewProvisioner(userRepo, cfg.Auth.BootstrapAdmins, groupCfg)
```

(c) Add the group claim to the OIDC config block (inside `adaptoidc.Config{...}`):

```go
			GroupClaim:   cfg.Auth.RBAC.GroupClaim,
```

- [ ] **Step 14: Build everything; fix any remaining `NewProvisioner` call sites**

Run: `go build ./... 2>&1 | grep -i "NewProvisioner\|too many\|not enough" || echo "build clean"`
Expected: `build clean`. If any test or wiring still calls the 2-arg `NewProvisioner`, update it to pass `identity.GroupConfig{DefaultGroup: "default"}` (tests) or the real `groupCfg` (wiring).

- [ ] **Step 15: Full test + vet for affected packages**

Run: `go test ./domain/instance/ ./adapter/oidc/ ./adapter/http/ ./cmd/... && go vet ./domain/instance/ ./adapter/oidc/`
Expected: PASS, clean.

- [ ] **Step 16: Commit**

```bash
git add domain/instance/provisioner.go domain/instance/provisioner_test.go domain/identity/ports.go adapter/oidc/provider.go adapter/oidc/provider_groups_test.go adapter/http/auth_handler.go cmd/arbitron/main.go
git commit -m "feat(rbac): live admin re-eval + department snapshot at login

Provisioner re-evaluates admin role from bootstrap config on every login
(promote/demote) and snapshots normalized departments into user_departments.
Claims gains Groups; OIDC adapter extracts the configured group claim
(array-or-string tolerant).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Test Scenario Coverage (this task)

| Category | Scenario | Step |
|----------|----------|------|
| Happy | New user in bootstrap list created as admin | (existing Resolve tests, re-confirmed) |
| Happy | Departments snapshotted from group claim | 3 |
| Edge | No groups → default group snapshotted | 3 |
| Edge | Mixed-case/whitespace groups normalized + de-duped | 3 |
| Edge | OIDC array vs single-string claim both parsed | 8 |
| Edge | Non-string array elements / missing claim / empty name → nil | 8 |
| Error | ReplaceDepartments failure surfaces as login error | 6 (code path) |
| Authz | Existing user promoted when added to bootstrap list | 3 |
| Authz | Existing admin demoted when removed from bootstrap list | 3 |
| Authz | Unverified email does NOT grant admin | 3 |
| Mode | (Local mode never calls the provisioner — verified in Task 5) | — |
