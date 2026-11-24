package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"strings"
)

// TurnCommit is the single durable write that makes a turn successful.
type TurnCommit struct {
	Execution          *instance.NodeExecution
	Create             bool
	RequireOpenSession bool
	RequireWorkspace   bool
	CheckpointRef      string
	PinRunner          string
	SessionGeneration  int64
	LeaseKey           string
	LeaseToken         uuid.UUID
	IntentID           uuid.UUID
	RequestID          uuid.UUID
	InvocationID       uuid.UUID
	TurnIndex          int
	ResourceRunID      uuid.UUID
	AcceptedBinding    AcceptedTurnBinding
	Completions        []StepCompletion
}

// TurnCommitter writes a TurnCommit in one transaction, fenced on the lease,
// the intent, every job and resource receipt and the prior committed pair.
type TurnCommitter interface {
	CommitTurn(ctx context.Context, c TurnCommit) error
}

var (
	// ErrTurnFenced means a lease, intent, job, resource or prior-pair check
	// failed: another owner (or a move/newer turn) superseded this turn.
	ErrTurnFenced = errors.New("engine: turn commit fenced")
	// ErrMissingTurnReceipt means required provenance (intent, lease, invocation
	// or exact completion receipt) is absent; nothing is exported or committed.
	ErrMissingTurnReceipt = errors.New("engine: turn has no exact completion receipt")
	// ErrUnknownSessionBackend means the agent backend is not in the workflow vocabulary.
	ErrUnknownSessionBackend = errors.New("engine: unknown interactive session backend")
)

const (
	// ConversationKeyWorkspaceCheckpointRef records the workspace candidate that
	// pairs with the turn's session checkpoint.
	ConversationKeyWorkspaceCheckpointRef = "workspaceCheckpointRef"
	// ConversationKeyAcceptedTurnBinding stores the accepted, credential-free
	// binding so idle moves never re-resolve an edited profile.
	ConversationKeyAcceptedTurnBinding = "acceptedTurnBinding"
)

// SessionExportRequest names the session to export, the runner holding it and
// the exact stream fence (and resource) it is exported under (contracts §24).
type SessionExportRequest struct {
	RunnerID  string
	Backend   string
	SessionID string
	WorkDir   string
	JobFence  JobFenceRef
	Resource  *ResourceFenceRef
}

// SessionExporter exports a harness session from the runner that holds it,
// inside the originating stream assignment.
type SessionExporter interface {
	ExportSession(ctx context.Context, req SessionExportRequest) (SessionBundle, error)
}

// TurnWorkspaceRequest asks to validate the workspace candidate a finished turn
// already prepared under its stream receipt.
type TurnWorkspaceRequest struct {
	RunID      uuid.UUID // resource ID (run workspace, or P4 T11 resolution resource)
	RunnerID   string
	WorkDir    string
	Completion StepCompletion
}

// StagedTurnWorkspace is the validated, still-prepared candidate.
type StagedTurnWorkspace struct {
	RunID      uuid.UUID
	RunnerID   string
	Version    int64
	Ref        string
	Completion WorkspaceCompletion
}

// TurnWorkspaceCheckpointer validates and reuses the exact prepared candidate.
// It never prepares a second candidate and never publishes a pointer.
type TurnWorkspaceCheckpointer interface {
	StageTurn(ctx context.Context, req TurnWorkspaceRequest) (StagedTurnWorkspace, error)
}

// SessionCheckpointDeps enables per-turn checkpoints. All fields required.
type SessionCheckpointDeps struct {
	Store               SessionCheckpointStore
	Exporter            SessionExporter
	Committer           TurnCommitter
	Workspace           TurnWorkspaceCheckpointer
	DefaultAgentBackend string
}

func (d SessionCheckpointDeps) validate() error {
	if d.Store == nil || d.Exporter == nil || d.Committer == nil || d.Workspace == nil {
		return errors.New("session checkpoints require a store, exporter, committer and workspace checkpointer")
	}
	if strings.TrimSpace(d.DefaultAgentBackend) == "" {
		return errors.New("session checkpoints require the deployment default agent backend")
	}
	return nil
}

