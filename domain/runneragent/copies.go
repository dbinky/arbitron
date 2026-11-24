package runneragent

import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// CopyState records the committed base of a local workspace. Dirty remains set
// for an interrupted attempt until the committed base is restored.
type CopyState struct {
	RunID             uuid.UUID       `json:"runId"`
	WorkDir           string          `json:"workDir"`
	HomeGeneration    int64           `json:"homeGeneration"`
	CheckpointVersion int64           `json:"checkpointVersion"`
	ManifestHash      string          `json:"manifestHash"`
	Dirty             bool            `json:"dirty"`
	Base              runner.Manifest `json:"base"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

// CopyStateStore persists copy records across runner restarts.
type CopyStateStore interface {
	Get(context.Context, uuid.UUID) (CopyState, bool, error)
	Put(context.Context, CopyState) error
	Delete(context.Context, uuid.UUID) error
	List(context.Context) ([]CopyState, error)
}

// PullTarget identifies the committed workspace required by the server.
type PullTarget struct {
	RunID                             uuid.UUID
	HomeGeneration, CheckpointVersion int64
	ManifestHash                      string
}

// ReuseLocalCopy requires the exact committed base and an undirtied copy.
func ReuseLocalCopy(s CopyState, t PullTarget) bool {
	return s.RunID == t.RunID && !s.Dirty &&
		s.HomeGeneration == t.HomeGeneration &&
		s.CheckpointVersion == t.CheckpointVersion &&
		s.ManifestHash == t.ManifestHash
}
