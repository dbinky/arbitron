package process

import "bytes"

// maxOutputBytes is the maximum size of stdout/stderr captured from script and
// tool_call executors. Output beyond this limit is silently discarded.
const maxOutputBytes = 1 << 20 // 1 MiB

// limitedWriter wraps a bytes.Buffer and stops writing once limit bytes have
// been written. Excess writes are silently discarded (no error returned) to
// avoid breaking exec pipelines.
type limitedWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil // discard
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	w.buf.Write(p)
	// Report full length to avoid short-write errors from exec.
	return len(p), nil
}
