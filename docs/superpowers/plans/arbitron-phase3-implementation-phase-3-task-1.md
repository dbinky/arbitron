# Phase 3.3 Task 1: Backend — Node Positions, Workflow Stats, Summary Enhancement

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the `NodePosition` field to the workflow domain model, implement `SaveNodePositions` in the filesystem adapter, add the `PUT /api/v1/workflows/:name/layout` endpoint, add the `GET /api/v1/workflows/:name/stats` endpoint, and extend `WorkflowSummary` with `NodeCount`.

**Architecture:** Following the existing hexagonal architecture. Domain types in `domain/workflow/`, filesystem adapter implementation, HTTP handlers in `adapter/http/`, and sqlc queries for stats aggregation.

**Tech Stack:** Go, chi, sqlc, filesystem I/O

---

### Task 1.1: NodePosition domain type

**Files:**
- Modify: `domain/workflow/workflow.go`

- [ ] **Step 1: Add NodePosition and Position field to Node**

In `domain/workflow/workflow.go`, add after the `Operation` struct:

```go
type NodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}
```

Add to the `Node` struct:
```go
Position *NodePosition `json:"position,omitempty"`
```

Add to `WorkflowSummary`:
```go
NodeCount int `json:"nodeCount"`
```

- [ ] **Step 2: Run existing tests**

```bash
go test ./domain/workflow/ -v
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add domain/workflow/workflow.go
git commit -m "feat: add NodePosition to workflow domain model"
```

---

### Task 1.2: SaveNodePositions port and filesystem adapter

**Files:**
- Modify: `domain/workflow/ports.go`
- Modify: `adapter/filesystem/workflow_repo.go`
- Create: `adapter/filesystem/workflow_repo_positions_test.go`

- [ ] **Step 1: Write the failing test**

Create `adapter/filesystem/workflow_repo_positions_test.go`:

```go
package filesystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"encoding/json"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveNodePositions_WritesPositionsToWorkflowJSON(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "test-wf")
	require.NoError(t, os.MkdirAll(wfDir, 0o755))

	manifest := map[string]any{
		"name": "test-wf",
		"nodes": []any{
			map[string]any{"id": "node-a", "type": "llm_call", "label": "A"},
			map[string]any{"id": "node-b", "type": "script", "label": "B"},
		},
		"edges": []any{},
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "workflow.json"), data, 0o644))

	repo := filesystem.NewWorkflowRepo(dir)

	positions := map[string]workflow.NodePosition{
		"node-a": {X: 100, Y: 200},
		"node-b": {X: 100, Y: 400},
	}
	err := repo.SaveNodePositions("test-wf", positions)
	require.NoError(t, err)

	// Read back and verify positions were written
	raw, _ := os.ReadFile(filepath.Join(wfDir, "workflow.json"))
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))

	nodes := result["nodes"].([]any)
	for _, n := range nodes {
		node := n.(map[string]any)
		pos := node["position"].(map[string]any)
		if node["id"] == "node-a" {
			assert.Equal(t, float64(100), pos["x"])
			assert.Equal(t, float64(200), pos["y"])
		}
		if node["id"] == "node-b" {
			assert.Equal(t, float64(100), pos["x"])
			assert.Equal(t, float64(400), pos["y"])
		}
	}
}

func TestSaveNodePositions_PreservesExistingFields(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "test-wf")
	require.NoError(t, os.MkdirAll(wfDir, 0o755))

	manifest := map[string]any{
		"name":        "test-wf",
		"description": "should be preserved",
		"customField": "also preserved",
		"nodes": []any{
			map[string]any{"id": "node-a", "type": "llm_call", "label": "A", "config": map[string]any{"model": "opus"}},
		},
		"edges": []any{},
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "workflow.json"), data, 0o644))

	repo := filesystem.NewWorkflowRepo(dir)
	positions := map[string]workflow.NodePosition{"node-a": {X: 50, Y: 75}}
	require.NoError(t, repo.SaveNodePositions("test-wf", positions))

	raw, _ := os.ReadFile(filepath.Join(wfDir, "workflow.json"))
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))

	assert.Equal(t, "should be preserved", result["description"])
	assert.Equal(t, "also preserved", result["customField"])
	node := result["nodes"].([]any)[0].(map[string]any)
	config := node["config"].(map[string]any)
	assert.Equal(t, "opus", config["model"])
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./adapter/filesystem/ -run TestSaveNodePositions -v
```

Expected: compilation error — `SaveNodePositions` does not exist.

- [ ] **Step 3: Add SaveNodePositions to WorkflowRepository port**

In `domain/workflow/ports.go`:

```go
type WorkflowRepository interface {
	Load(ctx context.Context, name string) (*Workflow, error)
	List(ctx context.Context) ([]WorkflowSummary, error)
	SaveNodePositions(name string, positions map[string]NodePosition) error
}
```

- [ ] **Step 4: Implement SaveNodePositions in filesystem adapter**

In `adapter/filesystem/workflow_repo.go`:

```go
func (r *WorkflowRepo) SaveNodePositions(name string, positions map[string]NodePosition) error {
	root := filepath.Clean(r.basePath) + string(os.PathSeparator)
	manifestPath := filepath.Join(r.basePath, name, "workflow.json")
	if !strings.HasPrefix(manifestPath, root) {
		return fmt.Errorf("workflow not found: %s", name)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("workflow not found: %s", name)
		}
		return fmt.Errorf("reading workflow %q: %w", name, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing workflow %q: %w", name, err)
	}

	nodes, ok := raw["nodes"].([]any)
	if !ok {
		return fmt.Errorf("workflow %q has no nodes array", name)
	}

	for i, n := range nodes {
		node, ok := n.(map[string]any)
		if !ok {
			continue
		}
		id, _ := node["id"].(string)
		if pos, exists := positions[id]; exists {
			node["position"] = map[string]any{"x": pos.X, "y": pos.Y}
			nodes[i] = node
		}
	}
	raw["nodes"] = nodes

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling workflow %q: %w", name, err)
	}

	return os.WriteFile(manifestPath, out, 0o644)
}
```

