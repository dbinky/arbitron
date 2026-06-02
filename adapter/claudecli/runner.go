package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// initialReadBuf is the starting capacity of the per-line read buffer. Lines
// are not capped at any fixed ceiling — see MaxResponseBytes on RunSpec for
// the per-request cap that bounds total stdout consumption.
const initialReadBuf = 64 * 1024

// stderrTailBytes bounds the per-request stderr tail we keep for diagnostics.
// Larger writes are silently truncated (older bytes dropped).
const stderrTailBytes = 64 * 1024

// ErrResponseTooLarge is returned by the runner's wait() function when the
// cumulative stdout bytes exceeded the per-request cap declared in
// RunSpec.MaxResponseBytes.
//
// SECURITY: see PR #8 thread 3291941918 — a hostile prompt + skip-permissions
// can instruct the agent to flood stdout (e.g. `dd if=/dev/urandom`), which
// would OOM the engine under an unbounded ReadBytes('\n').
var ErrResponseTooLarge = errors.New("claudecli: response exceeded MaxResponseBytes")

// RunSpec describes a subprocess invocation.
type RunSpec struct {
	Bin   string
	Args  []string
	Stdin string
	Dir   string
	Env   []string
	// MaxResponseBytes is the per-request cumulative stdout cap. When 0
	// (the zero value), no cap is applied — only test configurations should
	// rely on that; production wires cfg.LLM.MaxResponseBytes.
	MaxResponseBytes int64
}

// CommandRunner starts a subprocess and streams its stdout line by line.
// The lines channel is closed when stdout reaches EOF (or the per-request
// cap is hit); wait then blocks until the process exits and returns either
// the exit error, a stdout read error, or ErrResponseTooLarge.
//
// Contract: callers MUST drain the lines channel to completion (until it is
// closed) before calling wait. Draining establishes the happens-before edge
// (via the internal `done` channel) that lets wait observe any stdout read
// state set by the reader goroutine.
type CommandRunner interface {
	Run(ctx context.Context, spec RunSpec) (lines <-chan string, wait func() error, err error)
}

// execRunner is the production CommandRunner backed by os/exec.
type execRunner struct{}

// newExecRunner constructs the os/exec-backed CommandRunner.
func newExecRunner() *execRunner { return &execRunner{} }

// NewExecRunner returns the shared production os/exec-backed CommandRunner.
// Exported so sibling driven adapters (e.g. adapter/opencode) can reuse the
// same subprocess seam instead of redefining a parallel runner.
func NewExecRunner() CommandRunner { return newExecRunner() }

