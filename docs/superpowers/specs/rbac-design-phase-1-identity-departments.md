# RBAC Design — Phase 1: Identity & Departments Foundation

> Implementation design for `docs/specs/rbac-spec.md`. This is the backbone every
> later phase builds on: it makes a caller's departments, lead status, and (live)
> admin status available everywhere authorization is decided.

## Purpose

Make group membership a first-class, persisted, context-resident property of an
authenticated caller, so that downstream phases can authorize reads, writes, and
interventions without threading new parameters through call sites. Introduce no
user-visible behavior change beyond the new `/me` endpoint; visibility/dashboard
behavior arrives in later phases.

## Scope

In scope:
- Configurable, multi-valued **group claim** parsing with normalization and
  default-group fallback.
- Per-user **department persistence** (`user_departments`), snapshotted at login.
- Config-derived **department-lead** mapping and **live admin** re-evaluation.
- Enrichment of `identity.Identity` with `Departments` and `LedDepartments`, and
  population by the auth middleware.
- A `GET /api/v1/me` whoami endpoint.
- Full no-op behavior in **local mode**.

Out of scope (later phases): workflow visibility, instance stamping, dashboard.

## Domain Model Changes

### `identity.Identity` (domain/identity/identity.go)
Add two fields, both empty/ignored in local mode:
```
type Identity struct {
    UserID         uuid.UUID
    Username       string
    Role           Role          // unchanged: user | admin
    Departments    []string      // effective membership: claim-derived ∪ led, normalized
    LedDepartments []string      // subset the caller leads (config-derived)
}
```
Add helper predicates on `Identity`:
- `InDepartment(dept string) bool`
- `LeadsDepartment(dept string) bool`
- `IsAdmin() bool` (already exists)

These are the single read surface later phases consume via `identity.FromContext`.

### Group resolution service (new: domain/identity/groups.go)
A pure domain helper that turns verified claims + config into the effective
department/lead/admin projection. No infra imports.

- `NormalizeDept(raw string) string` — `strings.ToLower(strings.TrimSpace(raw))`.
- `ResolveDepartments(claimValues []string, cfg GroupConfig) []string` —
  normalizes, de-duplicates, drops empties; if the result is empty, returns the
  single **default group** sentinel (`cfg.DefaultGroup`, default `"default"`).
- `LedDepartments(subject, email string, cfg GroupConfig) []string` — departments
  for which this identity is a configured lead, matched case-insensitively against
  subject **or** email (mirrors the bootstrap-admin match rule).
- Effective membership = `ResolveDepartments(...) ∪ LedDepartments(...)` so a
  configured lead of a department always sees that department even if their token
  omits it.

### Configuration (domain/config + adapter/config/loader.go)
New keys under `auth.rbac`:
```yaml
auth:
  rbac:
    groupclaim: ""            # token claim carrying group membership; "" ⇒ default group only
    defaultgroup: "default"   # sentinel department for claimless users
    leads:                    # department → list of lead subjects/emails
      engineering: ["alice@corp.example", "bob-subject-id"]
```
`auth.bootstrapadmins` is **reused unchanged** as the authoritative admin set.

`GroupConfig` (domain/config) carries `GroupClaim string`, `DefaultGroup string`,
and `Leads map[string][]string` (lead identifiers normalized to lower-case at load,
keyed by normalized department).

## Claims & Login Flow

### Claim extraction (adapter/oidc/provider.go + domain/identity Claims)
`identity.Claims` gains `Groups []string`. The OIDC adapter reads the configured
`groupclaim` from the verified ID token and populates `Groups` — tolerating both a
JSON array (the common IdP shape) and a single string value. When `groupclaim` is
empty, `Groups` is always nil (→ default group).

### Provisioning (domain/instance/provisioner.go)
`Provisioner.Resolve` changes in two ways:

1. **Live admin re-evaluation.** On *every* login (not just create), set the
   user's role to `admin` iff `IsBootstrapAdmin(subject, verifiedEmail)`, else
   demote to `user`. This makes config authoritative for admin (spec §Roles). The
   existing "last admin" / bootstrap-floor protections in the role service remain
   for the runtime role API but no longer block config-driven demotion at login.
2. **Department snapshot.** After resolving the user, replace the user's stored
   departments with `ResolveDepartments(claims.Groups, cfg)` via a new
   `UserRepository.ReplaceDepartments(ctx, userID, depts)` (delete-all + insert,
   in one transaction).

The `Provisioner` gains a `GroupConfig` dependency. Lead resolution is *not*
persisted (it is config-derived and cheap to compute at request time); only
departments are persisted, because non-interactive triggers need them.

### Persistence (adapter/sqlite)
Migration `009_user_departments`:
```sql
CREATE TABLE user_departments (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    department TEXT NOT NULL,
    PRIMARY KEY (user_id, department)
);
CREATE INDEX idx_user_departments_dept ON user_departments(department);
```
sqlc queries: `ReplaceUserDepartments` (delete-by-user), `InsertUserDepartment`,
`ListUserDepartments(user_id)`. The user repo loads departments alongside the user
in `GetByID`/`GetBySubject` (or a dedicated loader the middleware calls).

## Request-Time Population (adapter/http/auth_middleware.go)

After the middleware resolves the persisted `User` (session or bearer path), it
builds the enriched identity:
- `Departments` = stored user departments ∪ `LedDepartments(subject,email,cfg)`.
- `LedDepartments` = `LedDepartments(subject,email,cfg)`.
- `Role` = the user's (now live-evaluated) role.

**Local mode:** the fixed default identity is given `Departments=[defaultGroup]`,
`LedDepartments=nil`, `Role=admin`. All RBAC predicates therefore pass trivially,
preserving today's behavior.

## Whoami Endpoint

`GET /api/v1/me` → `{ userId, username, role, departments[], ledDepartments[], isAdmin }`.
Served behind the same auth middleware; drives the Phase 4 dashboard selector and
any future client gating. No secrets (subject/email never serialized).

## Postgres-Readiness (cross-cutting, per scaling decision)

- All new persistence sits behind repository ports; `user_departments` uses
  standard SQL (no SQLite-only constructs).
- Indexing chosen with a future Postgres swap in mind.

## Testing (strict TDD)

Component-level scenarios to drive out, covering happy-path, edge, error,
authorization, and mode-specific categories:

- **NormalizeDept / ResolveDepartments:** mixed case & whitespace collapse to one
  department; empty/blank claims → default group; array vs single-string claim;
  duplicates de-duplicated.
- **LedDepartments:** match by subject; match by email (case-insensitive); no
  match → empty; lead-of-D adds D to effective membership.
- **Provisioner:** new user in config admin list → admin; existing admin removed
  from config → demoted at login; departments replaced on each login; verified vs
  unverified email gating preserved.
- **user_departments repo:** replace is idempotent and transactional; cascade on
  user delete; list returns normalized set.
- **Middleware:** org-mode session/bearer populate Departments+LedDepartments;
  local mode yields default-group admin; missing identity → 401 unchanged.
- **/me:** returns the caller's projection; never leaks subject/email.

## Dependencies & Order

No dependency on other phases. Must land first — Phases 2–4 consume
`identity.Identity.Departments`, `LedDepartments`, the `GroupConfig`, and the
persisted departments.
