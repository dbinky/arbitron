package filesystem_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
)

func writeSample(t *testing.T, root, name, manifest string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSampleSource_ListAndRead(t *testing.T) {
	root := t.TempDir()
	writeSample(t, root, "pr-review", `{"name":"pr-review","nodes":[]}`, map[string]string{"manager.md": "m", "sub/x.md": "y"})

	src := filesystem.NewSampleSource(root)
	names, err := src.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "pr-review" {
		t.Fatalf("List = %v", names)
	}
	wf, files, err := src.Read(context.Background(), "pr-review")
	if err != nil {
		t.Fatal(err)
	}
	if wf.Name != "pr-review" {
		t.Errorf("wf.Name = %q", wf.Name)
	}
	if string(files["manager.md"]) != "m" || string(files["sub/x.md"]) != "y" {
		t.Errorf("files = %v", files)
	}
	if _, ok := files["workflow.json"]; ok {
		t.Errorf("workflow.json must not be in supporting files")
	}
}

func TestSampleSource_MissingDirIsEmpty(t *testing.T) {
	src := filesystem.NewSampleSource(filepath.Join(t.TempDir(), "nope"))
	names, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List on missing dir: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("List = %v, want empty", names)
	}
}

func TestSampleSource_RejectsTraversal(t *testing.T) {
	src := filesystem.NewSampleSource(t.TempDir())
	if _, _, err := src.Read(context.Background(), "../escape"); err == nil {
		t.Error("expected error for traversal name")
	}
}
