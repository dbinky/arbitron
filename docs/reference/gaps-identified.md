# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

### FA2-001: No test for server-side context cancellation in system stream handler
**Area**: System WebSocket (`adapter/http/system_stream_handler.go` line 65)
**Detail**: The `ctx.Done()` branch — triggered on server shutdown — is not covered by any test. The two existing tests verify event delivery and client disconnect, but not the graceful server-side teardown path. Low risk (the code is correct), but the branch is dark.

### FA12-001: QueueView surfaces no error UI when initial fetch fails
**Area**: Queue View (`dashboard/src/routes/QueueView.tsx` lines 38-41)
**Detail**: Errors from `fetchAll()` are only logged to `console.error`. If the initial load fails, all three sections display empty state with no error message or retry button. Users see "no instances" rather than a failure indicator.

### FA12-002: QueueSection sort-by-time uses updatedAt instead of createdAt
**Area**: Queue View (`dashboard/src/components/QueueSection.tsx` line 40)
**Detail**: Sorting by elapsed time sorts on `updatedAt` descending. For the Active section, sorting by elapsed time from `createdAt` would surface the longest-running instances. A recently-updated instance with a very recent `updatedAt` always floats to the top regardless of actual runtime.

### FA18-001: Off-by-one in dismissed notifications paging
**Area**: Notification Bell (`dashboard/src/components/NotificationBell.tsx` line 74)
**Detail**: `setHasMoreDismissed(data.length === PAGE_SIZE)` produces a false `hasMoreDismissed = false` when the total dismissed count is an exact multiple of `PAGE_SIZE`. Resolving this definitively requires a server-side `hasMore` flag or `total` count in the List response.

## Fixed Previously

### FA4: Non-atomic SaveNodePositions write
**Area**: Filesystem adapter (`adapter/filesystem/workflow_repo.go`)
**Fixed**: Iteration 1
**Detail**: `os.WriteFile` could leave `workflow.json` truncated on a mid-write crash. Changed to write to `manifestPath + ".tmp"` then `os.Rename` for atomic commit.

### FA6/FA7: WorkflowName always empty string on engine notifications
**Area**: Engine (`domain/engine/engine.go`)
**Fixed**: Iteration 1
**Detail**: Both `TypeCompleted` and `TypeFailed` notification structs omitted `WorkflowName`. The field was available via `inst.WorkflowName` after the initial `instances.Get` call. Fixed by storing the workflow name in a closure variable and populating both structs.

### FA7: Panic recovery path skipped notification emit
**Area**: Engine (`domain/engine/engine.go`)
**Fixed**: Iteration 1
**Detail**: The deferred panic handler called `SetFailed` but never created a notification, leaving users with no bell alert for panicking instances. Fixed by adding notification creation (with `WorkflowName`) to the panic recovery path.

### FA10: notifications.list offset=0 silently dropped in API client
**Area**: API Client (`dashboard/src/api/client.ts` line 178)
**Fixed**: Iteration 1
**Detail**: `if (params.offset)` is falsy for `offset === 0`, so the first page could never be explicitly requested. Changed to `if (params.offset != null)`.

### FA10: workflows.get return type was Promise<unknown>
**Area**: API Client (`dashboard/src/api/client.ts` line 129)
**Fixed**: Iteration 1
**Detail**: Typed as `Promise<unknown>`, forcing callers to use double-cast. Fixed to `Promise<WorkflowDefinition>` with an import from `../graph/types`.

### FA9: markRead return type was Promise<void>
**Area**: API Client (`dashboard/src/api/client.ts` line 189)
**Fixed**: Iteration 1
**Detail**: Backend returns `{"status":"ok"}` but the client typed it as `void`. Fixed to `Promise<{ status: string }>`.

### FA12: borderCancelled CSS used queued colour (copy-paste error)
**Area**: Queue View (`dashboard/src/components/InstanceRow.module.css` line 108)
**Fixed**: Iteration 1
**Detail**: `borderCancelled` was `var(--state-queued)` (blue-grey) instead of a distinct cancelled colour. Added `--state-cancelled: #546e7a` to `theme.css` and updated the class.

### FA9: Dead .iconButton CSS in NavBar.module.css
**Area**: NavBar (`dashboard/src/components/NavBar.module.css`)
**Fixed**: Iteration 1
**Detail**: `.iconButton` and `.iconButton:hover` were defined but never referenced in `NavBar.tsx` (the bell uses its own module's `.bellButton`). Removed dead CSS.

<!--
Example of a fixed issue entry:

### COMP-A-001: Input validation missing for empty strings
**Area**: Component A (`path/to/component_a.py`)
**Fixed**: Iteration 3
**Detail**: The `process()` function accepted empty strings without validation, which caused a downstream `IndexError` in the parser. The code handled `None` but not `""`. Added an early return with a structured error response. Added 2 tests: empty string input returns error, whitespace-only input returns error.
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### COMP-C-W01: No retry logic for transient network failures
**Area**: Component C (`path/to/component_c.ts`)
**Detail**: The external API client has no retry/backoff for 5xx responses. Adding retry logic is a robustness improvement beyond the current feature scope. Low risk since failures are surfaced to the caller.
-->
