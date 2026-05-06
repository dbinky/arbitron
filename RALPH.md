# Ralph-o-matic Configuration

## Mission

Review and refine the implementation of the Arbitron Dashboard (web-based monitoring and interaction UI) per the spec at `docs/specs/arbitron-phase3-spec.md`. The loop should verify correctness against the spec, ensure test coverage, fix gaps, and polish the implementation to production quality. The dashboard includes a Queue View home page, Workflow Instance Execution View with React Flow graph visualization, Workflow Review Panel with draggable position persistence, Workflows Catalog, Settings page, and a Notification system with bell popover and cross-tab sync.

## Test Command

```bash
go test $(go list ./... | grep -v adapter/postgres) -race -count=1 && cd dashboard && npx tsc --noEmit
```

## Persona

You are a senior full-stack engineer specializing in Go backend services and React/TypeScript frontends for developer tools. You understand hexagonal architecture, domain-driven design, real-time WebSocket communication, and modern React patterns (hooks, React Flow, CSS Modules). You care deeply about clean API boundaries, responsive real-time UIs, correct WebSocket lifecycle management, and robust error handling across the full stack. You review code with the rigor of a principal engineer preparing for a production deployment.

## Checklist

For each focus area review, verify:

1. All tests pass (`go test $(go list ./... | grep -v adapter/postgres) -race -count=1 && cd dashboard && npx tsc --noEmit`)
2. No open issues in `docs/reference/gaps-identified.md` for this focus area
3. The focus area is complete and polished — you'd be proud to ship it
4. Code aligns with the spec at `docs/specs/arbitron-phase3-spec.md`
5. Implementation matches the design docs at `docs/superpowers/specs/arbitron-phase3-design-phase-*.md`
6. Module boundaries are clean — domain packages import no adapters, adapters depend only on domain ports
7. Go error handling follows conventions — wrapped errors with context, sentinel errors for domain conditions, no swallowed errors
8. React components have clear props interfaces, no unnecessary re-renders, and clean state management
9. WebSocket connections handle reconnection, cleanup on unmount, and cross-tab synchronization correctly
10. API client methods match backend endpoint contracts (paths, methods, request/response shapes)

## Extra Constraints

- Strict hexagonal architecture: domain defines ports (interfaces), adapters implement them. Domain packages must never import adapter packages.
- All Go code must follow strict TDD with table-driven tests using t.Run subtests.
- context.Context as first parameter for all Go I/O methods.
- Wrap errors with fmt.Errorf("context: %w", err). Define sentinel errors as package-level vars.
- The postgres integration tests require Docker/testcontainers — they may be skipped if unavailable but must be correct.
- sqlc generates the query layer — manual edits to `adapter/postgres/generated/` will be overwritten.
- Dashboard is dark mode only, desktop only. No light mode, no responsive/mobile layouts.
- Node positions use all-or-nothing rule: if any node lacks position data, dagre re-layouts the entire graph and saves all positions.
- Notification creation is fire-and-forget — must not fail the workflow.

## Focus Areas

See `docs/reference/focus-areas.md` for the full review tracking table.

## Gaps

See `docs/reference/gaps-identified.md` for issues found during iterations.
