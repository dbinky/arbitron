# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

### MCP-WF-001: `workflowValidateHandler` and `workflowListFilesHandler` return JSON null instead of []
**Area**: MCP Workflow Tools (`adapter/mcp/tools_workflow.go`)
**Fixed**: Iteration 4
**Detail**: `workflowValidateHandler` called `workflow.Validate()` which returns nil on success; the handler passed this directly to `MarshalResult`, producing `{"valid":true,"errors":null}`. Similarly, `workflowListFilesHandler` passed a nil `files` slice to `MarshalResult`, producing `null` instead of `[]`. LLM clients iterating over the errors/files arrays would fail with type errors on a null value. Fixed by normalising nil slices to empty slices before passing to `MarshalResult` in both handlers. Also added missing `workflow_write_file` tests for missing-name and missing-path cases, and edge-case tests asserting that both handlers emit JSON arrays (not null) when the domain returns nil.

### MCP-HLP-001: `ParseUUID` emits misleading "required" error when key exists with non-string value
**Area**: MCP Server Foundation (`adapter/mcp/helpers.go`)
**Fixed**: Iteration 3
**Detail**: `ParseUUID` used a silent comma-ok type assertion (`str, _ := args[key].(string)`). When the key was present but held a non-string value (e.g., JSON number `{"instanceId": 42}`), the assertion failed silently, `str` became `""`, and the function returned `"instanceId is required"` — factually wrong. LLM clients receiving this error could not tell whether they omitted the parameter or used the wrong type. Fixed by separating key-presence check from type assertion: key absent/nil → "required", key present with wrong type → "invalid %s: expected string, got %T", key present with valid string → proceed. Added test cases for wrong-type value and empty-string value. Also added `MarshalResult(nil)` edge-case test (nil marshals to JSON "null" — must produce a text result, not an error).

### HTTP-WH-001: `safeWorkflowName(".")` returns true, causing 500 on Create with name="."
**Area**: Workflow CRUD REST Endpoints (`adapter/http/workflow_handler.go`)
**Fixed**: Iteration 2
**Detail**: `safeWorkflowName` passed `"."` through all four guards: non-empty, no path separators, `filepath.Clean(".")` equals `"."`, and the explicit `name != ".."` check only blocked `".."`. A POST /workflows body with `{"name":"."}` reached the repo, which returned `"invalid workflow name: ."`, and the handler then returned 500 INTERNAL_ERROR instead of 400 BAD_REQUEST. Fixed by adding `name != "."` to `safeWorkflowName`. Also added missing edge-case tests: unsafe workflow name for ReadFile, WriteFile, DeleteFile, and ListFiles handlers; and a dot-name rejection test for Create.

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
