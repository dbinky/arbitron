# Queue List Visibility — Product Specification

## Purpose

Improve the queue list so users can quickly distinguish and monitor multiple simultaneous workflow runs, especially feature-pipeline runs, without opening each instance. The primary outcome is immediate disambiguation: a user looking at the queue should be able to tell which run is which from sortable fields, a concise generated run name, and the current operational state.

## Features

The queue list shows all workflow instances in a richer sortable table.

Each row includes a concise generated run name based on the best available submitted title or slug plus workflow name. When no clear title or slug is available, the name falls back to the most relevant submitted input, then to a short run identifier.

The table exposes separate sortable columns for workflow, submitting user, submitted local time, status, friendly current step, elapsed time, priority-aware queue position, model/backend, and retry count.

In local mode, the user-facing submitter can read as "you". In shared mode, the friendly display name is primary, with account identity available in row details.

The default queue ordering prioritizes operational usefulness: active and pending work are grouped by state, and queued items show their priority-aware position so users can understand why work will run in that order. Users can still sort by individual columns.

A compact icon button in the queue list cycles through three density views: balanced, dense, and expanded. The first-time default is balanced. After that, each user’s last selected view is remembered.

Last error details are not shown as default table columns, but are available in row details.

## Outcomes

Users can immediately distinguish simultaneous runs that use the same workflow, including multiple feature-pipeline runs started around the same time.

Users can scan the queue to understand what is running, what is waiting, where each item sits within priority ordering, how long it has been active or pending, and what step it is currently on.

Users can compare and sort runs by meaningful product fields instead of relying on one long generated name. The generated run name remains useful as a quick label, but the list is primarily useful because the identity parts and operational fields are visible separately.

Users can adjust queue density to match their current task: balanced for normal use, dense for monitoring many runs, and expanded for closer inspection.

## Non-Goals

This spec does not add new queue actions such as pause, cancel, rerun, reprioritize, or bulk operations.

This spec does not change workflow execution behavior, scheduling rules, priority calculation, or concurrency limits.

This spec does not define new workflow authoring requirements or require users to manually name every run.

This spec does not expose detailed error content directly in the default table, and it does not turn the queue list into a full debugging console.

This spec does not limit the improvement to feature-pipeline workflows; the richer queue list applies to all workflow instances.
