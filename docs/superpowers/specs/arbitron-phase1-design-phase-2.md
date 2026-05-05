# Arbitron Phase 1 — Design Phase 2: Engine

## Overview

This design phase covers the execution engine: variable substitution, node executors for all five types, the graph walker, error handling with retries, and instance lifecycle management. The goal: the system can trigger a workflow and execute it to completion, handling errors, pauses, cancels, retries, and re-runs.

---

## 1. Variable Substitution

**`domain/variable/substitution.go`** — a pure function with no dependencies.

```go
func Resolve(input string, store *VariableStore) string
```

**Algorithm:**
1. Scan the input string for `{{...}}` patterns using a simple state machine (not regex — handles nested braces correctly)
2. For each match, extract the dot-path (e.g. `validate.score.value`)
3. Split on `.` to get path segments
4. Walk the variable store:
   - First segment: look up in top-level variables, then check if it's a node output namespace
   - Subsequent segments: navigate into the JSON value — string keys for objects, parse integer for array indices
5. If the path resolves to a value:
   - Strings: insert the raw string value (no quotes)
   - Numbers, booleans: insert the literal representation
   - Objects, arrays: insert the JSON-serialized representation
6. If the path doesn't resolve (missing key, out-of-bounds index, navigating into a non-container): leave the literal `{{...}}` in place

**`domain/variable/variable.go`** — the VariableStore type:

```go
type VariableStore struct {
    Inputs      map[string]any        // Top-level input variables
    NodeOutputs map[string]any        // Keyed by node ID, values are JSON objects
}
```

The store provides helper methods:
- `Get(dotPath string) (any, bool)` — resolve a dot-path, return value and whether it was found
- `SetInput(key string, value any)` — set a top-level variable
- `SetNodeOutput(nodeID string, output any)` — store a node's structured output
- `DeleteInput(key string)` — remove a top-level variable
- `Snapshot() map[string]any` — return a merged view for serialization to Postgres (inputs + node outputs flattened into one map)
- `FromSnapshot(data map[string]any)` — reconstruct from a Postgres snapshot

**Testing:**
- Simple path: `{{name}}` resolves from inputs
- Nested path: `{{validate.score}}` resolves from node outputs
- Deep nested path: `{{analyze.result.items.0.name}}` resolves through objects and arrays
- Array index: `{{list.2}}` resolves to third element
- Unresolved path: `{{missing.path}}` left as literal
- Multiple substitutions in one string: `Hello {{name}}, score is {{validate.score}}`
- Substitution mid-text: `Score: {{score}}/100`
- Empty variable store: all paths unresolved
- Null value: path exists but value is null — resolves to `null` string
- Out-of-bounds array index: left as literal
- Path into non-object: `{{name.foo}}` where name is a string — left as literal
- Object/array values: serialized as JSON
- No substitution markers: input returned unchanged
- Adjacent substitutions: `{{a}}{{b}}`
- Escaped-like patterns: `{{{name}}}` — resolves inner `{{name}}` and keeps outer braces

---

## 2. Node Executors

Each node type has an executor implementing a common interface defined in `domain/engine/ports.go`:

```go
type NodeExecutor interface {
    Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error)
}
```

**`domain/engine/result.go`** — the NodeResult type:

```go
type NodeResult struct {
    Output         any             // Structured JSON output (nil for Conditional/Transform)
    OutputPort     string          // For Conditional: which output port was selected
    LLMConversation any           // For LLM nodes: full conversation record
    LLMModel       string         // For LLM nodes: model used
    LLMTokensIn    int            // For LLM nodes: input token count
    LLMTokensOut   int            // For LLM nodes: output token count
    ScriptStdout   string         // For Script/ToolCall: raw stdout
    ScriptStderr   string         // For Script/ToolCall: raw stderr
    ScriptExitCode int            // For Script/ToolCall: exit code
}
```

### LLM Call Executor

**`domain/engine/executor_llm.go`:**

Dependencies (injected via ports):
- `FileReader` — reads template files from the workflow folder
- `LLMProvider` — calls the LLM API

