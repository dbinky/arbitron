// adapter/checkpoint/archive.go
package checkpoint

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/runner"
)

const maxDeltaControlBytes = 64 << 20

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// writeArchiveFile writes items to a new file through the store's creator and
// fsyncs it before close.
func (s *Store) writeArchiveFile(path string, items []item) ([]runner.ManifestEntry, int64, error) {
	w, err := s.create(path, 0o600)
	if err != nil {
		return nil, 0, err
	}
	cw := &countingWriter{w: w}
	entries, werr := writeArchive(cw, items)
	if err := finish(w, werr); err != nil {
		return nil, 0, err
	}
	return entries, cw.n, nil
}

func writeArchive(w io.Writer, items []item) ([]runner.ManifestEntry, error) {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	entries := make([]runner.ManifestEntry, 0, len(items))
	for _, it := range items {
		e, err := writeItem(tw, it)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return entries, nil
}

func writeItem(tw *tar.Writer, it item) (runner.ManifestEntry, error) {
	m := it.info.Mode()
	hdr := &tar.Header{Name: it.rel, ModTime: it.info.ModTime(), Format: tar.FormatPAX}
	switch {
	case m.IsDir():
		hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, it.rel+"/", int64(m.Perm())
	case m.IsRegular():
		hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeReg, int64(m.Perm()), it.info.Size()
	case m&fs.ModeSymlink != 0:
		hdr.Typeflag, hdr.Linkname, hdr.Mode = tar.TypeSymlink, it.link, 0o777
	default:
		return runner.ManifestEntry{}, fmt.Errorf("%w: %s (%s)", runner.ErrUnsupportedFileType, it.rel, m.Type())
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return runner.ManifestEntry{}, err
	}
	sum := ""
	if m.IsRegular() {
		f, err := os.Open(it.abs)
		if err != nil {
			return runner.ManifestEntry{}, err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(tw, h), f)
		f.Close()
		if err != nil {
			return runner.ManifestEntry{}, fmt.Errorf("checkpoint: archive %s: %w", it.rel, err)
		}
		if n != hdr.Size {
			return runner.ManifestEntry{}, fmt.Errorf("checkpoint: %s changed while archiving", it.rel)
		}
		sum = hex.EncodeToString(h.Sum(nil))
	}
	return runner.EntryFromInfo(it.rel, it.info, sum, it.link)
}

// budget enforces Limits across one extraction.
type budget struct {
	l       Limits
	entries int
	bytes   int64
}

func (b *budget) admit(name string, size int64) error {
	b.entries++
	b.bytes += size
	switch {
	case len(name) > b.l.MaxPathBytes:
		return fmt.Errorf("%w: path longer than %d bytes", runner.ErrCorruptCheckpoint, b.l.MaxPathBytes)
	case b.entries > b.l.MaxEntries:
		return fmt.Errorf("%w: more than %d entries", runner.ErrCorruptCheckpoint, b.l.MaxEntries)
	case size < 0 || b.bytes > b.l.MaxBytes:
		return fmt.Errorf("%w: expands beyond %d bytes", runner.ErrCorruptCheckpoint, b.l.MaxBytes)
	}
	return nil
}

// openGzip reads exactly one gzip member and reports trailing data.
func openGzip(r io.Reader) (*gzip.Reader, *bufio.Reader, error) {
	br := bufio.NewReader(r)
	gz, err := gzip.NewReader(br)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: gzip: %v", runner.ErrCorruptCheckpoint, err)
	}
	gz.Multistream(false)
	return gz, br, nil
}

// endOfArchive accepts only zero padding after the tar end and nothing after
// the gzip member.
func endOfArchive(gz *gzip.Reader, br *bufio.Reader) error {
	rest, err := io.ReadAll(io.LimitReader(gz, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: archive tail: %v", runner.ErrCorruptCheckpoint, err)
	}
	for _, c := range rest {
		if c != 0 {
			return fmt.Errorf("%w: unexpected data after archive end", runner.ErrCorruptCheckpoint)
		}
	}
	if _, err := br.Peek(1); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: unexpected trailing part after archive", runner.ErrCorruptCheckpoint)
	}
	return nil
}

