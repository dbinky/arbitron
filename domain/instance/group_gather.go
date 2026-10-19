package instance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
)

// Gather modes mirror the workflow vocabulary without importing domain/workflow.
const (
	GatherModeResults         = "results"
	GatherModeResultsAndFiles = "results_and_files"
)

const (
	GatherStatePending       = "pending"
	GatherStateMerging       = "merging"
	GatherStateResolving     = "resolving"
	GatherStateApplied       = "applied"
	GatherStateFailed        = "failed"
	GatherStateRolledBack    = "rolled_back"
	GatherStateNotApplicable = "not_applicable"
)

const (
	RollbackStatusRolledBack = "rolled_back"
	RollbackStatusFailed     = "rollback_failed"
)

// An empty outcome denotes an open attempt or one awaiting conflict verification.
const (
	ResolutionOutcomeResolved        = "resolved"
	ResolutionOutcomeStillConflicted = "still_conflicted"
	ResolutionOutcomeAbandoned       = "abandoned"
	ResolutionOutcomeCancelled       = "cancelled"
	ResolutionOutcomeError           = "error"
)

// ErrInvalidGatherReport identifies malformed gather state.
var ErrInvalidGatherReport = errors.New("invalid gather report")

// ErrGatherReportFrozen prevents rewriting the report after continuation is prepared.
var ErrGatherReportFrozen = errors.New("gather report is frozen: the group run is done")

// ErrGatherReportStale rejects a conditional report write whose expected
// report is no longer the stored one: another writer got there first.
var ErrGatherReportStale = errors.New("gather report changed concurrently")

// SameGatherReport reports whether a and b store identically: both nil, or
// both non-nil with equal normalized JSON (the stored representation).
func SameGatherReport(a, b *GatherReport) (bool, error) {
	if a == nil || b == nil {
		return a == nil && b == nil, nil
	}
	ra, err := json.Marshal(a.Normalized())
	if err != nil {
		return false, fmt.Errorf("marshalling gather report: %w", err)
	}
	rb, err := json.Marshal(b.Normalized())
	if err != nil {
		return false, fmt.Errorf("marshalling gather report: %w", err)
	}
	return bytes.Equal(ra, rb), nil
}

// GatherReport is the durable crash-recovery state and final merge/resolution report.
type GatherReport struct {
	Mode  string `json:"mode"`
	State string `json:"state"`
	// Handle is the working-area gather handle encoded as a JSON object.
	Handle         map[string]any        `json:"handle,omitempty"`
	MergeIndex     int                   `json:"mergeIndex"`
	Tasks          []GatherTaskReport    `json:"tasks"`
	Attempts       []ResolutionAttempt   `json:"attempts"`
	ActiveSession  *ResolutionSessionRef `json:"activeSession,omitempty"`
	RollbackStatus string                `json:"rollbackStatus,omitempty"`
	Error          string                `json:"error,omitempty"`
}

// GatherTaskReport describes one task's merge outcome, in author order.
type GatherTaskReport struct {
	Key           string   `json:"key"`
	Merged        bool     `json:"merged"`
	Reason        string   `json:"reason"`
	ConflictPaths []string `json:"conflictPaths,omitempty"`
}

