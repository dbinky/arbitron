package lunaroute_test

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type isolationPath struct{ root, name string }

// Snapshot only the protected codex/pi inputs, without following links or
// exposing their paths and credential-bearing bytes in an observation.
func snapshotIsolationPaths(home string, targets []isolationPath) (backendHomeSnapshot, error) {
	out := backendHomeSnapshot{}
	total := 0
	for _, target := range targets {
		// WalkDir does not follow links, but ancestors must be checked as well.
		path := target.root
		for _, component := range append([]string{""}, strings.Split(target.name, string(os.PathSeparator))...) {
			path = filepath.Join(path, component)
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() && info.Mode().Perm()&0500 != 0500 {
				return nil, errAgentEvidence
			}
			if info.IsDir() {
				out[path] = backendHomeEntry{Mode: info.Mode()}
			}
		}
		err := filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return errAgentEvidence
			}
			info, err := d.Info()
			if err != nil || !info.IsDir() && !info.Mode().IsRegular() {
				return errAgentEvidence
			}
			rel, err := filepath.Rel(target.root, path)
			if err != nil {
				return errAgentEvidence
			}
			entry := backendHomeEntry{Mode: info.Mode()}
			total += len(rel) + 1
			if total > backendTreeLimit || info.Size() > int64(backendTreeLimit-total) && info.Mode().IsRegular() {
				return errAgentEvidence
			}
			if info.Mode().IsRegular() {
				f, err := os.Open(path)
				if err != nil {
					return errAgentEvidence
				}
				entry.Data, err = io.ReadAll(io.LimitReader(f, int64(backendTreeLimit-total)+1))
				closeErr := f.Close()
				if err != nil || closeErr != nil || len(entry.Data) > backendTreeLimit-total {
					return errAgentEvidence
				}
				total += len(entry.Data)
			}
			out[target.root+string(os.PathSeparator)+rel] = entry
			return nil
		})
		if err != nil {
			return nil, errAgentEvidence
		}
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errAgentEvidence
	}
	return out, nil
}
