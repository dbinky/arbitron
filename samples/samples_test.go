package samples_test

import (
	"context"
	"io/fs"
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
	for _, name := range allSampleNames(t) {
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
	for _, name := range allSampleNames(t) {
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

func TestSamplesLibFolderInvariants(t *testing.T) {
	root := "_lib"
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatalf("samples/_lib must exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("samples/_lib is not a directory")
	}
	if _, err := os.Lstat(filepath.Join(root, "workflow.json")); err == nil {
		t.Errorf("samples/_lib must not contain a workflow.json")
	}
	files := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			t.Errorf("%s: symlinks are not allowed in the library", path)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		files++
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if err := workflow.ValidLibRel(filepath.ToSlash(rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Errorf("samples/_lib has no files")
	}
}
