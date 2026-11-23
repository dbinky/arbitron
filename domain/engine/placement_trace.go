package engine

import "github.com/arbitron/arbitron/domain/instance"

// applyPlacement copies an attempt's placement facts onto its trace record:
// the LAST fact names where the attempt finally ran; every fact (re-routes
// after exhaustion included) is appended to PlacementHistory. No facts = no change.
func applyPlacement(exec *instance.NodeExecution, facts []PlacementFact) {
	if len(facts) == 0 {
		return
	}
	last := facts[len(facts)-1]
	runner := last.RunnerID
	exec.RunnerID = &runner
	exec.SourceID = nil
	if last.SourceID != "" {
		src := last.SourceID
		exec.SourceID = &src
	}
	if last.Reason != "" {
		reason := last.Reason
		exec.PlacementReason = &reason
	}
	hist := make([]instance.PlacementRecord, 0, len(facts))
	for _, f := range facts {
		hist = append(hist, instance.PlacementRecord{
			JobID: f.JobID.String(), RunnerID: f.RunnerID, SourceID: f.SourceID, Reason: f.Reason,
			Outcome: f.Outcome, Generation: f.Generation, At: f.At,
		})
	}
	exec.PlacementHistory = append(exec.PlacementHistory, hist...)
}
