# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

_(none yet — as issues are fixed during iterations, move them here with a summary of what was wrong and how it was fixed)_

<!--
Example of a fixed issue entry:

### QUEUE-001: Race between timer fire and cancel
**Area**: Timer Scheduler (`domain/queue/scheduler.go`)
**Fixed**: Iteration N
**Detail**: A race existed where Cancel() could be called after the timer fired but before the transition completed, leaving the instance in an inconsistent state. Added a state check in OnTimerFired to verify the instance is still in waiting state before transitioning.
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### QUEUE-W01: No distributed locking for multi-instance deployment
**Area**: Queue Manager (`domain/queue/manager.go`)
**Detail**: The queue evaluation logic assumes a single process. In a multi-instance deployment, two processes could dequeue the same instance. This is out of scope for Phase 2 which targets single-user local mode.
-->
