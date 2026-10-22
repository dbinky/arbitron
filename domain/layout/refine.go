package layout

// Refine applies each blocking criterion's targeted increments once, in policy
// order. False proves that no applicable option can change, not that review passed.
func Refine(o Options, findings []Finding) (Options, bool) {
	criteria := make(map[Criterion]bool)
	advisoryCrossings := false
	for _, f := range findings {
		if f.Severity == SeverityBlocking {
			criteria[f.Criterion] = true
		}
		if f.Criterion == CriterionCrossings && f.Severity == SeverityAdvisory {
			advisoryCrossings = true
		}
	}

	next := o
	grow := func(value *float64, delta, cap float64) {
		if *value < cap {
			*value = min(cap, *value+delta)
		}
	}
	growSearch := func() {
		const cap = 64
		switch {
		case next.CrossingSweeps <= 0:
			next.CrossingSweeps = 1
		case next.CrossingSweeps < cap:
			next.CrossingSweeps = min(cap, 2*next.CrossingSweeps)
		}
	}

	if criteria[CriterionNodeOverlap] {
		grow(&next.NodeSpacing, 24, 400)
	}
	if criteria[CriterionEdgeThroughNode] {
		grow(&next.NodeSpacing, 16, 400)
		grow(&next.RankSpacing, 16, 400)
	}
	if criteria[CriterionTextCollision] {
		grow(&next.LabelGap, 6, 60)
		grow(&next.PortSpacing, 12, 120)
		grow(&next.NodeSpacing, 16, 400)
	}
	if criteria[CriterionTextClipped] {
		grow(&next.NodeTextSlack, 8, 64)
	}
	if criteria[CriterionMergedLines] {
		grow(&next.LaneSeparation, 6, 60)
		grow(&next.PortSpacing, 8, 120)
	}
	if criteria[CriterionAmbiguousAttachment] {
		grow(&next.PortSpacing, 12, 120)
	}
	if criteria[CriterionAmbiguousCrossing] {
		grow(&next.LaneSeparation, 6, 60)
		grow(&next.PortSpacing, 8, 120)
		growSearch()
	}
	if criteria[CriterionIndistinctLoop] {
		grow(&next.GutterWidth, 24, 400)
		grow(&next.LaneSeparation, 4, 60)
	}
	if criteria[CriterionIndistinctBranch] {
		grow(&next.PortSpacing, 12, 120)
		grow(&next.LabelGap, 6, 60)
	}
	if advisoryCrossings && next != o && !criteria[CriterionAmbiguousCrossing] {
		growSearch()
	}
	return next, next != o
}
