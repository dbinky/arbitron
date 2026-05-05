# Phase 2, Task 1: Workflow Model Changes (Timer Delay Node Type)

## Objective
Add the `NodeTypeTimerDelay` constant, `TimerDelayConfig` fields to `NodeConfig`, and validation rules for timer delay nodes in the workflow model.

## Dependencies
- Phase 1, Task 1 (StateWaiting must exist for the broader Phase 2 context)

## Files to Create/Modify
- `domain/workflow/workflow.go` — add `NodeTypeTimerDelay` constant, add `DurationSeconds` field to `NodeConfig`
- `domain/workflow/validate.go` — add validation rules for timer delay nodes (or create if not existing)
- `domain/workflow/workflow_test.go` or `domain/workflow/validate_test.go` — validation tests

## Implementation Steps

1. **Add NodeTypeTimerDelay constant** in `domain/workflow/workflow.go`:
   ```go
   const (
       NodeTypeLLMCall     NodeType = "llm_call"
       NodeTypeScript      NodeType = "script"
       NodeTypeToolCall    NodeType = "tool_call"
       NodeTypeConditional NodeType = "conditional"
       NodeTypeTransform   NodeType = "transform"
       NodeTypeTimerDelay  NodeType = "timer_delay"  // new
   )
   ```

2. **Add DurationSeconds to NodeConfig**:
   ```go
   type NodeConfig struct {
       // ... existing fields ...
       DurationSeconds string `json:"durationSeconds,omitempty"` // timer_delay: supports {{substitution}}
   }
   ```

3. **Add validation rules** — either in existing `validate.go` or create `domain/workflow/validate.go`:
   ```go
   func validateTimerDelayNode(node Node, edges []Edge) error {
       // Rule 1: Timer delay nodes must have exactly one outgoing edge
       outgoing := countOutgoingEdges(node.ID, edges)
       if outgoing != 1 {
           return fmt.Errorf("timer_delay node %q must have exactly one outgoing edge, has %d", node.ID, outgoing)
       }

       // Rule 2: DurationSeconds is required (must not be empty)
       if node.Config.DurationSeconds == "" {
           return fmt.Errorf("timer_delay node %q: durationSeconds is required", node.ID)
       }

       // Rule 3: Timer delay nodes cannot be entry points
       if node.Entrypoint {
           return fmt.Errorf("timer_delay node %q cannot be an entrypoint", node.ID)
       }

       return nil
   }
   ```

4. **Wire validation into workflow loading** — ensure that when a workflow is loaded/parsed, timer_delay nodes are validated. Add a call to `validateTimerDelayNode` in the existing validation pipeline (look for where other node types are validated).

5. **Update any node type validation lists** — if there is a set of known node types used for validation, add `NodeTypeTimerDelay` to it.

## Tests
- `domain/workflow/validate_test.go`:
  - `TestValidateTimerDelay_Valid` — valid node with one outgoing edge and durationSeconds set
  - `TestValidateTimerDelay_MissingDurationSeconds` — returns error
  - `TestValidateTimerDelay_NoOutgoingEdge` — returns error
  - `TestValidateTimerDelay_MultipleOutgoingEdges` — returns error
  - `TestValidateTimerDelay_AsEntrypoint` — returns error
  - `TestValidateTimerDelay_WithSubstitution` — `"{{delay}}"` is valid (not validated at parse time)

## Acceptance Criteria
- `NodeTypeTimerDelay` constant equals `"timer_delay"`
- `NodeConfig.DurationSeconds` field exists as string
- Validation rejects timer_delay nodes that: have no outgoing edge, have multiple outgoing edges, have empty durationSeconds, or are entrypoints
- Validation accepts valid timer_delay nodes (including those with `{{substitution}}` syntax)
- All existing workflow tests continue to pass
