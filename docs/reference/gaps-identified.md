# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

### WFVAL-001: `validateFileReferences` did not check `OrchestrationFile` for interactive nodes
**Area**: Interactive Node Type (`domain/workflow/validation.go`)
**Fixed**: Iteration 1
**Detail**: The `validateFileReferences` function validated `TemplateFile` and `ScriptFile` but silently ignored `OrchestrationFile`. An interactive node with a nonexistent orchestration file would pass validation and fail at runtime in the executor. Added the check alongside the existing file checks, plus two new tests: one verifying the error is produced when the file is missing (with a FileChecker), and one verifying the check is skipped when the checker is nil._

<!--
Example of a fixed issue entry:

### EXEC-001: InteractiveExecutor missing error handling for empty orchestration file
**Area**: InteractiveExecutor (`domain/engine/executor_interactive.go`)
**Fixed**: Iteration 3
**Detail**: The executor did not check for empty orchestration file content after reading. Added validation that returns a descriptive error. Added test case for empty file content.
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### CONV-W01: No rate limiting on interactive message sends
**Area**: ConversationHandler (`domain/engine/conversation.go`)
**Detail**: Users can send messages as fast as they want during an interactive session. Rate limiting would prevent abuse but is an enhancement beyond the Phase 4 scope.
-->
