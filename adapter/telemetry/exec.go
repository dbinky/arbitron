package telemetry

import (
	"bytes"
	"context"
	"os"
	"os/exec"
)

// execCommand runs name with a timeout and the C locale, returning stdout
// even on a non-zero exit.
func execCommand(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}
