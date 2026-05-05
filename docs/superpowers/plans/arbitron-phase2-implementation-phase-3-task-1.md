# Phase 3, Task 1: Workflow Model Changes (Webhook Wait Node Type)

## Objective
Add the `NodeTypeWebhookWait` constant, webhook-specific fields to `NodeConfig`, and validation rules for webhook wait nodes.

## Dependencies
- Phase 2, Task 1 (pattern established for adding new node types)

## Files to Create/Modify
- `domain/workflow/workflow.go` — add `NodeTypeWebhookWait` constant, add `Secret` and webhook-related fields to `NodeConfig`
- `domain/workflow/validate.go` — add validation rules for webhook wait nodes
- `domain/workflow/validate_test.go` — validation tests

## Implementation Steps

1. **Add NodeTypeWebhookWait constant** in `domain/workflow/workflow.go`:
   ```go
   const (
       NodeTypeLLMCall     NodeType = "llm_call"
       NodeTypeScript      NodeType = "script"
       NodeTypeToolCall    NodeType = "tool_call"
       NodeTypeConditional NodeType = "conditional"
       NodeTypeTransform   NodeType = "transform"
       NodeTypeTimerDelay  NodeType = "timer_delay"
       NodeTypeWebhookWait NodeType = "webhook_wait"  // new
   )
   ```

2. **Add webhook fields to NodeConfig**:
   ```go
   type NodeConfig struct {
       // ... existing fields ...
       DurationSeconds string `json:"durationSeconds,omitempty"` // timer_delay
       Secret          string `json:"secret,omitempty"`          // webhook_wait: optional shared secret
       WebhookTimeout  string `json:"timeoutSeconds,omitempty"`  // webhook_wait: optional timeout, supports {{substitution}}
   }
   ```
   Note: `TimeoutSeconds` already exists on NodeConfig for scripts. Use a distinct field name like `WebhookTimeout` or reuse `TimeoutSeconds` (check if the existing field is `int` — it is: `TimeoutSeconds int`). Since the webhook timeout needs string type for substitution support, add a new field:
   ```go
   WebhookTimeoutSeconds string `json:"webhookTimeoutSeconds,omitempty"` // webhook_wait: supports {{substitution}}
   ```
   Actually, looking at the design doc, it uses `timeoutSeconds` as the JSON key. Since the existing `TimeoutSeconds int` is for script timeout, we need a string variant. Best approach: add a new Go field with a different JSON tag or use the same JSON tag with different field name. Since `timeoutSeconds` is already taken by the int field, use a distinct JSON key for webhooks:
   
   Simplest approach — reuse the `DurationSeconds` pattern and just check node type when parsing:
   ```go
   WebhookTimeoutSeconds string `json:"webhookTimeoutSeconds,omitempty"`
   ```
   Or keep it as `timeoutSeconds` in the JSON (workflow authors expect this) and handle the dual type. Actually, since NodeConfig is a flat struct and `timeoutSeconds` is an int for scripts, the cleanest approach for the webhook is a separate string field with a different JSON key that matches the design doc's `timeoutSeconds` for webhook_wait nodes only. Since the JSON deserialization happens per-node-type in practice, use:
   ```go
   WaitTimeoutSeconds string `json:"waitTimeoutSeconds,omitempty"` // webhook_wait/timer could use this
   ```
   
   **Final decision**: Use `Secret string` and `WaitTimeoutSeconds string` as new fields on NodeConfig.

3. **Add validation rules** for webhook wait nodes in `domain/workflow/validate.go`:
   ```go
   func validateWebhookWaitNode(node Node, edges []Edge) error {
       // Rule 1: Must have exactly one outgoing edge
       outgoing := countOutgoingEdges(node.ID, edges)
       if outgoing != 1 {
           return fmt.Errorf("webhook_wait node %q must have exactly one outgoing edge, has %d", node.ID, outgoing)
       }

       // Rule 2: Cannot be an entrypoint
       if node.Entrypoint {
           return fmt.Errorf("webhook_wait node %q cannot be an entrypoint", node.ID)
       }

       // Rule 3: No required fields (both secret and timeout are optional)
       // Nothing to validate — valid with empty config

       return nil
   }
   ```

4. **Wire validation** into the workflow loading pipeline — add `validateWebhookWaitNode` call for `webhook_wait` node types alongside the timer_delay validation.

5. **Update known node type list** — add `NodeTypeWebhookWait` to any validation that checks for recognized node types.

## Tests
- `domain/workflow/validate_test.go`:
  - `TestValidateWebhookWait_Valid_NoConfig` — valid with no secret, no timeout
  - `TestValidateWebhookWait_Valid_WithSecret` — valid with secret set
  - `TestValidateWebhookWait_Valid_WithTimeout` — valid with timeout set
  - `TestValidateWebhookWait_Valid_WithBoth` — valid with both secret and timeout
  - `TestValidateWebhookWait_NoOutgoingEdge` — returns error
  - `TestValidateWebhookWait_MultipleOutgoingEdges` — returns error
  - `TestValidateWebhookWait_AsEntrypoint` — returns error
  - `TestValidateWebhookWait_TimeoutWithSubstitution` — `"{{timeout}}"` is valid

## Acceptance Criteria
- `NodeTypeWebhookWait` constant equals `"webhook_wait"`
- `NodeConfig.Secret` and `NodeConfig.WaitTimeoutSeconds` fields exist
- Validation rejects webhook_wait nodes with: no outgoing edge, multiple outgoing edges, or set as entrypoint
- Validation accepts valid webhook_wait nodes with any combination of optional fields
- All existing workflow tests continue to pass
