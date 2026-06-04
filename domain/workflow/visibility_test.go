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
