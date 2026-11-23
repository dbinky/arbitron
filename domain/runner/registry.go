package runner

import (
	"context"
	"errors"
	"time"
)

// MaxStepLimit bounds a runner's concurrent step limit.
const MaxStepLimit = 256

// RunnerConfigPatch is an operator change to a runner. nil = unchanged;
// a non-nil Labels map replaces the labels.
type RunnerConfigPatch struct {
	StepLimit *int
	Labels    map[string]string
}

// Registry persists runners, their operator config and their sources' health.
type Registry interface {
	Snapshot(ctx context.Context) (PoolSnapshot, error) // runners+sources+slots+in-flight (+homes from P3); Telemetry filled by caller
	GetRunner(ctx context.Context, id RunnerID) (Runner, []Source, error)
	UpsertRunner(ctx context.Context, r Runner) error
	UpdateConfig(ctx context.Context, id RunnerID, p RunnerConfigPatch) (Runner, error)
	SetCordoned(ctx context.Context, id RunnerID, cordoned bool) error
	SetDraining(ctx context.Context, id RunnerID, draining bool) error
	SetLiveness(ctx context.Context, id RunnerID, l Liveness, at time.Time) error
	SetCompat(ctx context.Context, id RunnerID, c Compatibility) error
	ReplaceSources(ctx context.Context, id RunnerID, sources []Source) error // keeps existing Health for unchanged IDs
	RecordHealth(ctx context.Context, id SourceID, h SourceHealth) error
	GetSource(ctx context.Context, id SourceID) (Source, error)
	ListRunners(ctx context.Context) ([]Runner, error)
}

var (
	ErrRunnerNotFound   = errors.New("runner: runner not found")
	ErrSourceNotFound   = errors.New("runner: source not found")
	ErrInvalidStepLimit = errors.New("runner: step limit must be between 1 and 256")
)

// ValidStepLimit reports whether n is an allowed step limit.
func ValidStepLimit(n int) bool { return n >= 1 && n <= MaxStepLimit }
