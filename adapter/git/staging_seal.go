package git

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// sealStaging restores the engine-owned git metadata of a staging repository
// before the server runs git in it.
//
// The conflict-resolution agent works inside the staging tree, .git included,
// as the same OS user, so it can rewrite the repository's local config or
// info/attributes (e.g. "[filter \"x\"] clean = <cmd>" or a merge driver).
// Runner drops system and global config and pins its "-c" isolation keys, but
// repository-local config is always read and arbitrary filter.* / merge.*
// keys cannot be neutralized from the command line. So every server staging
// operation first rewrites:
//   - .git/config to the core keys git init writes, keeping only the
//     filesystem-probed ignorecase/precomposeunicode booleans; every other
//     section and key (include, includeIf, filter, merge, extensions, ...)
//     is dropped;
//   - .git/info/attributes to backingAttributes;
//
// and removes config.worktree and commondir (which would redirect the
// repository to another config). A .git that is not a real directory is
// refused. A staging tree without .git (removed, never created) is left
// alone: ensureStaging rebuilds it.
func sealStaging(staging string) error {
	gitDir := filepath.Join(staging, ".git")
	info, err := os.Lstat(gitDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("seal staging: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("seal staging: %s is not a directory", gitDir)
	}
	for _, name := range []string{"config.worktree", "commondir"} {
		if err := os.RemoveAll(filepath.Join(gitDir, name)); err != nil {
			return fmt.Errorf("seal staging: remove %s: %w", name, err)
		}
	}
	current, _ := readRegularFile(filepath.Join(gitDir, "config"))
	if err := replaceFile(gitDir, "config", stagingConfig(current)); err != nil {
		return fmt.Errorf("seal staging: %w", err)
	}
	infoDir := filepath.Join(gitDir, "info")
	if info, err := os.Lstat(infoDir); err != nil || !info.IsDir() {
		if err := os.RemoveAll(infoDir); err != nil {
			return fmt.Errorf("seal staging: %w", err)
		}
		if err := os.Mkdir(infoDir, 0o700); err != nil {
			return fmt.Errorf("seal staging: %w", err)
		}
	}
	if err := replaceFile(infoDir, "attributes", []byte(backingAttributes)); err != nil {
		return fmt.Errorf("seal staging: %w", err)
	}
	return nil
}

// stagingConfig renders the only repository config a staging repository may
// have. ignorecase and precomposeunicode describe the filesystem (git init
// probes them) and are carried over from current when they appear, in strict
// form, in its [core] section with a boolean value.
func stagingConfig(current []byte) []byte {
	var b bytes.Buffer
	b.WriteString("[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n")
	probed := map[string]string{}
	inCore := false
	sc := bufio.NewScanner(bytes.NewReader(current))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inCore = line == "[core]"
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !inCore || !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if (key == "ignorecase" || key == "precomposeunicode") && (value == "true" || value == "false") {
			probed[key] = value
		}
	}
	for _, key := range []string{"ignorecase", "precomposeunicode"} {
		if v, ok := probed[key]; ok {
			fmt.Fprintf(&b, "\t%s = %s\n", key, v)
		}
	}
	return b.Bytes()
}

// readRegularFile returns path's content when it is a regular file.
func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}

// replaceFile atomically replaces dir/name with content (mode 0600). The
// temporary file is created exclusively and the rename replaces whatever was
// at the name (a planted symlink included) without following it.
func replaceFile(dir, name string, content []byte) error {
	target := filepath.Join(dir, name)
	if info, err := os.Lstat(target); err == nil && info.IsDir() {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	tmp := filepath.Join(dir, "."+name+".arbitron-seal")
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
