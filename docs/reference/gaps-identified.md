# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

_(none yet — as issues are fixed during iterations, move them here with a summary of what was wrong and how it was fixed)_

<!--
Example of a fixed issue entry:

### MCP-WF-001: Input validation missing for empty strings
**Area**: MCP Workflow Tools (`adapter/mcp/tools_workflow.go`)
**Fixed**: Iteration 3
**Detail**: The `workflow_create` handler accepted empty workflow names without validation, which caused a downstream filesystem error. Added an early return with a tool error result. Added test for empty name input.
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### MCP-AUTH-W01: No role-based access control on MCP tools
**Area**: MCP Server (`adapter/mcp/`)
**Detail**: MCP tools have no auth gating — any connected client can call any tool including admin operations. This is by design for Phase 5 (local mode). Phase 6 will add multi-user auth and role enforcement.
-->
