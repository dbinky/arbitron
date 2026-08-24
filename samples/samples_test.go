package samples_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
)

// loadSample loads a sample workflow from this directory by copying it into a
// temp workspace under users/<owner>/<name>/ and loading via the real repo.
func loadSample(t *testing.T, name string) *workflow.Workflow {
	t.Helper()
	owner := uuid.New()
	base := t.TempDir()
	dst := filepath.Join(base, "users", owner.String(), name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	// os.DirFS(name) is relative; go test sets cwd to the package dir (samples/).
	if err := os.CopyFS(dst, os.DirFS(name)); err != nil {
		t.Fatalf("copy sample %q: %v", name, err)
	}
	repo := filesystem.NewWorkflowRepo(base)
	wf, err := repo.Load(context.Background(), workflow.Private(owner), name)
	if err != nil {
		t.Fatalf("load sample %q: %v", name, err)
	}
	return wf
}

func TestSamplesParse(t *testing.T) {
	for _, name := range []string{"pr-review", "feature-pipeline", "ralph-loop"} {
		t.Run(name, func(t *testing.T) {
			wf := loadSample(t, name)
			if wf.Name != name {
				t.Errorf("sample %q: manifest name = %q", name, wf.Name)
			}
			if len(wf.Nodes) == 0 {
				t.Errorf("sample %q: expected nodes, got 0", name)
			}
		})
	}
}
