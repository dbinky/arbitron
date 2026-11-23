package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// SessionBundle is a harness's own session state, exported at a turn boundary
// on the runner that holds the session. Data is opaque to everything except
// the backend's SessionPortability adapter.
type SessionBundle struct {
	Backend        string // workflow.BackendClaudeCode | workflow.BackendCodex
	SessionID      string
	HarnessVersion string
	OriginWorkDir  string
	Checksum       string // lowercase hex SHA-256 of Data
	Data           []byte
}

// SessionPortability moves a harness session between machines. It is the only
// code that knows a harness's session file layout.
type SessionPortability interface {
	Export(ctx context.Context, sessionID, workDir string) (SessionBundle, error)
	Import(ctx context.Context, b SessionBundle, workDir string) (sessionID string, err error)
}

// ErrBundleRequiresRebuild means native restoration is unsafe or impossible
// (version skew, foreign paths, compaction, corrupt bundle). Callers fall back
// to the visible transcript rebuild; it never means "capacity exhausted".
var ErrBundleRequiresRebuild = errors.New("engine: session bundle cannot be restored natively; rebuild from the recorded transcript")

// ErrBundleChecksum means Data does not match Checksum.
var ErrBundleChecksum = errors.New("engine: session bundle checksum mismatch")

// BundleChecksum is the canonical bundle checksum.
func BundleChecksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Verify checks Data against Checksum.
func (b SessionBundle) Verify() error {
	if b.Checksum == "" || b.Checksum != BundleChecksum(b.Data) {
		return ErrBundleChecksum
	}
	return nil
}
