# Final Org Gaps — Phase 2 Design: SSO Group/Department Mapping

Implements **Capability B** of `docs/specs/final-org-gaps-spec.md`: scope a
non-admin edge-authenticated user to the department(s) their SSO group claim
names, via a configured mapping with verbatim fallback, with remap consistency
and email-only admin.

## Context (existing code)

- `adapter/edge/verifier.go` — `Verifier.Verify(ctx, rawToken) (email string,
  err error)` independently verifies a Cloudflare-Access JWT and extracts only
  the `email` claim. The port is `identity.EdgeVerifier`.
- `domain/instance/provisioner.go` — `ResolveByEmail` is the edge provisioning
  path. It calls `identity.ResolveDepartments(nil, p.groups)` — **hardcoded
  nil** — so every edge user lands in the default group. (`Resolve`, the OIDC
  path, already threads `c.Groups`.)
- `domain/identity/groups.go` — `GroupConfig{GroupClaim, DefaultGroup, Leads}`
  and `ResolveDepartments(claimValues, cfg)` are the single department-resolution
  chokepoint. Departments are plain normalized strings; there is no department
  catalog entity. `ReplaceDepartments` snapshots the resolved set onto the user
  at every login.
- Admin role is set solely from the bootstrap email/subject allow-list
  (`IsBootstrapAdmin`); groups never touch role.

## Design

### 1. Edge verifier returns groups

Change the port to carry groups alongside email:

```go
// domain/identity/edge.go
type EdgeIdentity struct {
    Email  string
    Groups []string // raw, un-normalized; empty when the claim is absent
}
type EdgeVerifier interface {
    Verify(ctx context.Context, rawToken string) (EdgeIdentity, error)
}
```

`edge.Verifier` gains a configured group-claim name and extracts it dynamically:

```go
type Config struct {
    TeamDomain string
    Audiences  []string
    GroupClaim string // "" ⇒ no group extraction (every user → default group)
}
```

In `Verify`, after the existing signature/issuer/audience/expiry checks and
email extraction, decode the configured group claim from the same verified
token. The claim may be a JSON string or array of strings; normalize both to
`[]string`. A missing/empty claim yields `Groups: nil`. Verification failure
semantics are unchanged (any failure ⇒ `(EdgeIdentity{}, err)` ⇒ 401). The raw
token is still never logged.

The auth middleware (`adapter/http/auth_middleware.go`) threads
`edgeIdentity.Groups` into the provisioner (below).

### 2. Provisioner consumes edge groups

`ResolveByEmail` gains the groups argument and stops hardcoding nil:

```go
func (p *Provisioner) ResolveByEmail(ctx context.Context, email string, groups []string) (*User, error)
// ...
depts := identity.ResolveDepartments(groups, p.groups) // was: ResolveDepartments(nil, ...)
```

Everything else (admin re-evaluation from the bootstrap list, user creation,
`ReplaceDepartments` snapshot) is unchanged. A user with no groups still resolves
to the default group (existing behavior preserved).

### 3. Mapping + verbatim fallback in ResolveDepartments

Extend `GroupConfig` with the operator mapping and apply it at the single
chokepoint:

```go
type GroupConfig struct {
    GroupClaim   string
    DefaultGroup string
    Leads        map[string][]string
    Mapping      map[string]string // normalized raw-claim ⇒ friendly department
}
```

`ResolveDepartments` policy (per claim value, preserving first-seen order):
1. `k := NormalizeDept(raw)`; skip blanks.
2. If `cfg.Mapping[k]` exists → use `NormalizeDept(mapped)` (the friendly name).
3. Else → use `k` verbatim.
4. Dedupe via the shared `DedupeDepts`.
5. If the result is empty (no claim / all blank) → `cfg.DefaultGroupOr()`.

`Mapping` keys are normalized at load time so lookups are case-insensitive.
`EffectiveMembership` is unaffected beyond inheriting the mapped output (leads
still union on top).

### 4. Remap consistency — startup reconcile pass

Because departments are stored as plain strings (on user memberships, schedules,
and instances), adding a mapping for a value that was previously stored verbatim
must rewrite those historical values. A new **idempotent reconciler** runs once
at startup, after config load, before serving:

