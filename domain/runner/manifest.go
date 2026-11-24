// domain/runner/manifest.go
package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// CleanRelPath validates a slash-separated workspace-relative path and returns
// it unchanged. Absolute paths, "..", empty/"." paths, redundant separators,
// backslashes and NUL bytes are rejected.
func CleanRelPath(p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return "", fmt.Errorf("runner: invalid workspace path %q", p)
	}
	c := path.Clean(p)
	if c != p || c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("runner: invalid workspace path %q", p)
	}
	return c, nil
}

// EntryFromInfo maps a Lstat result to a manifest entry. Only directories,
// regular files and symlinks are portable; devices, sockets, named pipes and
// irregular files are rejected explicitly.
func EntryFromInfo(rel string, info fs.FileInfo, sha256Hex, link string) (ManifestEntry, error) {
	m := info.Mode()
	switch {
	case m.IsDir():
		return ManifestEntry{Path: rel, Mode: uint32(fs.ModeDir | m.Perm())}, nil
	case m.IsRegular():
		return ManifestEntry{Path: rel, Size: info.Size(), Mode: uint32(m.Perm()), SHA256: sha256Hex}, nil
	case m&fs.ModeSymlink != 0:
		return ManifestEntry{Path: rel, Mode: uint32(fs.ModeSymlink | 0o777), Symlink: link}, nil
	default:
		return ManifestEntry{}, fmt.Errorf("%w: %s (%s)", ErrUnsupportedFileType, rel, m.Type())
	}
}

func sortedEntries(es []ManifestEntry) []ManifestEntry {
	out := append([]ManifestEntry(nil), es...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ManifestHash is the SHA-256 over the version and the sorted entries.
func ManifestHash(version int64, entries []ManifestEntry) string {
	h := sha256.New()
	fmt.Fprintf(h, "arbitron-manifest-v1\nversion %d\n", version)
	for _, e := range sortedEntries(entries) {
		fmt.Fprintf(h, "%q %d %o %s %q %t\n", e.Path, e.Size, e.Mode, e.SHA256, e.Symlink, e.Deleted)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NewManifest sorts entries and computes the hash.
func NewManifest(version int64, entries []ManifestEntry) Manifest {
	s := sortedEntries(entries)
	return Manifest{Version: version, Entries: s, Hash: ManifestHash(version, s)}
}

// Verify recomputes the hash.
func (m Manifest) Verify() error {
	if got := ManifestHash(m.Version, m.Entries); got != m.Hash {
		return fmt.Errorf("%w: manifest v%d hash %s != recorded %s", ErrCorruptCheckpoint, m.Version, got, m.Hash)
	}
	return nil
}

// TotalBytes sums regular-file sizes (the move-size input for remote homes).
func (m Manifest) TotalBytes() int64 {
	var n int64
	for _, e := range m.Entries {
		n += e.Size
	}
	return n
}

// SameEntry compares two entries ignoring Deleted.
func SameEntry(a, b ManifestEntry) bool {
	return a.Path == b.Path && a.Size == b.Size && a.Mode == b.Mode && a.SHA256 == b.SHA256 && a.Symlink == b.Symlink
}

// SameEntries compares two entry sets regardless of order.
func SameEntries(a, b []ManifestEntry) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := sortedEntries(a), sortedEntries(b)
	for i := range sa {
		if !SameEntry(sa[i], sb[i]) {
			return false
		}
	}
	return true
}

// DiffManifests returns the entries of `to` that are absent from or differ in
// `from` (to fetch on pull / to send on push), and the sorted paths of `from`
// absent from `to` (to delete).
func DiffManifests(from, to []ManifestEntry) (changed []ManifestEntry, deleted []string) {
	old := make(map[string]ManifestEntry, len(from))
	for _, e := range from {
		old[e.Path] = e
	}
	seen := make(map[string]bool, len(to))
	for _, e := range sortedEntries(to) {
		seen[e.Path] = true
		if o, ok := old[e.Path]; !ok || !SameEntry(o, e) {
			changed = append(changed, e)
		}
	}
	for _, e := range sortedEntries(from) {
		if !seen[e.Path] {
			deleted = append(deleted, e.Path)
		}
	}
	return changed, deleted
}
