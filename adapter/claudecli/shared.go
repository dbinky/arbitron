package claudecli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// validateWorkDir enforces the per-call WorkDir invariants shared by every
// claude-cli adapter:
//   - non-empty (avoid the silent "inherit engine cwd" fallback that would
//     hand the agent the launch directory containing arbitron.db, config.yaml,
//     and other secrets — PR #8 thread 3291944671 item 1)
//   - resolves to an absolute path under workspaceRoot when one is configured
//     (PR #8 thread 3291944671 item 2)
//
// An empty workspaceRoot disables the pin check (test convenience only).
func validateWorkDir(workDir, workspaceRoot string) error {
	if workDir == "" {
		return ErrWorkDirRequired
	}
	if workspaceRoot == "" {
		return nil
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("claudecli: resolve WorkDir: %w", err)
	}
	abs = filepath.Clean(abs)
	root := filepath.Clean(workspaceRoot)
	// Allow root itself, or any subdir of root. Use a separator-terminated
	// prefix check to avoid /root/wsX shadowing /root/ws.
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q not under %q", ErrWorkDirEscapesRoot, abs, root)
	}
	return nil
}

// collectResult runs spec to completion via runner, draining the stdout lines
// and returning the parsed terminal result event plus every non-result event
// that preceded it (in order). It encapsulates the non-streaming consume loop
// shared by ChatAdapter.Chat (which discards the steps) and
// AgentAdapter.RunAgent (which folds them into AgentResult.Steps): it surfaces
// start errors, mid-stream parse errors, process exit errors, a missing result
// event, and an is_error result.
func collectResult(ctx context.Context, runner CommandRunner, spec RunSpec) (streamEvent, []streamEvent, error) {
	lines, wait, err := runner.Run(ctx, spec)
	if err != nil {
		return streamEvent{}, nil, fmt.Errorf("claudecli: call: %w", err)
	}

	var result streamEvent
	var haveResult bool
	var parseErr error
	var steps []streamEvent
	for line := range lines {
		if parseErr != nil {
			continue // keep draining; don't process further
		}
		ev, ok, perr := parseEvent([]byte(line))
		if perr != nil {
			parseErr = perr
			continue
		}
		if !ok {
			continue
		}
		if ev.Kind == eventResult {
			result, haveResult = ev, true
			continue
		}
		steps = append(steps, ev)
	}
	if waitErr := wait(); waitErr != nil {
		return streamEvent{}, nil, fmt.Errorf("claudecli: claude exited with error: %w", waitErr)
	}
	if parseErr != nil {
		return streamEvent{}, nil, parseErr
	}
	if !haveResult {
		return streamEvent{}, nil, fmt.Errorf("claudecli: no result event in claude output")
	}
	if result.IsError {
		return streamEvent{}, nil, fmt.Errorf("claudecli: claude reported an error: %s", result.Text)
	}
	return result, steps, nil
}
