# Phase 2 Task 1: Variable Store & Substitution Engine

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the VariableStore type and the `{{dot.path}}` substitution engine as pure domain logic with zero dependencies.

**Architecture:** All code in `domain/variable/`. The VariableStore holds top-level input variables and node output namespaces. The substitution engine is a pure function that takes a string and a store, returning the resolved string.

**Tech Stack:** Go 1.23+, no external dependencies

---

### Task 1: VariableStore Type

**Files:**
- Create: `domain/variable/variable.go`
- Create: `domain/variable/variable_test.go`

- [ ] **Step 1: Write failing tests for VariableStore**

Create `domain/variable/variable_test.go`:

```go
package variable_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/variable"
)

func TestVariableStore_GetInput(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("name", "alice")

	val, ok := store.Get("name")
	if !ok {
		t.Fatal("expected to find 'name'")
	}
	if val != "alice" {
		t.Errorf("expected alice, got %v", val)
	}
}

func TestVariableStore_GetNodeOutput(t *testing.T) {
	store := variable.NewStore()
	store.SetNodeOutput("validate", map[string]any{"score": 85, "passed": true})

	val, ok := store.Get("validate.score")
	if !ok {
		t.Fatal("expected to find 'validate.score'")
	}
	if val != float64(85) {
		t.Errorf("expected 85, got %v", val)
	}
}

func TestVariableStore_GetDeepNested(t *testing.T) {
	store := variable.NewStore()
	store.SetNodeOutput("analyze", map[string]any{
		"result": map[string]any{
			"items": []any{
				map[string]any{"name": "first"},
				map[string]any{"name": "second"},
			},
		},
	})

	val, ok := store.Get("analyze.result.items.0.name")
	if !ok {
		t.Fatal("expected to find nested path")
	}
	if val != "first" {
		t.Errorf("expected 'first', got %v", val)
	}
}

func TestVariableStore_GetMissing(t *testing.T) {
	store := variable.NewStore()

	_, ok := store.Get("missing.path")
	if ok {
		t.Error("expected not found for missing path")
	}
}

func TestVariableStore_GetArrayOutOfBounds(t *testing.T) {
	store := variable.NewStore()
	store.SetNodeOutput("node", map[string]any{
		"items": []any{"a", "b"},
	})

	_, ok := store.Get("node.items.5")
	if ok {
		t.Error("expected not found for out-of-bounds index")
	}
}

func TestVariableStore_GetPathIntoNonObject(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("name", "alice")

	_, ok := store.Get("name.foo")
	if ok {
		t.Error("expected not found for path into non-object")
	}
}

func TestVariableStore_DeleteInput(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("x", "value")
	store.DeleteInput("x")

	_, ok := store.Get("x")
	if ok {
		t.Error("expected not found after delete")
	}
}

func TestVariableStore_DeleteMissing(t *testing.T) {
	store := variable.NewStore()
	store.DeleteInput("nonexistent") // should not panic
}

func TestVariableStore_Snapshot(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("msg", "hello")
	store.SetNodeOutput("step1", map[string]any{"result": 42})

	snap := store.Snapshot()
	if snap["msg"] != "hello" {
		t.Errorf("expected msg=hello in snapshot, got %v", snap["msg"])
	}
	step1, ok := snap["step1"].(map[string]any)
	if !ok {
		t.Fatal("expected step1 in snapshot")
	}
	if step1["result"] != 42 {
		t.Errorf("expected step1.result=42, got %v", step1["result"])
	}
}

func TestVariableStore_FromSnapshot(t *testing.T) {
	snap := map[string]any{
		"msg":   "hello",
		"step1": map[string]any{"result": 42},
	}

	store := variable.FromSnapshot(snap)

	val, ok := store.Get("msg")
	if !ok || val != "hello" {
		t.Errorf("expected msg=hello, got %v", val)
	}

	val, ok = store.Get("step1.result")
	if !ok || val != 42 {
		t.Errorf("expected step1.result=42, got %v", val)
	}
}

func TestVariableStore_GetNull(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("x", nil)

	val, ok := store.Get("x")
	if !ok {
		t.Fatal("expected to find 'x' even if nil")
	}
	if val != nil {
		t.Errorf("expected nil, got %v", val)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./domain/variable/ -v
```

