# Phase 1 Task 3: Workflow Validation & FileReader Port

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement pure domain workflow validation (graph connectivity, entry node, edge integrity, conditional completeness, input schema) and the FileReader port used for file reference checking.

**Architecture:** Validation is pure domain logic in `domain/workflow/validation.go`. It accepts a `FileChecker` interface for filesystem checks to avoid importing adapter code. The FileReader port + filesystem adapter live in `domain/engine/` and `adapter/filesystem/`.

**Tech Stack:** Go 1.23+, no external dependencies

---

### Task 1: FileReader Port & Filesystem Adapter

**Files:**
- Create: `domain/engine/ports.go`
- Create: `adapter/filesystem/file_reader.go`
- Create: `adapter/filesystem/file_reader_test.go`

- [ ] **Step 1: Define the FileReader port**

Create `domain/engine/ports.go`:

```go
package engine

import (
	"context"
)

type FileReader interface {
	ReadFile(workflowName string, relativePath string) ([]byte, error)
	ResolvePath(workflowName string, relativePath string) (string, error)
	FileExists(workflowName string, relativePath string) bool
}

type LLMProvider interface {
	Call(ctx context.Context, request LLMRequest) (*LLMResponse, error)
	StreamCall(ctx context.Context, request LLMRequest) (<-chan LLMStreamChunk, error)
}

type LLMRequest struct {
	Model        string
	Prompt       string
	MaxTokens    int
	Temperature  float64
	OutputSchema any
}

type LLMResponse struct {
	Output       any
	Conversation any
	Model        string
	TokensIn     int
	TokensOut    int
}

type LLMStreamChunk struct {
	Token string
	Done  bool
	Error error
}
```

- [ ] **Step 2: Write failing tests for FileReader adapter**

Create `adapter/filesystem/file_reader_test.go`:

```go
package filesystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
)

func TestFileReader_ReadFile_Success(t *testing.T) {
	tmpDir := t.TempDir()
	wfDir := filepath.Join(tmpDir, "my-workflow", "steps")
	os.MkdirAll(wfDir, 0755)
	os.WriteFile(filepath.Join(wfDir, "prompt.md"), []byte("Hello {{name}}"), 0644)

	reader := filesystem.NewFileReader(tmpDir)

	data, err := reader.ReadFile("my-workflow", "steps/prompt.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "Hello {{name}}" {
		t.Errorf("expected 'Hello {{name}}', got %s", string(data))
	}
}

func TestFileReader_ReadFile_NotFound(t *testing.T) {
	reader := filesystem.NewFileReader(t.TempDir())

	_, err := reader.ReadFile("my-workflow", "missing.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFileReader_ResolvePath(t *testing.T) {
	tmpDir := t.TempDir()
	wfDir := filepath.Join(tmpDir, "my-workflow")
	os.MkdirAll(wfDir, 0755)
	os.WriteFile(filepath.Join(wfDir, "script.sh"), []byte("#!/bin/bash"), 0644)

	reader := filesystem.NewFileReader(tmpDir)

	path, err := reader.ResolvePath("my-workflow", "script.sh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(tmpDir, "my-workflow", "script.sh")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestFileReader_ResolvePath_NotFound(t *testing.T) {
	reader := filesystem.NewFileReader(t.TempDir())

	_, err := reader.ResolvePath("my-workflow", "missing.sh")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFileReader_FileExists_True(t *testing.T) {
	tmpDir := t.TempDir()
	wfDir := filepath.Join(tmpDir, "my-workflow")
	os.MkdirAll(wfDir, 0755)
	os.WriteFile(filepath.Join(wfDir, "prompt.md"), []byte("content"), 0644)

	reader := filesystem.NewFileReader(tmpDir)

	if !reader.FileExists("my-workflow", "prompt.md") {
		t.Error("expected file to exist")
	}
}

func TestFileReader_FileExists_False(t *testing.T) {
	reader := filesystem.NewFileReader(t.TempDir())

	if reader.FileExists("my-workflow", "missing.md") {
		t.Error("expected file to not exist")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./adapter/filesystem/ -v -run TestFileReader
```

Expected: FAIL — `filesystem.NewFileReader` not defined.

- [ ] **Step 4: Implement FileReader adapter**

Create `adapter/filesystem/file_reader.go`:

