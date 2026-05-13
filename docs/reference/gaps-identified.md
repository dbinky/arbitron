# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

### MCP-INST-ERR-001: TestInstancePause and TestInstanceRerun engine error tests lacked text assertions
**Area**: MCP Instance Tools (`adapter/mcp/tools_instance_test.go`)
**Fixed**: Pair 23
**Detail**: Both `TestInstancePause` and `TestInstanceRerun` had an "engine error returned as tool error" sub-test that only asserted `result.IsError == true`. No assertion verified the error message text was propagated through the MCP layer. In contrast, `TestInstanceResume`, `TestInstanceCancel`, and `TestInstanceRetry` all asserted `assert.Contains(t, text, <error-keyword>)`. Fixed by adding `text := extractText(t, result)` and the corresponding `assert.Contains` assertion to both missing tests, confirming the error context (`"not running"`, `"cannot rerun"`) is preserved end-to-end.

### FS-FR-001: FileReader.safeJoin only checked workspace root — cross-workflow path traversal allowed
**Area**: Workflow File Reading (`adapter/filesystem/file_reader.go`)
**Fixed**: Iteration 22
**Detail**: `safeJoin` verified that the resolved path starts with `basePath` (workspace root), but did not verify it stays within the specific workflow subdirectory. A relative path like `"../other-wf/secret.txt"` resolved to `basePath/other-wf/secret.txt`, which passes the workspace-root check but reads from a different workflow's files. An LLM calling `workflow_read_file(name="wf-a", path="../wf-b/secret.txt")` could access any file in any other workflow directory. Fixed by adding a second `strings.HasPrefix` check against the workflow-directory root (`wfDir+sep`) after the workspace-root check. Added test `TestFileReader_PathTraversal_CrossWorkflowRejected` that places a secret in a sibling workflow directory and verifies the traversal is rejected with "escapes workflow directory".

### MCP-INT-NIL-001: interactiveGetHistoryHandler missing nil guard for instance before CurrentNodeID access
**Area**: Interactive Session MCP Tools (`adapter/mcp/tools_interactive.go`)
**Fixed**: Iteration 21
**Detail**: `interactiveGetHistoryHandler` called `inst.CurrentNodeID` without first checking if `inst` was nil. `Instances.Get` returns `(*Instance, error)`, and a repo returning `(nil, nil)` (the default stub behaviour) causes a nil pointer dereference on the field access, panicking the MCP server process. Fixed by adding a nil guard after the error check (same pattern as MCP-NIL-001, MCP-WF-NIL-001). Test added: "nil instance without error returns not found" in `TestInteractiveGetHistory`.

### MCP-INST-PRIO-001: instanceSetPriorityHandler and instanceTriggerHandler accept invalid priority strings
**Area**: MCP Instance Tools (`adapter/mcp/tools_instance.go`)
**Fixed**: Iteration 19
**Detail**: Both `instanceSetPriorityHandler` and `instanceTriggerHandler` passed the `priority` argument directly to the engine without validating it against the three valid values (`high`, `normal`, `low`). The engine performs only a type cast to `instance.Priority(priority)`, so an LLM passing `"urgent"` or `"CRITICAL"` would persist that invalid string to the database without error. Fixed by adding an `isValidPriority` helper and guarding both handlers before the engine call: `instanceSetPriorityHandler` rejects any non-empty value outside the valid set; `instanceTriggerHandler` rejects non-empty values outside the valid set (empty string still allowed as "no priority specified"). Tests added: "invalid priority value returns error" in both `TestInstanceTrigger` and `TestInstanceSetPriority`.