Expected: FAIL — `variable.NewStore` not defined.

- [ ] **Step 3: Implement VariableStore**

Create `domain/variable/variable.go`:

```go
package variable

import (
	"strconv"
	"strings"
)

type VariableStore struct {
	inputs      map[string]any
	nodeOutputs map[string]any
}

func NewStore() *VariableStore {
	return &VariableStore{
		inputs:      make(map[string]any),
		nodeOutputs: make(map[string]any),
	}
}

func (s *VariableStore) Get(dotPath string) (any, bool) {
	segments := strings.Split(dotPath, ".")
	if len(segments) == 0 {
		return nil, false
	}

	first := segments[0]
	rest := segments[1:]

	if val, ok := s.inputs[first]; ok {
		if len(rest) == 0 {
			return val, true
		}
		return walkPath(val, rest)
	}

	if val, ok := s.nodeOutputs[first]; ok {
		if len(rest) == 0 {
			return val, true
		}
		return walkPath(val, rest)
	}

	return nil, false
}

func walkPath(current any, segments []string) (any, bool) {
	for _, seg := range segments {
		switch v := current.(type) {
		case map[string]any:
			val, ok := v[seg]
			if !ok {
				return nil, false
			}
			current = val
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, false
			}
			current = v[idx]
		default:
			return nil, false
		}
	}
	return current, true
}

func (s *VariableStore) SetInput(key string, value any) {
	s.inputs[key] = value
}

func (s *VariableStore) SetNodeOutput(nodeID string, output any) {
	s.nodeOutputs[nodeID] = output
}

func (s *VariableStore) DeleteInput(key string) {
	delete(s.inputs, key)
}

func (s *VariableStore) Snapshot() map[string]any {
	result := make(map[string]any)
	for k, v := range s.inputs {
		result[k] = v
	}
	for k, v := range s.nodeOutputs {
		result[k] = v
	}
	return result
}

func FromSnapshot(data map[string]any) *VariableStore {
	store := NewStore()
	for k, v := range data {
		if m, ok := v.(map[string]any); ok {
			store.nodeOutputs[k] = m
		} else {
			store.inputs[k] = v
		}
	}
	return store
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./domain/variable/ -v -run TestVariableStore
```

Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add domain/variable/
git commit -m "feat: VariableStore with dot-path access"
```

---

### Task 2: Substitution Engine

**Files:**
- Create: `domain/variable/substitution.go`
- Create: `domain/variable/substitution_test.go`

- [ ] **Step 6: Write failing tests for substitution**

Create `domain/variable/substitution_test.go`:

```go
package variable_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/variable"
)

func TestResolve_SimplePath(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("name", "alice")

	result := variable.Resolve("Hello {{name}}", store)
	if result != "Hello alice" {
		t.Errorf("expected 'Hello alice', got %q", result)
	}
}

func TestResolve_NodeOutput(t *testing.T) {
	store := variable.NewStore()
	store.SetNodeOutput("validate", map[string]any{"score": float64(85)})

	result := variable.Resolve("Score: {{validate.score}}", store)
	if result != "Score: 85" {
		t.Errorf("expected 'Score: 85', got %q", result)
	}
}

func TestResolve_DeepPath(t *testing.T) {
	store := variable.NewStore()
	store.SetNodeOutput("analyze", map[string]any{
		"result": map[string]any{
			"items": []any{
				map[string]any{"name": "first"},
			},
		},
	})

	result := variable.Resolve("{{analyze.result.items.0.name}}", store)
	if result != "first" {
		t.Errorf("expected 'first', got %q", result)
	}
}

func TestResolve_Unresolved(t *testing.T) {
	store := variable.NewStore()

	result := variable.Resolve("{{missing.path}}", store)
	if result != "{{missing.path}}" {
		t.Errorf("expected literal '{{missing.path}}', got %q", result)
	}
}

