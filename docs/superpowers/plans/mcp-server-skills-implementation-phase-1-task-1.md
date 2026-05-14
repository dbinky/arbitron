# Phase 1, Task 1: Workflow Repository Write Operations

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the `WorkflowRepository` port with write operations (Create, Update, Delete, WriteFile, DeleteFile, ListFiles) and implement them in the filesystem adapter.

**Architecture:** The existing `WorkflowRepository` interface in `domain/workflow/ports.go` is read-only. We extend it with 6 new methods. The filesystem adapter at `adapter/filesystem/workflow_repo.go` implements them with the same path-traversal protections used by `Load` and `SaveNodePositions`.

**Tech Stack:** Go, os/filepath, encoding/json, testing

---

### Task 1.1: Extend WorkflowRepository Port

**Files:**
- Modify: `domain/workflow/ports.go`

- [ ] **Step 1: Add write methods to the WorkflowRepository interface**

```go
// WorkflowRepository defines the port for loading, listing, and managing workflow definitions.
type WorkflowRepository interface {
	Load(ctx context.Context, name string) (*Workflow, error)
	List(ctx context.Context) ([]WorkflowSummary, error)
	SaveNodePositions(ctx context.Context, name string, positions map[string]NodePosition) error
	Create(ctx context.Context, name string, wf *Workflow) error
	Update(ctx context.Context, name string, wf *Workflow) error
	Delete(ctx context.Context, name string) error
	WriteFile(ctx context.Context, name string, relativePath string, content []byte) error
	DeleteFile(ctx context.Context, name string, relativePath string) error
	ListFiles(ctx context.Context, name string) ([]string, error)
}
```

- [ ] **Step 2: Verify existing code compiles**

Run: `go build ./...`
Expected: BUILD SUCCESS (the filesystem adapter already has the existing methods)

Note: This will fail until the adapter implements the new methods. Add stub implementations first:

```go
func (r *WorkflowRepo) Create(_ context.Context, name string, wf *workflow.Workflow) error {
	return fmt.Errorf("not implemented")
}
func (r *WorkflowRepo) Update(_ context.Context, name string, wf *workflow.Workflow) error {
	return fmt.Errorf("not implemented")
}
func (r *WorkflowRepo) Delete(_ context.Context, name string) error {
	return fmt.Errorf("not implemented")
}
func (r *WorkflowRepo) WriteFile(_ context.Context, name string, relativePath string, content []byte) error {
	return fmt.Errorf("not implemented")
}
func (r *WorkflowRepo) DeleteFile(_ context.Context, name string, relativePath string) error {
	return fmt.Errorf("not implemented")
}
func (r *WorkflowRepo) ListFiles(_ context.Context, name string) ([]string, error) {
	return nil, fmt.Errorf("not implemented")
}
```

- [ ] **Step 3: Commit**

```bash
git add domain/workflow/ports.go adapter/filesystem/workflow_repo.go
git commit -m "feat: extend WorkflowRepository port with write operations (stubs)"
```

---

### Task 1.2: Implement Create

**Files:**
- Modify: `adapter/filesystem/workflow_repo.go`
- Test: `adapter/filesystem/workflow_repo_test.go`

- [ ] **Step 1: Write failing tests for Create**

Add to `adapter/filesystem/workflow_repo_test.go`:

```go
func TestWorkflowRepo_Create_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf", Description: "A test workflow"}

	err := repo.Create(context.Background(), "test-wf", wf)
	require.NoError(t, err)

	// Verify the manifest was written
	loaded, err := repo.Load(context.Background(), "test-wf")
	require.NoError(t, err)
	assert.Equal(t, "test-wf", loaded.Name)
	assert.Equal(t, "A test workflow", loaded.Description)
}

func TestWorkflowRepo_Create_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}

	err := repo.Create(context.Background(), "test-wf", wf)
	require.NoError(t, err)

	err = repo.Create(context.Background(), "test-wf", wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestWorkflowRepo_Create_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "evil"}

	err := repo.Create(context.Background(), "../escape", wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_Create_SpecialCharacters(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "my.workflow"}

	err := repo.Create(context.Background(), "my.workflow", wf)
	require.NoError(t, err)

	loaded, err := repo.Load(context.Background(), "my.workflow")
	require.NoError(t, err)
	assert.Equal(t, "my.workflow", loaded.Name)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_Create -v`
Expected: FAIL with "not implemented"

- [ ] **Step 3: Implement Create**

In `adapter/filesystem/workflow_repo.go`:

