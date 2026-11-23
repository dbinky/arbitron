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
