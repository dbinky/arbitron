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
		})
	}

	return summaries, nil
}
