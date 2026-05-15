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
		return nil, fmt.Errorf("%s: %w", name, workflow.ErrNotFound)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %w", name, workflow.ErrNotFound)
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
		return fmt.Errorf("%s: %w", name, workflow.ErrNotFound)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s: %w", name, workflow.ErrNotFound)
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
