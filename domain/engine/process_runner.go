package engine

import "context"

// ProcessKind names the node family a ProcessSpec was built for.
type ProcessKind string

const (
	ProcessScript   ProcessKind = "script"
	ProcessToolCall ProcessKind = "tool_call"
)

// ProcessSpec is a fully rendered subprocess request. It is built on the server
// by the script/tool_call executors and executed by whichever runner placement
// chose. NodeEnv values are already template-resolved; Passthrough carries env
// variable NAMES only, whose values are read on the executing machine.
type ProcessSpec struct {
	Kind        ProcessKind
	Command     string            // interpreter (script) or command (tool_call)
	Args        []string          // rendered; for script Args[0] is the script path (Phase 1)
	Dir         string            // cwd; script = script dir; tool_call = "" (inherit) until Phase 3
	Stdin       []byte            // nil = none
	NodeEnv     map[string]string // resolved node env
	Passthrough []string          // passthrough NAMES
	TimeoutSecs int               // 0 = none
	WorkDir     string            // canonical {{workspace}} for affinity; "" = no workspace
	Bundle      *ScriptBundleRef  // nil for unbundled scripts
	Portable    bool              // author opt-in to remote process placement
	Requires    []string          // bare executable names required on the runner
}

// ScriptBundleRef names a content-addressed script bundle (Phase 3).
type ScriptBundleRef struct {
	Hash       string
	Entrypoint string // bundle-relative
}

// ProcessResult is the outcome of a completed (or attempted) subprocess.
type ProcessResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ProcessRunner executes a ProcessSpec. A completed non-zero exit is reported in
// ProcessResult.ExitCode with a nil error; a non-nil error means an execution
// fault (start failure, timeout/cancellation, wait failure).
type ProcessRunner interface {
	RunProcess(ctx context.Context, spec ProcessSpec) (ProcessResult, error)
}
