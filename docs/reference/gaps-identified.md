# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

### FS-WR-001: Empty or "." workflow name bypasses path-traversal guard in write operations
**Area**: Workflow Repository Write Ops (`adapter/filesystem/workflow_repo.go`)
**Fixed**: Iteration 1
**Detail**: The path-traversal guard checked `strings.HasPrefix(wfDir+sep, root)`. When `name=""` or `name="."`, `filepath.Join(basePath, name)` resolves to `basePath` itself; since `basePath+sep == root`, the guard passed. This meant `Delete("")` would call `os.RemoveAll(basePath)`, wiping the entire workspace. Fixed by adding `wfDir == filepath.Clean(r.basePath)` as an additional guard condition in all six write methods (Create, Update, Delete, WriteFile, DeleteFile, ListFiles). Added `TestWorkflowRepo_Delete_EmptyNameRejected` and `TestWorkflowRepo_Delete_DotNameRejected` to verify the fix, plus `TestWorkflowRepo_Update_PreservesOtherFiles` to cover a previously untested success scenario from the design doc.

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### MCP-AUTH-W01: No role-based access control on MCP tools
**Area**: MCP Server (`adapter/mcp/`)
**Detail**: MCP tools have no auth gating — any connected client can call any tool including admin operations. This is by design for Phase 5 (local mode). Phase 6 will add multi-user auth and role enforcement.
-->
