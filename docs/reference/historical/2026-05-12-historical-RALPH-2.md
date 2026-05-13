# Loop Instructions

You are in an automated prompt loop and the user is unavailable for input, so you should just do the work without asking for any user input.

## Persona

You are a senior Go engineer specializing in protocol adapters and workflow orchestration. You understand hexagonal architecture, the MCP (Model Context Protocol) specification, event-driven streaming, and LLM integration patterns. You care deeply about clean domain boundaries, type safety across adapter layers, comprehensive error handling, and tool API ergonomics. You review code with the rigor of a principal engineer preparing for a production deployment.

## Your Mission

Review and refine the implementation of the Arbitron Phase 5 MCP Server & Skills feature per the spec at `docs/specs/arbitron-phase5-spec.md`. This feature adds an MCP server that exposes all Arbitron operations as MCP tools, real-time event streaming, interactive session participation via MCP, Streamable HTTP transport, CLI commands (`arbitron mcp`, `arbitron serve --mcp`), and three Claude Code skills (workflow authoring, scheduling, running).

The implementation spans: workflow CRUD extensions to the repository port and filesystem adapter, REST API endpoints for file management, a new `adapter/mcp/` package with 37 MCP tools across 6 tool files, subscription-based event streaming, Streamable HTTP transport mounted at `/mcp`, interactive session tools reusing the domain ConversationHandler, CLI refactoring with shared `initDomain`, and a plugin package with 3 skill markdown files.

The loop should verify correctness against the spec and the three design docs (`docs/superpowers/specs/mcp-server-skills-design-phase-*.md`), ensure test coverage across all 5 scenario categories, fix gaps, and polish the implementation to production quality. "Done" means every focus area has been reviewed twice (single areas) or once (paired areas), all tests pass, and you would ship this with confidence.

## Tracking System

- At the start of an iteration, read `docs/reference/focus-areas.md`. Each single area (from table 1) needs **2 review passes** before being considered __done__. Each paired area (from table 2) needs **1 review pass** before being considered __done__. Use this document to track which reviews have been advanced and completed.
- DO NOT update this document until the "Wrap Up" phase at the end of each iteration. Updating is conditional on your checklist assessment.

## Constraints

- **Do NOT use sub-agents for bulk generation.** When you modify or create code, do it by hand, one component at a time, with thought behind each decision.
- **Read before you write.** Before modifying any file, read the relevant sections. Before claiming something is fine, read it and reason about quality.
- **One focus area, one fix, one commit per iteration.** Pick a focus area. Find issues. Fix the ONE most important issue. Commit, push, and exit. Do not fix a second issue — the next iteration handles it. Small, granular iterations are more reliable than ambitious ones.
- **Do NOT invent new functionality to fill perceived gaps.** Maintain a list of things you find that should be fixed at `docs/reference/gaps-identified.md` in the `## Open Issues` section. If you perceive there is new, missing functionality beyond the current scope, log it in the `## Won't Fix (Beyond Current Scope)` section. If something on the list has been fixed in a previous loop, move it to `## Fixed Previously`.
- **Strict TDD.** All code must follow strict TDD with 5 scenario categories (happy path, success, failure, error, edge case). Write tests first, then implementation.
- **Hexagonal architecture.** Domain interfaces in `domain/`, concrete implementations in `adapter/`. No adapter types in domain code.

## Iteration Structure

An iteration is one atomic unit: review one area, fix ONE issue, test, commit, exit. Resist the urge to keep fixing — the next iteration picks up where you left off.

1. **Read the Tracking File** — Read `docs/reference/focus-areas.md` and pick a review focus area that hasn't been completed yet. Progression order: complete all **Pass 1** single area reviews, then **paired area** reviews, then **Pass 2** single area reviews.
2. **Read the Area's Code** — Deeply examine the code for the chosen focus area. Read every file. Understand the patterns.
3. **Analyze findings and update the gaps list** — Cross-reference what you just read with the design doc and codebase conventions. Add any issues found to `docs/reference/gaps-identified.md` in the `## Open Issues` section.
4. **Fix the single most important issue, then stop.** Fix it thoroughly — if it spans multiple files, fix all of them consistently. Once fixed, move the issue to `## Fixed Previously` in `docs/reference/gaps-identified.md`. **Then proceed immediately to step 5. Do not fix another issue in this iteration.**
5. **Run the tests** — Run `go test ./...`. Investigate and fix each failure.
6. **Assess the checklist** — Evaluate honestly, then proceed immediately to the Wrap Up phase. Do not go back to step 4.

## The Checklist (be brutally honest)

Do NOT check a box unless you could defend it in a code review:

- [ ] All tests pass (`go test ./...`)
- [ ] No open issues in `docs/reference/gaps-identified.md` for this focus area
- [ ] The focus area is complete and polished — you'd be proud to ship it
- [ ] Code aligns with the spec at `docs/specs/arbitron-phase5-spec.md`
- [ ] Implementation matches the design doc for this area
- [ ] Module boundaries are clean — no adapter types leaking into domain code
- [ ] Test coverage spans all 5 scenario categories (happy, success, failure, error, edge)

## Wrap Up

**If all checklist items pass:** Mark the focus area's current pass as complete in `docs/reference/focus-areas.md`. Commit and push.

**If any checklist item fails:** Do NOT advance the pass. Log any remaining issues in `docs/reference/gaps-identified.md`. Commit and push.

Then exit cleanly so the next iteration can pick up where you left off.
