package wsclient

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
)

func wirePullReceipt(r runneragent.PullReceipt) runnerproto.WirePullReceipt {
	return runnerproto.WirePullReceipt{OperationID: r.OperationID, RunID: r.RunID, BaseRef: r.BaseRef, FenceKey: r.FenceKey,
		FenceOwner: r.FenceOwner, FenceGeneration: r.FenceGeneration, FenceToken: r.FenceToken, OK: r.OK, Error: r.Error}
}

func wirePullReceipts(rs []runneragent.PullReceipt) []runnerproto.WirePullReceipt {
	if len(rs) == 0 {
		return nil
	}
	out := make([]runnerproto.WirePullReceipt, 0, len(rs))
	for _, r := range rs {
		out = append(out, wirePullReceipt(r))
	}
	return out
}

func pullWorkspaceFromWire(p runnerproto.PullWorkspace) *runneragent.PullWorkspace {
	return &runneragent.PullWorkspace{OperationID: p.OperationID, Job: runneragent.WorkspaceJob{RunID: p.RunID, WorkDir: p.WorkDir,
		HomeGeneration: p.HomeGeneration, CheckpointVersion: p.CheckpointVersion, ManifestHash: p.ManifestHash, BaseRef: p.BaseRef,
		FenceKey: p.FenceKey, FenceOwner: p.FenceOwner, FenceGeneration: p.FenceGeneration, FenceToken: p.FenceToken,
		PullToken: p.PullToken, PartBytes: p.PartBytes}}
}
