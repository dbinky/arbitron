package runnerproto

import "errors"

const (
	KindPullWorkspace  = "pull_workspace"
	KindPullReceipt    = "pull_receipt"
	KindPullReceiptAck = "pull_receipt_ack"
	maxPullReceipts    = 256
)

// PullWorkspace asks the runner to fetch and verify one committed checkpoint
// with no workflow step (drain). Field names match WireWorkspace's scope fields.
type PullWorkspace struct {
	OperationID       string `json:"operationId"`
	RunID             string `json:"runId"`
	WorkDir           string `json:"workDir"`
	HomeGeneration    int64  `json:"homeGeneration"`
	CheckpointVersion int64  `json:"checkpointVersion"`
	ManifestHash      string `json:"manifestHash"`
	BaseRef           string `json:"baseRef"`
	FenceKey          string `json:"fenceKey"`
	FenceOwner        string `json:"fenceOwner"`
	FenceGeneration   int64  `json:"fenceGeneration"`
	FenceToken        string `json:"fenceToken"`
	PullToken         string `json:"pullToken"`
	PartBytes         int64  `json:"partBytes"`
}

func (p *PullWorkspace) Validate() error {
	switch {
	case p.OperationID == "" || p.RunID == "" || p.WorkDir == "" || p.BaseRef == "":
		return errors.New("pull_workspace needs operationId, runId, workDir and baseRef")
	case p.FenceKey == "" || p.FenceOwner == "" || p.FenceGeneration < 1 || p.FenceToken == "":
		return errors.New("pull_workspace needs the exact resource claim")
	case p.PullToken == "" || p.HomeGeneration < 1 || p.CheckpointVersion < 1 || p.PartBytes < 1:
		return errors.New("pull_workspace needs pullToken, homeGeneration, checkpointVersion and partBytes")
	}
	return nil
}

// WirePullReceipt is the runner's terminal answer; it is replayed in hello until acknowledged.
type WirePullReceipt struct {
	OperationID     string `json:"operationId"`
	RunID           string `json:"runId"`
	BaseRef         string `json:"baseRef"`
	FenceKey        string `json:"fenceKey"`
	FenceOwner      string `json:"fenceOwner"`
	FenceGeneration int64  `json:"fenceGeneration"`
	FenceToken      string `json:"fenceToken"`
	OK              bool   `json:"ok"`
	Error           string `json:"error,omitempty"`
}

func (r *WirePullReceipt) Validate() error {
	switch {
	case r.OperationID == "" || r.RunID == "" || r.BaseRef == "":
		return errors.New("pull_receipt needs operationId, runId and baseRef")
	case r.FenceKey == "" || r.FenceOwner == "" || r.FenceGeneration < 1 || r.FenceToken == "":
		return errors.New("pull_receipt needs the exact resource claim")
	case !r.OK && r.Error == "":
		return errors.New("a failed pull_receipt names its error")
	}
	return nil
}

type PullReceiptAck struct {
	OperationID string `json:"operationId"`
}

func (a *PullReceiptAck) Validate() error {
	if a.OperationID == "" {
		return errors.New("pull_receipt_ack needs operationId")
	}
	return nil
}
