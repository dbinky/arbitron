package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// ErrStepPersistence marks a step whose durable record could not be written.
// The run halts as failed: a step that is not durably recorded is never done,
// and it is never retried, skipped or routed (RR-10).
var ErrStepPersistence = errors.New("engine: step persistence failed")

// StepCommitMode distinguishes a node that finished (advance) from a node that
// consumed validated suboperation receipts and is now waiting (suspend).
type StepCommitMode string

const (
	StepCommitAdvance StepCommitMode = "advance"
	StepCommitSuspend StepCommitMode = "suspend"
)

// StepSuspension names the durable wait a suspend commit records.
type StepSuspension struct {
	Kind        string
	ChildLinkID *uuid.UUID
	State       map[string]any
}

// StepCommit is the single durable fact that a node finished (or suspended):
// its execution record, variables, continuation and every exact receipt.
type StepCommit struct {
	RunID, InvocationID uuid.UUID
	NodeID              string
	Execution           *instance.NodeExecution
	Variables           map[string]any
	NextNodeID          string // "" = the node was the run's last step
	Completions         []StepCompletion
	Mode                StepCommitMode
	Suspension          *StepSuspension
}

// StepCommitter persists StepCommits atomically and answers whether an
// invocation already committed (restart replay).
type StepCommitter interface {
	CommitStep(ctx context.Context, c StepCommit) error
	CommittedResult(ctx context.Context, invocationID uuid.UUID) (*StepCommit, bool, error)
}

// Validate checks the commit's shape before any storage is touched. Advance
// and suspend are distinguished explicitly.
func (c StepCommit) Validate() error {
	if c.RunID == uuid.Nil || c.InvocationID == uuid.Nil || c.NodeID == "" {
		return fmt.Errorf("%w: a step commit needs a run, an invocation and a node", ErrInvalidStepCompletion)
	}
	seen := make(map[uuid.UUID]bool, len(c.Completions))
	for _, comp := range c.Completions {
		if err := comp.Validate(); err != nil {
			return err
		}
		if seen[comp.JobFence.JobID] {
			return fmt.Errorf("%w: job %s appears twice in one commit", ErrInvalidStepCompletion, comp.JobFence.JobID)
		}
		seen[comp.JobFence.JobID] = true
		if w := comp.Workspace; w != nil {
			if run, ok := resourceRun(w.Resource.Key); !ok || run != c.RunID {
				return fmt.Errorf("%w: job %s workspace receipt names resource %q, not run %s",
					ErrInvalidStepCompletion, comp.JobFence.JobID, w.Resource.Key, c.RunID)
			}
		}
	}
	switch c.Mode {
	case StepCommitAdvance:
		x := c.Execution
		if c.Suspension != nil || x == nil || x.ID == uuid.Nil || x.InstanceID != c.RunID || x.NodeID != c.NodeID || x.Status != instance.NodeStatusSucceeded {
			return fmt.Errorf("%w: an advance commit of node %q needs that node's own succeeded execution in run %s and no suspension",
				ErrInvalidStepCompletion, c.NodeID, c.RunID)
		}
	case StepCommitSuspend:
		if c.Execution != nil || c.NextNodeID != "" || c.Suspension == nil || c.Suspension.Kind == "" || len(c.Completions) == 0 {
			return fmt.Errorf("%w: a suspend commit of node %q carries receipts and suspension state, never an execution or a next node",
				ErrInvalidStepCompletion, c.NodeID)
		}
	default:
		return fmt.Errorf("%w: unknown step commit mode %q", ErrInvalidStepCompletion, c.Mode)
	}
	return nil
}

// SetStepCommitter installs the durable step committer. nil restores the
// legacy per-write persistence path (whose errors also fail the step).
func (e *Engine) SetStepCommitter(c StepCommitter) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stepCommitter = c
}

func (e *Engine) committer() StepCommitter {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stepCommitter
}

// StepCompletionReleaser releases the receipts of an attempt that ends without
// a successful CommitStep: the dispatcher discards only that attempt's
// candidate, rolls back/releases its exact claim and fails its job (P3 T6).
type StepCompletionReleaser interface {
	ReleaseCompletions(ctx context.Context, completions []StepCompletion, cause error) error
}

// SetStepCompletionReleaser installs the releaser. The production root must
// install one whenever it installs a committer (P3 T13 startup check).
func (e *Engine) SetStepCompletionReleaser(r StepCompletionReleaser) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.completionReleaser = r
}

func (e *Engine) releaser() StepCompletionReleaser {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.completionReleaser
}

// releaseAttempt hands an uncommitted attempt's receipts back with a detached
// bounded context. A release failure is ErrStepPersistence: it never triggers
// another provider retry.
func (e *Engine) releaseAttempt(ctx context.Context, nodeID string, completions []StepCompletion, cause error) error {
	if len(completions) == 0 {
		return nil
	}
	r := e.releaser()
	if r == nil {
		if e.committer() != nil {
			return &stepPersistenceError{fmt.Errorf("%w: releasing node %q requires a completion releaser", ErrStepPersistence, nodeID)}
		}
		return nil
	}
	pctx, cancel := persistenceContext(ctx)
	defer cancel()
	if err := r.ReleaseCompletions(pctx, completions, cause); err != nil {
		return &stepPersistenceError{fmt.Errorf("%w: releasing node %q attempt receipts: %w", ErrStepPersistence, nodeID, err)}
	}
	return nil
}

// releaseAfter releases completions after failure and returns failure joined
// with any release error.
func (e *Engine) releaseAfter(ctx context.Context, nodeID string, completions []StepCompletion, failure error) error {
	if rerr := e.releaseAttempt(ctx, nodeID, completions, failure); rerr != nil {
		return errors.Join(failure, rerr)
	}
	return failure
}