```go
package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
)

type FileReaderAdapter struct {
	basePath string
}

func NewFileReader(basePath string) *FileReaderAdapter {
	return &FileReaderAdapter{basePath: basePath}
}

func (r *FileReaderAdapter) ReadFile(workflowName string, relativePath string) ([]byte, error) {
	fullPath := filepath.Join(r.basePath, workflowName, relativePath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s/%s: %w", workflowName, relativePath, err)
	}
	return data, nil
}

func (r *FileReaderAdapter) ResolvePath(workflowName string, relativePath string) (string, error) {
	fullPath := filepath.Join(r.basePath, workflowName, relativePath)
	if _, err := os.Stat(fullPath); err != nil {
		return "", fmt.Errorf("resolving %s/%s: %w", workflowName, relativePath, err)
	}
	return fullPath, nil
}

func (r *FileReaderAdapter) FileExists(workflowName string, relativePath string) bool {
	fullPath := filepath.Join(r.basePath, workflowName, relativePath)
	_, err := os.Stat(fullPath)
	return err == nil
}
```

- [ ] **Step 5: Run FileReader tests**

```bash
go test ./adapter/filesystem/ -v -run TestFileReader
```

Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add domain/engine/ adapter/filesystem/file_reader*
git commit -m "feat: FileReader port and filesystem adapter"
```

---

### Task 2: Workflow Validation

**Files:**
- Create: `domain/workflow/validation.go`
- Create: `domain/workflow/validation_test.go`

- [ ] **Step 7: Write failing tests for validation**

Create `domain/workflow/validation_test.go`:

```go
package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
)

type mockFileChecker struct {
	existing map[string]bool
}

func (m *mockFileChecker) FileExists(workflowName string, relativePath string) bool {
	return m.existing[relativePath]
}

func validWorkflow() *workflow.Workflow {
	return &workflow.Workflow{
		Name: "test",
		Inputs: []workflow.InputField{
			{Name: "msg", Type: "string", Required: true},
		},
		Nodes: []workflow.Node{
			{ID: "a", Type: workflow.NodeTypeTransform, Entrypoint: true, Config: workflow.NodeConfig{
				Operations: []workflow.Operation{{Variable: "x", Operation: "set", Value: "1"}},
			}},
			{ID: "b", Type: workflow.NodeTypeTransform, Config: workflow.NodeConfig{
				Operations: []workflow.Operation{{Variable: "y", Operation: "set", Value: "2"}},
			}},
		},
		Edges: []workflow.Edge{
			{Source: "a", Target: "b"},
		},
	}
}

func TestValidate_ValidWorkflow(t *testing.T) {
	wf := validWorkflow()
	errs := workflow.Validate(wf, nil)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_NoEntryNode(t *testing.T) {
	wf := validWorkflow()
	wf.Nodes[0].Entrypoint = false
	wf.Nodes[1].Entrypoint = false

	errs := workflow.Validate(wf, nil)
	if len(errs) == 0 {
		t.Fatal("expected error for no entry node")
	}
	found := false
	for _, e := range errs {
		if e.Code == "NO_ENTRY_NODE" {
			found = true
		}
	}
	if !found {
		t.Error("expected NO_ENTRY_NODE error")
	}
}

func TestValidate_MultipleEntryNodes(t *testing.T) {
	wf := validWorkflow()
	wf.Nodes[1].Entrypoint = true

	errs := workflow.Validate(wf, nil)
	found := false
	for _, e := range errs {
		if e.Code == "MULTIPLE_ENTRY_NODES" {
			found = true
		}
	}
	if !found {
		t.Error("expected MULTIPLE_ENTRY_NODES error")
	}
}

func TestValidate_DuplicateNodeIDs(t *testing.T) {
	wf := validWorkflow()
	wf.Nodes[1].ID = "a" // duplicate

	errs := workflow.Validate(wf, nil)
	found := false
	for _, e := range errs {
		if e.Code == "DUPLICATE_NODE_ID" {
			found = true
		}
	}
	if !found {
		t.Error("expected DUPLICATE_NODE_ID error")
	}
}

func TestValidate_EdgeReferencesNonexistentNode(t *testing.T) {
	wf := validWorkflow()
	wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "nonexistent"})

	errs := workflow.Validate(wf, nil)
	found := false
	for _, e := range errs {
		if e.Code == "INVALID_EDGE_TARGET" {
			found = true
		}
	}
	if !found {
		t.Error("expected INVALID_EDGE_TARGET error")
	}
}