```go
// domain/identity/remap.go (port) + adapter/sqlite implementation
type DepartmentRenamer interface {
    // RenameDepartment rewrites every stored occurrence of `from` to `to`
    // across user memberships, schedules, and instances. Idempotent.
    RenameDepartment(ctx context.Context, from, to string) (rows int64, err error)
}

// Reconcile walks cfg.Mapping and renames each from=normalize(rawKey) to
// to=normalize(friendly) when from != to. Idempotent: once values are already
// friendly, the WHERE matches nothing.
func ReconcileMappings(ctx context.Context, cfg GroupConfig, r DepartmentRenamer, log *slog.Logger) error
```

The SQLite implementation issues `UPDATE ... SET department = ? WHERE department
= ?` against the three tables (user-department rows, schedules, instances) inside
one transaction per mapping entry. Memberships additionally self-heal on each
user's next login (the `ReplaceDepartments` snapshot already re-derives from the
mapped resolution), so the reconcile is belt-and-suspenders for historical rows
and currently-logged-out users.

> **Scope guard.** The reconcile rewrites only the exact `from` value named by a
> mapping key. It never collapses or invents departments beyond the configured
> map, and is safe to run on every boot.

### 5. Config wiring (headless)

`cfg.Auth.Edge` gains `GroupClaim`, `DefaultGroup`, and `Mapping`
(`map[string]string`), all from environment/secrets (koanf), consistent with the
existing edge settings. The loader normalizes mapping keys/values and builds the
`identity.GroupConfig` passed to the provisioner and reconciler. When
`GroupClaim` is unset, behavior is identical to today (default group for all
edge users).

## Data flow

```
Cloudflare Access JWT
        ▼
edge.Verifier.Verify ─► EdgeIdentity{Email, Groups (configured claim)}
        ▼ (auth middleware)
Provisioner.ResolveByEmail(email, groups)
        ▼
identity.ResolveDepartments(groups, cfg)   ── mapping ▸ verbatim ▸ default
        ▼
users.ReplaceDepartments(userID, depts)    ── snapshot onto user
        ▼
identity.Identity{Departments}             ── drives visibility/stamping everywhere

(startup, once) ReconcileMappings(cfg, renamer) ─► UPDATE department from→to
                                                   across users/schedules/instances
```

## Error handling

- Token verification failure (signature/issuer/audience/expiry, missing email):
  unchanged — fail closed, 401. A malformed/absent group claim is **not** fatal:
  it yields `Groups: nil` → default group (a missing group must not lock a valid
  user out).
- `ReplaceDepartments` / provisioning repo errors surface as non-`ErrInvalidClaims`
  errors so the middleware returns 500 (could-not-check), never a silent
  mis-scope.
- Reconcile failure at startup is logged and fails boot (fail-fast), consistent
  with other config-driven boot steps; a partial reconcile is avoided by the
  per-entry transaction + idempotent re-run.

## Testing (TDD — 5 scenario categories)

- **Happy path:** edge token with a mapped group → user scoped to the friendly
  department; token with an unmapped group → verbatim department; token with no
  group claim → default group; multiple groups → union.
- **Input validation:** group claim as string vs array; blank/whitespace values
  dropped; mapping keys matched case-insensitively.
- **Authorization/role:** a group claim never confers admin; an admin (email
  allow-list) keeps full visibility regardless of groups; a non-admin with groups
  loses see-everything and is scoped.
- **Remap consistency:** seed instances/schedules/membership under verbatim
  `guid-123`; add mapping `guid-123→engineering`; run reconcile → all three
  stores now read `engineering`; running reconcile twice is a no-op; a user's
  next login also yields `engineering`.
- **Failure modes:** malformed group claim → default group (not 401); repo error
  during snapshot → 500; reconcile DB error → boot fails loudly.
- **Verifier unit tests:** forged/absent token still 401 with the new return
  type; email-only token (no group claim configured) behaves as today.

## Out of scope (this phase)

- Admin-by-group (explicitly email-only).
- A department catalog/management UI (departments remain claim-derived strings).
- Group→lead auto-assignment beyond the existing `Leads` config.