```go
func (r *WorkflowRepo) Create(_ context.Context, name string, wf *workflow.Workflow) error {
	root := filepath.Clean(r.basePath) + string(os.PathSeparator)
	wfDir := filepath.Join(r.basePath, name)
	if !strings.HasPrefix(wfDir, root) {
		return fmt.Errorf("workflow not found: %s", name)
	}

	if _, err := os.Stat(wfDir); err == nil {
		return fmt.Errorf("workflow already exists: %s", name)
	}

	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		return fmt.Errorf("creating workflow directory %q: %w", name, err)
	}

	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		_ = os.RemoveAll(wfDir)
		return fmt.Errorf("marshalling workflow %q: %w", name, err)
	}
	data = append(data, '\n')

	manifestPath := filepath.Join(wfDir, "workflow.json")
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		_ = os.RemoveAll(wfDir)
		return fmt.Errorf("writing workflow %q: %w", name, err)
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_Create -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/filesystem/workflow_repo.go adapter/filesystem/workflow_repo_test.go
git commit -m "feat: implement WorkflowRepository.Create in filesystem adapter"
```

---

### Task 1.3: Implement Update

**Files:**
- Modify: `adapter/filesystem/workflow_repo.go`
- Test: `adapter/filesystem/workflow_repo_test.go`

- [ ] **Step 1: Write failing tests for Update**

```go
func TestWorkflowRepo_Update_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf", Description: "original"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))

	wf.Description = "updated"
	err := repo.Update(context.Background(), "test-wf", wf)
	require.NoError(t, err)

	loaded, err := repo.Load(context.Background(), "test-wf")
	require.NoError(t, err)
	assert.Equal(t, "updated", loaded.Description)
}

func TestWorkflowRepo_Update_PreservesOtherFiles(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))
	require.NoError(t, repo.WriteFile(context.Background(), "test-wf", "steps/prompt.md", []byte("hello")))

	wf.Description = "updated"
	require.NoError(t, repo.Update(context.Background(), "test-wf", wf))

	// Other files should still be there
	files, err := repo.ListFiles(context.Background(), "test-wf")
	require.NoError(t, err)
	assert.Contains(t, files, "steps/prompt.md")
}

func TestWorkflowRepo_Update_NotFound(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "nonexistent"}

	err := repo.Update(context.Background(), "nonexistent", wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_Update_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "evil"}

	err := repo.Update(context.Background(), "../escape", wf)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_Update -v`
Expected: FAIL

- [ ] **Step 3: Implement Update**

```go
func (r *WorkflowRepo) Update(_ context.Context, name string, wf *workflow.Workflow) error {
	root := filepath.Clean(r.basePath) + string(os.PathSeparator)
	manifestPath := filepath.Join(r.basePath, name, "workflow.json")
	if !strings.HasPrefix(manifestPath, root) {
		return fmt.Errorf("workflow not found: %s", name)
	}

	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("workflow not found: %s", name)
	}

	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling workflow %q: %w", name, err)
	}
	data = append(data, '\n')

	tmp := manifestPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing workflow %q: %w", name, err)
	}
	if err := os.Rename(tmp, manifestPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("committing workflow %q: %w", name, err)
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_Update -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/filesystem/workflow_repo.go adapter/filesystem/workflow_repo_test.go
git commit -m "feat: implement WorkflowRepository.Update in filesystem adapter"
```

---

### Task 1.4: Implement Delete

**Files:**
- Modify: `adapter/filesystem/workflow_repo.go`
- Test: `adapter/filesystem/workflow_repo_test.go`

- [ ] **Step 1: Write failing tests for Delete**

```go
func TestWorkflowRepo_Delete_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))

	err := repo.Delete(context.Background(), "test-wf")
	require.NoError(t, err)

	_, err = repo.Load(context.Background(), "test-wf")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_Delete_NotFound(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)

	err := repo.Delete(context.Background(), "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_Delete_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)

	err := repo.Delete(context.Background(), "../escape")
	assert.Error(t, err)
}

func TestWorkflowRepo_Delete_RemovesAllContents(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))
	require.NoError(t, repo.WriteFile(context.Background(), "test-wf", "steps/prompt.md", []byte("hello")))

	err := repo.Delete(context.Background(), "test-wf")
	require.NoError(t, err)

	wfDir := filepath.Join(dir, "test-wf")
	_, err = os.Stat(wfDir)
	assert.True(t, os.IsNotExist(err))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_Delete -v`
Expected: FAIL

- [ ] **Step 3: Implement Delete**

```go
func (r *WorkflowRepo) Delete(_ context.Context, name string) error {
	root := filepath.Clean(r.basePath) + string(os.PathSeparator)
	wfDir := filepath.Join(r.basePath, name)
	if !strings.HasPrefix(wfDir, root) {
		return fmt.Errorf("workflow not found: %s", name)
	}

	if _, err := os.Stat(wfDir); os.IsNotExist(err) {
		return fmt.Errorf("workflow not found: %s", name)
	}

	if err := os.RemoveAll(wfDir); err != nil {
		return fmt.Errorf("deleting workflow %q: %w", name, err)
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_Delete -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/filesystem/workflow_repo.go adapter/filesystem/workflow_repo_test.go
git commit -m "feat: implement WorkflowRepository.Delete in filesystem adapter"
```

