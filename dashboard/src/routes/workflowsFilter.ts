import type { WorkflowSummary, Me } from '../api/types'

export type Scope = 'mine' | 'shared' | 'all'
export type VisFilter = 'all' | 'public' | 'department'

/**
 * Mode-aware default scope: local mode shows everything, org/edge default to the
 * shared catalog. Used when no explicit ?scope= URL param is present. Single
 * source of truth for the default — referenced by both the parser and the view.
 */
export function defaultScope(me: Me | null): Scope {
  return me && me.authMode !== 'local' ? 'shared' : 'all'
}

/**
 * The fully-resolved filter inputs the predicate matrix consumes. `showVisibility`
 * and `showDepartment` gate the corresponding facets so a hidden facet's value can
 * never silently narrow the table (the visibility/department predicates are inert
 * unless their facet is actually presented).
 */
export interface FilterCriteria {
  scope: Scope
  vis: VisFilter
  dept: string
  search: string
  showVisibility: boolean
  showDepartment: boolean
}

/**
 * filterWorkflows is the pure predicate matrix behind the catalog table. It is
 * deliberately free of React/URL/DOM dependencies so the scope × visibility ×
 * department × search combinations are unit-testable without mounting. This is
 * presentation-only filtering — see the catalog design doc: the server (every
 * read path through the WorkflowService boundary) is the authorization gate, not
 * this function.
 */
export function filterWorkflows(
  workflows: WorkflowSummary[],
  criteria: FilterCriteria,
): WorkflowSummary[] {
  const { scope, vis, dept, search, showVisibility, showDepartment } = criteria
  const q = search.trim().toLowerCase()
  return workflows.filter((w) => {
    if (scope === 'mine' && w.scope !== 'private') return false
    if (scope === 'shared' && w.scope !== 'shared') return false
    if (showVisibility && vis !== 'all' && w.visibility !== vis) return false
    if (showDepartment && dept && w.department !== dept) return false
    if (q && !w.name.toLowerCase().includes(q)) return false
    return true
  })
}

/** The validated, presentation-ready filter selection parsed from the URL. */
export interface FilterParams {
  scope: Scope
  vis: VisFilter
  dept: string
  search: string
}

/**
 * parseFilterParams validates the catalog's URL params at the boundary, before
 * any of them reach the predicate matrix. Each field neutralizes to a safe default
 * when it carries a stale or unrecognized value:
 *   - scope → the mode-aware default (defaultScope) for an unknown value;
 *   - vis → 'all' for an unknown value;
 *   - dept → '' (All departments) when the value is not in the DERIVED department
 *     set. This is the fix for the bookmark-a-removed-department footgun: a stale
 *     ?dept=legal where "legal" no longer exists would otherwise strand the table
 *     in a confusing empty state.
 *
 * @param departments - the department set derived from the loaded workflows; the
 *   authority a `?dept=` value is validated against.
 */
export function parseFilterParams(
  searchParams: URLSearchParams,
  me: Me | null,
  departments: string[],
): FilterParams {
  const scopeParam = searchParams.get('scope')
  const scope: Scope =
    scopeParam === 'mine' || scopeParam === 'shared' || scopeParam === 'all'
      ? scopeParam
      : defaultScope(me)

  const visParam = searchParams.get('vis')
  const vis: VisFilter = visParam === 'public' || visParam === 'department' ? visParam : 'all'

  const deptParam = searchParams.get('dept') ?? ''
  const dept = departments.includes(deptParam) ? deptParam : ''

  const search = searchParams.get('q') ?? ''

  return { scope, vis, dept, search }
}