Execution:
1. Read template file via `FileReader.ReadFile(workflowName, node.Config.TemplateFile)`
2. Run `variable.Resolve()` on the template content
3. Build the LLM request: model, max tokens, temperature, output schema (if provided)
4. Call `LLMProvider.Call(ctx, request)` (or `StreamCall` if an event bus subscriber is active)
5. If output schema was provided, extract the structured output from the tool use response
6. If no output schema, attempt to parse the response as JSON; if that fails, wrap the text response as `{"response": "..."}`
7. Return `NodeResult` with output, conversation, model, token counts

**Testing:**
- Successful call with output schema: structured JSON returned
- Successful call without output schema: response parsed as JSON
- Non-JSON response without schema: wrapped in `{"response": "..."}`
- Template substitution applied before sending
- Provider error propagated as executor error
- Context cancellation stops the call
- Template file not found returns clear error

### Script Executor

**`domain/engine/executor_script.go`:**

Dependencies:
- `FileReader` — resolves the full path to the script file

Execution:
1. Resolve `{{substitution}}` in each arg
2. Get the script file path via `FileReader.ResolvePath(workflowName, node.Config.ScriptFile)`
3. Build command: `exec.CommandContext(ctx, interpreter, scriptFile, args...)`
4. Set up a context with timeout: `context.WithTimeout(ctx, timeoutSeconds)`
5. Pipe instance variables as JSON to stdin
6. Capture stdout and stderr via separate buffers
7. Run the command
8. If exit code != 0: return error with stderr and exit code
9. Parse stdout as JSON
10. If stdout is not valid JSON: return error
11. Return `NodeResult` with output, stdout, stderr, exit code

**Testing:**
- Successful script: JSON stdout parsed as output
- Non-zero exit code: error returned with stderr content
- Timeout exceeded: process killed, error returned
- Non-JSON stdout: clear error message
- Substitution in args applied correctly
- Variables passed on stdin as valid JSON
- Context cancellation kills the process
- Script file not found: clear error
- Empty stdout: error (expected JSON)

### Tool Call Executor

**`domain/engine/executor_toolcall.go`:**

Same pattern as Script executor but simpler:
1. Resolve `{{substitution}}` in command and args
2. Build command: `exec.CommandContext(ctx, command, args...)`
3. Set up context with timeout
4. No stdin — Tool Call does not pass variables on stdin (unlike Script, which does). Variables are available via substitution in command and args only.
5. Capture stdout and stderr, parse stdout as JSON
6. Same timeout and error handling as Script

**Testing:** Same scenarios as Script executor, adapted for command+args construction.

### Conditional Executor

**`domain/engine/executor_conditional.go`:**

No external dependencies — pure domain logic.

Execution:
1. Iterate conditions in order
2. For each condition:
   a. Resolve `field` as a dot-path via `vars.Get(field)`
   b. If operator is `exists`: return true if value was found, false otherwise
   c. For all other operators: if value not found, condition doesn't match (skip to next)
   d. Apply operator:
      - `eq`: deep equality comparison (works for strings, numbers, booleans)
      - `neq`: negation of eq
      - `gt`, `gte`, `lt`, `lte`: numeric comparison only. If either side is not numeric, condition doesn't match (skip).
      - `contains`: if field value is a string, check substring. If field value is an array, check element membership. Otherwise, condition doesn't match.
3. First matching condition: set `OutputPort` to the condition's `output`, return
4. No match: set `OutputPort` to `defaultOutput`, return
5. Output is nil (Conditional produces no JSON output)

**Testing:**
- `eq` with matching string, number, boolean
- `eq` with non-matching values
- `neq` with matching and non-matching
- `gt`/`gte`/`lt`/`lte` with numeric values
- `gt` with non-numeric values: condition skipped (not error)
- `contains` with substring match in string
- `contains` with element in array
- `contains` with non-string/non-array: condition skipped
- `exists` with present field, missing field, null field
- First-match-wins: two conditions match, first one's output is used
- Default output when nothing matches
- Dot-path field resolution into nested node outputs
- Empty conditions array: always falls through to default

### Transform Executor

**`domain/engine/executor_transform.go`:**

No external dependencies — pure domain logic. Mutates the variable store directly.

Execution:
1. Iterate operations in order
2. For each operation:
   - `set`: resolve `{{substitution}}` in value, call `vars.SetInput(variable, resolvedValue)`
   - `increment`: get current value, verify numeric, add value (default 1), write back. Error if not numeric.
   - `decrement`: get current value, verify numeric, subtract value (default 1), write back. Error if not numeric.
   - `append`: get current value, verify array, append value. Error if not an array. If variable doesn't exist, create new array with the value.
   - `delete`: call `vars.DeleteInput(variable)`. No error if doesn't exist.
