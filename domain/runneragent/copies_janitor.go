package runneragent

import "context"

const KindDeleteCopy = "delete_copy"

// HeldCopy is a local workspace copy reported in hello.
type HeldCopy struct {
	RunID          string
	HomeGeneration int64
}

// DeleteCopy is the server's generation-bounded deletion order.
type DeleteCopy struct {
	RunID          string
	HomeGeneration int64
	Reason         string
}

// CopyJanitor must keep a copy whose generation is newer than upTo.
type CopyJanitor interface {
	HeldCopies(ctx context.Context) ([]HeldCopy, error)
	DeleteCopy(ctx context.Context, runID string, upTo int64) (deleted bool, err error)
}
