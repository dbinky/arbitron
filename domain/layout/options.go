package layout

// Options is the explicit arrangement policy. Values are CSS px at zoom 1,
// except CrossingSweeps. Refinement adjusts this policy between attempts.
type Options struct {
	// RankSpacing is the clear space between channel tracks and the next node row.
	RankSpacing float64 `json:"rankSpacing"`
	// NodeSpacing is the minimum horizontal gap between items in a row.
	NodeSpacing float64 `json:"nodeSpacing"`
	// LaneSeparation separates horizontal tracks and loop-return lanes.
	LaneSeparation float64 `json:"laneSeparation"`
	// GutterWidth separates the widest row from the first loop lane.
	GutterWidth float64 `json:"gutterWidth"`
	// LabelGap separates row bottoms, branch-label bands and tracks.
	LabelGap float64 `json:"labelGap"`
	// PortSpacing is the minimum distance between attachments on one side.
	PortSpacing float64 `json:"portSpacing"`
	// NodeTextSlack adds rounding slack to node and branch text boxes.
	NodeTextSlack float64 `json:"nodeTextSlack"`
	// DummyWidth reserves a row slot for an edge passing through a rank.
	DummyWidth float64 `json:"dummyWidth"`
	// CrossingSweeps is the barycentre sweep budget for crossing reduction.
	CrossingSweeps int `json:"crossingSweeps"`
	// Margin is the empty border around the canvas.
	Margin float64 `json:"margin"`
}

// DefaultOptions returns the initial arrangement policy for attempt 1.
func DefaultOptions() Options {
	return Options{
		RankSpacing:    48,
		NodeSpacing:    56,
		LaneSeparation: 14,
		GutterWidth:    48,
		LabelGap:       10,
		PortSpacing:    24,
		NodeTextSlack:  0,
		DummyWidth:     24,
		CrossingSweeps: 8,
		Margin:         40,
	}
}
