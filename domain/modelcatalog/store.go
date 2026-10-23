package modelcatalog

import (
	"context"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

type CycleCommit struct {
	Report            RefreshReport
	Catalog           CatalogSnapshot
	Evidence          []UsabilityEvidence
	Advances          []provider.AdvanceRequest
	WebhookConfigured *bool
	UnmatchedTargets  []provider.FollowTarget
	ProbeCursor       map[string]string
	// DeferDiscovery names canonical models whose advancement proposals this
	// cycle dropped on a concurrent configuration change. Their first usable
	// observation (and its frozen announcement) waits for the cycle that can
	// commit those changes, so the one lifetime event never claims that no
	// profiles changed while the advancement is merely postponed.
	DeferDiscovery []string
	// PolicyRevision is the identity-policy revision the cycle's fresh
	// classifications were mapped under. CommitCycle refuses the whole cycle
	// when the persisted policy has moved since, so no stale tier reaches the
	// catalog or a frozen event. Zero only when the cycle re-commits the stored
	// catalog unchanged (a failed fetch or parse).
	PolicyRevision int64
}

// CatalogView is trusted internal state. Public adapters use ProjectCatalog.
// Historical inputs retain the absent current model's source and order metadata
// so the refresh pipeline can remap both sides under one captured policy.
type CatalogView struct {
	Catalog           CatalogSnapshot
	HistoricalModels  []modelidentity.CatalogModel
	HistoricalEntries []modelidentity.Entry
	Evidence          []UsabilityEvidence
	Reports           []RefreshReport
	UnmatchedTargets  []provider.FollowTarget
	ProbeCursor       map[string]string
}

type CatalogStore interface {
	CommitCycle(context.Context, CycleCommit) (RefreshReport, error)
	Recover(context.Context) ([]RefreshReport, error)
	Snapshot(context.Context) (CatalogView, error)
	MarkPublished(context.Context, string) error
}