---

### Task 1.5: Implement WriteFile

**Files:**
- Modify: `adapter/filesystem/workflow_repo.go`
- Test: `adapter/filesystem/workflow_repo_test.go`

- [ ] **Step 1: Write failing tests for WriteFile**

```go
func TestWorkflowRepo_WriteFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))

	err := repo.WriteFile(context.Background(), "test-wf", "steps/prompt.md", []byte("# Hello"))
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, "test-wf", "steps", "prompt.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Hello", string(content))
}

func TestWorkflowRepo_WriteFile_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))
	require.NoError(t, repo.WriteFile(context.Background(), "test-wf", "prompt.md", []byte("v1")))

	err := repo.WriteFile(context.Background(), "test-wf", "prompt.md", []byte("v2"))
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, "test-wf", "prompt.md"))
	require.NoError(t, err)
	assert.Equal(t, "v2", string(content))
}

func TestWorkflowRepo_WriteFile_CreatesIntermediateDirectories(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))

	err := repo.WriteFile(context.Background(), "test-wf", "deep/nested/dir/file.md", []byte("deep"))
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, "test-wf", "deep", "nested", "dir", "file.md"))
	require.NoError(t, err)
	assert.Equal(t, "deep", string(content))
}

func TestWorkflowRepo_WriteFile_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))

	err := repo.WriteFile(context.Background(), "test-wf", "../../../etc/passwd", []byte("evil"))
	assert.Error(t, err)
}

func TestWorkflowRepo_WriteFile_WorkflowNotFound(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)

	err := repo.WriteFile(context.Background(), "nonexistent", "file.md", []byte("data"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_WriteFile_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))

	err := repo.WriteFile(context.Background(), "test-wf", "empty.md", []byte{})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, "test-wf", "empty.md"))
	require.NoError(t, err)
	assert.Empty(t, content)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_WriteFile -v`
Expected: FAIL

- [ ] **Step 3: Implement WriteFile**

```go
func (r *WorkflowRepo) WriteFile(_ context.Context, name string, relativePath string, content []byte) error {
	root := filepath.Clean(r.basePath) + string(os.PathSeparator)
	wfDir := filepath.Join(r.basePath, name)
	if !strings.HasPrefix(wfDir, root) {
		return fmt.Errorf("workflow not found: %s", name)
	}

	if _, err := os.Stat(wfDir); os.IsNotExist(err) {
		return fmt.Errorf("workflow not found: %s", name)
	}

	fullPath := filepath.Join(wfDir, relativePath)
	wfDirClean := filepath.Clean(wfDir) + string(os.PathSeparator)
	if !strings.HasPrefix(fullPath, wfDirClean) {
		return fmt.Errorf("path traversal denied: %s", relativePath)
	}

	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory for %q: %w", relativePath, err)
	}

	if err := os.WriteFile(fullPath, content, 0o644); err != nil {
		return fmt.Errorf("writing file %q: %w", relativePath, err)
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_WriteFile -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/filesystem/workflow_repo.go adapter/filesystem/workflow_repo_test.go
git commit -m "feat: implement WorkflowRepository.WriteFile in filesystem adapter"
```

---

### Task 1.6: Implement DeleteFile

**Files:**
- Modify: `adapter/filesystem/workflow_repo.go`
- Test: `adapter/filesystem/workflow_repo_test.go`

- [ ] **Step 1: Write failing tests for DeleteFile**

```go
func TestWorkflowRepo_DeleteFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))
	require.NoError(t, repo.WriteFile(context.Background(), "test-wf", "prompt.md", []byte("hello")))

	err := repo.DeleteFile(context.Background(), "test-wf", "prompt.md")
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "test-wf", "prompt.md"))
	assert.True(t, os.IsNotExist(err))
}

func TestWorkflowRepo_DeleteFile_NotFound(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))

	err := repo.DeleteFile(context.Background(), "test-wf", "nonexistent.md")
	assert.Error(t, err)
}

func TestWorkflowRepo_DeleteFile_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))

	err := repo.DeleteFile(context.Background(), "test-wf", "../../../etc/passwd")
	assert.Error(t, err)
}

func TestWorkflowRepo_DeleteFile_LeavesManifestIntact(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))
	require.NoError(t, repo.WriteFile(context.Background(), "test-wf", "only-file.md", []byte("data")))

	require.NoError(t, repo.DeleteFile(context.Background(), "test-wf", "only-file.md"))

	// Manifest should still exist
	loaded, err := repo.Load(context.Background(), "test-wf")
	require.NoError(t, err)
	assert.Equal(t, "test-wf", loaded.Name)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_DeleteFile -v`
