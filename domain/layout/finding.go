package layout

// Severity says whether a finding prevents a pass.
type Severity string

const (
	// SeverityBlocking findings must be resolved before a candidate passes.
	SeverityBlocking Severity = "blocking"
	// SeverityAdvisory findings are recorded but do not block a pass.
	SeverityAdvisory Severity = "advisory"
)

// Criterion names the spec acceptance criterion a finding concerns.
type Criterion string

const (
	CriterionCoverage            Criterion = "coverage"
	CriterionNodeOverlap         Criterion = "node_overlap"
	CriterionEdgeThroughNode     Criterion = "edge_through_node"
	CriterionTextCollision       Criterion = "text_collision"
	CriterionTextClipped         Criterion = "text_clipped"
	CriterionTextUnreadable      Criterion = "text_unreadable"
	CriterionMergedLines         Criterion = "merged_lines"
	CriterionAmbiguousAttachment Criterion = "ambiguous_attachment"
	CriterionArrowDirection      Criterion = "arrow_direction"
	CriterionAmbiguousCrossing   Criterion = "ambiguous_crossing"
	CriterionUpwardProgression   Criterion = "upward_progression"
	CriterionIndistinctLoop      Criterion = "indistinct_loop_return"
	CriterionIndistinctBranch    Criterion = "indistinct_branch"
	CriterionRenderMismatch      Criterion = "render_mismatch"
	// CriterionCrossings is advisory only: remaining, visibly treated crossings.
	CriterionCrossings Criterion = "crossings"
)

// FindingSource says which stage produced a finding.
type FindingSource string

const (
	SourcePreflight FindingSource = "preflight"
	SourceReview    FindingSource = "review"
)

// Finding is one readability issue of one candidate.
type Finding struct {
	Criterion    Criterion     `json:"criterion"`
	Severity     Severity      `json:"severity"`
	Source       FindingSource `json:"source"`
	NodeIDs      []string      `json:"nodeIds,omitempty"`
	EdgeOrdinals []int         `json:"edgeOrdinals,omitempty"`
	Evidence     string        `json:"evidence"`
}

// BlockingCount counts blocking findings.
func BlockingCount(fs []Finding) int {
	n := 0
	for _, f := range fs {
		if f.Severity == SeverityBlocking {
			n++
		}
	}
	return n
}

// Score ranks candidates: fewer blocking findings first, then fewer advisory
// findings plus crossings, then earlier attempt. Compactness is not a factor.
type Score struct {
	Blocking  int `json:"blocking"`
	Advisory  int `json:"advisory"`
	Crossings int `json:"crossings"`
	Attempt   int `json:"attempt"`
}

// ScoreOf scores a candidate from all of its findings (preflight + review) and
// its crossing count. The advisory crossings finding is represented by the
// crossing count and not counted again.
func ScoreOf(attempt int, findings []Finding, crossings int) Score {
	s := Score{Attempt: attempt, Crossings: crossings}
	for _, f := range findings {
		switch {
		case f.Severity == SeverityBlocking:
			s.Blocking++
		case f.Criterion != CriterionCrossings:
			s.Advisory++
		}
	}
	return s
}

// Less reports whether s ranks strictly better than o.
func (s Score) Less(o Score) bool {
	if s.Blocking != o.Blocking {
		return s.Blocking < o.Blocking
	}
	if sa, oa := s.Advisory+s.Crossings, o.Advisory+o.Crossings; sa != oa {
		return sa < oa
	}
	return s.Attempt < o.Attempt
}
