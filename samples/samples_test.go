package samples_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestSampleInputsCarryNoSecrets guards the bundled samples, which ship in the
// Docker image and the release tarball: a credential-bearing input (webhook,
// token, secret, key) must have an empty committed default. A live workflow
// export copied back over a sample once carried a signed Teams webhook URL;
// the runtime default is where the secret belongs, never the sample.
func TestSampleInputsCarryNoSecrets(t *testing.T) {
	sensitive := []string{"webhook", "token", "secret", "password", "api_key"}
	for _, name := range []string{"pr-review", "feature-pipeline", "ralph-loop"} {
		t.Run(name, func(t *testing.T) {
			wf := loadSample(t, name)
			for _, in := range wf.Inputs {
				lname := strings.ToLower(in.Name)
				for _, word := range sensitive {
					if !strings.Contains(lname, word) {
						continue
					}
					if in.Default != nil && in.Default != "" {
						t.Errorf("sample %q input %q: committed default must be empty (credential-bearing)", name, in.Name)
					}
				}
				if s, ok := in.Default.(string); ok && strings.Contains(s, "sig=") {
					t.Errorf("sample %q input %q: default carries a signed URL", name, in.Name)
				}
			}
		})
	}
}