func TestValidate_UnreachableNode(t *testing.T) {
	wf := validWorkflow()
	wf.Nodes = append(wf.Nodes, workflow.Node{
		ID: "c", Type: workflow.NodeTypeTransform, Config: workflow.NodeConfig{
			Operations: []workflow.Operation{{Variable: "z", Operation: "set", Value: "3"}},
		},
	})
	// c has no incoming edge from a or b

	errs := workflow.Validate(wf, nil)
	found := false
	for _, e := range errs {
		if e.Code == "UNREACHABLE_NODE" {
			found = true
		}
	}
	if !found {
		t.Error("expected UNREACHABLE_NODE error")
	}
}

func TestValidate_ConditionalMissingEdge(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "test",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeConditional, Entrypoint: true, Config: workflow.NodeConfig{
				Conditions:    []workflow.Condition{{Field: "x", Operator: "eq", Value: "1", Output: "yes"}},
				DefaultOutput: "no",
			}},
			{ID: "yes-node", Type: workflow.NodeTypeTransform, Config: workflow.NodeConfig{
				Operations: []workflow.Operation{{Variable: "r", Operation: "set", Value: "y"}},
			}},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "yes-node", Output: "yes"},
			// Missing edge for "no" output
		},
	}

	errs := workflow.Validate(wf, nil)
	found := false
	for _, e := range errs {
		if e.Code == "MISSING_CONDITIONAL_EDGE" {
			found = true
		}
	}
	if !found {
		t.Error("expected MISSING_CONDITIONAL_EDGE error")
	}
}

func TestValidate_InvalidInputType(t *testing.T) {
	wf := validWorkflow()
	wf.Inputs = []workflow.InputField{
		{Name: "x", Type: "invalid_type", Required: true},
	}

	errs := workflow.Validate(wf, nil)
	found := false
	for _, e := range errs {
		if e.Code == "INVALID_INPUT_TYPE" {
			found = true
		}
	}
	if !found {
		t.Error("expected INVALID_INPUT_TYPE error")
	}
}

func TestValidate_MissingTemplateFile(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "test",
		Nodes: []workflow.Node{
			{ID: "llm", Type: workflow.NodeTypeLLMCall, Entrypoint: true, Config: workflow.NodeConfig{
				TemplateFile: "steps/prompt.md",
				Model:        "claude-sonnet-4-6",
			}},
		},
		Edges: []workflow.Edge{},
	}

	checker := &mockFileChecker{existing: map[string]bool{}}
	errs := workflow.Validate(wf, checker)
	found := false
	for _, e := range errs {
		if e.Code == "MISSING_FILE" {
			found = true
		}
	}
	if !found {
		t.Error("expected MISSING_FILE error")
	}
}

func TestValidate_TemplateFileExists(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "test",
		Nodes: []workflow.Node{
			{ID: "llm", Type: workflow.NodeTypeLLMCall, Entrypoint: true, Config: workflow.NodeConfig{
				TemplateFile: "steps/prompt.md",
				Model:        "claude-sonnet-4-6",
			}},
		},
		Edges: []workflow.Edge{},
	}

	checker := &mockFileChecker{existing: map[string]bool{"steps/prompt.md": true}}
	errs := workflow.Validate(wf, checker)
	for _, e := range errs {
		if e.Code == "MISSING_FILE" {
			t.Error("should not report missing file when it exists")
		}
	}
}
```

- [ ] **Step 8: Run tests to verify they fail**

```bash
go test ./domain/workflow/ -v
```

Expected: FAIL — `workflow.Validate` not defined.

- [ ] **Step 9: Implement workflow validation**

Create `domain/workflow/validation.go`:

```go
package workflow

type FileChecker interface {
	FileExists(workflowName string, relativePath string) bool
}

type ValidationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	NodeID  string `json:"nodeId,omitempty"`
	EdgeIdx int    `json:"edgeIdx,omitempty"`
}

var validInputTypes = map[string]bool{
	"string": true, "number": true, "boolean": true, "object": true, "array": true,
}