### MCP-WF-NIL-001: workflowGetHandler and workflowValidateHandler missing nil guards for Load returning (nil, nil)
**Area**: MCP Workflow Tools (`adapter/mcp/tools_workflow.go`)
**Fixed**: Iteration 18
**Detail**: Two nil-guard omissions found during Pair 18 review. (1) `workflowGetHandler` called `MarshalResult(wf)` without a nil check — a repo returning `(nil, nil)` produces JSON `null` rather than a not-found error. (2) `workflowValidateHandler` passed `wf` directly to `workflow.Validate(wf, ...)` without a nil check — `Validate` dereferences `wf.Nodes` on line 65, causing a nil pointer panic. Both handlers now have nil guards (same pattern as MCP-NIL-001). Tests added: "nil workflow without error returns not found" in both `TestWorkflowGet` and `TestWorkflowValidate`.

### MCP-WF-UPD-001: workflowUpdateHandler allowed manifest name to overwrite tool-arg-authoritative name
**Area**: MCP Workflow Tools (`adapter/mcp/tools_workflow.go`)
**Fixed**: Iteration 17
**Detail**: `workflowUpdateHandler` decoded the `manifest` argument into a `workflow.Workflow` and passed it directly to `repo.Update` without enforcing that `wf.Name` matches the `name` tool arg. An LLM calling `workflow_update` with `name="prod-wf"` and a manifest containing `"name":"dev-wf"` would store `workflow.json` with `"name":"dev-wf"` inside the `prod-wf/` directory — the same name/directory mismatch bug fixed in the HTTP handler (HTTP-UPD-001). Fixed by setting `wf.Name = name` (from the tool arg) before calling `repo.Update`. Added "tool name arg wins when manifest name differs" test that captures the workflow passed to the repo and asserts its Name equals the tool arg name.

### HTTP-UPD-001: Update handler allowed body name to overwrite URL-authoritative name in workflow.json
**Area**: Workflow CRUD REST Endpoints (`adapter/http/workflow_handler.go`)
**Fixed**: Iteration 16
**Detail**: The `Update` handler decoded the request body into a `workflow.Workflow` and passed it directly to `repo.Update` without enforcing that `wf.Name` matches the URL path segment. A `PUT /workflows/prod-wf` request with body `{"name":"dev-wf", ...}` would write `workflow.json` containing `"name":"dev-wf"` inside the `prod-wf/` directory — a permanent name/directory mismatch visible on every subsequent `Load("prod-wf")` call. Fixed by setting `wf.Name = name` (from the URL param) before calling repo.Update. Added "URL name wins when body name differs" test that captures the workflow passed to the repo and asserts both the stored name and response body name equal the URL name.

### MCP-NIL-001: variablesGetHandler panics and instanceGetHandler returns null on nil-instance repo return
**Area**: MCP Trace & Instance Tools (`adapter/mcp/tools_trace.go`, `adapter/mcp/tools_instance.go`)
**Fixed**: Iteration 15
**Detail**: Two nil-guard omissions found during Test Coverage Quality review. (1) `variablesGetHandler` called `inst.Variables` without first checking if `inst` was nil. `Instances.Get` returns `(*Instance, error)`, and a repo returning `(nil, nil)` (default stub behaviour) causes a nil pointer dereference, panicking the MCP server process. (2) `instanceGetHandler` called `MarshalResult(inst)` without a nil guard — a `(nil, nil)` return produces JSON `null` rather than a not-found error, confusing LLM clients. Fixed by adding nil guards to both handlers (same pattern as MCP-INT-001 and MCP-BOUND-001). Tests added: "nil instance without error returns not found" in both `TestVariablesGet` and `TestInstanceGet`.

### MCP-BOUND-001: workflowListHandler and traceGetNodeHandler missing nil guards
**Area**: MCP-to-Domain Boundary (`adapter/mcp/tools_workflow.go`, `adapter/mcp/tools_trace.go`)
**Fixed**: Iteration 14
**Detail**: Two nil boundary issues found during cross-handler consistency review. (1) `workflowListHandler` passed nil summaries directly to `MarshalResult`, producing JSON `null` for empty workspaces instead of `[]` — an LLM client's first list call on a fresh installation would receive null and fail to iterate. Fixed by normalising nil to `[]workflow.WorkflowSummary{}`. (2) `traceGetNodeHandler` called `MarshalResult(exec)` without a nil guard — if `GetByInstanceAndNode` returns `(nil, nil)` (the default stub behaviour), the handler returns confusing JSON `null` rather than an error. Fixed by adding a nil guard (same pattern as MCP-INT-001). Tests added for both: "nil summaries returns empty array not null" and "nil exec without error returns not found".

