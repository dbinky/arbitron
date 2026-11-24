package runnerproto

import (
	"encoding/hex"
	"errors"
	"fmt"
	"path"

	"github.com/google/uuid"
)

// KindSessionImport is the only session envelope kind (contracts §15/§24).
// Session export is NOT a separate kind: it runs inside the originating stream
// assignment and its bundle descriptor rides that stream's Result.
const KindSessionImport = "session_import"

// OpSessionImport is Dispatch.Op on a session_import frame.
const OpSessionImport = "session_import"

// SessionChecksumHeader carries the lowercase hex SHA-256 of a session bundle
// on the transfer endpoints (PUT request, GET response).
const SessionChecksumHeader = "X-Session-SHA256"

const maxSessionField = 512

// SessionKindForOp returns the envelope kind of a session op.
func SessionKindForOp(op string) (string, bool) {
	if op == OpSessionImport {
		return KindSessionImport, true
	}
	return "", false
}

// WireSessionBundle describes bundle bytes moved over the transfer endpoint.
type WireSessionBundle struct {
	Backend        string `json:"backend"`
	SessionID      string `json:"sessionId"`
	HarnessVersion string `json:"harnessVersion,omitempty"`
	OriginWorkDir  string `json:"originWorkDir,omitempty"`
	Checksum       string `json:"checksum"`
	Size           int64  `json:"size"`
}

func (b *WireSessionBundle) Validate() error {
	sum, err := hex.DecodeString(b.Checksum)
	switch {
	case b.Backend == "" || len(b.Backend) > 64:
		return errors.New("bundle.backend is required")
	case b.SessionID == "" || len(b.SessionID) > maxSessionField:
		return errors.New("bundle.sessionId is required")
	case err != nil || len(sum) != 32 || b.Checksum != hex.EncodeToString(sum):
		return errors.New("bundle.checksum must be lowercase hex SHA-256")
	case b.Size < 1:
		return errors.New("bundle.size must be positive")
	case len(b.OriginWorkDir) > maxSessionField || len(b.HarnessVersion) > 64:
		return errors.New("bundle descriptor field too long")
	}
	return nil
}

// WireResource mirrors runner.ResourceDescriptor (P4 T11 Cycle 5): the
// separately owned resource a session works in. The actual parent run stays
// in the job key; the resource never substitutes for it.
type WireResource struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"` // workspace | resolution
	WorkDir         string `json:"workDir"`
	FenceKey        string `json:"fenceKey"`
	FenceOwner      string `json:"fenceOwner"`
	FenceGeneration int64  `json:"fenceGeneration"`
	FenceToken      string `json:"fenceToken"`
	CheckpointRef   string `json:"checkpointRef,omitempty"`
}

func (r *WireResource) Validate() error {
	if _, err := uuid.Parse(r.ID); err != nil {
		return errors.New("resource.id must be a UUID")
	}
	if tok, err := uuid.Parse(r.FenceToken); err != nil || tok == uuid.Nil {
		return errors.New("resource.fenceToken must be a nonzero UUID")
	}
	switch {
	case r.Kind != "workspace" && r.Kind != "resolution":
		return fmt.Errorf("resource.kind %q is not workspace or resolution", r.Kind)
	case r.WorkDir == "" || !path.IsAbs(r.WorkDir) || path.Clean(r.WorkDir) != r.WorkDir || len(r.WorkDir) > maxSessionField:
		return errors.New("resource.workDir must be a clean absolute path")
	case r.FenceKey == "" || r.FenceOwner == "" || r.FenceGeneration < 1:
		return errors.New("resource fence requires key, owner and positive generation")
	case len(r.CheckpointRef) > maxSessionField:
		return errors.New("resource.checkpointRef too long")
	}
	return nil
}

// WireSessionOp is a session_import job (a move target). TransferToken is the
// job generation's "session" transfer token for the bundle bytes.
type WireSessionOp struct {
	Backend       string             `json:"backend"`
	SessionID     string             `json:"sessionId,omitempty"`
	WorkDir       string             `json:"workDir"`
	TransferToken string             `json:"transferToken"`
	Bundle        *WireSessionBundle `json:"bundle"`
}

func (o *WireSessionOp) Validate() error {
	switch {
	case o.Backend == "" || len(o.Backend) > 64:
		return errors.New("session.backend is required")
	case o.WorkDir == "" || !path.IsAbs(o.WorkDir) || len(o.WorkDir) > maxSessionField:
		return errors.New("session.workDir must be an absolute path")
	case o.TransferToken == "" || len(o.TransferToken) > maxSessionField:
		return errors.New("session.transferToken is required")
	case o.Bundle == nil:
		return errors.New("session_import carries the bundle descriptor")
	}
	if err := o.Bundle.Validate(); err != nil {
		return err
	}
	if o.Bundle.Backend != o.Backend {
		return fmt.Errorf("bundle backend %q does not match session backend %q", o.Bundle.Backend, o.Backend)
	}
	return nil
}

// WireSessionResult is a session_import result: the resumable session id.
type WireSessionResult struct {
	SessionID string `json:"sessionId"`
}

func (r *WireSessionResult) Validate() error {
	if r.SessionID == "" || len(r.SessionID) > maxSessionField {
		return errors.New("session result names the resumable session")
	}
	return nil
}

func (d *Dispatch) validateSession() error {
	if d.Session == nil {
		return fmt.Errorf("%s dispatch carries the session payload", d.Op)
	}
	if d.Chat != nil || d.Agent != nil || d.Process != nil || d.Workspace != nil || d.Confirmation != nil {
		return fmt.Errorf("%s dispatch carries only the session payload", d.Op)
	}
	return d.Session.Validate()
}
