package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/adapter/filesystem"
)

// ErrRepositoryLocked is the shared metadata copier's lock sentinel.
var ErrRepositoryLocked = filesystem.ErrGroupRepositoryLocked

// captureMetadata uses an area-relative skeleton of gitdir pointers as input to
// the shared isolation helper. This excludes the backing store even when the
// parent IS the workspace root, and supplies the nested directories the helper
// normally receives from the ordinary content copier. Only the shared helper
// copies/normalizes repository metadata; it preserves worktree HEAD and index.
func (a *WorkingArea) captureMetadata(ctx context.Context, area, backing string, entries []gitEntry) ([]metadataEntry, error) {
	if len(entries) == 0 {
		return []metadataEntry{}, nil
	}
	input, err := os.MkdirTemp(a.backing, "metadata-input-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(input)
	copyRel := "arbitron/metadata-tree"
	copyRoot := filepath.Join(backing, filepath.FromSlash(copyRel))
	if err := os.MkdirAll(copyRoot, 0o700); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Git treats differently-cased metadata names as invalid content paths,
		// but only the literal .git is an author repository on case-sensitive hosts.
		if filepath.Base(entry.Rel) != ".git" {
			continue
		}
		original := filepath.Join(area, filepath.FromSlash(entry.Rel))
		target := original
		if !entry.IsDir {
			info, err := os.Lstat(original)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("unsupported git metadata %q: %s", original, info.Mode())
			}
			raw, err := os.ReadFile(original)
			if err != nil {
				return nil, err
			}
			var ok bool
			target, ok = parseGitdirPointer(string(raw), filepath.Dir(original))
			if !ok {
				return nil, fmt.Errorf("malformed gitdir pointer %q", original)
			}
		}
		source := filepath.Join(input, filepath.FromSlash(entry.Rel))
		for _, dir := range []string{filepath.Dir(source), filepath.Dir(filepath.Join(copyRoot, filepath.FromSlash(entry.Rel)))} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
		}
		if err := os.WriteFile(source, []byte("gitdir: "+target+"\n"), 0o600); err != nil {
			return nil, err
		}
	}
	if err := filesystem.CopyGroupGitMetadata(ctx, input, copyRoot); err != nil {
		return nil, err
	}
	return []metadataEntry{{Path: ".", Kind: "metadata-tree", Copy: copyRel}}, nil
}

// restoreMetadata reads only the immutable captured tree, never the live parent.
func restoreMetadata(ctx context.Context, backing, taskDir string, m manifest) error {
	for _, entry := range m.Metadata {
		if entry.Path != "." || entry.Kind != "metadata-tree" || entry.Copy != "arbitron/metadata-tree" {
			return fmt.Errorf("unsupported group metadata manifest entry %q", entry.Kind)
		}
		if err := filesystem.CopyGroupGitMetadata(ctx, filepath.Join(backing, filepath.FromSlash(entry.Copy)), taskDir); err != nil {
			return err
		}
	}
	return nil
}

// parseGitdirPointer resolves a pointer for the shared copier, not its metadata.
func parseGitdirPointer(content, base string) (string, bool) {
	line := firstLine(content)
	if !strings.HasPrefix(line, "gitdir:") {
		return "", false
	}
	p := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if p == "" || strings.ContainsAny(p, "\x00\r\n") {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p), true
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
