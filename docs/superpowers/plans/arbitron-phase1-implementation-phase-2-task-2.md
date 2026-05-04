# Phase 2 Task 2: Node Executors

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement all five node executors: LLM Call, Script, Tool Call, Conditional, and Transform. Each implements the `NodeExecutor` interface.

**Architecture:** Executors live in `domain/engine/`. The LLM Call executor depends on ports (`LLMProvider`, `FileReader`). Script and Tool Call executors use `os/exec` for subprocess management. Conditional and Transform executors are pure domain logic. The Anthropic LLM provider adapter lives in `adapter/anthropic/`.

**Tech Stack:** Go 1.23+, Anthropic API (via HTTP client)

---

### Task 1: NodeExecutor Interface & Result Types

**Files:**
- Create: `domain/engine/executor.go`
- Create: `domain/engine/result.go`

- [ ] **Step 1: Define the NodeExecutor interface and NodeResult**

Create `domain/engine/executor.go`:

```go
package engine

import (
	"context"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

type NodeExecutor interface {
	Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error)
}
```

Create `domain/engine/result.go`:

```go
package engine

type NodeResult struct {
	Output          any
	OutputPort      string
	LLMConversation any
	LLMModel        string
	LLMTokensIn     int
	LLMTokensOut    int
	ScriptStdout    string
	ScriptStderr    string
	ScriptExitCode  int
}
```

- [ ] **Step 2: Commit interface definitions**

```bash
git add domain/engine/executor.go domain/engine/result.go
git commit -m "feat: NodeExecutor interface and NodeResult type"
```

---

### Task 2: Conditional Executor

**Files:**
- Create: `domain/engine/executor_conditional.go`
- Create: `domain/engine/executor_conditional_test.go`

- [ ] **Step 3: Write failing tests for Conditional executor**

Create `domain/engine/executor_conditional_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

func conditionalNode(conditions []workflow.Condition, defaultOutput string) workflow.Node {
	return workflow.Node{
		ID:   "cond",
		Type: workflow.NodeTypeConditional,
		Config: workflow.NodeConfig{
			Conditions:    conditions,
			DefaultOutput: defaultOutput,
		},
	}
}

func TestConditionalExecutor_EqMatch(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetInput("status", "active")

	node := conditionalNode([]workflow.Condition{
		{Field: "status", Operator: "eq", Value: "active", Output: "yes"},
	}, "no")

	result, err := exec.Execute(context.Background(), node, store, "wf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.OutputPort != "yes" {
		t.Errorf("expected output port 'yes', got %q", result.OutputPort)
	}
	if result.Output != nil {
		t.Error("conditional should produce nil output")
	}
}

func TestConditionalExecutor_EqNoMatch(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetInput("status", "inactive")

	node := conditionalNode([]workflow.Condition{
		{Field: "status", Operator: "eq", Value: "active", Output: "yes"},
	}, "no")

	result, err := exec.Execute(context.Background(), node, store, "wf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.OutputPort != "no" {
		t.Errorf("expected default output 'no', got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_NeqMatch(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetInput("x", "a")

	node := conditionalNode([]workflow.Condition{
		{Field: "x", Operator: "neq", Value: "b", Output: "different"},
	}, "same")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "different" {
		t.Errorf("expected 'different', got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_GtNumeric(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetNodeOutput("validate", map[string]any{"score": float64(85)})

	node := conditionalNode([]workflow.Condition{
		{Field: "validate.score", Operator: "gt", Value: float64(80), Output: "pass"},
	}, "fail")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "pass" {
		t.Errorf("expected 'pass', got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_GtNonNumericSkips(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetInput("name", "alice")

	node := conditionalNode([]workflow.Condition{
		{Field: "name", Operator: "gt", Value: float64(10), Output: "yes"},
	}, "no")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "no" {
		t.Errorf("expected default 'no' for non-numeric gt, got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_ContainsString(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetInput("msg", "hello world")

	node := conditionalNode([]workflow.Condition{
		{Field: "msg", Operator: "contains", Value: "world", Output: "found"},
	}, "notfound")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "found" {
		t.Errorf("expected 'found', got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_ContainsArray(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetInput("tags", []any{"go", "rust", "python"})

	node := conditionalNode([]workflow.Condition{
		{Field: "tags", Operator: "contains", Value: "rust", Output: "has_rust"},
	}, "no_rust")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "has_rust" {
		t.Errorf("expected 'has_rust', got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_ExistsTrue(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetInput("x", "value")

	node := conditionalNode([]workflow.Condition{
		{Field: "x", Operator: "exists", Output: "yes"},
	}, "no")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "yes" {
		t.Errorf("expected 'yes', got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_ExistsFalseNull(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetInput("x", nil)

	node := conditionalNode([]workflow.Condition{
		{Field: "x", Operator: "exists", Output: "yes"},
	}, "no")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "no" {
		t.Errorf("expected 'no' for null value, got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_ExistsFalseMissing(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()

	node := conditionalNode([]workflow.Condition{
		{Field: "missing", Operator: "exists", Output: "yes"},
	}, "no")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "no" {
		t.Errorf("expected 'no' for missing field, got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_FirstMatchWins(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetInput("x", "a")

	node := conditionalNode([]workflow.Condition{
		{Field: "x", Operator: "eq", Value: "a", Output: "first"},
		{Field: "x", Operator: "eq", Value: "a", Output: "second"},
	}, "default")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "first" {
		t.Errorf("expected 'first' (first match wins), got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_EmptyConditions(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()

	node := conditionalNode(nil, "fallback")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "fallback" {
		t.Errorf("expected 'fallback', got %q", result.OutputPort)
	}
}

func TestConditionalExecutor_LteMatch(t *testing.T) {
	exec := engine.NewConditionalExecutor()
	store := variable.NewStore()
	store.SetInput("count", float64(5))

	node := conditionalNode([]workflow.Condition{
		{Field: "count", Operator: "lte", Value: float64(5), Output: "yes"},
	}, "no")

	result, _ := exec.Execute(context.Background(), node, store, "wf")
	if result.OutputPort != "yes" {
		t.Errorf("expected 'yes', got %q", result.OutputPort)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

```bash
go test ./domain/engine/ -v -run TestConditionalExecutor
```

Expected: FAIL

- [ ] **Step 5: Implement Conditional executor**

Create `domain/engine/executor_conditional.go`:

```go
package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

