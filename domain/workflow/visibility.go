package workflow

import "github.com/google/uuid"

// Visibility is a published workflow's sharing level. Note that VisibilityPrivate
// is never persisted — a workflow with no publication record is private to its
// owner. Only VisibilityDepartment and VisibilityPublic appear as stored rows.
type Visibility string

const (
	VisibilityPrivate    Visibility = "private"
	VisibilityDepartment Visibility = "department"
	VisibilityPublic     Visibility = "public"
)

// Valid reports whether v is a storable publication level (department or
// public). private is excluded: it is the absence of a publication, not a row.
func (v Visibility) Valid() bool {
	return v == VisibilityDepartment || v == VisibilityPublic
}

// Publication is the domain view of a shared workflow's publication record: who
// published it, the private source it came from, and at what visibility level.
// Department is empty for public. SourceName is carried so SetVisibility (Task 4)
// can re-Upsert the record (source_name is NOT NULL) without re-reading it.
type Publication struct {
	Publisher  uuid.UUID
	SourceName string
	Visibility Visibility
	Department string // "" for public
}