3. Output is nil (Transform produces no JSON output)

**Testing:**
- `set` with literal value
- `set` with substitution in value
- `increment` on numeric variable
- `increment` on non-numeric variable: error
- `increment` on missing variable: error
- `decrement` same scenarios
- `append` to existing array
- `append` to missing variable: creates new array
- `append` to non-array: error
- `delete` existing variable
- `delete` missing variable: no error
- Multiple operations in sequence: later operations see effects of earlier ones

---

## 3. Graph Walker

**`domain/engine/engine.go`** — the core orchestrator.

```go
type Engine struct {
    workflows      workflow.WorkflowRepository
    instances      instance.InstanceRepository
    nodeExecutions instance.NodeExecutionRepository
    snapshots      instance.VariableSnapshotRepository
    executors      map[workflow.NodeType]NodeExecutor
    eventBus       EventBus
}
```

### Adjacency Map

When an instance starts, the engine parses the workflow's edges into an adjacency map for fast lookup:

```go
type adjacencyMap map[string][]edgeTarget  // source node ID → list of targets

type edgeTarget struct {
    NodeID     string
    OutputPort string  // empty for non-conditional source nodes
}
```

For conditional nodes, the engine finds the next node by matching the `OutputPort` from the executor result against the edge targets.

### Execution Loop

`Engine.Run(ctx context.Context, instanceID uuid.UUID)`:

1. Load the instance from the repository
2. Load the variable store from the instance's current variables
3. Parse the snapshotted workflow definition
4. Build the adjacency map from edges
5. Determine the starting node:
   - If instance was just triggered: use the entry node
   - If resuming from pause: use the next node after `current_node_id`
   - If retrying: use `failed_node_id`
6. Enter the execution loop:

```
for currentNode != nil {
    // Check for pause/cancel
    instance = instances.Get(instanceID)
    if instance.State == Paused { return }
    if instance.State == Cancelled { return }

    // Snapshot variables
    snapshots.Create(instanceID, currentNode.ID, vars.Snapshot())

    // Update current node pointer
    instances.UpdateCurrentNode(instanceID, currentNode.ID)

    // Publish node_started event
    eventBus.Publish(instanceID, NodeStartedEvent{...})

    // Execute with error handling
    result, err = executeWithRetry(ctx, currentNode, vars, workflowName)

    // Record execution
    nodeExec = buildNodeExecution(currentNode, result, err)
    nodeExecutions.Create(nodeExec)

    if err != nil {
        // Apply error behavior (node override → workflow default → halt)
        errorBehavior := currentNode.ErrorBehavior
        if errorBehavior.Type == "" {
            errorBehavior = workflowDef.DefaultErrorBehavior
        }
        switch errorBehavior.Type {
        case "halt", "":
            instances.SetFailed(instanceID, currentNode.ID)
            eventBus.Publish(instanceID, NodeFailedEvent{...})
            eventBus.Publish(instanceID, StateChangedEvent{...})
            return
        case "skip":
            result = &NodeResult{Output: currentNode.ErrorBehavior.DefaultOutput}
        case "route":
            currentNode = findNode(currentNode.ErrorBehavior.ErrorNodeID)
            continue
        }
    }

    // Publish node_completed event
    eventBus.Publish(instanceID, NodeCompletedEvent{...})

    // Store output in variable store
    if result.Output != nil {
        vars.SetNodeOutput(currentNode.ID, result.Output)
    }

    // Update instance variables in DB
    instances.UpdateVariables(instanceID, vars.Snapshot())

    // Publish variable_updated event
    eventBus.Publish(instanceID, VariableUpdatedEvent{...})

    // Find next node
    currentNode = findNextNode(adjacencyMap, currentNode.ID, result.OutputPort)
}

// No more nodes — workflow complete
instances.SetCompleted(instanceID)
eventBus.Publish(instanceID, StateChangedEvent{...})
```

### Retry Logic

`executeWithRetry(ctx, node, vars, workflowName) (*NodeResult, error)`:

