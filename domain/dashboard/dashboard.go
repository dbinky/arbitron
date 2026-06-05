// Package dashboard aggregates department-scoped operational statistics over
// the department-stamped instances produced in Phase 3. It owns the result
// structs and a StatsRepository port; the HTTP adapter stays thin.
package dashboard

import "time"

// QueueHealth is the current count of instances by lifecycle state, plus a
// derived "failures needing attention" signal. These are point-in-time counts
// (no window) for live operational monitoring.
type QueueHealth struct {
	Running                int `json:"running"`
	Queued                 int `json:"queued"`
	Waiting                int `json:"waiting"`
	Paused                 int `json:"paused"`
	FailedNeedingAttention int `json:"failedNeedingAttention"`
}

// ThroughputBucket is the number of runs created within one time bucket.
type ThroughputBucket struct {
	Bucket time.Time `json:"bucket"` // bucket start (RFC3339 when serialized)
	Runs   int       `json:"runs"`
}

// Outcomes summarises success/failure and duration over the window.
type Outcomes struct {
	TotalRuns          int     `json:"totalRuns"`
	SuccessRate        float64 `json:"successRate"` // 0.0..1.0
	FailureRate        float64 `json:"failureRate"` // 0.0..1.0
	AvgDurationSeconds float64 `json:"avgDurationSeconds"`
	P95DurationSeconds float64 `json:"p95DurationSeconds"`
}

// WorkflowBreakdown is one row of the per-workflow ranking.
type WorkflowBreakdown struct {
	WorkflowName       string  `json:"workflowName"`
	Runs               int     `json:"runs"`
	FailureRate        float64 `json:"failureRate"` // 0.0..1.0
	AvgDurationSeconds float64 `json:"avgDurationSeconds"`
}

// Stats is the full dashboard envelope returned to the caller.
type Stats struct {
	Departments []string            `json:"departments"`
	Queue       QueueHealth         `json:"queue"`
	Throughput  []ThroughputBucket  `json:"throughput"`
	Outcomes    Outcomes            `json:"outcomes"`
	PerWorkflow []WorkflowBreakdown `json:"perWorkflow"`
}
