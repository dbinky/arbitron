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

// Read must publish only the manifest's own well-formed supporting files: a
// regular .md/.sh ships, but a symlink (file-disclosure vector), a dotfile
// (.DS_Store / editor cruft), and a non-Go test file (_test.*) must all be
// skipped so they never reach the public shared workflow.
func TestSampleSource_Read_SkipsSymlinkDotfileAndTestFiles(t *testing.T) {
	root := t.TempDir()
	writeSample(t, root, "pr-review", `{"name":"pr-review","nodes":[]}`, map[string]string{
		"scripts/init.sh":                "ok",
		"manager.md":                     "m",
		".DS_Store":                      "junk",
		"scripts/init-workspace_test.sh": "test",
	})
	dir := filepath.Join(root, "pr-review")

	// A secret outside the sample dir, reachable only via a symlink planted inside.
	secret := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-MASTER-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "leak.txt")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	src := filesystem.NewSampleSource(root)
	_, files, err := src.Read(context.Background(), "pr-review")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if string(files["scripts/init.sh"]) != "ok" || string(files["manager.md"]) != "m" {
		t.Errorf("regular files missing: %v", keys(files))
	}
	for _, bad := range []string{"leak.txt", ".DS_Store", "scripts/init-workspace_test.sh"} {
		if _, ok := files[bad]; ok {
			t.Errorf("unsafe entry %q was published; files=%v", bad, keys(files))
		}
	}
	for rel, content := range files {
		if string(content) == "TOP-SECRET-MASTER-KEY" {
			t.Fatalf("symlinked secret exfiltrated via %q", rel)
		}
	}
}

// A manifest whose name disagrees with its directory name is malformed: Read
// must reject it (skip-as-malformed) so the on-disk dir name and the manifest's
// identity can never silently diverge.
func TestSampleSource_Read_RejectsNameMismatch(t *testing.T) {
	root := t.TempDir()
	writeSample(t, root, "pr-review", `{"name":"something-else","nodes":[]}`, map[string]string{"manager.md": "m"})
	src := filesystem.NewSampleSource(root)
	if _, _, err := src.Read(context.Background(), "pr-review"); err == nil {
		t.Error("expected error when manifest name != directory name")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