// Run starts the process described by spec. The caller's ctx is used both
// for the subprocess lifecycle (exec.CommandContext) and for early abort if
// the response-bytes cap is exceeded (via an internal derived cancel).
func (r *execRunner) Run(ctx context.Context, spec RunSpec) (<-chan string, func() error, error) {
	// Derive a cancellable context so the reader goroutine can abort the
	// subprocess on a bytes-cap overrun without affecting the caller's ctx.
	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, spec.Bin, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("claudecli: stdout pipe: %w", err)
	}

	// SF1: capture stderr into a bounded ring-style buffer so diagnostics
	// survive failed runs without unbounded memory growth. The tail is
	// surfaced via wait()'s error message.
	stderrBuf := newTailBuf(stderrTailBytes)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("claudecli: start %q: %w", spec.Bin, err)
	}

	lines := make(chan string)
	// SF2: done is closed by the reader goroutine after it sets readErr.
	// wait() reads readErr only after observing done, giving Go's happens-
	// before contract for cross-goroutine reads of the variable.
	done := make(chan struct{})
	var readErr error

	go func() {
		defer close(lines)
		defer close(done)

		reader := bufio.NewReaderSize(stdout, initialReadBuf)
		var totalBytes int64

		for {
			// MF8: ReadBytes has no fixed per-line ceiling. The total-bytes
			// cap below is the runner-level backstop.
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				totalBytes += int64(len(line))
				if spec.MaxResponseBytes > 0 && totalBytes > spec.MaxResponseBytes {
					// MF7: bytes cap exceeded. Kill the subprocess; report
					// via readErr so wait() surfaces it. Do NOT push the
					// over-cap line to consumers — it is unsafe to use.
					readErr = fmt.Errorf("%w: read %d bytes, cap %d", ErrResponseTooLarge, totalBytes, spec.MaxResponseBytes)
					cancel()
					// Drain remaining stdout so the subprocess's writes do
					// not block; ignore further data.
					_, _ = io.Copy(io.Discard, reader)
					return
				}
				// Trim the trailing '\n' (ReadBytes keeps it; the previous
				// Scanner contract did not). Empty lines pass through as "".
				if line[len(line)-1] == '\n' {
					line = line[:len(line)-1]
				}
				lines <- string(line)
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readErr = fmt.Errorf("claudecli: read stdout: %w", err)
				}
				return
			}
		}
	}()

	wait := func() error {
		// Wait for the reader goroutine to finish so readErr is safe to read
		// (SF2 happens-before).
		<-done
		waitErr := cmd.Wait()
		// Always release the derived context (covers the normal-exit path).
		cancel()

		// Order: a bytes-cap overrun should be surfaced verbatim; otherwise
		// a stdout read error wins over the exit code; otherwise the exit
		// code (or nil) is returned. Stderr tail is attached when there's
		// an underlying error to surface.
		if readErr != nil {
			return decorateWithStderr(readErr, stderrBuf)
		}
		if waitErr != nil {
			return decorateWithStderr(
				fmt.Errorf("claudecli: claude exited with error: %w", waitErr),
				stderrBuf,
			)
		}
		return nil
	}
	return lines, wait, nil
}

// tailBuf is an io.Writer that keeps only the last max bytes written to it.
// Older bytes are discarded silently. Safe for single-writer use; the runner
// owns it.
type tailBuf struct {
	max int
	buf bytes.Buffer
}

func newTailBuf(max int) *tailBuf { return &tailBuf{max: max} }

func (b *tailBuf) Write(p []byte) (int, error) {
	n := len(p)
	if b.max <= 0 {
		return n, nil
	}
	if len(p) >= b.max {
		// p alone exceeds the window — keep only its tail.
		b.buf.Reset()
		b.buf.Write(p[len(p)-b.max:])
		return n, nil
	}
	// Trim existing buffer so existing + new fits in max.
	if b.buf.Len()+len(p) > b.max {
		excess := b.buf.Len() + len(p) - b.max
		keep := b.buf.Bytes()[excess:]
		// Re-seed the buffer with the trimmed prefix.
		trimmed := make([]byte, len(keep))
		copy(trimmed, keep)
		b.buf.Reset()
		b.buf.Write(trimmed)
	}
	b.buf.Write(p)
	return n, nil
}

// Bytes returns a snapshot of the retained tail.
func (b *tailBuf) Bytes() []byte {
	out := make([]byte, b.buf.Len())
	copy(out, b.buf.Bytes())
	return out
}

// decorateWithStderr augments err with a single-line summary derived from the
// stderr tail. The full tail is intentionally NOT included to avoid leaking
// secrets into log/Error chains; callers wanting the full tail should log it
// separately. SECURITY redaction note from PR #8 thread 3291907729.
func decorateWithStderr(err error, tail *tailBuf) error {
	if err == nil {
		return nil
	}
	raw := tail.Bytes()
	if len(raw) == 0 {
		return err
	}
	summary := stderrSummary(raw)
	if summary == "" {
		return err
	}
	return fmt.Errorf("%w (stderr: %s)", err, summary)
}

// stderrSummary extracts the last non-empty line of the stderr tail, capped
// at 256 chars, for inclusion in the returned error message. The full tail
// is available to operators via wait()-internal logging in a future commit
// if needed; today the runner surfaces only the summary so it survives
// passing through downstream wrappers.
func stderrSummary(raw []byte) string {
	const maxLen = 256
	// Find the last non-empty line.
	lines := bytes.Split(raw, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		if len(line) > maxLen {
			line = line[:maxLen]
		}
		return string(line)
	}
	return ""
}
