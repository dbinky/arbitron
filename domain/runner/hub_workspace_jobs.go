package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

// ProcessDispatch is the domain mirror of runnerproto.WireProcess.
type ProcessDispatch struct {
	Kind             engine.ProcessKind
	Command          string
	Args             []string
	Dir              string // tool_call: canonical workspace; script: "" (unpacked bundle)
	Stdin            []byte
	NodeEnv          map[string]string
	Passthrough      []string
	TimeoutSecs      int
	WorkDir          string
	BundleHash       string
	BundleEntrypoint string
	BundleToken      string
	Requires         []string // RR-09: checked by the runner before accept
}

// AgentDispatch is the domain mirror of runnerproto.WireAgent: mission request
// facts plus a binding descriptor. It never carries endpoints, keys, extras or
// a server-side capability ruleset (the runner uses its own rules, P3 T15).
type AgentDispatch struct {
	Model, Prompt, WorkDir, Provider string
	ToolScope                        []string
	SessionID                        string
	TimeoutSecs                      int
	CredentialKind                   CredentialKind
	Backend, Effort, ContextWindow   string
	RequireExecutedModel             bool
}

// WorkspaceDispatch is the domain mirror of runnerproto.WireWorkspace. BaseRef
// and Resource are the exact committed base and live claim the pull/push
// tokens were issued for (P3 T6 PullPlan); the runner rejects a dispatch
// without them (P3 T9).
type WorkspaceDispatch struct {
	RunID             uuid.UUID
	WorkDir           string
	HomeGeneration    int64
	CheckpointVersion int64
	ManifestHash      string
	BaseRef           string
	Resource          engine.ResourceFenceRef
	PullToken         string
	PushToken         string
	PartBytes         int64
}

// WorkspacePuller is the slice of *WorkspaceSync the hub needs.
type WorkspacePuller interface {
	PreparePull(ctx context.Context, a Assignment, runID uuid.UUID) (PullPlan, error)
}

// BundleTokenIssuer is the slice of the transfer-token service the hub needs
// (P3 T7 TransferTokens.Issue).
type BundleTokenIssuer interface {
	Issue(ctx context.Context, scope TransferScope, ttl time.Duration) (TransferToken, error)
}

// WorkspaceJobDeps enables agent and process dispatch on the hub.
// Capabilities is retained for the composition root (P3 T13) but is never
// copied into a dispatch.
type WorkspaceJobDeps struct {
	Sync           WorkspacePuller
	Tokens         BundleTokenIssuer
	Capabilities   provider.CapabilityRules
	PartBytes      int64
	BundleTokenTTL time.Duration
}

var (
	// ErrBundleRequired: a script without a bundle can only run built-in (RR-09).
	ErrBundleRequired = errors.New("runner: script step has no bundle; it can only run on the built-in runner (RR-09)")
	// ErrWorkspaceJobsUnavailable: the hub was not given WorkspaceJobDeps.
	ErrWorkspaceJobsUnavailable = errors.New("runner: hub has no workspace job support configured")
)

const defaultBundleTokenTTL = 10 * time.Minute

// SetWorkspaceJobs enables OpAgent/OpProcess dispatch. Call before Start.
func (h *Hub) SetWorkspaceJobs(d WorkspaceJobDeps) {
	if d.BundleTokenTTL <= 0 {
		d.BundleTokenTTL = defaultBundleTokenTTL
	}
	h.wsJobs.Store(&d)
}

