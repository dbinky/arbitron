package runneragent

import (
	"context"
	"sort"
)

// MaintenancePuller fetches and verifies a committed workspace copy (the P3 T9
// *workspace.Syncer satisfies it).
type MaintenancePuller interface {
	Pull(ctx context.Context, ws WorkspaceJob) (string, error)
}

func (a *Agent) runContext(ctx context.Context) context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runCtx != nil {
		return a.runCtx
	}
	return ctx
}

// PendingPullReceipts lists the receipts not yet acknowledged, oldest operation first.
func (a *Agent) PendingPullReceipts() []PullReceipt {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]PullReceipt, 0, len(a.pullReceipts))
	for _, r := range a.pullReceipts {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OperationID < out[j].OperationID })
	return out
}

// onPullWorkspace runs one maintenance pull off the receive loop and answers
// with a receipt; the receipt is kept until the server acknowledges it.
func (a *Agent) onPullWorkspace(ctx context.Context, s Session, p PullWorkspace) {
	go func() {
		rc := PullReceipt{OperationID: p.OperationID, RunID: p.Job.RunID, BaseRef: p.Job.BaseRef, FenceKey: p.Job.FenceKey,
			FenceOwner: p.Job.FenceOwner, FenceGeneration: p.Job.FenceGeneration, FenceToken: p.Job.FenceToken}
		switch {
		case a.deps.Pulls == nil:
			rc.Error = "workspace transfers are not configured on this runner"
		default:
			if _, err := a.deps.Pulls.Pull(a.runContext(ctx), p.Job); err != nil {
				rc.Error = err.Error()
			} else {
				rc.OK = true
			}
		}
		a.mu.Lock()
		a.pullReceipts[rc.OperationID] = rc
		a.mu.Unlock()
		_ = s.Send(ctx, Outbound{Kind: KindPullReceipt, PullReceipt: &rc}) // a lost send is replayed in the next hello
	}()
}

func (a *Agent) ackPullReceipt(opID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pullReceipts, opID)
}
