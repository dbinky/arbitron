# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

_(none yet — as issues are fixed during iterations, move them here with a summary of what was wrong and how it was fixed)_

<!--
Example of a fixed issue entry:

### ENG-001: Missing context cancellation check in retry sleep
**Area**: Engine Graph Walker (`domain/engine/engine.go`)
**Fixed**: Iteration N
**Detail**: The retry loop slept for retryDelaySeconds without checking context cancellation, meaning a cancelled instance could block for up to retryDelaySeconds before stopping. Added select with ctx.Done() during the sleep.
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### OBS-W01: No log rotation or retention policy
**Area**: Observability (`adapter/observability/logging.go`)
**Detail**: Log files grow unbounded. Adding rotation is explicitly out of scope for Phase 1 per the spec. Low risk for local single-user mode.
-->
