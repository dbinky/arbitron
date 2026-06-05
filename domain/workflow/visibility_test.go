package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var defaultPublisher = uuid.MustParse("00000000-0000-0000-0000-0000000000aa")

func TestVisibility_Constants(t *testing.T) {
	assert.Equal(t, workflow.Visibility("private"), workflow.VisibilityPrivate)
	assert.Equal(t, workflow.Visibility("department"), workflow.VisibilityDepartment)
	assert.Equal(t, workflow.Visibility("public"), workflow.VisibilityPublic)
}

func TestVisibility_Valid(t *testing.T) {
	assert.True(t, workflow.VisibilityDepartment.Valid())
	assert.True(t, workflow.VisibilityPublic.Valid())
	// private is a "no row" sentinel, not a stored publication level.
	assert.False(t, workflow.VisibilityPrivate.Valid())
	assert.False(t, workflow.Visibility("bogus").Valid())
}

func TestVisibility_Valid_EmptyString(t *testing.T) {
	assert.False(t, workflow.Visibility("").Valid(), "empty string is not a storable visibility level")
}

func TestPublishedStore_PortShape(t *testing.T) {
	// Compile-time guarantee that fakePublishers satisfies the widened port.
	var _ workflow.PublishedStore = newFakePublishers()

	p := newFakePublishers()
	require.NoError(t, p.Upsert(nil, "wf", defaultPublisher, "src", workflow.VisibilityPublic, ""))
	pub, ok, err := p.PublicationOf(nil, "wf")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, workflow.VisibilityPublic, pub.Visibility)
}

// TestPublicationOf_AllFields verifies that ALL Publication fields round-trip
// through Upsert/PublicationOf: not just Visibility but also Publisher UUID,
// SourceName, and Department. A partial field mapping (e.g. missing SourceName)
// would only be caught at SetVisibility re-upsert time, not at the port level.
func TestPublicationOf_AllFields(t *testing.T) {
	p := newFakePublishers()
	otherPublisher := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")

	require.NoError(t, p.Upsert(nil, "dept-wf", otherPublisher, "private-src", workflow.VisibilityDepartment, "engineering"))
	pub, ok, err := p.PublicationOf(nil, "dept-wf")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, otherPublisher, pub.Publisher, "Publisher must round-trip")
	assert.Equal(t, "private-src", pub.SourceName, "SourceName must round-trip (needed for SetVisibility re-upsert)")
	assert.Equal(t, workflow.VisibilityDepartment, pub.Visibility)
	assert.Equal(t, "engineering", pub.Department, "Department must round-trip")
}

// TestPublicationOf_AbsentName verifies the port contract: an absent name
// returns (zero, false, nil) — not an error and not a zero Publication with ok=true.
func TestPublicationOf_AbsentName(t *testing.T) {
	p := newFakePublishers()
	pub, ok, err := p.PublicationOf(nil, "ghost")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, workflow.Publication{}, pub, "absent name must return zero Publication")
}
