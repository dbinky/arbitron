package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

// SampleSource reads bundled sample workflows from a source directory whose
// immediate subdirectories each contain a workflow.json plus supporting files.
type SampleSource struct {
	root string
}

// NewSampleSource roots a SampleSource at dir (the install's samples/ path).
func NewSampleSource(dir string) *SampleSource { return &SampleSource{root: dir} }

// isTestFile reports whether base is a test file by the conventional
// "<stem>_test.<ext>" (or "<stem>_test") naming, covering non-Go test scripts
// such as init-workspace_test.sh that the Makefile's *_test.go exclude misses.
func isTestFile(base string) bool {
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return strings.HasSuffix(stem, "_test")
}

// resolve validates name (no traversal/separators) and returns its abs dir.
func (s *SampleSource) resolve(name string) (string, error) {
	if name == "" || name == "." || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid sample name %q", name)
	}
	return filepath.Join(s.root, name), nil
}

// List returns sample names (subdirs containing workflow.json). A missing root
// is an empty list, not an error.
func (s *SampleSource) List(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("reading sample source: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.root, e.Name(), "workflow.json")); err == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Read parses the manifest and gathers supporting files (relative path ->
// content), excluding workflow.json itself.
func (s *SampleSource) Read(_ context.Context, name string) (*workflow.Workflow, map[string][]byte, error) {
	dir, err := s.resolve(name)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "workflow.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("reading manifest for %q: %w", name, err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(manifest, &wf); err != nil {
		return nil, nil, fmt.Errorf("parsing manifest for %q: %w", name, err)
	}
	// The on-disk directory name is authoritative; a manifest whose name diverges
	// is malformed (skip-as-malformed) so identity can never silently disagree.
	if wf.Name != name {
		return nil, nil, fmt.Errorf("manifest name %q does not match sample directory %q", wf.Name, name)
	}
	files := map[string][]byte{}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip dot-prefixed entries entirely (e.g. .DS_Store, .swp, .git). When a
		// directory is a dotdir, skip its whole subtree.
		if base := d.Name(); base != "." && strings.HasPrefix(base, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Defensive: only publish regular files. d.Type() comes from an Lstat-based
		// DirEntry, so a symlink reports ModeSymlink WITHOUT being followed — this
		// closes the file-disclosure vector where a symlink points at a secret
		// (e.g. the master key) outside the sample dir. Devices/sockets/pipes are
		// likewise excluded.
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "workflow.json" {
			return nil
		}
		// Skip test files (e.g. scripts/init-workspace_test.sh): the Makefile
		// tarball only excludes *_test.go, so non-Go test files would otherwise
		// ship verbatim into the public shared workflow.
		if isTestFile(d.Name()) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = content
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("reading sample files for %q: %w", name, err)
	}
	return &wf, files, nil
}