// ResolutionAttempt records an interactive conflict-resolution attempt.
type ResolutionAttempt struct {
	MergeIndex    int        `json:"mergeIndex"`
	Attempt       int        `json:"attempt"`
	TaskKey       string     `json:"taskKey"`
	ConflictPaths []string   `json:"conflictPaths"`
	Outcome       string     `json:"outcome"`
	StartedAt     time.Time  `json:"startedAt"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
}

// ResolutionSessionRef identifies the active resolution session.
type ResolutionSessionRef struct {
	GroupRunID uuid.UUID `json:"groupRunId"`
	MergeIndex int       `json:"mergeIndex"`
	Attempt    int       `json:"attempt"`
	InstanceID uuid.UUID `json:"instanceId"`
	NodeID     string    `json:"nodeId"`
}

// Validate returns every vocabulary/invariant violation in one wrapped error.
func (r GatherReport) Validate() error {
	var problems []string
	switch r.Mode {
	case GatherModeResults, GatherModeResultsAndFiles:
	default:
		problems = append(problems, fmt.Sprintf("mode %q is not results or results_and_files", r.Mode))
	}
	switch r.State {
	case GatherStatePending, GatherStateMerging, GatherStateResolving, GatherStateApplied, GatherStateFailed, GatherStateRolledBack, GatherStateNotApplicable:
	default:
		problems = append(problems, fmt.Sprintf("state %q is unknown", r.State))
	}
	if r.MergeIndex < 0 {
		problems = append(problems, "mergeIndex is negative")
	}
	switch r.RollbackStatus {
	case "", RollbackStatusRolledBack, RollbackStatusFailed:
	default:
		problems = append(problems, fmt.Sprintf("rollbackStatus %q is unknown", r.RollbackStatus))
	}
	seen := map[string]bool{}
	for i, task := range r.Tasks {
		if task.Key == "" {
			problems = append(problems, fmt.Sprintf("tasks[%d] has no key", i))
		} else if seen[task.Key] {
			problems = append(problems, fmt.Sprintf("task %q appears twice", task.Key))
		}
		seen[task.Key] = true
	}
	for i, a := range r.Attempts {
		if a.TaskKey == "" {
			problems = append(problems, fmt.Sprintf("attempts[%d] has no taskKey", i))
		}
		if a.Attempt < 1 {
			problems = append(problems, fmt.Sprintf("attempts[%d].attempt must be >= 1", i))
		}
		if a.MergeIndex < 0 {
			problems = append(problems, fmt.Sprintf("attempts[%d].mergeIndex is negative", i))
		}
		switch a.Outcome {
		case "", ResolutionOutcomeResolved, ResolutionOutcomeStillConflicted, ResolutionOutcomeAbandoned, ResolutionOutcomeCancelled, ResolutionOutcomeError:
		default:
			problems = append(problems, fmt.Sprintf("attempts[%d].outcome %q is unknown", i, a.Outcome))
		}
	}
	if s := r.ActiveSession; s != nil {
		if s.GroupRunID == uuid.Nil || s.InstanceID == uuid.Nil || s.NodeID == "" || s.Attempt < 1 || s.MergeIndex < 0 {
			problems = append(problems, "activeSession needs groupRunId, instanceId, nodeId, attempt >= 1 and mergeIndex >= 0")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidGatherReport, strings.Join(problems, "; "))
	}
	return nil
}

// Normalized returns a deep copy with never-null task and attempt lists.
func (r GatherReport) Normalized() GatherReport {
	out := r.Clone()
	if out.Tasks == nil {
		out.Tasks = []GatherTaskReport{}
	}
	if out.Attempts == nil {
		out.Attempts = []ResolutionAttempt{}
	}
	for i := range out.Attempts {
		if out.Attempts[i].ConflictPaths == nil {
			out.Attempts[i].ConflictPaths = []string{}
		}
	}
	return out
}

// Clone returns a deep copy sharing no maps, slices or pointers with r.
func (r GatherReport) Clone() GatherReport {
	out := r
	out.Handle = variable.DeepCopyMap(r.Handle)
	if r.Tasks != nil {
		out.Tasks = make([]GatherTaskReport, len(r.Tasks))
		for i, task := range r.Tasks {
			task.ConflictPaths = cloneStrings(task.ConflictPaths)
			out.Tasks[i] = task
		}
	}
	if r.Attempts != nil {
		out.Attempts = make([]ResolutionAttempt, len(r.Attempts))
		for i, a := range r.Attempts {
			a.ConflictPaths = cloneStrings(a.ConflictPaths)
			if a.EndedAt != nil {
				ended := *a.EndedAt
				a.EndedAt = &ended
			}
			out.Attempts[i] = a
		}
	}
	if r.ActiveSession != nil {
		s := *r.ActiveSession
		out.ActiveSession = &s
	}
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