### SKILL-RUN-001: Running skill interactive trigger lacked specific notification type
**Area**: Scheduling & Running Skills (`plugin/skills/workflow-running.md`)
**Fixed**: Iteration 13
**Detail**: The "Interactive participation flow" trigger condition said "detected via a notification" without specifying the `needs_interaction` notification type. The design doc (phase-3.md §4) explicitly names `notification.created` with type `needs_interaction` as the signal. Without this specificity, Claude Code could respond to the wrong notifications. Fixed by updating the trigger condition to reference `notification.created` with type `needs_interaction`. Also added `variables_history` → "How did variables change?" to the instance management natural-language mapping table (it was listed in available tools but had no mapping).

### SKILL-AUTH-001: Authoring skill missing "propose and confirm" step before applying modifications
**Area**: Plugin Package (`plugin/skills/workflow-authoring.md`)
**Fixed**: Iteration 12
**Detail**: The "Modifying an existing workflow" section jumped from "Understand the requested changes" directly to "Apply the changes" — skipping the design doc's explicit step 3: "Propose the changes and confirm with the user." For destructive operations (deleting files, removing nodes), this meant Claude Code could make irreversible changes without user confirmation. Added a new step 4: "Propose the changes and confirm" that requires describing what will be added, changed, or removed and getting the user's agreement before any destructive action.

### MCP-REG-001: RegisterAllTools had no test coverage
**Area**: MCP Tool Registration (`adapter/mcp/register.go`)
**Fixed**: Iteration 11
**Detail**: `register.go` had no test file. Created `register_test.go` with 4 tests: `TestRegisterAllTools_Count` (exactly 37 tools), `TestRegisterAllTools_NamesAreUnique` (no duplicates), `TestRegisterAllTools_APISurface` (all 37 design-doc names present), `TestRegisterAllTools_Idempotent` (double-call behavior documented). All 37 tool names verified against phase-1.md and phase-2.md design docs.

### CLI-001: Tracing provider not shut down when RecoverCrashedInstances fails in initDomain
**Area**: CLI Commands (`cmd/arbitron/main.go`)
**Fixed**: Iteration 10
**Detail**: When `RecoverCrashedInstances` fails after `SetupTracing` has already succeeded, the error path closed `pool` and `logCloser` but skipped calling `tracingShutdown`. This leaks the OTel tracer and forfeits any pending span flushes. Fixed by adding a nil-guarded `tracingShutdown(ctx)` call before closing pool/logger in that error path. Also added: `TestServeCommand_HasRunE` and `TestMCPCommand_HasRunE` to verify both commands have their `RunE` handler wired (a nil RunE silently does nothing).

### MCP-INT-001: Nil exec dereference in interactiveGetHistoryHandler
**Area**: Interactive Session MCP Tools (`adapter/mcp/tools_interactive.go`)
**Fixed**: Iteration 9
**Detail**: `interactiveGetHistoryHandler` called `exec.LLMConversation` without first checking if `exec` was nil. `GetByInstanceAndNode` returns `(*NodeExecution, error)`, and a repo that returns `(nil, nil)` (the default stub behaviour) would cause a nil pointer dereference, panicking the MCP server process. Fixed by adding a nil guard after the error check. Also added: invalid UUID test for `interactive_end_session`, invalid UUID test for `interactive_get_history`, and a "nil exec without error returns not found" edge case test.

