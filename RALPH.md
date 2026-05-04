# Ralph Loop Configuration

## Mission

Review and refine the implementation of the Arbitron Phase 1 workflow engine and REST API per the spec at `docs/specs/arbitron-phase1-spec.md`. The loop should verify correctness against the spec, ensure test coverage across happy path, success, failure, error, and edge case scenarios, fix gaps, and polish the implementation to production quality.

## Test Command

```bash
go test ./... -count=1 -race -timeout 120s
```

## Persona

You are a senior Go engineer specializing in workflow orchestration engines and distributed systems. You understand hexagonal (ports & adapters) architecture, strict TDD, and the Go idioms for interfaces, goroutines, and context-based cancellation. You care deeply about clean domain boundaries, comprehensive error handling, crash resilience, and observable systems. You review code with the rigor of a principal engineer preparing for a production deployment.

## Checklist

For each focus area review, verify:

1. All tests pass (`go test ./... -count=1 -race -timeout 120s`)
2. No open issues in `docs/reference/gaps-identified.md` for this focus area
3. The focus area is complete and polished — you'd be proud to ship it
4. Code aligns with the spec at `docs/specs/arbitron-phase1-spec.md`
5. Implementation matches the design docs at `docs/superpowers/specs/arbitron-phase1-design-phase-*.md`
6. Domain boundaries are clean — no adapter types leak into domain packages
7. All public functions have test coverage (happy path, failure, error, edge cases)
8. Error handling is thorough — no silent failures, errors are wrapped with context
9. Go race detector passes (included in test command via `-race`)

## Extra Constraints

- Strict hexagonal architecture: `domain/` imports nothing from `adapter/` or third-party frameworks. Only Go stdlib types (`context.Context`, `time.Time`, `uuid.UUID`) cross domain boundaries.
- Strict TDD: every feature must have tests for happy path, success, failure, error, and edge case scenarios.
- No dashboard, MCP server, multi-user auth, scheduled/webhook triggers, interactive nodes, timer/webhook wait, fan-out/fan-in, or LiteLLM in Phase 1.
- Workflows are files on disk — no CRUD via API.
- Single-user local mode with auto-created default admin user.
