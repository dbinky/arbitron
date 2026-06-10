# Workflows Catalog Filtering — Design

**Date:** 2026-06-10
**Status:** Approved (brainstorm)
**Branch:** dev-predeploy-testing

## Problem

The dashboard Workflows tab (`dashboard/src/routes/WorkflowsCatalog.tsx`) renders a
flat table (Name · Description · Author · Nodes) with a single name search box. It
offers no way to change the *view* of the list — no separation of the caller's own
(private) workflows from the shared/published org catalog, and no surfacing of the
visibility/department structure that the RBAC/department work introduced.

The backend already exposes this structure. `GET /api/v1/workflows` returns, per
entry: `scope` (`private`|`shared`), `publisher`, `visibility` (`public`|`department`),
and `department` (`domain/workflow/workflow.go` `WorkflowSummary`;
`WorkflowService.List` in `domain/workflow/service.go` merges the caller's private
workflows with all visible shared workflows and tags each). The dashboard's
TypeScript `WorkflowSummary` (`dashboard/src/api/types.ts`) silently drops these
fields at the type boundary, and the component never uses them. This is therefore
a frontend feature, not a backend gap.

## Goal

Add scope / visibility / department filtering to the Workflows tab, performed
client-side, with URL-persisted state and a mode-aware default view.

## Non-Goals (YAGNI)

- Server-side filtering (query params → SQL on the list endpoint). The list
  endpoint already returns the caller's complete visible set; at current catalog
  size, in-memory filtering is sufficient and matches the existing name-search
  pattern. Add later only if catalog size demands it.
- Saved/named views.
- Column sorting.
- An admin "all users' private workflows" cross-user view.

## Approach

**Chosen: Approach A — data-driven adaptive chrome, client-side filtering.**

Considered and rejected:
- **Always-on chrome** — show all filters/columns unconditionally. Simpler logic
  but shows perpetually-empty Visibility/Department columns and inert dropdowns in
  local mode (visual noise for the mode actually being run).
- **Server-side filtering** — materially more work (handler + query + tests) for no
  benefit at this scale, and breaks from the in-memory name-search pattern.

## Backend Changes

Two small touches; the list endpoint is unchanged.

1. **Expose auth mode on `/me`.** `MeHandler` (`adapter/http/me_handler.go`) is
   stateless and the in-context `Identity` does not carry the operating mode, so
   the configured `auth.Mode` string (`"local"` | `"org"` | `"edge"`;
   `domain/config/config.go` `AuthModeLocal/Org/Edge`) is passed into the handler at
   the composition root via `NewMeHandler(mode string)`. `meView` serializes it as a
   new field `authMode`. SECURITY: this is a non-secret operating-mode projection,
   consistent with the existing `meView` boundary doc.

2. **No change to `GET /api/v1/workflows`.** It already returns `scope`,
   `publisher`, `visibility`, `department`.

## Frontend Changes

### Types (`dashboard/src/api/types.ts`)

- Widen `WorkflowSummary` to add optional fields matching the API JSON
  (`omitempty` on the Go side): `scope?: 'private' | 'shared'`,
  `visibility?: 'public' | 'department'`, `publisher?: string`,
  `department?: string`.
- Add `authMode: 'local' | 'org' | 'edge'` to the `Me` interface.

### Component (`dashboard/src/routes/WorkflowsCatalog.tsx`)

**State** — backed by react-router `useSearchParams` so the view lives in the URL:

| Param  | Values                      | Meaning                       |
|--------|-----------------------------|-------------------------------|
| `scope`| `mine` \| `shared` \| `all` | Scope filter                  |
| `vis`  | `all` \| `public` \| `department` | Visibility filter       |
| `dept` | department name             | Department filter             |
| `q`    | free text                   | Name search (existing)        |

- Changing any control updates the URL query params (`/workflows?scope=mine`).
- Params equal to the active default are omitted, keeping URLs clean.
- Reload, back/forward, and link-sharing all reproduce the view.

**Default view** — when no `scope` param is present, derive from `me.authMode`:
`local` → `all`; otherwise (`org`/`edge`) → `shared`. An explicit URL `scope`
param always overrides the default. (`me` is fetched alongside the workflow list;
until it resolves, fall back to `all`.)

**Filtering** — an in-memory `useMemo` chain over the loaded list, same approach as
the current name search: scope → visibility → department → name `q`.

**Adaptive chrome (data-driven)** — facets are derived from the *loaded workflows*,
not from mode flags:

- Scope tabs (`Mine | Shared | All`) and the **Scope** column: always shown
  (private-vs-shared is always meaningful).
- **Visibility** filter + column: shown only when the visible set contains ≥1
  shared workflow.
- **Department** filter + column: shown only when the visible set contains ≥1
  workflow with a non-empty `department`; the dropdown's options are the distinct
  departments present in the data.

This mirrors the existing `DepartmentSelector` convention (self-hides when there is
nothing to choose). Net effect in local mode (seeded samples are public, no
department): the user sees `Mine | Shared | All` + a Scope column, and the
visibility/department controls do not render.

### Presentation (`dashboard/src/routes/WorkflowsCatalog.module.css`)

- `scope` and `visibility` cells render as small badge "pills".
- Reuse the existing header/table styling; add minimal badge and
  segmented-control classes.
- Empty cells show `—`, matching the current convention.

## Data Flow

```
GET /api/v1/me           -> { ..., authMode }            (default-view choice)
GET /api/v1/workflows    -> WorkflowSummary[] (with scope/visibility/department)
   |
   v
useSearchParams (scope/vis/dept/q)  <->  URL
   |
   v
useMemo filter chain: scope -> visibility -> department -> q
   |
   v
derive facets present (anyShared, departments[]) -> adaptive chrome
   |
   v
render table (columns conditional on facets)
```

## Error Handling

- List/`/me` fetch failures use the component's existing error state.
- An unknown/garbage URL param value falls back to that filter's default rather
  than erroring (e.g. `?scope=bogus` → mode default).
- A `dept` param naming a department not present in the data is treated as "no
  match" (empty result with the standard empty-state message), not an error.

## Testing (TDD)

**Go (`adapter/http`):**
- `MeHandler` returns `authMode` correct for a local identity and for an org
  identity.

**Vitest (`dashboard`):**
- Filtering logic for each axis (scope, visibility, department), combined, and with
  the name search.
- Default scope derived from `authMode` (`local` → all, `org`/`edge` → shared) when
  no `scope` param is present.
- An explicit URL param overrides the mode default.
- URL round-trip: changing a filter writes the query param; mounting with params
  applies them.
- Adaptive chrome: visibility/department controls + columns are hidden for
  public-only / no-department data and shown when such data is present.

## Affected Files

- `adapter/http/me_handler.go` (+ composition root wiring `NewMeHandler`)
- `adapter/http/me_handler_test.go`
- `dashboard/src/api/types.ts`
- `dashboard/src/routes/WorkflowsCatalog.tsx`
- `dashboard/src/routes/WorkflowsCatalog.module.css`
- `dashboard/src/routes/WorkflowsCatalog.test.tsx` (new/extended)