// sessionBackend names the harness of a turn and whether it has a native,
// portable session bundle (claude-code, codex). Connection-backed API chat is
// stateless ("", false). Unknown agent backends are an error.
func sessionBackend(b provider.ResolvedBinding, defaultAgent string) (string, bool, error) {
	switch b.Kind {
	case provider.ProfileKindAgent:
		be := strings.TrimSpace(b.Backend)
		if be == "" {
			be = strings.TrimSpace(defaultAgent)
		}
		if be == "" || !workflow.IsValidAgentBackend(be) {
			return "", false, fmt.Errorf("%w: %q", ErrUnknownSessionBackend, be)
		}
		return be, be == workflow.BackendClaudeCode || be == workflow.BackendCodex, nil
	case provider.ProfileKindChat:
		if b.ConnectionRef == "" {
			return workflow.BackendClaudeCode, true, nil
		}
	}
	return "", false, nil
}

// turnCheckpointPlan is what a turn commit needs beyond the record itself.
// Every identity field comes from the persisted intent and held lease.
type turnCheckpointPlan struct {
	binding      provider.ResolvedBinding
	accepted     AcceptedTurnBinding
	workDir      string
	turnIndex    int
	intentID     uuid.UUID
	requestID    uuid.UUID
	invocationID uuid.UUID
	kind         string // TurnIntentFirst | Message | End | Abandon
	group        bool
	create       bool
	lease        TurnLease
	resourceID   uuid.UUID        // P4 T11 resolution resource; Nil = run workspace
	completions  []StepCompletion // adopted turn's original receipts (P4 T10); nil = read the sink
}

type turnCheckpointPlanKey struct{}

func withTurnCheckpointPlan(ctx context.Context, p turnCheckpointPlan) context.Context {
	return context.WithValue(ctx, turnCheckpointPlanKey{}, p)
}

func turnCheckpointPlanFrom(ctx context.Context) (turnCheckpointPlan, bool) {
	p, ok := ctx.Value(turnCheckpointPlanKey{}).(turnCheckpointPlan)
	return p, ok
}

func (p turnCheckpointPlan) validate() error {
	switch {
	case p.intentID == uuid.Nil, p.requestID == uuid.Nil:
		return fmt.Errorf("%w: no persisted turn intent", ErrMissingTurnReceipt)
	case p.invocationID == uuid.Nil:
		return fmt.Errorf("%w: no session invocation", ErrMissingTurnReceipt)
	case p.lease.Token == uuid.Nil || p.lease.Key == "":
		return fmt.Errorf("%w: no held durable turn lease", ErrMissingTurnReceipt)
	case p.turnIndex < 1:
		return fmt.Errorf("%w: turn index %d", ErrMissingTurnReceipt, p.turnIndex)
	}
	return nil
}

// streamReceipt returns the single exact completion of this turn's stream.
func (p turnCheckpointPlan) streamReceipt(ctx context.Context) (StepCompletion, error) {
	comps := p.completions
	if comps == nil {
		sink, ok := StepCompletionSinkFromContext(ctx)
		if !ok {
			return StepCompletion{}, fmt.Errorf("%w: no completion sink", ErrMissingTurnReceipt)
		}
		comps = sink.Completions()
	}
	if len(comps) != 1 {
		return StepCompletion{}, fmt.Errorf("%w: want exactly one stream receipt, got %d", ErrMissingTurnReceipt, len(comps))
	}
	c := comps[0]
	if c.JobFence.JobID == uuid.Nil || c.JobFence.RunnerID == "" || c.JobFence.Generation < 1 {
		return StepCompletion{}, fmt.Errorf("%w: incomplete job fence", ErrMissingTurnReceipt)
	}
	return c, nil
}

func copyConversation(conv map[string]any) map[string]any {
	out := make(map[string]any, len(conv)+3)
	for k, v := range conv {
		out[k] = v
	}
	return out
}

