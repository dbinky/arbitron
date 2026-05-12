# Phase 1, Task 2: Workflow CRUD & File Management REST Endpoints

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add REST API endpoints for creating, updating, and deleting workflows and managing files within workflow folders.

**Architecture:** New handler methods on the existing `WorkflowHandler` in `adapter/http/workflow_handler.go`. New routes wired into the chi router in `adapter/http/server.go`. All handlers follow the existing patterns: `chi.URLParam` for path params, `WriteJSON`/`WriteError` for responses, `safeWorkflowName` for input validation.

**Tech Stack:** Go, chi, net/http, httptest, encoding/json

---

## Prerequisites

- Task 1 complete (WorkflowRepository write operations available)

## Files to Create/Modify

### Modify: `adapter/http/workflow_handler.go`

Add 7 new handler methods to `WorkflowHandler`:
- `Create` — POST `/workflows`
- `Update` — PUT `/workflows/{name}`
- `Delete` — DELETE `/workflows/{name}`
- `ListFiles` — GET `/workflows/{name}/files`
- `ReadFile` — GET `/workflows/{name}/files/{path}`
- `WriteFile` — PUT `/workflows/{name}/files/{path}`
- `DeleteFile` — DELETE `/workflows/{name}/files/{path}`

### Modify: `adapter/http/server.go`

Wire the new routes into the chi router.

### Test: `adapter/http/workflow_handler_test.go`

Add tests for all 7 new endpoints across 5 scenario categories.

---

### Task 2.1: Create Endpoint

- [ ] **Step 1: Write failing tests**

Add to `adapter/http/workflow_handler_test.go`:

```go
func TestWorkflowHandler_Create_HappyPath(t *testing.T) {
	repo := &mockWorkflowRepo{}
	handler := NewWorkflowHandler(repo, nil, nil, nil)

	body := `{"name":"my-wf","description":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Post("/api/v1/workflows", handler.Create)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, 1, repo.createCalled)
}

