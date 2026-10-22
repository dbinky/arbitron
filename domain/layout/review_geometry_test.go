package layout_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/stretchr/testify/require"
)

func TestReviewRequest_GroupHeaderCannotUseOrdinaryBoxes(t *testing.T) {
	geo := layouttest.Arranged(t, layouttest.SampleWorkflow())
	geo.Nodes[0].TypeCaption = "parallel group"
	require.ErrorIs(t, (layout.ReviewRequest{Geometry: geo}).ValidateGeometry(), layout.ErrInvalidGroups)
}
