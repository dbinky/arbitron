// Package filesystem provides file-based adapters for Arbitron repositories.
package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

// Compile-time assertion: WorkflowRepo satisfies workflow.WorkflowRepository.
var _ workflow.WorkflowRepository = (*WorkflowRepo)(nil)

// WorkflowRepo implements workflow.WorkflowRepository by reading workflow
// definitions from the local filesystem. Each workflow lives in its own
// subdirectory under basePath and contains a workflow.json manifest.
type WorkflowRepo struct {
	basePath string
}

// NewWorkflowRepo creates a WorkflowRepo rooted at the given directory.
func NewWorkflowRepo(basePath string) *WorkflowRepo {
	return &WorkflowRepo{basePath: basePath}
}

// Load reads and parses the workflow.json file from the named subdirectory.
func (r *WorkflowRepo) Load(_ context.Context, name string) (*workflow.Workflow, error) {
	root := filepath.Clean(r.basePath) + string(os.PathSeparator)
	manifestPath := filepath.Join(r.basePath, name, "workflow.json")
	if !strings.HasPrefix(manifestPath, root) {
		return nil, fmt.Errorf("workflow not found: %s", name)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("workflow not found: %s", name)
		}
		return nil, fmt.Errorf("reading workflow %q: %w", name, err)
	}

	var wf workflow.Workflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parsing workflow %q: %w", name, err)
	}

	return &wf, nil
}

// List scans subdirectories of basePath and returns a summary for each valid
// workflow (i.e. each directory that contains a parseable workflow.json).
// Directories without a valid manifest are silently skipped.
func (r *WorkflowRepo) List(ctx context.Context) ([]workflow.WorkflowSummary, error) {
	entries, err := os.ReadDir(r.basePath)
	if err != nil {
		return nil, fmt.Errorf("listing workflows: %w", err)
	}

	var summaries []workflow.WorkflowSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		wf, err := r.Load(ctx, entry.Name())
		if err != nil {
			// Skip directories that don't contain a valid workflow
			continue
		}

		summaries = append(summaries, workflow.WorkflowSummary{
			Name:        wf.Name,
			Description: wf.Description,
			Author:      wf.Author,
			NodeCount:   len(wf.Nodes),
		})
	}

	return summaries, nil
}

// SaveNodePositions updates the position field of each node in the workflow's
// manifest file. It reads the raw JSON (preserving unknown fields), patches
// matching nodes, and writes the file back with indentation.
func (r *WorkflowRepo) SaveNodePositions(_ context.Context, name string, positions map[string]workflow.NodePosition) error {
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

	nodesRaw, ok := raw["nodes"]
	if !ok {
		return nil // no nodes to update
	}
	nodes, ok := nodesRaw.([]any)
	if !ok {
		return fmt.Errorf("unexpected nodes type in workflow %q", name)
	}

	for i, n := range nodes {
		nodeMap, ok := n.(map[string]any)
		if !ok {
			continue
		}
		id, _ := nodeMap["id"].(string)
		if pos, found := positions[id]; found {
			nodeMap["position"] = map[string]any{"x": pos.X, "y": pos.Y}
			nodes[i] = nodeMap
		}
	}

	raw["nodes"] = nodes

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling workflow %q: %w", name, err)
	}
	out = append(out, '\n')

	// Write to a temp file in the same directory then rename atomically so a
	// mid-write crash never leaves the manifest in a truncated state.
	tmp := manifestPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("writing workflow %q: %w", name, err)
	}
	if err := os.Rename(tmp, manifestPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("committing workflow %q: %w", name, err)
	}

	return nil
}

// Create creates a new workflow directory and writes its workflow.json manifest.
// It returns an error if the workflow already exists or the name is invalid.
func (r *WorkflowRepo) Create(_ context.Context, name string, wf *workflow.Workflow) error {
	root := filepath.Clean(r.basePath) + string(os.PathSeparator)
	wfDir := filepath.Join(r.basePath, name)
	if !strings.HasPrefix(wfDir+string(os.PathSeparator), root) {
		return fmt.Errorf("invalid workflow name: %s", name)
	}

	// Fail if the directory already exists.
	if _, err := os.Stat(wfDir); err == nil {
		return fmt.Errorf("workflow already exists: %s", name)
	}

	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		return fmt.Errorf("creating workflow directory %q: %w", name, err)
	}

	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		// Rollback: remove the directory we just created.
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

// Update atomically replaces the workflow.json manifest for an existing workflow.
// It returns an error if the workflow does not exist.
func (r *WorkflowRepo) Update(_ context.Context, name string, wf *workflow.Workflow) error {
	root := filepath.Clean(r.basePath) + string(os.PathSeparator)
	wfDir := filepath.Join(r.basePath, name)
	if !strings.HasPrefix(wfDir+string(os.PathSeparator), root) {
		return fmt.Errorf("invalid workflow name: %s", name)
	}

	manifestPath := filepath.Join(wfDir, "workflow.json")
	if _, err := os.Stat(manifestPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("workflow not found: %s", name)
		}
		return fmt.Errorf("checking workflow %q: %w", name, err)
	}

	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling workflow %q: %w", name, err)
	}
	data = append(data, '\n')

	// Atomic write: temp file + rename.
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

// Delete removes an entire workflow directory and all its contents.
// It returns an error if the workflow does not exist.
func (r *WorkflowRepo) Delete(_ context.Context, name string) error {
	_ = name
	return fmt.Errorf("not implemented")
}

// WriteFile writes content to a file at relativePath inside the named workflow
// directory, creating intermediate directories as needed.
func (r *WorkflowRepo) WriteFile(_ context.Context, name string, relativePath string, content []byte) error {
	_ = name
	_ = relativePath
	_ = content
	return fmt.Errorf("not implemented")
}

// DeleteFile removes a single file from inside the named workflow directory.
func (r *WorkflowRepo) DeleteFile(_ context.Context, name string, relativePath string) error {
	_ = name
	_ = relativePath
	return fmt.Errorf("not implemented")
}

// ListFiles returns all file paths inside the named workflow directory,
// excluding workflow.json. Paths use forward slashes and are relative to the
// workflow directory.
func (r *WorkflowRepo) ListFiles(_ context.Context, name string) ([]string, error) {
	_ = name
	return nil, fmt.Errorf("not implemented")
}
