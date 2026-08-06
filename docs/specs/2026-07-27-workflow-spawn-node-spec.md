# Workflow Spawn Node — Product Specification

## Purpose

Add a first-class `spawn workflow` capability so workflow authors can compose larger processes from smaller workflow instances. A parent workflow can start child workflows either independently or as awaited child runs, making long development pipelines easier to split, observe, resume, and evolve over time.

## Features

- The product introduces a single `spawn workflow` node that can start a child workflow instance from within a parent workflow.

- Authors choose one of two modes: fire-and-forget, where the parent continues after the child is started, or wait-for-child, where the parent waits for the child to finish before continuing.

- The node uses LLM-assisted input preparation to turn the parent's available context into startup values for the child workflow. The child workflow's declared inputs remain the contract.

- If required startup values cannot be produced, the spawn node fails.

- In wait-for-child mode, the parent resumes through one of two required paths: `success` when the child completes successfully, or `failure` when the child fails or is cancelled.

- Both wait-for-child paths must be connected for the parent workflow to be valid.

- Child workflow instances inherit the parent instance owner.

- Users see child instances grouped under their parent in a recursive expandable hierarchy.

- Cancelling a parent cancels active children recursively.

- Fire-and-forget child outcomes do not affect the parent after the child has been spawned.

## Outcomes

- Workflow authors can break large development workflows into smaller reusable workflows without losing visibility or resumability.

- Operators can resume, retry, inspect, or cancel work at the child workflow level instead of restarting an entire larger process.

- Parent workflows can delegate work to child workflows while still handling child success and failure explicitly when the parent needs to wait.

- Workflow families can evolve more independently because parent workflows rely on the child workflow's declared inputs and LLM-assisted startup value preparation rather than rigid static mappings.

- Users can understand complex runs as a hierarchy of related workflow instances instead of a flat list of disconnected runs.

## Non-Goals

- This spec does not define separate node types for fire-and-forget versus wait-for-child behavior; both are modes of one `spawn workflow` capability.

- This spec does not add a separate cancellation path for awaited children. Cancelled children route through the same parent `failure` path as failed children.

- This spec does not make fire-and-forget child outcomes change the parent after the parent has continued.

- This spec does not require static field-by-field startup mapping between parent and child workflows.

- This spec does not define recursion limits, loop prevention, scheduling policy, storage design, APIs, or implementation architecture.