// checkpointAndCommit exports under the stream fence, validates its prepared
// workspace, then commits once. The caller's record changes only after commit.
func (d *SessionCheckpointDeps) checkpointAndCommit(ctx context.Context, exec *instance.NodeExecution, p turnCheckpointPlan) error {
	if err := p.validate(); err != nil {
		return err
	}
	backend, native, err := sessionBackend(p.binding, d.DefaultAgentBackend)
	if err != nil {
		return err
	}
	stream, err := p.streamReceipt(ctx)
	if err != nil {
		return err
	}
	mutable := p.binding.Kind == provider.ProfileKindAgent && p.workDir != ""
	resource := exec.InstanceID
	if p.resourceID != uuid.Nil {
		resource = p.resourceID
	} else if p.group && mutable {
		return fmt.Errorf("%w: group staging session has no resolution resource", ErrMissingTurnReceipt)
	}
	if mutable && stream.Workspace == nil {
		return fmt.Errorf("%w: mutable turn has no workspace receipt", ErrMissingTurnReceipt)
	}
	runnerID := stream.JobFence.RunnerID
	var ref string
	if native {
		sid := SessionID(exec.LLMConversation)
		if sid == "" {
			return fmt.Errorf("%w: harness returned no session id", ErrMissingTurnReceipt)
		}
		req := SessionExportRequest{RunnerID: runnerID, Backend: backend, SessionID: sid, WorkDir: p.workDir, JobFence: stream.JobFence}
		if stream.Workspace != nil {
			r := stream.Workspace.Resource
			req.Resource = &r
		}
		b, err := d.Exporter.ExportSession(ctx, req)
		if err != nil {
			return fmt.Errorf("checkpoint session: export: %w", err)
		}
		if err := b.Verify(); err != nil {
			return fmt.Errorf("checkpoint session: export: %w", err)
		}
		if b.Backend != backend || b.SessionID != sid {
			return fmt.Errorf("checkpoint session: exported %s/%s, want %s/%s", b.Backend, b.SessionID, backend, sid)
		}
		if ref, err = d.Store.Put(ctx, SessionKey{RunID: exec.InstanceID, InvocationID: p.invocationID, TurnIndex: p.turnIndex}, b); err != nil {
			return fmt.Errorf("checkpoint session: store: %w", err)
		}
	}
	conv := copyConversation(exec.LLMConversation)
	if mutable {
		staged, err := d.Workspace.StageTurn(ctx, TurnWorkspaceRequest{RunID: resource, RunnerID: runnerID, WorkDir: p.workDir, Completion: stream})
		if err != nil {
			return fmt.Errorf("checkpoint workspace: %w", err)
		}
		w := staged.Completion
		stream.Workspace = &w
		conv[ConversationKeyWorkspaceCheckpointRef] = staged.Ref
	}
	conv[ConversationKeyAcceptedTurnBinding] = p.accepted
	conv[ConversationKeyTurnIdentity] = InteractiveTurnIdentity{RunID: exec.InstanceID, NodeID: exec.NodeID, InvocationID: p.invocationID, RequestID: p.requestID, TurnIndex: p.turnIndex, Generation: p.lease.Generation}
	commitExec := *exec
	commitExec.LLMConversation = conv
	if err := d.Committer.CommitTurn(ctx, TurnCommit{
		Execution: &commitExec, Create: p.create, RequireOpenSession: p.group, RequireWorkspace: mutable,
		CheckpointRef: ref, PinRunner: runnerID, SessionGeneration: p.lease.Generation,
		LeaseKey: p.lease.Key, LeaseToken: p.lease.Token, IntentID: p.intentID, RequestID: p.requestID,
		InvocationID: p.invocationID, TurnIndex: p.turnIndex, ResourceRunID: resource,
		AcceptedBinding: p.accepted, Completions: []StepCompletion{stream},
	}); err != nil {
		return fmt.Errorf("commit turn: %w", err)
	}
	*exec = commitExec
	if ref != "" {
		// Retention failure is cleanup debt; the committed pair is authoritative.
		_ = d.Store.Prune(context.WithoutCancel(ctx), exec.InstanceID, p.invocationID, ref)
	}
	return nil
}

// commitOperation commits End/Abandon against the existing pair without export.
func (d *SessionCheckpointDeps) commitOperation(ctx context.Context, exec *instance.NodeExecution, p turnCheckpointPlan) error {
	if p.kind != TurnIntentEnd && p.kind != TurnIntentAbandon {
		return fmt.Errorf("%w: %q is not an operation-only intent", ErrMissingTurnReceipt, p.kind)
	}
	if err := p.validate(); err != nil {
		return err
	}
	var ref, pin string
	if exec.SessionCheckpointRef != nil {
		ref = *exec.SessionCheckpointRef
	}
	if exec.SessionPinRunner != nil {
		pin = *exec.SessionPinRunner
	}
	resource := exec.InstanceID
	if p.resourceID != uuid.Nil {
		resource = p.resourceID
	}
	return d.Committer.CommitTurn(ctx, TurnCommit{
		Execution: exec, RequireOpenSession: p.group, CheckpointRef: ref, PinRunner: pin,
		SessionGeneration: p.lease.Generation, LeaseKey: p.lease.Key, LeaseToken: p.lease.Token,
		IntentID: p.intentID, RequestID: p.requestID, InvocationID: p.invocationID, TurnIndex: p.turnIndex,
		ResourceRunID: resource, AcceptedBinding: p.accepted,
	})
}
