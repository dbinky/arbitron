package runner

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
)

var _ engine.TurnWorkspaceCheckpointer = (*TurnWorkspaceCheckpointer)(nil)

// TurnWorkspaceCheckpointer validates the receipt's exact prepared candidate.
// Only the turn transaction may promote it and move the committed pointer.
type TurnWorkspaceCheckpointer struct {
	cp    CheckpointStore
	homes HomeStore
}

func NewTurnWorkspaceCheckpointer(cp CheckpointStore, homes HomeStore) *TurnWorkspaceCheckpointer {
	return &TurnWorkspaceCheckpointer{cp: cp, homes: homes}
}

// FormatTurnWorkspaceRef renders a portable workspace candidate's identity.
func FormatTurnWorkspaceRef(ref CheckpointRef) string { return ref.String() }

// ParseTurnWorkspaceRef requires a nonzero portable workspace identity.
func ParseTurnWorkspaceRef(s string) (CheckpointRef, error) {
	ref, err := ParseCheckpointRef(s)
	if err != nil || ref.IsZero() {
		return CheckpointRef{}, fmt.Errorf("%w: malformed workspace ref %q", engine.ErrInvalidStepCompletion, s)
	}
	return ref, nil
}

func (t *TurnWorkspaceCheckpointer) StageTurn(ctx context.Context, req engine.TurnWorkspaceRequest) (engine.StagedTurnWorkspace, error) {
	w := req.Completion.Workspace
	if w != nil && (w.NonPortable || w.LocalRef != "") {
		return t.stageNonPortable(ctx, req, *w)
	}
	if w == nil {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("%w: mutable turn on %s has no prepared workspace receipt", engine.ErrInvalidStepCompletion, req.RunnerID)
	}
	if req.Completion.JobFence.RunnerID != req.RunnerID || w.Resource.Owner != req.RunnerID {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("%w: receipt job/resource are not owned by %s", engine.ErrInvalidStepCompletion, req.RunnerID)
	}
	ref, err := ParseTurnWorkspaceRef(w.Ref)
	if err != nil {
		return engine.StagedTurnWorkspace{}, err
	}
	if ref.RunID != req.RunID {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("%w: candidate %s is not resource %s", engine.ErrInvalidStepCompletion, w.Ref, req.RunID)
	}
	home, err := t.homes.GetHome(ctx, req.RunID)
	if err != nil {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("runner: workspace home: %w", err)
	}
	if ref.Version <= home.CheckpointVersion {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("%w: candidate v%d is not newer than committed v%d", engine.ErrInvalidStepCompletion, ref.Version, home.CheckpointVersion)
	}
	job := Fence{JobID: req.Completion.JobFence.JobID, RunnerID: RunnerID(req.Completion.JobFence.RunnerID), Generation: req.Completion.JobFence.Generation}
	got, ok, err := t.cp.PreparedFor(ctx, req.RunID, job, w.Resource)
	if err != nil {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("runner: prepared candidate: %w", err)
	}
	if !ok || got != ref {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("%w: %s is not the prepared candidate of job %s under its claim", engine.ErrInvalidStepCompletion, w.Ref, job.JobID)
	}
	return engine.StagedTurnWorkspace{RunID: req.RunID, RunnerID: req.RunnerID, Version: ref.Version, Ref: FormatTurnWorkspaceRef(ref), Completion: *w}, nil
}

// stageNonPortable reuses the dispatcher's server-local checkpoint receipt;
// the turn does not copy the workspace a second time.
func (t *TurnWorkspaceCheckpointer) stageNonPortable(ctx context.Context, req engine.TurnWorkspaceRequest, w engine.WorkspaceCompletion) (engine.StagedTurnWorkspace, error) {
	if !w.NonPortable || w.LocalRef == "" || w.Ref != "" {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("%w: LocalRef must be set exactly when NonPortable", engine.ErrInvalidStepCompletion)
	}
	ref, err := ParseLocalCheckpointRef(w.LocalRef)
	if err != nil || ref.RunID != req.RunID {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("%w: local checkpoint %q for run %s", engine.ErrInvalidStepCompletion, w.LocalRef, req.RunID)
	}
	if RunnerID(req.RunnerID) != BuiltInRunnerID {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("runner: non-portable run %s turn on %s: %w", req.RunID, req.RunnerID, ErrHomeOwned)
	}
	if err := req.Completion.Validate(); err != nil {
		return engine.StagedTurnWorkspace{}, err
	}
	if req.Completion.JobFence.RunnerID != req.RunnerID || w.Resource.Owner != req.RunnerID {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("%w: local receipt job/resource are not owned by %s", engine.ErrInvalidStepCompletion, req.RunnerID)
	}
	home, err := t.homes.GetHome(ctx, req.RunID)
	if err != nil {
		return engine.StagedTurnWorkspace{}, err
	}
	if home.NonPortableReason == "" {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("%w: run %s is portable", engine.ErrInvalidStepCompletion, req.RunID)
	}
	cur, err := committedLocal(home)
	if err != nil {
		return engine.StagedTurnWorkspace{}, err
	}
	if ref.Version != cur.Version+1 {
		return engine.StagedTurnWorkspace{}, fmt.Errorf("%w: local checkpoint %s is not the successor of %q", engine.ErrInvalidStepCompletion, ref, home.LocalCheckpoint)
	}
	return engine.StagedTurnWorkspace{RunID: req.RunID, RunnerID: req.RunnerID, Version: ref.Version, Ref: w.LocalRef, Completion: w}, nil
}
