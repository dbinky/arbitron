// adapter/checkpoint/walk.go
package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

type item struct {
	rel  string // slash-separated, workspace-relative
	abs  string // where the bytes are read from (may be a materialized git dir)
	info fs.FileInfo
	link string
}

// collect walks root without following symlinks. When materialize is true,
// every repository whose metadata is not self-contained (a linked-worktree or
// separate-gitdir `.git` file pointing outside root, alternates, a partial
// clone) is replaced by a self-contained `.git` directory built in tmp by
// materializePortableGit; the user's repository is only read. When false
// (runner-side ScanEntries, staged trees) an external `.git` link is
// ErrUnsupportedGit. Sockets/fifos inside git directories are runtime
// artefacts (fsmonitor) and skipped; anywhere else ErrUnsupportedFileType.
func (s *Store) collect(ctx context.Context, root string, materialize bool, tmp string) ([]item, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: workspace %s: %w", root, err)
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("checkpoint: workspace %s is not a directory", root)
	}
	var items []item
	err = filepath.WalkDir(real, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == real {
			return nil
		}
		relOS, err := filepath.Rel(real, p)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relOS)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		mode := info.Mode()
		portable := mode.IsDir() || mode.IsRegular() || mode&fs.ModeSymlink != 0
		if !portable && insideGitDir(rel) {
			return nil
		}
		if d.Name() == ".git" && (mode.IsRegular() || mode.IsDir()) {
			replace := false
			if mode.IsRegular() {
				target, err := readGitFile(p)
				if err != nil {
					return fmt.Errorf("%w: %s: %v", runner.ErrUnsupportedGit, rel, err)
				}
				if !within(real, target) {
					if !materialize {
						return fmt.Errorf("%w: %s links to a git directory outside the workspace", runner.ErrUnsupportedGit, rel)
					}
					replace = true
				}
			} else if materialize {
				if replace, err = s.needsMaterialize(ctx, p); err != nil {
					return fmt.Errorf("%s: %w", rel, err)
				}
			}
			if replace {
				out := filepath.Join(tmp, "git-"+uuid.NewString())
				if err := materializePortableGit(ctx, s.git, filepath.Dir(p), out); err != nil {
					return fmt.Errorf("%s: %w", rel, err)
				}
				sub, err := walkPlain(filepath.Join(out, ".git"), rel)
				if err != nil {
					return err
				}
				items = append(items, sub...)
				if mode.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if !portable {
			return fmt.Errorf("%w: %s (%s)", runner.ErrUnsupportedFileType, rel, mode.Type())
		}
		link := ""
		if mode&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		items = append(items, item{rel: rel, abs: p, info: info, link: link})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].rel < items[j].rel })
	return items, nil
}

// walkPlain lists a materialized git directory under the workspace path rel.
func walkPlain(dir, rel string) ([]item, error) {
	var out []item
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		sub, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := rel
		if sub != "." {
			name = rel + "/" + filepath.ToSlash(sub)
		}
		mode := info.Mode()
		switch {
		case mode.IsDir(), mode.IsRegular():
			out = append(out, item{rel: name, abs: p, info: info})
		case mode&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out = append(out, item{rel: name, abs: p, info: info, link: l})
		}
		return nil
	})
	return out, err
}

func insideGitDir(rel string) bool {
	return strings.HasPrefix(rel, ".git/") || strings.Contains(rel, "/.git/")
}

// readGitFile resolves a `.git` file's "gitdir:" target.
func readGitFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	if !strings.HasPrefix(line, "gitdir:") {
		return "", fmt.Errorf("not a gitdir file")
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(p), target)
	}
	return filepath.EvalSymlinks(filepath.Clean(target))
}

// within reports whether target is root or below it.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// appended to adapter/checkpoint/walk.go
func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ScanEntries computes the manifest entries of a working copy with the same
// algorithm the store uses (strict: an external .git link is an error, nothing
// is materialized). The runner agent (P3 T9) uses it to diff and push.
func ScanEntries(ctx context.Context, root string) ([]runner.ManifestEntry, error) {
	s := &Store{}
	items, err := s.collect(ctx, root, false, "")
	if err != nil {
		return nil, err
	}
	out := make([]runner.ManifestEntry, 0, len(items))
	for _, it := range items {
		sum := ""
		if it.info.Mode().IsRegular() {
			if sum, err = hashFile(it.abs); err != nil {
				return nil, err
			}
		}
		e, err := runner.EntryFromInfo(it.rel, it.info, sum, it.link)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