// extractVerified extracts an archive into dst and checks it reproduces m
// exactly (truncated, tampered or extra entries fail with ErrCorruptCheckpoint).
func extractVerified(archive, dst string, m runner.Manifest, l Limits) error {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	modes, err := extractInto(archive, root, m, l)
	if err != nil {
		return err
	}
	return applyDirModes(root, modes)
}

func extractInto(archive string, root *os.Root, m runner.Manifest, l Limits) (map[string]fs.FileMode, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, br, err := openGzip(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	modes := map[string]fs.FileMode{}
	seen, err := applyTar(tar.NewReader(gz), root, false, modes, &budget{l: l})
	if err != nil {
		return nil, err
	}
	if err := endOfArchive(gz, br); err != nil {
		return nil, err
	}
	if len(seen) != len(m.Entries) {
		return nil, fmt.Errorf("%w: archive has %d entries, manifest %d", runner.ErrCorruptCheckpoint, len(seen), len(m.Entries))
	}
	for _, e := range m.Entries {
		got, ok := seen[e.Path]
		if !ok || !runner.SameEntry(got, e) {
			return nil, fmt.Errorf("%w: %s", runner.ErrCorruptCheckpoint, e.Path)
		}
	}
	return modes, nil
}

// noSymlinkAncestor refuses to write beneath a symlink, even one that stays
// inside root (classic tar symlink-then-child attack).
func noSymlinkAncestor(root *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		p := strings.Join(parts[:i], "/")
		fi, err := root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s lies beneath symlink %s", runner.ErrCorruptCheckpoint, name, p)
		}
	}
	return nil
}

// applyTar writes entries under root (os.Root refuses escapes). Directories are
// created 0700 and their recorded modes collected for applyDirModes, so
// read-only directories never block their children.
func applyTar(tr *tar.Reader, root *os.Root, overwrite bool, modes map[string]fs.FileMode, b *budget) (map[string]runner.ManifestEntry, error) {
	seen := map[string]runner.ManifestEntry{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return seen, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: read archive: %v", runner.ErrCorruptCheckpoint, err)
		}
		name, err := runner.CleanRelPath(strings.TrimSuffix(hdr.Name, "/"))
		if err != nil {
			return nil, err
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("%w: duplicate archive entry %s", runner.ErrCorruptCheckpoint, name)
		}
		if err := b.admit(name, hdr.Size); err != nil {
			return nil, err
		}
		if err := noSymlinkAncestor(root, name); err != nil {
			return nil, err
		}
		if overwrite {
			if err := removeExisting(root, name, hdr.Typeflag == tar.TypeDir); err != nil {
				return nil, err
			}
		}
		if dir := path.Dir(name); dir != "." {
			if err := root.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("checkpoint: mkdir %s: %w", dir, err)
			}
		}
		perm := fs.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o700); err != nil {
				return nil, fmt.Errorf("checkpoint: mkdir %s: %w", name, err)
			}
			modes[name] = perm
			seen[name] = runner.ManifestEntry{Path: name, Mode: uint32(fs.ModeDir | perm)}
		case tar.TypeReg:
			f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, fmt.Errorf("checkpoint: create %s: %w", name, err)
			}
			h := sha256.New()
			n, cpErr := io.Copy(io.MultiWriter(f, h), tr)
			clErr := f.Close()
			if cpErr != nil {
				return nil, fmt.Errorf("checkpoint: write %s: %w", name, cpErr)
			}
			if clErr != nil {
				return nil, fmt.Errorf("checkpoint: close %s: %w", name, clErr)
			}
			if err := root.Chmod(name, perm); err != nil {
				return nil, err
			}
			seen[name] = runner.ManifestEntry{Path: name, Size: n, Mode: uint32(perm), SHA256: hex.EncodeToString(h.Sum(nil))}
		case tar.TypeSymlink:
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return nil, fmt.Errorf("checkpoint: symlink %s: %w", name, err)
			}
			seen[name] = runner.ManifestEntry{Path: name, Mode: uint32(fs.ModeSymlink | 0o777), Symlink: hdr.Linkname}
		default:
			return nil, fmt.Errorf("%w: archive entry %s has type %q", runner.ErrUnsupportedFileType, name, hdr.Typeflag)
		}
	}
}