func TestResolve_MultipleSubstitutions(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("name", "alice")
	store.SetNodeOutput("validate", map[string]any{"score": float64(85)})

	result := variable.Resolve("Hello {{name}}, score is {{validate.score}}", store)
	if result != "Hello alice, score is 85" {
		t.Errorf("got %q", result)
	}
}

func TestResolve_MidText(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("score", float64(85))

	result := variable.Resolve("Score: {{score}}/100", store)
	if result != "Score: 85/100" {
		t.Errorf("got %q", result)
	}
}

func TestResolve_NoSubstitution(t *testing.T) {
	store := variable.NewStore()

	result := variable.Resolve("no markers here", store)
	if result != "no markers here" {
		t.Errorf("got %q", result)
	}
}

func TestResolve_EmptyStore(t *testing.T) {
	store := variable.NewStore()

	result := variable.Resolve("{{a}} and {{b}}", store)
	if result != "{{a}} and {{b}}" {
		t.Errorf("got %q", result)
	}
}

func TestResolve_BooleanValue(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("flag", true)

	result := variable.Resolve("{{flag}}", store)
	if result != "true" {
		t.Errorf("expected 'true', got %q", result)
	}
}

func TestResolve_ObjectValue(t *testing.T) {
	store := variable.NewStore()
	store.SetNodeOutput("step", map[string]any{"a": float64(1), "b": float64(2)})

	result := variable.Resolve("{{step}}", store)
	// Should be JSON serialized
	if result != `{"a":1,"b":2}` {
		t.Errorf("got %q", result)
	}
}

func TestResolve_ArrayValue(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("list", []any{"x", "y"})

	result := variable.Resolve("{{list}}", store)
	if result != `["x","y"]` {
		t.Errorf("got %q", result)
	}
}

func TestResolve_NullValue(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("x", nil)

	result := variable.Resolve("{{x}}", store)
	if result != "null" {
		t.Errorf("expected 'null', got %q", result)
	}
}

func TestResolve_AdjacentSubstitutions(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("a", "hello")
	store.SetInput("b", "world")

	result := variable.Resolve("{{a}}{{b}}", store)
	if result != "helloworld" {
		t.Errorf("got %q", result)
	}
}

func TestResolve_TripleBraces(t *testing.T) {
	store := variable.NewStore()
	store.SetInput("name", "alice")

	result := variable.Resolve("{{{name}}}", store)
	if result != "{alice}" {
		t.Errorf("expected '{alice}', got %q", result)
	}
}
```

- [ ] **Step 7: Run tests to verify they fail**

```bash
go test ./domain/variable/ -v -run TestResolve
```

Expected: FAIL — `variable.Resolve` not defined.

- [ ] **Step 8: Implement substitution engine**

Create `domain/variable/substitution.go`:

```go
package variable

import (
	"encoding/json"
	"fmt"
	"strings"
)

func Resolve(input string, store *VariableStore) string {
	var result strings.Builder
	i := 0
	for i < len(input) {
		if i+1 < len(input) && input[i] == '{' && input[i+1] == '{' {
			end := strings.Index(input[i+2:], "}}")
			if end == -1 {
				result.WriteByte(input[i])
				i++
				continue
			}
			path := input[i+2 : i+2+end]
			val, ok := store.Get(path)
			if ok {
				result.WriteString(formatValue(val))
			} else {
				result.WriteString("{{")
				result.WriteString(path)
				result.WriteString("}}")
			}
			i = i + 2 + end + 2
		} else {
			result.WriteByte(input[i])
			i++
		}
	}
	return result.String()
}

func formatValue(val any) string {
	if val == nil {
		return "null"
	}
	switch v := val.(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case int:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		data, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(data)
	}
}
```

- [ ] **Step 9: Run tests**

```bash
go test ./domain/variable/ -v -run TestResolve
```

Expected: all PASS

- [ ] **Step 10: Commit**

```bash
git add domain/variable/substitution*
git commit -m "feat: {{dot.path}} substitution engine"
```
