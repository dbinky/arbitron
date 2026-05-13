# Focus Areas Review Tracking

This document tracks the systematic review of the Phase 5 MCP Server & Skills implementation for correctness, security, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

**Progression order:** Complete all Pass 1 single area reviews first. Then do paired area reviews (which catch integration issues while the code is fresh). Then do Pass 2 single area reviews.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Workflow Repository Write Ops (`domain/workflow/ports.go`, `adapter/filesystem/workflow_repo.go` — Create, Update, Delete, WriteFile, DeleteFile, ListFiles, path-traversal protection, atomic writes) | [x] | [ ] | |
| 2 | Workflow CRUD REST Endpoints (`adapter/http/workflow_handler.go`, `adapter/http/server.go` — POST/PUT/DELETE /workflows, file management endpoints, input validation, error mapping) | [x] | [ ] | |
| 3 | MCP Server Foundation (`adapter/mcp/server.go`, `adapter/mcp/deps.go`, `adapter/mcp/helpers.go` — Server construction, AddTool, ServeStdio, Listen, HTTPHandler, Deps wiring, MarshalResult, ParseUUID) | [x] | [ ] | |
| 4 | MCP Workflow Tools (`adapter/mcp/tools_workflow.go` — 10 tools: list, get, create, update, delete, validate, list_files, read_file, write_file, delete_file) | [x] | [ ] | |
| 5 | MCP Instance Tools (`adapter/mcp/tools_instance.go` — 9 tools: list, get, trigger, pause, resume, cancel, retry, rerun, set_priority) | [x] | [ ] | |
| 6 | MCP Trace & System Tools (`adapter/mcp/tools_trace.go`, `adapter/mcp/tools_system.go` — trace_get, trace_get_node, variables_get, variables_history, system_config, system_update_queue_config, notification_list/unread/mark_read/dismiss, workflow_stats) | [x] | [ ] | |
| 7 | MCP Event Streaming (`adapter/mcp/subscriptions.go`, `adapter/mcp/tools_streaming.go` — SubscriptionManager, instance/system subscribe/unsubscribe, event forwarding, cleanup, goroutine safety) | [x] | [ ] | |
| 8 | Streamable HTTP Transport (`adapter/mcp/server.go:HTTPHandler`, `adapter/http/server.go` — /mcp mount, StreamableHTTPServer wiring, router integration, parameter threading) | [x] | [ ] | |
| 9 | Interactive Session MCP Tools (`adapter/mcp/tools_interactive.go` — send_message, end_session, get_history, ConversationHandler integration, nil-safety) | [x] | [ ] | |
| 10 | CLI Commands (`cmd/arbitron/main.go` — initDomain extraction, domainStack, mcpDeps(), arbitron mcp subcommand, --mcp flag on serve, graceful shutdown) | [x] | [ ] | |
| 11 | MCP Tool Registration (`adapter/mcp/register.go` — RegisterAllTools, tool count, tool name uniqueness, complete API surface coverage) | [x] | [ ] | |
| 12 | Plugin Package (`plugin/plugin.json`, `plugin/mcp/arbitron.json`, `plugin/skills/workflow-authoring.md` — metadata, MCP config, authoring skill completeness and tool references) | [x] | [ ] | |
| 13 | Scheduling & Running Skills (`plugin/skills/workflow-scheduling.md`, `plugin/skills/workflow-running.md` — scheduling skill completeness, running skill flows, tool references, behavioral guidelines) | [x] | [ ] | |
| 14 | MCP-to-Domain Boundary (how MCP tool handlers translate arguments, call domain ports, and map errors — consistency across all 37 tools) | [x] | [ ] | |
| 15 | Test Coverage Quality (`adapter/mcp/*_test.go`, `adapter/filesystem/workflow_repo_test.go`, `adapter/http/workflow_handler_test.go` — 5 scenario categories, mock quality, assertion specificity, edge cases) | [x] | [ ] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 16 | Workflow Repo + REST Endpoints (Create/Update/Delete flow through handler → repo, error mapping consistency, path validation at both layers) | [x] | |
| 17 | MCP Foundation + Tool Implementation (Deps threading, handler closure pattern, MarshalResult/ParseUUID usage consistency, error result pattern across all tool files) | [x] | |
| 18 | MCP Workflow Tools + Workflow Repo (tool args → repo method args, error strings match handler mapping, file operations round-trip) | [x] | |
| 19 | MCP Instance Tools + Engine Service (tool args → engine method signatures, UUID parsing, priority/state string handling) | [x] | |
| 20 | Event Streaming + Subscription Manager (subscribe/unsubscribe lifecycle, EventBus channel cleanup, goroutine leak safety, connection close cleanup) | [x] | |
| 21 | Interactive Tools + ConversationHandler (HandleMessage/EndSession call signatures, get_history node execution lookup, nil handler safety) | [x] | |
| 22 | MCP-to-Domain Boundary + Workflow Repo (do MCP tool error strings match what the repo returns? does the MCP layer add or swallow errors?) | [x] | |
| 23 | MCP-to-Domain Boundary + Engine Service (do MCP tool error results match engine sentinel errors? is error context preserved or lost?) | [x] | |
| 24 | Streamable HTTP + Router Integration (NewRouter parameter threading, /mcp mount position, existing route collision check, nil handler fallthrough) | [x] | |
| 25 | CLI + MCP Server (initDomain → mcpDeps() → NewServerWithDeps flow, shared state between HTTP and MCP in --mcp mode, shutdown coordination) | [x] | |
| 26 | Skills + MCP Tool Names (every tool referenced in skill markdown exists in RegisterAllTools, no stale/renamed references, parameter names match) | [x] | |
| 27 | Test Coverage Quality + All MCP Tool Files (do tests across all tool files follow the same 5-category pattern? are mocks consistent? are edge cases covered uniformly?) | [ ] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage (happy path, failure, error, edge cases)
- The code does what the design doc says it should
- Error handling is thorough — no silent failures
- Module boundaries are clean (no leaking types across boundaries)

**Pass 2 (Robustness & Extensibility):**
- Code aligns to the spirit of the design, not just surface conformance
- The component is ready to be extended by future work (Phase 6 auth, Phase 7 triggers)
- Test assertions test real risks, not just happy-path ceremony
- Edge cases are handled (empty inputs, concurrent access, boundary values)

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- Data flows cleanly across the boundary
- Error propagation works end-to-end
- No assumptions in one component that the other doesn't satisfy
- The integration tests cover the seams between the pair