type ConditionalExecutor struct{}

func NewConditionalExecutor() *ConditionalExecutor {
	return &ConditionalExecutor{}
}

func (e *ConditionalExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	for _, cond := range node.Config.Conditions {
		if cond.Operator == "exists" {
			val, ok := vars.Get(cond.Field)
			if ok && val != nil {
				return &NodeResult{OutputPort: cond.Output}, nil
			}
			continue
		}

		fieldVal, ok := vars.Get(cond.Field)
		if !ok {
			continue
		}

		if evaluateCondition(fieldVal, cond.Operator, cond.Value) {
			return &NodeResult{OutputPort: cond.Output}, nil
		}
	}

	return &NodeResult{OutputPort: node.Config.DefaultOutput}, nil
}

func evaluateCondition(fieldVal any, operator string, condValue any) bool {
	switch operator {
	case "eq":
		return fmt.Sprintf("%v", fieldVal) == fmt.Sprintf("%v", condValue)
	case "neq":
		return fmt.Sprintf("%v", fieldVal) != fmt.Sprintf("%v", condValue)
	case "gt":
		a, b, ok := toFloat64Pair(fieldVal, condValue)
		return ok && a > b
	case "gte":
		a, b, ok := toFloat64Pair(fieldVal, condValue)
		return ok && a >= b
	case "lt":
		a, b, ok := toFloat64Pair(fieldVal, condValue)
		return ok && a < b
	case "lte":
		a, b, ok := toFloat64Pair(fieldVal, condValue)
		return ok && a <= b
	case "contains":
		return evalContains(fieldVal, condValue)
	}
	return false
}

