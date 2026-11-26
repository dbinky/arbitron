package runneragent

// OpSessionImport is Dispatch.Op on a session_import frame (a move target).
// Session export has no op: it runs inside the originating stream (contracts §24).
const OpSessionImport = "session_import"

// SessionBundleMeta describes bundle bytes moved over the transfer endpoint.
type SessionBundleMeta struct {
	Backend, SessionID, HarnessVersion, OriginWorkDir, Checksum string
	Size                                                        int64
}

// SessionJob imports the described bundle at WorkDir.
type SessionJob struct {
	Backend, SessionID, WorkDir, TransferToken string
	Bundle                                     *SessionBundleMeta
}

// SessionResult is a session_import result.
type SessionResult struct {
	SessionID string
}

// ResourceJob mirrors runner.ResourceDescriptor (P4 T11): the separately
// owned resource the job works in. Pull/push use ID, never the parent run.
type ResourceJob struct {
	ID, Kind, WorkDir         string
	FenceKey, FenceOwner      string
	FenceGeneration           int64
	FenceToken, CheckpointRef string
}