func TestWorkflowHandler_Create_DuplicateName(t *testing.T) {
	repo := &mockWorkflowRepo{createErr: fmt.Errorf("workflow already exists: my-wf")}
	handler := NewWorkflowHandler(repo, nil, nil, nil)

	body := `{"name":"my-wf"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Post("/api/v1/workflows", handler.Create)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestWorkflowHandler_Create_MalformedJSON(t *testing.T) {
	handler := NewWorkflowHandler(&mockWorkflowRepo{}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Post("/api/v1/workflows", handler.Create)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestWorkflowHandler_Create_EmptyName(t *testing.T) {
	handler := NewWorkflowHandler(&mockWorkflowRepo{}, nil, nil, nil)

	body := `{"name":"","description":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Post("/api/v1/workflows", handler.Create)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
```

- [ ] **Step 2: Implement Create handler**

In `adapter/http/workflow_handler.go`:

```go
// Create handles POST /workflows and creates a new workflow definition.
func (h *WorkflowHandler) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
	var wf workflow.Workflow
	if err := json.NewDecoder(r.Body).Decode(&wf); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}

	if !safeWorkflowName(wf.Name) {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid workflow name")
		return
	}

	if err := h.repo.Create(r.Context(), wf.Name, &wf); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	WriteJSON(w, http.StatusCreated, &wf)
}
```

- [ ] **Step 3: Wire route in server.go**

In the workflows route group in `NewRouter`:

```go
r.Post("/", wfHandler.Create)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./adapter/http/ -run TestWorkflowHandler_Create -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/http/workflow_handler.go adapter/http/workflow_handler_test.go adapter/http/server.go
git commit -m "feat: add POST /workflows endpoint for workflow creation"
```

---

### Task 2.2: Update Endpoint

- [ ] **Step 1: Write failing tests**

```go
func TestWorkflowHandler_Update_HappyPath(t *testing.T) {
	repo := &mockWorkflowRepo{}
	handler := NewWorkflowHandler(repo, nil, nil, nil)

	body := `{"name":"my-wf","description":"updated"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/workflows/my-wf", strings.NewReader(body))
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Put("/api/v1/workflows/{name}", handler.Update)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestWorkflowHandler_Update_NotFound(t *testing.T) {
	repo := &mockWorkflowRepo{updateErr: fmt.Errorf("workflow not found: ghost")}
	handler := NewWorkflowHandler(repo, nil, nil, nil)

	body := `{"name":"ghost"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/workflows/ghost", strings.NewReader(body))
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Put("/api/v1/workflows/{name}", handler.Update)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
```

- [ ] **Step 2: Implement Update handler**

```go
// Update handles PUT /workflows/{name} and replaces the workflow manifest.
func (h *WorkflowHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
	var wf workflow.Workflow
	if err := json.NewDecoder(r.Body).Decode(&wf); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}

	if err := h.repo.Update(r.Context(), name, &wf); err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, &wf)
}
```

- [ ] **Step 3: Wire route**: `r.Put("/{name}", wfHandler.Update)`

- [ ] **Step 4: Run tests, verify pass**

- [ ] **Step 5: Commit**

```bash
git add adapter/http/workflow_handler.go adapter/http/workflow_handler_test.go adapter/http/server.go
git commit -m "feat: add PUT /workflows/{name} endpoint for workflow updates"
```

---

### Task 2.3: Delete Endpoint

- [ ] **Step 1: Write failing tests**

```go
func TestWorkflowHandler_Delete_HappyPath(t *testing.T) {
	repo := &mockWorkflowRepo{}
	handler := NewWorkflowHandler(repo, nil, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/workflows/my-wf", nil)
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Delete("/api/v1/workflows/{name}", handler.Delete)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestWorkflowHandler_Delete_NotFound(t *testing.T) {
	repo := &mockWorkflowRepo{deleteErr: fmt.Errorf("workflow not found: ghost")}
	handler := NewWorkflowHandler(repo, nil, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/workflows/ghost", nil)
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Delete("/api/v1/workflows/{name}", handler.Delete)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
```

- [ ] **Step 2: Implement Delete handler**

```go
// Delete handles DELETE /workflows/{name} and removes the workflow and its folder.
func (h *WorkflowHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	if err := h.repo.Delete(r.Context(), name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 3: Wire route**: `r.Delete("/{name}", wfHandler.Delete)`

- [ ] **Step 4: Run tests, verify pass**

- [ ] **Step 5: Commit**

```bash
git add adapter/http/workflow_handler.go adapter/http/workflow_handler_test.go adapter/http/server.go
git commit -m "feat: add DELETE /workflows/{name} endpoint"
```

---

### Task 2.4: File Management Endpoints (ListFiles, ReadFile, WriteFile, DeleteFile)

- [ ] **Step 1: Write failing tests for all 4 file endpoints**

```go
func TestWorkflowHandler_ListFiles_HappyPath(t *testing.T) {
	repo := &mockWorkflowRepo{files: []string{"steps/prompt.md", "scripts/run.sh"}}
	handler := NewWorkflowHandler(repo, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/my-wf/files", nil)
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Get("/api/v1/workflows/{name}/files", handler.ListFiles)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var result []string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&result))
	assert.Equal(t, 2, len(result))
}

func TestWorkflowHandler_WriteFile_HappyPath(t *testing.T) {
	repo := &mockWorkflowRepo{}
	handler := NewWorkflowHandler(repo, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/workflows/my-wf/files/steps/prompt.md", strings.NewReader("# Hello"))
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Put("/api/v1/workflows/{name}/files/*", handler.WriteFile)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestWorkflowHandler_ReadFile_HappyPath(t *testing.T) {
	fr := &mockFileReader{content: []byte("# Hello")}
	handler := NewWorkflowHandler(&mockWorkflowRepo{}, nil, fr, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/my-wf/files/steps/prompt.md", nil)
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Get("/api/v1/workflows/{name}/files/*", handler.ReadFile)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "# Hello", w.Body.String())
}

func TestWorkflowHandler_DeleteFile_HappyPath(t *testing.T) {
	repo := &mockWorkflowRepo{}
	handler := NewWorkflowHandler(repo, nil, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/workflows/my-wf/files/steps/prompt.md", nil)
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Delete("/api/v1/workflows/{name}/files/*", handler.DeleteFile)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestWorkflowHandler_WriteFile_PathTraversal(t *testing.T) {
	repo := &mockWorkflowRepo{writeFileErr: fmt.Errorf("path traversal denied")}
	handler := NewWorkflowHandler(repo, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/workflows/my-wf/files/../../../etc/passwd", strings.NewReader("evil"))
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Put("/api/v1/workflows/{name}/files/*", handler.WriteFile)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestWorkflowHandler_ReadFile_NotFound(t *testing.T) {
	fr := &mockFileReader{err: fmt.Errorf("file not found")}
	handler := NewWorkflowHandler(&mockWorkflowRepo{}, nil, fr, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/my-wf/files/missing.md", nil)
	w := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Get("/api/v1/workflows/{name}/files/*", handler.ReadFile)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
```

- [ ] **Step 2: Implement all 4 file handlers**

```go
// ListFiles handles GET /workflows/{name}/files.
func (h *WorkflowHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	files, err := h.repo.ListFiles(r.Context(), name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	if files == nil {
		files = []string{}
	}
	WriteJSON(w, http.StatusOK, files)
}

// ReadFile handles GET /workflows/{name}/files/*.
func (h *WorkflowHandler) ReadFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	filePath := chi.URLParam(r, "*")
	if filePath == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "file path is required")
		return
	}

	content, err := h.fileReader.ReadFile(name, filePath)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such file") {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "file not found: "+filePath)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// WriteFile handles PUT /workflows/{name}/files/*.
func (h *WorkflowHandler) WriteFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	filePath := chi.URLParam(r, "*")
	if filePath == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "file path is required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10 MiB
	content, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "failed to read body: "+err.Error())
		return
	}

	if err := h.repo.WriteFile(r.Context(), name, filePath, content); err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		if strings.Contains(err.Error(), "traversal") {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "written"})
}

// DeleteFile handles DELETE /workflows/{name}/files/*.
func (h *WorkflowHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	filePath := chi.URLParam(r, "*")
	if filePath == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "file path is required")
		return
	}

	if err := h.repo.DeleteFile(r.Context(), name, filePath); err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		if strings.Contains(err.Error(), "traversal") {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
```

Add `"io"` to imports in `workflow_handler.go`.

- [ ] **Step 3: Wire all routes in server.go**

In the workflows route group, add inside the existing `r.Route("/workflows", ...)`:

```go
r.Post("/", wfHandler.Create)
r.Put("/{name}", wfHandler.Update)
r.Delete("/{name}", wfHandler.Delete)
r.Get("/{name}/files", wfHandler.ListFiles)
r.Get("/{name}/files/*", wfHandler.ReadFile)
r.Put("/{name}/files/*", wfHandler.WriteFile)
r.Delete("/{name}/files/*", wfHandler.DeleteFile)
```

- [ ] **Step 4: Run all tests**

Run: `go test ./adapter/http/ -run TestWorkflowHandler -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/http/workflow_handler.go adapter/http/workflow_handler_test.go adapter/http/server.go
git commit -m "feat: add file management REST endpoints for workflows"
```

---

## Acceptance Criteria

- POST `/workflows` creates a workflow and returns 201; 409 on duplicate; 400 on bad JSON or empty name
- PUT `/workflows/{name}` updates a manifest and returns 200; 404 on missing
- DELETE `/workflows/{name}` deletes and returns 204; 404 on missing
- GET `/workflows/{name}/files` lists files (excluding workflow.json); 404 on missing workflow
- GET `/workflows/{name}/files/*` reads file contents; 404 on missing
- PUT `/workflows/{name}/files/*` writes file; 400 on path traversal; 404 on missing workflow
- DELETE `/workflows/{name}/files/*` deletes file; 204 on success; 404 on missing
- All existing tests continue to pass
