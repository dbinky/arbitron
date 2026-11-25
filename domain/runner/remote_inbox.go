package runner

import "context"

// InboxReceipt is the durable receipt for one job generation.
type InboxReceipt struct {
	Fence         Fence
	LastOutputSeq uint64
	Result        *RemoteResult
}

// RemoteInbox journals remote output before it can be acknowledged.
type RemoteInbox interface {
	AcceptOutput(context.Context, Fence, OutputFrame) (uint64, error)
	AcceptResult(context.Context, Fence, RemoteResult) error
	Receipt(context.Context, Fence) (InboxReceipt, bool, error)
	Outputs(context.Context, Fence, uint64) ([]OutputFrame, error)
}

// JobAbsenceRecorder is an optional RemoteInbox capability (decision D6).
// RecordAbsent durably records that the runner owning f reconnected and its
// hello did not list the job. It succeeds only while f is the job's live
// (offered/running) fence; otherwise it returns ErrFenced. A repeated report
// for the same generation is a no-op.
type JobAbsenceRecorder interface {
	RecordAbsent(ctx context.Context, f Fence) error
}