1. Get the executor for the node type
2. First attempt: `executor.Execute(ctx, node, vars, workflowName)`
3. If success: return result
4. If error and error behavior is not `retry`: return error
5. If retry:
   - For attempt 2 to `retries + 1`:
     - Sleep for `retryDelaySeconds` (respect context cancellation during sleep)
     - Record the failed attempt as a `NodeExecution` record
     - Publish retry event
     - Execute again
     - If success: return result
   - All attempts exhausted: return the last error

### Instance Lifecycle Methods

**`Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, error)`:**
1. Load workflow via repository
2. Validate inputs against input schema (type checking, required fields)
3. Initialize variable store: schema defaults overlaid with provided variables
4. Snapshot the workflow definition as JSON
5. Get the default user ID
6. Create instance record (state: `running`, since Phase 1 has no concurrency caps). Store the initialized variable store as both `variables` and `original_variables` (the latter is immutable, used by re-run).
7. Launch `Run()` in a goroutine with a cancellable context stored in an in-memory map
8. Return instance ID

**`Pause(instanceID uuid.UUID) error`:**
1. Get instance, verify state is `running`
2. Set state to `paused` in DB
3. The run loop detects this on next node boundary and stops

**`Resume(instanceID uuid.UUID) error`:**
1. Get instance, verify state is `paused`
2. Set state to `running` in DB
3. Launch `Run()` in a goroutine (it determines resume point from `current_node_id`)

**`Cancel(instanceID uuid.UUID) error`:**
1. Get instance, verify state is `running`, `queued`, or `paused`
2. Set state to `cancelled` in DB
3. Cancel the context for the running goroutine (kills subprocesses, cancels LLM calls)
4. Publish state changed event

**`Retry(instanceID uuid.UUID, variableEdits map[string]any) error`:**
1. Get instance, verify state is `failed`
2. Apply variable edits to the instance's current variables
3. Update variables in DB
4. Set state to `running` in DB
5. Launch `Run()` in a goroutine (it starts at `failed_node_id`)

**`Rerun(instanceID uuid.UUID) (uuid.UUID, error)`:**
1. Get instance, verify state is `completed`, `failed`, or `cancelled`
2. Read `original_variables` from the instance record (these are the immutable trigger-time inputs)
3. Create a new instance with the same workflow snapshot and `original_variables` as both `variables` and `original_variables`
4. Launch `Run()` in a goroutine
5. Return the new instance ID

### Context and Goroutine Management

The engine maintains an in-memory map of `instanceID → context.CancelFunc` for running instances. This allows Cancel to kill in-flight operations. On graceful shutdown, all contexts are cancelled.

---

## 4. Testing Strategy

**Variable substitution:** (covered in Section 1 above)

**Node executors:** (covered in Section 2 above)

**Graph walker tests:**
- Linear workflow (A → B → C): all nodes execute in order, instance completes
- Branching workflow (A → Conditional → B or C → D): correct branch taken based on condition
- Conditional with default output: fallback path followed when no conditions match
- Transform modifies variables: downstream nodes see updated values
- Terminal node (no outgoing edges): instance moves to completed
- Single-node workflow: entry node executes, instance completes

**Error handling tests:**
- Node fails with halt: instance moves to failed, failed_node_id recorded
- Node fails with skip: execution continues with default output
- Node fails with route: execution follows error edge to error-handling node
- Retry succeeds on second attempt: instance continues normally
- Retry exhausts all attempts: instance moves to failed
- Retry delay is respected (not just immediate retries)

**Lifecycle operation tests:**
- Trigger with valid inputs: instance created and running
- Trigger with missing required input: error returned
- Trigger with wrong type: error returned
- Pause running instance: stops at next node boundary
- Pause non-running instance: 409 error
- Resume paused instance: continues from correct node
- Resume non-paused instance: 409 error
- Cancel running instance: subprocess killed, instance cancelled
- Cancel already terminal: 409 error
- Retry failed instance: resumes at failed node with edited variables
- Retry non-failed instance: 409 error
- Rerun terminal instance: new instance created with original inputs
- Rerun running instance: 409 error

**Crash resilience tests:**
- Instance left in running state detected on startup
- Detected instance moved to failed state
- Failed instance can be retried after crash recovery

**Integration tests:**
- End-to-end: trigger a multi-node workflow, verify execution trace, verify variable history, verify final state
- Workflow with LLM call: mock the LLM provider, verify template substitution and output handling
- Workflow with script: real script execution, verify stdin/stdout handling