// stepCommitTimeout bounds a detached commit: a cancel landing after the node
// finished must not lose its result, but a wedged store must not hang forever.
const stepCommitTimeout = 30 * time.Second

func persistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), stepCommitTimeout)
}

// requireReceipts fails closed when a dispatched job completed during the
// attempt but no receipt names it. Placement history is never the authority
// for what to commit; it only detects a missing receipt.
func requireReceipts(exec *instance.NodeExecution, completions []StepCompletion) error {
	if exec == nil {
		return nil
	}
	have := make(map[string]bool, len(completions))
	for _, c := range completions {
		have[c.JobFence.JobID.String()] = true
	}
	for _, p := range exec.PlacementHistory {
		if p.Outcome == "completed" && !have[p.JobID] {
			return fmt.Errorf("%w: dispatched job %s completed without a completion receipt", ErrInvalidStepCompletion, p.JobID)
		}
	}
	return nil
}

// commitNodeSuccess applies a validated, non-wait node result through the
// committer: output into vars, continuation, then one atomic CommitStep with
// the attempt's exact receipts. Any error is an ErrStepPersistence failure.
func (e *Engine) commitNodeSuccess(ctx context.Context, committer StepCommitter, instanceID uuid.UUID, node workflow.Node,
	result *NodeResult, exec *instance.NodeExecution, invocationID uuid.UUID, completions []StepCompletion,
	vars *variable.VariableStore, adj map[string][]edgeTarget) (string, error) {
	if err := requireReceipts(exec, completions); err != nil {
		return "", e.releaseAfter(ctx, node.ID, completions, &stepPersistenceError{fmt.Errorf("%w: node %q: %w", ErrStepPersistence, node.ID, err)})
	}
	if result != nil && result.Output != nil {
		vars.SetNodeOutput(node.ID, result.Output)
	}
	port := ""
	if result != nil {
		port = result.OutputPort
	}
	next := e.findNextNode(node.ID, port, adj)
	pctx, cancel := persistenceContext(ctx)
	defer cancel()
	if err := committer.CommitStep(pctx, StepCommit{
		Mode: StepCommitAdvance, RunID: instanceID, InvocationID: invocationID, NodeID: node.ID,
		Execution: exec, Variables: vars.Snapshot(), NextNodeID: next, Completions: completions,
	}); err != nil {
		return "", e.releaseAfter(ctx, node.ID, completions, &stepPersistenceError{fmt.Errorf("%w: committing node %q: %w", ErrStepPersistence, node.ID, err)})
	}
	e.eventBus.Publish(instanceID, Event{
		Type: EventTypeNodeCompleted, Timestamp: time.Now(),
		Data: map[string]any{"nodeId": node.ID, "nodeType": string(node.Type)},
	})
	return next, nil
}

// commitSuspension consumes a waiting node's suboperation receipts without
// closing its invocation or advancing the current node.
func (e *Engine) commitSuspension(ctx context.Context, committer StepCommitter, instanceID uuid.UUID, node workflow.Node,
	invocationID uuid.UUID, result *NodeResult, completions []StepCompletion) error {
	state := map[string]any{}
	if out, ok := result.Output.(map[string]any); ok {
		for k, v := range out {
			state[k] = v
		}
	}
	s := &StepSuspension{Kind: string(node.Type), State: state}
	if node.Type == workflow.NodeTypeSpawnWorkflow {
		raw, _ := state["childInstanceId"].(string)
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			return e.releaseAfter(ctx, node.ID, completions, &stepPersistenceError{fmt.Errorf("%w: %w: awaited spawn %q suspended without a durable child link", ErrStepPersistence, ErrInvalidStepCompletion, node.ID)})
		}
		s.ChildLinkID = &id
	}
	pctx, cancel := persistenceContext(ctx)
	defer cancel()
	if err := committer.CommitStep(pctx, StepCommit{Mode: StepCommitSuspend, RunID: instanceID, InvocationID: invocationID, NodeID: node.ID, Completions: completions, Suspension: s}); err != nil {
		return e.releaseAfter(ctx, node.ID, completions, &stepPersistenceError{fmt.Errorf("%w: suspending node %q: %w", ErrStepPersistence, node.ID, err)})
	}
	return nil
}

// publishStepPersistenceFailure makes a persistence failure visible.
func (e *Engine) publishStepPersistenceFailure(instanceID uuid.UUID, node workflow.Node, err error) {
	e.eventBus.Publish(instanceID, Event{
		Type: EventTypeNodeFailed, Timestamp: time.Now(),
		Data: map[string]any{"nodeId": node.ID, "nodeType": string(node.Type), "error": err.Error()},
	})
}

// committedReplay returns the committed ADVANCE result of the node's open
// invocation, or nil. Begin returns the same open invocation executeWithRetry
// reuses, so asking never creates a second visit. A suspension record is not a
// completion: the node re-enters (an awaited spawn short-circuits on its link).
func (e *Engine) committedReplay(ctx context.Context, committer StepCommitter, instanceID uuid.UUID, nodeID string) (*StepCommit, error) {
	inv, err := e.invocationStore().Begin(ctx, instanceID, nodeID)
	if err != nil {
		return nil, &stepPersistenceError{fmt.Errorf("%w: open invocation of node %q: %w", ErrStepPersistence, nodeID, err)}
	}
	c, ok, err := committer.CommittedResult(ctx, inv.ID)
	if err != nil {
		return nil, &stepPersistenceError{fmt.Errorf("%w: committed result of node %q: %w", ErrStepPersistence, nodeID, err)}
	}
	if !ok || c == nil || c.Mode == StepCommitSuspend {
		return nil, nil
	}
	if c.InvocationID == uuid.Nil {
		c.InvocationID = inv.ID
	}
	return c, nil
}
