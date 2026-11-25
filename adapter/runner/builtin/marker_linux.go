package builtin

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// processStartMarker is boot_id + /proc/<pid>/stat field 22 (starttime, in
// clock ticks since boot).
func processStartMarker(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, fs.ErrNotExist) {
		return "", errProcessGone
	}
	if err != nil {
		return "", fmt.Errorf("builtin supervisor: read /proc/%d/stat: %w", pid, err)
	}
	// comm (field 2) may contain spaces and parens, so the fields resume after
	// the LAST ')'. fields[0] is field 3 (state); starttime is field 22.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return "", fmt.Errorf("builtin supervisor: malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(b[i+1:]))
	if len(fields) < 20 {
		return "", fmt.Errorf("builtin supervisor: malformed /proc/%d/stat", pid)
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return "", errProcessGone
	}
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return "linux:" + strings.TrimSpace(string(boot)) + ":" + fields[19], nil
}