func removeExisting(root *os.Root, name string, wantDir bool) error {
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if wantDir && fi.IsDir() {
		return nil
	}
	return root.RemoveAll(name)
}

func applyDirModes(root *os.Root, modes map[string]fs.FileMode) error {
	names := make([]string, 0, len(modes))
	for n := range modes {
		names = append(names, n)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // children before parents
	for _, n := range names {
		if err := root.Chmod(n, modes[n]); err != nil {
			return fmt.Errorf("checkpoint: chmod %s: %w", n, err)
		}
	}
	return nil
}

// appended to adapter/checkpoint/archive.go
// applyDelta reads the control entry, applies its deletions, then writes every
// changed entry. modes accumulates directory permissions for applyDirModes.
func applyDelta(r io.Reader, root *os.Root, modes map[string]fs.FileMode, l Limits) (runner.DeltaControl, error) {
	gz, br, err := openGzip(r)
	if err != nil {
		return runner.DeltaControl{}, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil {
		return runner.DeltaControl{}, fmt.Errorf("%w: delta: %v", runner.ErrCorruptCheckpoint, err)
	}
	if hdr.Name != runner.DeltaControlPath || hdr.Typeflag != tar.TypeReg || hdr.Size > maxDeltaControlBytes {
		return runner.DeltaControl{}, fmt.Errorf("%w: delta must start with %s", runner.ErrCorruptCheckpoint, runner.DeltaControlPath)
	}
	b, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
	if err != nil {
		return runner.DeltaControl{}, fmt.Errorf("%w: delta control: %v", runner.ErrCorruptCheckpoint, err)
	}
	var ctl runner.DeltaControl
	if err := json.Unmarshal(b, &ctl); err != nil {
		return runner.DeltaControl{}, fmt.Errorf("%w: delta control: %v", runner.ErrCorruptCheckpoint, err)
	}
	expected := map[string]bool{}
	for _, e := range ctl.Expected {
		if _, err := runner.CleanRelPath(e.Path); err != nil {
			return runner.DeltaControl{}, err
		}
		if expected[e.Path] {
			return runner.DeltaControl{}, fmt.Errorf("%w: duplicate expected path %s", runner.ErrCorruptCheckpoint, e.Path)
		}
		expected[e.Path] = true
	}
	for _, d := range ctl.Deleted {
		clean, err := runner.CleanRelPath(d)
		if err != nil {
			return runner.DeltaControl{}, err
		}
		if err := noSymlinkAncestor(root, clean); err != nil {
			return runner.DeltaControl{}, err
		}
		if err := root.RemoveAll(clean); err != nil {
			return runner.DeltaControl{}, fmt.Errorf("checkpoint: delete %s: %w", clean, err)
		}
		for k := range modes {
			if k == clean || strings.HasPrefix(k, clean+"/") {
				delete(modes, k)
			}
		}
	}
	if _, err := applyTar(tr, root, true, modes, &budget{l: l}); err != nil {
		return runner.DeltaControl{}, err
	}
	if err := endOfArchive(gz, br); err != nil {
		return runner.DeltaControl{}, err
	}
	return ctl, nil
}

// WriteDelta encodes a push: the DeltaControl entry first, then every changed
// path (directories, regular files, symlinks) read from root.
func WriteDelta(w io.Writer, root string, changed []string, ctl runner.DeltaControl) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	b, err := json.Marshal(ctl)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: runner.DeltaControlPath, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(b)), Format: tar.FormatPAX}); err != nil {
		return err
	}
	if _, err := tw.Write(b); err != nil {
		return err
	}
	paths := append([]string(nil), changed...)
	sort.Strings(paths)
	for _, p := range paths {
		clean, err := runner.CleanRelPath(p)
		if err != nil {
			return err
		}
		abs := filepath.Join(root, filepath.FromSlash(clean))
		info, err := os.Lstat(abs)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(abs); err != nil {
				return err
			}
		}
		if _, err := writeItem(tw, item{rel: clean, abs: abs, info: info, link: link}); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