### HTTP-MCP-001: Security headers not verified on /mcp endpoint responses
**Area**: Streamable HTTP Transport (`adapter/http/server.go`)
**Fixed**: Iteration 8
**Detail**: `securityHeaders` middleware is applied via `r.Use(...)` before routes so it wraps the `/mcp` mount — but this was untested. Added `TestRouter_MCPEndpoint_SecurityHeaders` to verify that X-Content-Type-Options, X-Frame-Options, and Referrer-Policy headers appear on MCP endpoint responses, confirming the middleware chain covers mounted sub-routers.

### MCP-STRM-001: Streaming tools "not available" guard not tested for 3 of 4 handlers
**Area**: MCP Event Streaming (`adapter/mcp/tools_streaming.go`, `adapter/mcp/tools_streaming_test.go`)
**Fixed**: Iteration 7
**Detail**: `TestStreamingToolsWithoutEventBus` only exercised the nil-guard path for `instance_subscribe`. Extended to cover all 4 tool handlers (instance_subscribe, instance_unsubscribe, system_subscribe, system_unsubscribe). Also added: missing instanceId test for instance_subscribe, invalid UUID test for instance_unsubscribe, and tightened text assertions on existing error tests.

### MCP-TRACE-001: Nil slice normalization missing in trace/system list handlers
**Area**: MCP Trace & System Tools (`adapter/mcp/tools_trace.go`, `adapter/mcp/tools_system.go`)
**Fixed**: Iteration 6
**Detail**: Three handlers passed nil slices directly to `MarshalResult`, producing JSON `null` instead of `[]`: `traceGetHandler` (NodeExecution), `variablesHistoryHandler` (VariableSnapshot), `notificationListHandler` (Notification). Fixed by normalising nil to typed empty slices. Also added: UUID error tests for `variables_get`/`variables_history`, domain error test for `trace_get_node`, domain error tests for `notification_unread_count`/`notification_mark_read`, "missing id" test for `notification_dismiss`, and the two missing `system_config` CountQueued/CountWaiting error paths.

### MCP-INST-001: `instanceListHandler` returns JSON null instead of [] when repo returns nil slice
**Area**: MCP Instance Tools (`adapter/mcp/tools_instance.go`)
**Fixed**: Iteration 5
**Detail**: `instanceListHandler` passed nil summaries directly to `MarshalResult`, producing JSON `null` instead of `[]`. Fixed by normalising nil to `[]instance.InstanceSummary{}` before marshalling. Also added missing nil-list edge case test in `TestInstanceList` (verifies `[]` output) and "missing id" test in `TestInstanceSetPriority`.

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

### STRM-W01: SubscriptionManager.OnEvent never wired in production — events silently dropped
**Area**: Event Streaming (`adapter/mcp/subscriptions.go`, `adapter/mcp/server.go`)
**Detail**: The design doc (phase-2.md §1-2) requires that the MCP adapter translate domain EventBus events into MCP notifications sent to the client. The `SubscriptionManager.OnEvent` callback is the intended hook, but it is never set in production code. Events from the EventBus flow into the forward goroutines but are silently dropped (`if cb != nil` check). Implementing this requires MCP server-push notification support from the mcp-go library or a custom SSE-based mechanism — this is new functionality beyond the current Phase 5 review scope.

### STRM-W02: CleanupAll not called on HTTP connection close — subscriptions persist after disconnect
**Area**: Event Streaming (`adapter/mcp/subscriptions.go`, `adapter/http/server.go`)
**Detail**: The comment on `CleanupAll` says "Called on connection close" but no production code actually calls it. For the Streamable HTTP transport, when an HTTP client disconnects, forward goroutines continue running indefinitely until the process exits. For stdio mode this is moot (process exits on disconnect). Wiring `CleanupAll` to connection lifecycle requires hooks into the mcp-go StreamableHTTPServer that are not currently available or implemented.

<!--
Example of a won't-fix entry:

### MCP-AUTH-W01: No role-based access control on MCP tools
**Area**: MCP Server (`adapter/mcp/`)
**Detail**: MCP tools have no auth gating — any connected client can call any tool including admin operations. This is by design for Phase 5 (local mode). Phase 6 will add multi-user auth and role enforcement.
-->
