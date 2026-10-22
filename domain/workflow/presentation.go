package workflow

import (
	"time"

	"github.com/google/uuid"
)

// SaveReason identifies the authoring operation that issued a revision.
type SaveReason string

const (
	SaveReasonCreate    SaveReason = "create"
	SaveReasonUpdate    SaveReason = "update"
	SaveReasonPublish   SaveReason = "publish"
	SaveReasonCopy      SaveReason = "copy"
	SaveReasonDuplicate SaveReason = "duplicate"
	SaveReasonRename    SaveReason = "rename"
	SaveReasonSeed      SaveReason = "seed"
)

// RetainedSource identifies the fallback's provenance while review is pending.
type RetainedSource string

const (
	RetainedFromLayout   RetainedSource = "layout"
	RetainedFromManifest RetainedSource = "manifest"
	RetainedNone         RetainedSource = "none"
)

// RetainedLayout holds surviving node positions and, for layout results, their
// immutable source reference. Positions are top-left canvas coordinates.
type RetainedLayout struct {
	Source         RetainedSource          `json:"source"`
	FromRevision   string                  `json:"fromRevision,omitempty"`
	FromGeneration int64                   `json:"fromGeneration,omitempty"`
	Positions      map[string]NodePosition `json:"positions,omitempty"`
}

// Presentation captures the server-issued revision and destination at save time.
// Ownership relocation does not rewrite this immutable snapshot.
type Presentation struct {
	Revision string         `json:"revision"`
	SavedAt  time.Time      `json:"savedAt"`
	Reason   SaveReason     `json:"reason"`
	Scope    string         `json:"scope"`
	OwnerID  uuid.UUID      `json:"ownerId,omitempty"`
	Name     string         `json:"name"`
	Retained RetainedLayout `json:"retained"`
}

// ScopeFromPresentation returns the manifest's saved destination scope.
func ScopeFromPresentation(p *Presentation) Scope {
	if p == nil {
		return Private(uuid.Nil)
	}
	if p.Scope == ScopeLabelShared {
		return Shared()
	}
	return Private(p.OwnerID)
}

// ScopeLabel is the serialized scope kind.
func ScopeLabel(s Scope) string {
	if s.Kind == ScopeShared {
		return ScopeLabelShared
	}
	return ScopeLabelPrivate
}

// SavedRevision is empty for legacy manifests and absent workflows.
func (w *Workflow) SavedRevision() string {
	if w == nil || w.Presentation == nil {
		return ""
	}
	return w.Presentation.Revision
}
