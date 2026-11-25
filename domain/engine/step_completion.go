package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// ErrInvalidStepCompletion marks a receipt that cannot identify its operation.
var ErrInvalidStepCompletion = errors.New("engine: invalid step completion")

// JobFenceRef identifies one ledger job generation without importing runner.
type JobFenceRef struct {
	JobID      uuid.UUID
	RunnerID   string
	Generation int64
}

// WorkspaceCompletion identifies a candidate prepared under a resource claim.
type WorkspaceCompletion struct {
	Ref         string
	Resource    ResourceFenceRef
	NonPortable bool
	LocalRef    string // Server-local recovery candidate for a non-portable home.
}

// StepCompletion is one dispatched operation's receipt.
type StepCompletion struct {
	JobFence  JobFenceRef
	Workspace *WorkspaceCompletion
}

// Validate rejects a fence that cannot identify one job generation.
func (f JobFenceRef) Validate() error {
	if f.JobID == uuid.Nil || f.RunnerID == "" || f.Generation < 1 {
		return fmt.Errorf("%w: incomplete job fence", ErrInvalidStepCompletion)
	}
	return nil
}

// Validate checks the receipt's self-contained evidence, including RR-08's
// empty-reference rule for non-portable workspaces.
func (c StepCompletion) Validate() error {
	if err := c.JobFence.Validate(); err != nil {
		return err
	}
	w := c.Workspace
	if w == nil {
		return nil
	}
	if err := w.Resource.Validate(); err != nil {
		return fmt.Errorf("%w: workspace resource: %v", ErrInvalidStepCompletion, err)
	}
	if w.NonPortable {
		if w.Ref != "" {
			return fmt.Errorf("%w: non-portable receipt carries a catalog ref", ErrInvalidStepCompletion)
		}
		return nil
	}
	refRun, ok := checkpointRefRun(w.Ref)
	if !ok {
		return fmt.Errorf("%w: malformed checkpoint ref %q", ErrInvalidStepCompletion, w.Ref)
	}
	if keyRun, isRun := resourceRun(w.Resource.Key); isRun && keyRun != refRun {
		return fmt.Errorf("%w: checkpoint belongs to another run", ErrInvalidStepCompletion)
	}
	return nil
}

// checkpointRefRun checks checkpoint ownership; runner.ParseCheckpointRef
// performs the full catalog-reference validation without an engine import cycle.
func checkpointRefRun(ref string) (uuid.UUID, bool) {
	runPart, rest, found := strings.Cut(ref, "@")
	if !found || !strings.Contains(rest, ":") {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(runPart)
	return id, err == nil && id != uuid.Nil
}

func resourceRun(key string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(key, "run:")
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	return id, err == nil && id != uuid.Nil
}

func (c StepCompletion) clone() StepCompletion {
	if c.Workspace != nil {
		w := *c.Workspace
		c.Workspace = &w
	}
	return c
}

// StepCompletionSink collects every receipt of one workflow retry attempt.
type StepCompletionSink struct {
	mu    sync.Mutex
	byJob map[uuid.UUID]int
	list  []StepCompletion
}

// NewStepCompletionSink returns an empty sink.
func NewStepCompletionSink() *StepCompletionSink {
	return &StepCompletionSink{byJob: map[uuid.UUID]int{}}
}

// Record retains a receipt in operation order, deduplicated by job.
func (s *StepCompletionSink) Record(c StepCompletion) error {
	if w := c.Workspace; w != nil {
		switch {
		case w.NonPortable && w.LocalRef == "":
			return fmt.Errorf("%w: non-portable workspace receipt has no local checkpoint", ErrInvalidStepCompletion)
		case w.NonPortable && w.Ref != "":
			return fmt.Errorf("%w: non-portable workspace receipt carries a catalog ref", ErrInvalidStepCompletion)
		case !w.NonPortable && w.LocalRef != "":
			return fmt.Errorf("%w: portable workspace receipt carries a local checkpoint", ErrInvalidStepCompletion)
		}
	}
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.byJob[c.JobFence.JobID]; ok {
		prior := s.list[i]
		if prior.JobFence == c.JobFence && ((prior.Workspace == nil && c.Workspace == nil) ||
			(prior.Workspace != nil && c.Workspace != nil && *prior.Workspace == *c.Workspace)) {
			return nil
		}
		return fmt.Errorf("%w: conflicting receipts for job %s", ErrInvalidStepCompletion, c.JobFence.JobID)
	}
	s.byJob[c.JobFence.JobID] = len(s.list)
	s.list = append(s.list, c.clone())
	return nil
}

// Completions returns defensive copies of the receipts in record order.
func (s *StepCompletionSink) Completions() []StepCompletion {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StepCompletion, len(s.list))
	for i, c := range s.list {
		out[i] = c.clone()
	}
	return out
}

type stepCompletionSinkKey struct{}

// ContextWithStepCompletionSink attaches s to ctx.
func ContextWithStepCompletionSink(ctx context.Context, s *StepCompletionSink) context.Context {
	return context.WithValue(ctx, stepCompletionSinkKey{}, s)
}

// StepCompletionSinkFromContext returns the sink installed on ctx.
func StepCompletionSinkFromContext(ctx context.Context) (*StepCompletionSink, bool) {
	s, ok := ctx.Value(stepCompletionSinkKey{}).(*StepCompletionSink)
	return s, ok && s != nil
}