// BuildWorkspaceJob translates an agent/process StepJob into the dispatch
// mirrors. Every workspace op gets a pull plan for its run (a.Job.Key.RunID)
// pinned to the exact claim and committed base; a script also gets a bundle
// token scoped to its exact content hash and that same claim.
func BuildWorkspaceJob(ctx context.Context, d *WorkspaceJobDeps, a Assignment, job StepJob) (*ProcessDispatch, *AgentDispatch, *WorkspaceDispatch, error) {
	if d == nil || d.Sync == nil || d.Tokens == nil {
		return nil, nil, nil, ErrWorkspaceJobsUnavailable
	}
	runID := a.Job.Key.RunID
	switch job.Op {
	case OpProcess:
		if job.Process == nil {
			return nil, nil, nil, errors.New("runner: process job without a ProcessSpec")
		}
		spec := *job.Process
		if spec.Kind == engine.ProcessScript && spec.Bundle == nil {
			return nil, nil, nil, ErrBundleRequired
		}
		if spec.Kind != engine.ProcessScript && spec.Kind != engine.ProcessToolCall {
			return nil, nil, nil, fmt.Errorf("runner: unknown process kind %q", spec.Kind)
		}
		if spec.WorkDir == "" {
			return nil, nil, nil, fmt.Errorf("%w: a remote process step needs its workspace", ErrNoWorkspace)
		}
		wd, err := workspacePlan(ctx, d, a, runID, spec.WorkDir)
		if err != nil {
			return nil, nil, nil, err
		}
		pd := &ProcessDispatch{
			Kind: spec.Kind, Command: spec.Command, Args: append([]string(nil), spec.Args...),
			Stdin: spec.Stdin, NodeEnv: spec.NodeEnv, Passthrough: spec.Passthrough,
			TimeoutSecs: spec.TimeoutSecs, WorkDir: spec.WorkDir, Requires: append([]string(nil), spec.Requires...),
		}
		if spec.Kind == engine.ProcessToolCall {
			pd.Dir = spec.WorkDir
			return pd, nil, wd, nil
		}
		tok, err := d.Tokens.Issue(ctx, TransferScope{Fence: a.Fence, RunID: runID, Direction: TransferBundle,
			BundleHash: spec.Bundle.Hash, Resource: wd.Resource}, d.BundleTokenTTL)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("runner: bundle token: %w", err)
		}
		pd.BundleHash, pd.BundleEntrypoint, pd.BundleToken = spec.Bundle.Hash, spec.Bundle.Entrypoint, tok.Token
		return pd, nil, wd, nil
	case OpAgent:
		if job.Agent == nil {
			return nil, nil, nil, errors.New("runner: agent job without a mission")
		}
		m := *job.Agent
		wd, err := workspacePlan(ctx, d, a, runID, m.WorkDir)
		if err != nil {
			return nil, nil, nil, err
		}
		return nil, &AgentDispatch{
			Model: m.Model, Prompt: m.Prompt, WorkDir: m.WorkDir, Provider: job.Binding.Provider,
			ToolScope: append([]string(nil), m.ToolScope...), SessionID: m.SessionID,
			TimeoutSecs: int(m.Timeout / time.Second), CredentialKind: a.Job.Group.Kind,
			Backend: job.Binding.Backend, Effort: string(job.Binding.Effort), ContextWindow: string(job.Binding.ContextWindow),
			RequireExecutedModel: job.Binding.RequiresModelEvidence(),
		}, wd, nil
	default:
		return nil, nil, nil, fmt.Errorf("runner: op %q is not a workspace op", job.Op)
	}
}

// workspacePlan reads the pull plan once and refuses incomplete evidence: the
// claim and committed base are never defaulted.
func workspacePlan(ctx context.Context, d *WorkspaceJobDeps, a Assignment, runID uuid.UUID, workDir string) (*WorkspaceDispatch, error) {
	plan, err := d.Sync.PreparePull(ctx, a, runID)
	if err != nil {
		return nil, fmt.Errorf("runner: prepare workspace pull: %w", err)
	}
	if err := plan.Resource.Validate(); err != nil {
		return nil, fmt.Errorf("%w: pull plan for run %s has no live claim: %v", ErrResourceFenced, runID, err)
	}
	if plan.Ref.IsZero() || plan.Ref.RunID != runID {
		return nil, fmt.Errorf("%w: pull plan for run %s has no pinned committed base", ErrResourceFenced, runID)
	}
	return &WorkspaceDispatch{
		RunID: runID, WorkDir: workDir,
		HomeGeneration: plan.Home.Generation, CheckpointVersion: plan.Ref.Version, ManifestHash: plan.Ref.ManifestHash,
		BaseRef: plan.Ref.String(), Resource: plan.Resource,
		PullToken: plan.PullToken.Token, PushToken: plan.PushToken.Token, PartBytes: d.PartBytes,
	}, nil
}

// WorkspaceStepOutput maps a successful remote result to the op's typed
// output. It never sets Completion: P3 T6's dispatcher attaches the exact
// prepared-candidate receipt.
func WorkspaceStepOutput(op OpType, res *RemoteResult) (StepOutput, error) {
	if res == nil {
		return StepOutput{}, errors.New("runner: remote result missing")
	}
	switch op {
	case OpProcess:
		if res.Process == nil {
			return StepOutput{}, errors.New("runner: remote process result missing")
		}
		return StepOutput{Process: res.Process}, nil
	case OpAgent:
		if res.Agent == nil {
			return StepOutput{}, errors.New("runner: remote agent result missing")
		}
		return StepOutput{Agent: res.Agent}, nil
	}
	return StepOutput{}, fmt.Errorf("runner: op %q is not a workspace op", op)
}