func Validate(wf *Workflow, checker FileChecker) []ValidationError {
	var errs []ValidationError

	nodeMap := make(map[string]*Node)
	var entryNodes []string

	// Check unique IDs and entry nodes
	for i := range wf.Nodes {
		n := &wf.Nodes[i]
		if _, exists := nodeMap[n.ID]; exists {
			errs = append(errs, ValidationError{
				Code: "DUPLICATE_NODE_ID", Message: "duplicate node ID: " + n.ID, NodeID: n.ID,
			})
		}
		nodeMap[n.ID] = n
		if n.Entrypoint {
			entryNodes = append(entryNodes, n.ID)
		}
	}

	if len(entryNodes) == 0 {
		errs = append(errs, ValidationError{Code: "NO_ENTRY_NODE", Message: "no entry node defined"})
	} else if len(entryNodes) > 1 {
		errs = append(errs, ValidationError{
			Code: "MULTIPLE_ENTRY_NODES", Message: "multiple entry nodes defined",
		})
	}

	// Check edge integrity
	for i, edge := range wf.Edges {
		if _, exists := nodeMap[edge.Source]; !exists {
			errs = append(errs, ValidationError{
				Code: "INVALID_EDGE_SOURCE", Message: "edge source references nonexistent node: " + edge.Source, EdgeIdx: i,
			})
		}
		if _, exists := nodeMap[edge.Target]; !exists {
			errs = append(errs, ValidationError{
				Code: "INVALID_EDGE_TARGET", Message: "edge target references nonexistent node: " + edge.Target, EdgeIdx: i,
			})
		}
	}

	// Check conditional edge completeness
	for _, n := range wf.Nodes {
		if n.Type != NodeTypeConditional {
			continue
		}
		edgeOutputs := make(map[string]bool)
		for _, e := range wf.Edges {
			if e.Source == n.ID {
				edgeOutputs[e.Output] = true
			}
		}
		for _, cond := range n.Config.Conditions {
			if !edgeOutputs[cond.Output] {
				errs = append(errs, ValidationError{
					Code: "MISSING_CONDITIONAL_EDGE", Message: "conditional node " + n.ID + " has no edge for output: " + cond.Output, NodeID: n.ID,
				})
			}
		}
		if n.Config.DefaultOutput != "" && !edgeOutputs[n.Config.DefaultOutput] {
			errs = append(errs, ValidationError{
				Code: "MISSING_CONDITIONAL_EDGE", Message: "conditional node " + n.ID + " has no edge for default output: " + n.Config.DefaultOutput, NodeID: n.ID,
			})
		}
	}

	// Check graph connectivity (BFS from entry node)
	if len(entryNodes) == 1 {
		reachable := make(map[string]bool)
		queue := []string{entryNodes[0]}
		adj := make(map[string][]string)
		for _, e := range wf.Edges {
			adj[e.Source] = append(adj[e.Source], e.Target)
		}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if reachable[cur] {
				continue
			}
			reachable[cur] = true
			for _, next := range adj[cur] {
				if !reachable[next] {
					queue = append(queue, next)
				}
			}
		}
		for _, n := range wf.Nodes {
			if !reachable[n.ID] {
				errs = append(errs, ValidationError{
					Code: "UNREACHABLE_NODE", Message: "node " + n.ID + " is not reachable from entry node", NodeID: n.ID,
				})
			}
		}
	}

	// Check input schema
	for _, input := range wf.Inputs {
		if !validInputTypes[input.Type] {
			errs = append(errs, ValidationError{
				Code: "INVALID_INPUT_TYPE", Message: "input " + input.Name + " has invalid type: " + input.Type,
			})
		}
	}

	// Check file references
	if checker != nil {
		for _, n := range wf.Nodes {
			switch n.Type {
			case NodeTypeLLMCall:
				if n.Config.TemplateFile != "" && !checker.FileExists(wf.Name, n.Config.TemplateFile) {
					errs = append(errs, ValidationError{
						Code: "MISSING_FILE", Message: "template file not found: " + n.Config.TemplateFile, NodeID: n.ID,
					})
				}
			case NodeTypeScript:
				if n.Config.ScriptFile != "" && !checker.FileExists(wf.Name, n.Config.ScriptFile) {
					errs = append(errs, ValidationError{
						Code: "MISSING_FILE", Message: "script file not found: " + n.Config.ScriptFile, NodeID: n.ID,
					})
				}
			}
		}
	}

	return errs
}
```

- [ ] **Step 10: Run tests**

```bash
go test ./domain/workflow/ -v
```

Expected: all PASS

- [ ] **Step 11: Commit**

```bash
git add domain/workflow/validation* domain/engine/ports.go adapter/filesystem/file_reader*
git commit -m "feat: workflow validation and FileReader port"
```