func toFloat64Pair(a, b any) (float64, float64, bool) {
	af, aOk := toFloat64(a)
	bf, bOk := toFloat64(b)
	return af, bf, aOk && bOk
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func evalContains(fieldVal, condValue any) bool {
	switch fv := fieldVal.(type) {
	case string:
		cv, ok := condValue.(string)
		return ok && strings.Contains(fv, cv)
	case []any:
		for _, item := range fv {
			if fmt.Sprintf("%v", item) == fmt.Sprintf("%v", condValue) {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./domain/engine/ -v -run TestConditionalExecutor
```

Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add domain/engine/executor_conditional*
git commit -m "feat: Conditional node executor"
```

---

### Task 3: Transform Executor

**Files:**
- Create: `domain/engine/executor_transform.go`
- Create: `domain/engine/executor_transform_test.go`

- [ ] **Step 8: Write failing tests for Transform executor**

Create `domain/engine/executor_transform_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

func transformNode(ops []workflow.Operation) workflow.Node {
	return workflow.Node{
		ID:   "transform",
		Type: workflow.NodeTypeTransform,
		Config: workflow.NodeConfig{
			Operations: ops,
		},
	}
}

func TestTransformExecutor_Set(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "x", Operation: "set", Value: "hello"},
	})

	result, err := exec.Execute(context.Background(), node, store, "wf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != nil {
		t.Error("transform should produce nil output")
	}

	val, ok := store.Get("x")
	if !ok || val != "hello" {
		t.Errorf("expected x=hello, got %v", val)
	}
}

func TestTransformExecutor_SetWithSubstitution(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()
	store.SetInput("name", "alice")

	node := transformNode([]workflow.Operation{
		{Variable: "greeting", Operation: "set", Value: "hello {{name}}"},
	})

	exec.Execute(context.Background(), node, store, "wf")

	val, _ := store.Get("greeting")
	if val != "hello alice" {
		t.Errorf("expected 'hello alice', got %v", val)
	}
}

func TestTransformExecutor_Increment(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()
	store.SetInput("count", float64(5))

	node := transformNode([]workflow.Operation{
		{Variable: "count", Operation: "increment", Value: float64(1)},
	})

	exec.Execute(context.Background(), node, store, "wf")

	val, _ := store.Get("count")
	if val != float64(6) {
		t.Errorf("expected 6, got %v", val)
	}
}

func TestTransformExecutor_IncrementNonNumeric(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()
	store.SetInput("x", "not a number")

	node := transformNode([]workflow.Operation{
		{Variable: "x", Operation: "increment", Value: float64(1)},
	})

	_, err := exec.Execute(context.Background(), node, store, "wf")
	if err == nil {
		t.Fatal("expected error for non-numeric increment")
	}
}

func TestTransformExecutor_IncrementMissing(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "missing", Operation: "increment", Value: float64(1)},
	})

	_, err := exec.Execute(context.Background(), node, store, "wf")
	if err == nil {
		t.Fatal("expected error for increment on missing variable")
	}
}

func TestTransformExecutor_Decrement(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()
	store.SetInput("count", float64(10))

	node := transformNode([]workflow.Operation{
		{Variable: "count", Operation: "decrement", Value: float64(3)},
	})

	exec.Execute(context.Background(), node, store, "wf")

	val, _ := store.Get("count")
	if val != float64(7) {
		t.Errorf("expected 7, got %v", val)
	}
}

func TestTransformExecutor_Append(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()
	store.SetInput("items", []any{"a", "b"})

	node := transformNode([]workflow.Operation{
		{Variable: "items", Operation: "append", Value: "c"},
	})

	exec.Execute(context.Background(), node, store, "wf")

	val, _ := store.Get("items")
	arr, ok := val.([]any)
	if !ok || len(arr) != 3 || arr[2] != "c" {
		t.Errorf("expected [a,b,c], got %v", val)
	}
}

func TestTransformExecutor_AppendMissingCreatesArray(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "items", Operation: "append", Value: "first"},
	})

	exec.Execute(context.Background(), node, store, "wf")

	val, _ := store.Get("items")
	arr, ok := val.([]any)
	if !ok || len(arr) != 1 || arr[0] != "first" {
		t.Errorf("expected [first], got %v", val)
	}
}

func TestTransformExecutor_AppendNonArray(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()
	store.SetInput("x", "not an array")

	node := transformNode([]workflow.Operation{
		{Variable: "x", Operation: "append", Value: "y"},
	})

	_, err := exec.Execute(context.Background(), node, store, "wf")
	if err == nil {
		t.Fatal("expected error for append on non-array")
	}
}

func TestTransformExecutor_Delete(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()
	store.SetInput("x", "value")

	node := transformNode([]workflow.Operation{
		{Variable: "x", Operation: "delete"},
	})

	exec.Execute(context.Background(), node, store, "wf")

	_, ok := store.Get("x")
	if ok {
		t.Error("expected x to be deleted")
	}
}

func TestTransformExecutor_DeleteMissing(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "nonexistent", Operation: "delete"},
	})

	_, err := exec.Execute(context.Background(), node, store, "wf")
	if err != nil {
		t.Fatalf("delete of missing variable should not error: %v", err)
	}
}

func TestTransformExecutor_MultipleOperations(t *testing.T) {
	exec := engine.NewTransformExecutor()
	store := variable.NewStore()
	store.SetInput("count", float64(0))

	node := transformNode([]workflow.Operation{
		{Variable: "count", Operation: "increment", Value: float64(1)},
		{Variable: "count", Operation: "increment", Value: float64(1)},
		{Variable: "msg", Operation: "set", Value: "done"},
	})

	exec.Execute(context.Background(), node, store, "wf")

	count, _ := store.Get("count")
	if count != float64(2) {
		t.Errorf("expected count=2, got %v", count)
	}
	msg, _ := store.Get("msg")
	if msg != "done" {
		t.Errorf("expected msg=done, got %v", msg)
	}
}
```

- [ ] **Step 9: Run tests to verify they fail**

```bash
go test ./domain/engine/ -v -run TestTransformExecutor
```

Expected: FAIL

- [ ] **Step 10: Implement Transform executor**

Create `domain/engine/executor_transform.go`:

```go
package engine

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

type TransformExecutor struct{}

func NewTransformExecutor() *TransformExecutor {
	return &TransformExecutor{}
}

func (e *TransformExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	for _, op := range node.Config.Operations {
		switch op.Operation {
		case "set":
			resolved := resolveOperationValue(op.Value, vars)
			vars.SetInput(op.Variable, resolved)

		case "increment":
			current, ok := vars.Get(op.Variable)
			if !ok {
				return nil, fmt.Errorf("increment: variable %q not found", op.Variable)
			}
			curNum, cOk := toFloat64(current)
			addNum, aOk := toFloat64(op.Value)
			if !cOk || !aOk {
				return nil, fmt.Errorf("increment: variable %q is not numeric", op.Variable)
			}
			vars.SetInput(op.Variable, curNum+addNum)

		case "decrement":
			current, ok := vars.Get(op.Variable)
			if !ok {
				return nil, fmt.Errorf("decrement: variable %q not found", op.Variable)
			}
			curNum, cOk := toFloat64(current)
			subNum, sOk := toFloat64(op.Value)
			if !cOk || !sOk {
				return nil, fmt.Errorf("decrement: variable %q is not numeric", op.Variable)
			}
			vars.SetInput(op.Variable, curNum-subNum)

		case "append":
			current, ok := vars.Get(op.Variable)
			if !ok {
				vars.SetInput(op.Variable, []any{op.Value})
				continue
			}
			arr, isArr := current.([]any)
			if !isArr {
				return nil, fmt.Errorf("append: variable %q is not an array", op.Variable)
			}
			vars.SetInput(op.Variable, append(arr, op.Value))

		case "delete":
			vars.DeleteInput(op.Variable)

		default:
			return nil, fmt.Errorf("unknown operation: %s", op.Operation)
		}
	}

	return &NodeResult{}, nil
}

func resolveOperationValue(value any, vars *variable.VariableStore) any {
	if s, ok := value.(string); ok {
		return variable.Resolve(s, vars)
	}
	return value
}
```

- [ ] **Step 11: Run tests**

```bash
go test ./domain/engine/ -v -run TestTransformExecutor
```

Expected: all PASS

- [ ] **Step 12: Commit**

```bash
git add domain/engine/executor_transform*
git commit -m "feat: Transform node executor"
```

---

### Task 4: Script & Tool Call Executors

**Files:**
- Create: `domain/engine/executor_script.go`
- Create: `domain/engine/executor_script_test.go`
- Create: `domain/engine/executor_toolcall.go`
- Create: `domain/engine/executor_toolcall_test.go`

- [ ] **Step 13: Write failing tests for Script executor**

Create `domain/engine/executor_script_test.go` with tests for:
- Successful script execution with JSON stdout → parsed as output
- Non-zero exit code → error with stderr content
- Timeout exceeded → process killed, error returned
- Non-JSON stdout → error
- Substitution in args applied
- Variables passed on stdin as JSON
- Context cancellation kills process
- Script file not found → error
- Empty stdout → error

The tests should create temporary script files in `t.TempDir()` and use a mock `FileReader` that returns known paths.

- [ ] **Step 14: Implement Script executor**

Create `domain/engine/executor_script.go` that:
1. Resolves `{{substitution}}` in args
2. Gets script path via `FileReader.ResolvePath()`
3. Builds `exec.CommandContext` with interpreter, script path, and args
4. Sets context timeout from `TimeoutSeconds`
5. Pipes variables JSON to stdin
6. Captures stdout and stderr
7. Parses stdout as JSON
8. Returns `NodeResult` with output, stdout, stderr, exit code

- [ ] **Step 15: Run Script tests**

```bash
go test ./domain/engine/ -v -run TestScriptExecutor
```

Expected: all PASS

- [ ] **Step 16: Write and implement Tool Call executor**

Create `domain/engine/executor_toolcall.go` and `executor_toolcall_test.go`. Same pattern as Script but:
- Uses `command` + `args` instead of `interpreter` + `scriptFile`
- Does NOT pass variables on stdin
- Otherwise identical subprocess handling

- [ ] **Step 17: Run Tool Call tests**

```bash
go test ./domain/engine/ -v -run TestToolCallExecutor
```

Expected: all PASS

- [ ] **Step 18: Commit**

```bash
git add domain/engine/executor_script* domain/engine/executor_toolcall*
git commit -m "feat: Script and Tool Call node executors"
```

---

### Task 5: LLM Call Executor & Anthropic Adapter

**Files:**
- Create: `domain/engine/executor_llm.go`
- Create: `domain/engine/executor_llm_test.go`
- Create: `adapter/anthropic/provider.go`
- Create: `adapter/anthropic/provider_test.go`

- [ ] **Step 19: Write failing tests for LLM Call executor (with mock provider)**

Create `domain/engine/executor_llm_test.go` with a mock `LLMProvider` and mock `FileReader`. Tests for:
- Successful call with output schema → structured JSON returned
- Successful call without schema → response parsed as JSON
- Non-JSON response without schema → wrapped in `{"response": "..."}`
- Template substitution applied before sending
- Provider error propagated
- Context cancellation
- Template file not found → error

- [ ] **Step 20: Implement LLM Call executor**

Create `domain/engine/executor_llm.go`:

```go
package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

type LLMCallExecutor struct {
	fileReader  FileReader
	llmProvider LLMProvider
}

func NewLLMCallExecutor(fileReader FileReader, llmProvider LLMProvider) *LLMCallExecutor {
	return &LLMCallExecutor{fileReader: fileReader, llmProvider: llmProvider}
}

func (e *LLMCallExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	templateData, err := e.fileReader.ReadFile(workflowName, node.Config.TemplateFile)
	if err != nil {
		return nil, fmt.Errorf("reading template: %w", err)
	}

	prompt := variable.Resolve(string(templateData), vars)

	request := LLMRequest{
		Model:        node.Config.Model,
		Prompt:       prompt,
		MaxTokens:    node.Config.MaxTokens,
		Temperature:  node.Config.Temperature,
		OutputSchema: node.Config.OutputSchema,
	}

	response, err := e.llmProvider.Call(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	output := response.Output
	if output == nil {
		// Try to parse the conversation response as JSON
		if convMap, ok := response.Conversation.(map[string]any); ok {
			if text, ok := convMap["response"].(string); ok {
				var parsed any
				if jsonErr := json.Unmarshal([]byte(text), &parsed); jsonErr == nil {
					output = parsed
				} else {
					output = map[string]any{"response": text}
				}
			}
		}
	}

	return &NodeResult{
		Output:          output,
		LLMConversation: response.Conversation,
		LLMModel:        response.Model,
		LLMTokensIn:     response.TokensIn,
		LLMTokensOut:    response.TokensOut,
	}, nil
}
```

- [ ] **Step 21: Run LLM Call tests**

```bash
go test ./domain/engine/ -v -run TestLLMCallExecutor
```

Expected: all PASS

- [ ] **Step 22: Implement Anthropic provider adapter**

Create `adapter/anthropic/provider.go` that implements the `LLMProvider` interface:
- Makes HTTP POST requests to `https://api.anthropic.com/v1/messages`
- Sets `x-api-key` and `anthropic-version` headers
- Maps the model name to Anthropic format
- If `OutputSchema` is provided, wraps it as a tool definition and forces tool use
- Parses the response, extracts structured output from tool use results
- For `StreamCall`, makes a streaming request and returns a channel of chunks
- Write tests with a mock HTTP server (`httptest.NewServer`) to verify request format and response parsing

- [ ] **Step 23: Run Anthropic adapter tests**

```bash
go test ./adapter/anthropic/ -v
```

Expected: all PASS

- [ ] **Step 24: Commit**

```bash
git add domain/engine/executor_llm* adapter/anthropic/
git commit -m "feat: LLM Call executor and Anthropic provider adapter"
```
