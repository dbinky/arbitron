# Phase 1 Task 2: Domain Workflow Types & Filesystem Repository

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Define all domain workflow types (Workflow, Node, Edge, InputField, type-specific configs) and implement the filesystem workflow repository that reads workflow.json from disk.

**Architecture:** Domain types are pure data structs in `domain/workflow/`. The filesystem adapter in `adapter/filesystem/` implements the `WorkflowRepository` port interface. The adapter parses JSON into domain types.

**Tech Stack:** Go 1.23+, standard `encoding/json`

---

### Task 1: Domain Workflow Types

**Files:**
- Create: `domain/workflow/workflow.go`
- Create: `domain/workflow/ports.go`

- [ ] **Step 1: Define domain workflow types**

Create `domain/workflow/workflow.go`:

```go
package workflow

type NodeType string

const (
	NodeTypeLLMCall     NodeType = "llm_call"
	NodeTypeScript      NodeType = "script"
	NodeTypeToolCall    NodeType = "tool_call"
	NodeTypeConditional NodeType = "conditional"
	NodeTypeTransform   NodeType = "transform"
)

type Workflow struct {
	Name                 string       `json:"name"`
	Description          string       `json:"description"`
	Author               string       `json:"author"`
	DefaultErrorBehavior ErrorBehavior `json:"defaultErrorBehavior"`
	Inputs               []InputField `json:"inputs"`
	Nodes                []Node       `json:"nodes"`
	Edges                []Edge       `json:"edges"`
}

type InputField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required"`
}

type Node struct {
	ID           string        `json:"id"`
	Type         NodeType      `json:"type"`
	Config       NodeConfig    `json:"config"`
	Label        string        `json:"label"`
	Description  string        `json:"description"`
	ErrorBehavior ErrorBehavior `json:"errorBehavior,omitempty"`
	Entrypoint   bool          `json:"entrypoint,omitempty"`
}

type NodeConfig struct {
	// LLM Call
	TemplateFile string `json:"templateFile,omitempty"`
	Model        string `json:"model,omitempty"`
	OutputSchema any    `json:"outputSchema,omitempty"`
	MaxTokens    int    `json:"maxTokens,omitempty"`
	Temperature  float64 `json:"temperature,omitempty"`

	// Script
	ScriptFile  string `json:"scriptFile,omitempty"`
	Interpreter string `json:"interpreter,omitempty"`

	// Tool Call
	Command string `json:"command,omitempty"`

	// Script + Tool Call shared
	Args           []string `json:"args,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`

	// Conditional
	Conditions    []Condition `json:"conditions,omitempty"`
	DefaultOutput string      `json:"defaultOutput,omitempty"`

	// Transform
	Operations []Operation `json:"operations,omitempty"`
}

type Condition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
	Output   string `json:"output"`
}

type Operation struct {
	Variable  string `json:"variable"`
	Operation string `json:"operation"`
	Value     any    `json:"value,omitempty"`
}

type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Output string `json:"output,omitempty"`
	Label  string `json:"label,omitempty"`
}

type ErrorBehavior struct {
	Type            string `json:"type,omitempty"`
	Retries         int    `json:"retries,omitempty"`
	RetryDelaySeconds int  `json:"retryDelaySeconds,omitempty"`
	DefaultOutput   any    `json:"defaultOutput,omitempty"`
	ErrorNodeID     string `json:"errorNodeId,omitempty"`
}

type WorkflowSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
}
```

- [ ] **Step 2: Define the WorkflowRepository port**

Create `domain/workflow/ports.go`:

```go
package workflow

type WorkflowRepository interface {
	Load(name string) (*Workflow, error)
	List() ([]WorkflowSummary, error)
}
```

- [ ] **Step 3: Commit domain types**

```bash
git add domain/workflow/
git commit -m "feat: define domain workflow types and repository port"
```

---

### Task 2: Filesystem Workflow Repository

**Files:**
- Create: `adapter/filesystem/workflow_repo.go`
- Create: `adapter/filesystem/workflow_repo_test.go`
- Create: `adapter/filesystem/testdata/valid-workflow/workflow.json`
- Create: `adapter/filesystem/testdata/empty-dir/.gitkeep`

- [ ] **Step 4: Create test fixture — valid workflow.json**

Create `adapter/filesystem/testdata/valid-workflow/workflow.json`:

```json
{
  "name": "valid-workflow",
  "description": "A test workflow",
  "author": "tester",
  "defaultErrorBehavior": {"type": "halt"},
  "inputs": [
    {"name": "message", "type": "string", "description": "Input message", "required": true}
  ],
  "nodes": [
    {
      "id": "start",
      "type": "transform",
      "config": {
        "operations": [{"variable": "result", "operation": "set", "value": "hello"}]
      },
      "label": "Start",
      "description": "Set result",
      "entrypoint": true
    }
  ],
  "edges": []
}
```

Create `adapter/filesystem/testdata/empty-dir/.gitkeep` (empty file).

- [ ] **Step 5: Write failing tests for workflow repo**

Create `adapter/filesystem/workflow_repo_test.go`:

```go
package filesystem_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
)

func testdataDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "testdata")
}

func TestWorkflowRepo_Load_ValidWorkflow(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataDir())

	wf, err := repo.Load("valid-workflow")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if wf.Name != "valid-workflow" {
		t.Errorf("expected name valid-workflow, got %s", wf.Name)
	}
	if wf.Description != "A test workflow" {
		t.Errorf("expected description 'A test workflow', got %s", wf.Description)
	}
	if wf.Author != "tester" {
		t.Errorf("expected author tester, got %s", wf.Author)
	}
	if len(wf.Inputs) != 1 {
		t.Fatalf("expected 1 input, got %d", len(wf.Inputs))
	}
	if wf.Inputs[0].Name != "message" {
		t.Errorf("expected input name message, got %s", wf.Inputs[0].Name)
	}
	if len(wf.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(wf.Nodes))
	}
	if wf.Nodes[0].Type != workflow.NodeTypeTransform {
		t.Errorf("expected transform node, got %s", wf.Nodes[0].Type)
	}
	if wf.Nodes[0].Entrypoint != true {
		t.Error("expected node to be entrypoint")
	}
}

func TestWorkflowRepo_Load_MissingDirectory(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataDir())

	_, err := repo.Load("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestWorkflowRepo_Load_MissingManifest(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataDir())

	_, err := repo.Load("empty-dir")
	if err == nil {
		t.Fatal("expected error for missing workflow.json")
	}
}

func TestWorkflowRepo_Load_MalformedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	wfDir := filepath.Join(tmpDir, "bad-workflow")
	if err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error { return nil }); err != nil {
		// just need the directory
	}
	os.MkdirAll(wfDir, 0755)
	os.WriteFile(filepath.Join(wfDir, "workflow.json"), []byte("{{not json"), 0644)

	repo := filesystem.NewWorkflowRepo(tmpDir)

	_, err := repo.Load("bad-workflow")
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestWorkflowRepo_List(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataDir())

	summaries, err := repo.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find valid-workflow, skip empty-dir (no workflow.json)
	found := false
	for _, s := range summaries {
		if s.Name == "valid-workflow" {
			found = true
			if s.Description != "A test workflow" {
				t.Errorf("expected description 'A test workflow', got %s", s.Description)
			}
		}
		if s.Name == "empty-dir" {
			t.Error("empty-dir should be skipped (no workflow.json)")
		}
	}
	if !found {
		t.Error("expected to find valid-workflow in list")
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

```bash
go test ./adapter/filesystem/ -v
```

Expected: FAIL — `filesystem.NewWorkflowRepo` not defined.

- [ ] **Step 7: Implement filesystem workflow repository**

Create `adapter/filesystem/workflow_repo.go`:

```go
package filesystem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arbitron/arbitron/domain/workflow"
)

type WorkflowRepo struct {
	basePath string
}

func NewWorkflowRepo(basePath string) *WorkflowRepo {
	return &WorkflowRepo{basePath: basePath}
}

func (r *WorkflowRepo) Load(name string) (*workflow.Workflow, error) {
	manifestPath := filepath.Join(r.basePath, name, "workflow.json")

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("workflow %q not found: %w", name, err)
		}
		return nil, fmt.Errorf("reading workflow %q: %w", name, err)
	}

	var wf workflow.Workflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parsing workflow %q: %w", name, err)
	}

	return &wf, nil
}

func (r *WorkflowRepo) List() ([]workflow.WorkflowSummary, error) {
	entries, err := os.ReadDir(r.basePath)
	if err != nil {
		return nil, fmt.Errorf("reading workspace directory: %w", err)
	}

	var summaries []workflow.WorkflowSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		manifestPath := filepath.Join(r.basePath, entry.Name(), "workflow.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue // skip directories without workflow.json
		}

		var wf workflow.Workflow
		if err := json.Unmarshal(data, &wf); err != nil {
			continue // skip directories with invalid JSON
		}

		summaries = append(summaries, workflow.WorkflowSummary{
			Name:        wf.Name,
			Description: wf.Description,
			Author:      wf.Author,
		})
	}

	return summaries, nil
}
```

- [ ] **Step 8: Add missing import to test file**

Add `"os"` to the imports in `workflow_repo_test.go`.

- [ ] **Step 9: Run tests**

```bash
go test ./adapter/filesystem/ -v
```

Expected: all PASS

- [ ] **Step 10: Commit**

```bash
git add domain/workflow/ adapter/filesystem/
git commit -m "feat: domain workflow types and filesystem workflow repository"
```