Add the import for `workflow` package:
```go
import "github.com/arbitron/arbitron/domain/workflow"
```

Note: You'll need to add `workflow.NodePosition` as the type alias or import path.

- [ ] **Step 5: Update List to include NodeCount**

In the `List` method, update the summary construction:

```go
summaries = append(summaries, workflow.WorkflowSummary{
	Name:        wf.Name,
	Description: wf.Description,
	Author:      wf.Author,
	NodeCount:   len(wf.Nodes),
})
```

- [ ] **Step 6: Run tests**

```bash
go test ./adapter/filesystem/ -v
```

Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add domain/workflow/ports.go adapter/filesystem/workflow_repo.go adapter/filesystem/workflow_repo_positions_test.go
git commit -m "feat: implement SaveNodePositions in filesystem adapter"
```

---

### Task 1.3: Layout and Stats HTTP endpoints

**Files:**
- Modify: `adapter/http/workflow_handler.go`
- Modify: `adapter/http/server.go`
- Modify: `domain/instance/instance.go` (add WorkflowStats)
- Modify: `domain/instance/ports.go` (add Stats method)
- Modify: `adapter/postgres/instance_repo.go`
- Modify: `adapter/postgres/queries/instances.sql`

- [ ] **Step 1: Add WorkflowStats type to domain/instance**

In `domain/instance/instance.go`:

```go
type WorkflowStats struct {
	TotalRuns       int
	SuccessCount    int
	FailureCount    int
	AvgDurationSecs float64
}
```

- [ ] **Step 2: Add Stats to InstanceRepository port**

In `domain/instance/ports.go`, add to the `InstanceRepository` interface:

```go
Stats(ctx context.Context, workflowName string, from time.Time, to time.Time) (*WorkflowStats, error)
```

- [ ] **Step 3: Add sqlc query for stats**

In `adapter/postgres/queries/instances.sql`, add:

```sql
-- name: WorkflowStats :one
SELECT
  COUNT(*) AS total_runs,
  COUNT(*) FILTER (WHERE state = 'completed') AS success_count,
  COUNT(*) FILTER (WHERE state = 'failed') AS failure_count,
  COALESCE(EXTRACT(EPOCH FROM AVG(completed_at - created_at)) FILTER (WHERE state = 'completed' AND completed_at IS NOT NULL), 0) AS avg_duration_secs
FROM instances
WHERE workflow_name = $1
  AND created_at >= $2
  AND created_at <= $3;
```

Regenerate sqlc:
```bash
cd adapter/postgres && sqlc generate
```

- [ ] **Step 4: Implement Stats in instance_repo.go**

```go
func (r *InstanceRepo) Stats(ctx context.Context, workflowName string, from time.Time, to time.Time) (*instance.WorkflowStats, error) {
	row, err := r.queries.WorkflowStats(ctx, generated.WorkflowStatsParams{
		WorkflowName: workflowName,
		CreatedAt:    toPgTimestamptz(from),
		CreatedAt_2:  toPgTimestamptz(to),
	})
	if err != nil {
		return nil, fmt.Errorf("querying workflow stats: %w", err)
	}

	return &instance.WorkflowStats{
		TotalRuns:       int(row.TotalRuns),
		SuccessCount:    int(row.SuccessCount),
		FailureCount:    int(row.FailureCount),
		AvgDurationSecs: row.AvgDurationSecs,
	}, nil
}
```

Note: The exact field names depend on sqlc output. Adapt accordingly.

- [ ] **Step 5: Add HTTP handlers**

In `adapter/http/workflow_handler.go`, add the `WorkflowHandler` a dependency on `InstanceRepo` (or add a `StatsQuerier` interface). Add to the struct and constructor.

Add layout handler:
```go
func (h *WorkflowHandler) SaveLayout(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Positions map[string]workflow.NodePosition `json:"positions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}

	if err := h.repo.SaveNodePositions(name, req.Positions); err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to save layout")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

Add stats handler:
```go
func (h *WorkflowHandler) Stats(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	now := time.Now()
	from := now.AddDate(0, 0, -30)
	to := now

	if f := r.URL.Query().Get("from"); f != "" {
		if parsed, err := time.Parse(time.RFC3339, f); err == nil {
			from = parsed
		}
	}
	if t := r.URL.Query().Get("to"); t != "" {
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			to = parsed
		}
	}

	stats, err := h.instances.Stats(r.Context(), name, from, to)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get stats")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"totalRuns":       stats.TotalRuns,
		"successCount":    stats.SuccessCount,
		"failureCount":    stats.FailureCount,
		"avgDurationSeconds": stats.AvgDurationSecs,
	})
}
```

- [ ] **Step 6: Register routes in server.go**

In the workflows route group, add:
```go
r.Put("/{name}/layout", wfHandler.SaveLayout)
r.Get("/{name}/stats", wfHandler.Stats)
```

- [ ] **Step 7: Run all tests**

```bash
go test ./... -count=1
```

Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add domain/ adapter/
git commit -m "feat: add layout save and workflow stats endpoints"
```