Expected: FAIL

- [ ] **Step 3: Implement DeleteFile**

```go
func (r *WorkflowRepo) DeleteFile(_ context.Context, name string, relativePath string) error {
	root := filepath.Clean(r.basePath) + string(os.PathSeparator)
	wfDir := filepath.Join(r.basePath, name)
	if !strings.HasPrefix(wfDir, root) {
		return fmt.Errorf("workflow not found: %s", name)
	}

	fullPath := filepath.Join(wfDir, relativePath)
	wfDirClean := filepath.Clean(wfDir) + string(os.PathSeparator)
	if !strings.HasPrefix(fullPath, wfDirClean) {
		return fmt.Errorf("path traversal denied: %s", relativePath)
	}

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return fmt.Errorf("file not found: %s", relativePath)
	}

	if err := os.Remove(fullPath); err != nil {
		return fmt.Errorf("deleting file %q: %w", relativePath, err)
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_DeleteFile -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/filesystem/workflow_repo.go adapter/filesystem/workflow_repo_test.go
git commit -m "feat: implement WorkflowRepository.DeleteFile in filesystem adapter"
```

---

### Task 1.7: Implement ListFiles

**Files:**
- Modify: `adapter/filesystem/workflow_repo.go`
- Test: `adapter/filesystem/workflow_repo_test.go`

- [ ] **Step 1: Write failing tests for ListFiles**

```go
func TestWorkflowRepo_ListFiles_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))
	require.NoError(t, repo.WriteFile(context.Background(), "test-wf", "steps/prompt.md", []byte("p")))
	require.NoError(t, repo.WriteFile(context.Background(), "test-wf", "scripts/validate.sh", []byte("s")))

	files, err := repo.ListFiles(context.Background(), "test-wf")
	require.NoError(t, err)
	assert.Contains(t, files, "steps/prompt.md")
	assert.Contains(t, files, "scripts/validate.sh")
	// workflow.json should NOT be in the list — it's the manifest, not a supporting file
}

func TestWorkflowRepo_ListFiles_NestedDirectories(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))
	require.NoError(t, repo.WriteFile(context.Background(), "test-wf", "a/b/c/deep.md", []byte("d")))

	files, err := repo.ListFiles(context.Background(), "test-wf")
	require.NoError(t, err)
	assert.Contains(t, files, "a/b/c/deep.md")
}

func TestWorkflowRepo_ListFiles_EmptyWorkflow(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)
	wf := &workflow.Workflow{Name: "test-wf"}
	require.NoError(t, repo.Create(context.Background(), "test-wf", wf))

	files, err := repo.ListFiles(context.Background(), "test-wf")
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestWorkflowRepo_ListFiles_NotFound(t *testing.T) {
	dir := t.TempDir()
	repo := NewWorkflowRepo(dir)

	_, err := repo.ListFiles(context.Background(), "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_ListFiles -v`
Expected: FAIL

- [ ] **Step 3: Implement ListFiles**

```go
func (r *WorkflowRepo) ListFiles(_ context.Context, name string) ([]string, error) {
	root := filepath.Clean(r.basePath) + string(os.PathSeparator)
	wfDir := filepath.Join(r.basePath, name)
	if !strings.HasPrefix(wfDir, root) {
		return nil, fmt.Errorf("workflow not found: %s", name)
	}

	if _, err := os.Stat(wfDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("workflow not found: %s", name)
	}

	var files []string
	err := filepath.Walk(wfDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(wfDir, path)
		if err != nil {
			return err
		}
		// Exclude the manifest file
		if rel == "workflow.json" {
			return nil
		}
		// Use forward slashes for consistency
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing files for %q: %w", name, err)
	}

	return files, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./adapter/filesystem/ -run TestWorkflowRepo_ListFiles -v`
Expected: PASS

- [ ] **Step 5: Run all filesystem tests**

Run: `go test ./adapter/filesystem/ -v`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
git add adapter/filesystem/workflow_repo.go adapter/filesystem/workflow_repo_test.go
git commit -m "feat: implement WorkflowRepository.ListFiles in filesystem adapter"
```

---

## Acceptance Criteria

- `WorkflowRepository` interface in `domain/workflow/ports.go` includes Create, Update, Delete, WriteFile, DeleteFile, ListFiles
- Filesystem adapter implements all 6 methods with path-traversal protection
- Create: creates workflow folder + manifest; rejects duplicates
- Update: replaces manifest atomically (temp file + rename); rejects non-existent workflows
- Delete: removes entire workflow folder; rejects non-existent workflows
- WriteFile: writes to arbitrary relative paths, creates intermediate directories, rejects traversal
- DeleteFile: removes files, rejects traversal, rejects non-existent files
- ListFiles: returns relative paths (forward-slash separated), excludes workflow.json, handles nested directories
- All existing tests continue to pass
