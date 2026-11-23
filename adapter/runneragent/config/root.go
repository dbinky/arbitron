package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VerifyWorkspaceRoot advertises only an existing, writable, physical directory.
func VerifyWorkspaceRoot(root string) (string, string) {
	clean := filepath.Clean(root)
	if !filepath.IsAbs(clean) {
		return "", fmt.Sprintf("workspace root %q must be absolute", root)
	}
	component := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(clean, component), string(filepath.Separator)) {
		component = filepath.Join(component, part)
		info, err := os.Lstat(component)
		if os.IsNotExist(err) {
			return "", fmt.Sprintf("workspace root %q does not exist", clean)
		}
		if err != nil {
			return "", fmt.Sprintf("workspace root %q cannot be inspected: %v", clean, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Sprintf("workspace root %q has a symlinked component %q", clean, component)
		}
		if !info.IsDir() {
			return "", fmt.Sprintf("workspace root %q is not a directory", clean)
		}
	}
	f, err := os.CreateTemp(clean, ".arbitron-write-check-*")
	if err != nil {
		return "", fmt.Sprintf("workspace root %q is not writable by the runner: %v", clean, err)
	}
	closeErr := f.Close()
	removeErr := os.Remove(f.Name())
	if closeErr != nil {
		return "", fmt.Sprintf("workspace root %q write check: %v", clean, closeErr)
	}
	if removeErr != nil {
		return "", fmt.Sprintf("workspace root %q write check cleanup: %v", clean, removeErr)
	}
	return clean, ""
}
