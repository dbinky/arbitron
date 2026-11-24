// domain/runner/checkpoint.go
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// Manifest lists one immutable checkpoint of a run's workspace. Hash covers the
// version and every entry, so two checkpoints of identical content at different
// versions are distinct manifests.
type Manifest struct {
	Version int64           `json:"version"`
	Entries []ManifestEntry `json:"entries"`
	Hash    string          `json:"hash"`
}

// ManifestEntry is one path. Mode carries fs.FileMode type bits (ModeDir,
// ModeSymlink) plus permission bits; regular files carry Size and SHA256,
// symlinks carry their link text. Deleted is used only in diffs.
type ManifestEntry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	SHA256  string `json:"sha256,omitempty"`
	Symlink string `json:"symlink,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

// CheckpointRef names one immutable checkpoint artifact. The zero value
// (Version 0) means "no checkpoint": an empty workspace.
type CheckpointRef struct {
	RunID        uuid.UUID `json:"runId"`
	Version      int64     `json:"version"`
	ManifestHash string    `json:"manifestHash"`
}

// IsZero reports whether r names no checkpoint.
func (r CheckpointRef) IsZero() bool { return r.Version == 0 }

// String renders "<runID>@<version>:<manifestHash>" ("" for the zero ref).
func (r CheckpointRef) String() string {
	if r.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s@%d:%s", r.RunID, r.Version, r.ManifestHash)
}

// ParseCheckpointRef parses String's output. "" is the zero ref.
func ParseCheckpointRef(s string) (CheckpointRef, error) {
	if s == "" {
		return CheckpointRef{}, nil
	}
	at := strings.IndexByte(s, '@')
	colon := strings.LastIndexByte(s, ':')
	if at <= 0 || colon <= at+1 || colon == len(s)-1 {
		return CheckpointRef{}, fmt.Errorf("runner: malformed checkpoint ref %q", s)
	}
	id, err := uuid.Parse(s[:at])
	if err != nil || id == uuid.Nil {
		return CheckpointRef{}, fmt.Errorf("runner: malformed checkpoint ref %q", s)
	}
	v, err := strconv.ParseInt(s[at+1:colon], 10, 64)
	if err != nil || v < 1 {
		return CheckpointRef{}, fmt.Errorf("runner: malformed checkpoint ref version %q", s)
	}
	hash := s[colon+1:]
	if !validManifestHash(hash) {
		return CheckpointRef{}, fmt.Errorf("runner: malformed checkpoint ref hash %q", s)
	}
	return CheckpointRef{RunID: id, Version: v, ManifestHash: hash}, nil
}

func validManifestHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// CheckpointCandidate is one immutable artifact offered to the catalog. A
// prepared candidate carries the exact job and resource receipts of the
// operation that produced it and the committed base it was built on; it never
// moves the committed pointer (P3 T5/P3 T13/P4 T6 transactions promote it).
type CheckpointCandidate struct {
	Ref, Base   CheckpointRef
	Job         *Fence
	Resource    engine.ResourceFenceRef
	ArchivePath string
	SizeBytes   int64
}

// ValidatePrepared rejects a candidate whose evidence is incomplete. Missing
// job or resource evidence is never a wildcard.
func (c CheckpointCandidate) ValidatePrepared() error {
	if err := c.validateArtifact(); err != nil {
		return err
	}
	if c.Job == nil || c.Job.JobID == uuid.Nil || c.Job.RunnerID == "" || c.Job.Generation < 1 {
		return fmt.Errorf("%w: prepared candidate %s has no exact job fence", ErrInvalidCandidate, c.Ref)
	}
	if err := c.Resource.Validate(); err != nil {
		return fmt.Errorf("%w: prepared candidate %s: %v", ErrInvalidCandidate, c.Ref, err)
	}
	if !c.Base.IsZero() && c.Base.RunID != c.Ref.RunID {
		return fmt.Errorf("%w: candidate %s has a base from another run", ErrInvalidCandidate, c.Ref)
	}
	if c.Ref.Version <= c.Base.Version {
		return fmt.Errorf("%w: candidate %s does not follow base %q", ErrInvalidCandidate, c.Ref, c.Base)
	}
	return nil
}

// ValidateBootstrap rejects a bootstrap candidate that carries operation
// evidence: a baseline is recorded before any job or resource claim exists.
func (c CheckpointCandidate) ValidateBootstrap() error {
	if err := c.validateArtifact(); err != nil {
		return err
	}
	if c.Job != nil || c.Resource != (engine.ResourceFenceRef{}) || !c.Base.IsZero() {
		return fmt.Errorf("%w: bootstrap %s must carry no job, resource or base", ErrInvalidCandidate, c.Ref)
	}
	return nil
}

func (c CheckpointCandidate) validateArtifact() error {
	if c.Ref.RunID == uuid.Nil || c.Ref.Version < 1 || !validManifestHash(c.Ref.ManifestHash) {
		return fmt.Errorf("%w: malformed ref %+v", ErrInvalidCandidate, c.Ref)
	}
	if c.ArchivePath == "" || c.SizeBytes < 0 {
		return fmt.Errorf("%w: candidate %s has no artifact", ErrInvalidCandidate, c.Ref)
	}
	return nil
}

// StagedCheckpoint is a fully built, verified next artifact that is not yet
// registered. Dir is owned by the store; Publish or Discard removes it.
// Resource is assigned by the caller from the durable transfer session
// (P3 T7), never from the uploaded archive.
type StagedCheckpoint struct {
	RunID       uuid.UUID
	Base        CheckpointRef
	Version     int64
	Manifest    Manifest
	Dir         string
	ArchivePath string
	SizeBytes   int64
	Resource    engine.ResourceFenceRef
}

// DeltaControlPath is the first entry of every pushed delta archive; its JSON
// body is a DeltaControl.
const DeltaControlPath = ".arbitron-delta/control.json"

// DeltaControl lists the deletions a delta applies and the FULL manifest
// entries the runner's copy has after the step; the server verifies the
// staged tree against Expected before anything is prepared.
type DeltaControl struct {
	Deleted  []string        `json:"deleted"`
	Expected []ManifestEntry `json:"expected"`
}

// CheckpointStore keeps immutable checkpoint artifacts. Only Commit (bootstrap)
// records committed progress; every other writer prepares a candidate.
type CheckpointStore interface {
	Commit(ctx context.Context, runID uuid.UUID, srcDir string) (CheckpointRef, Manifest, error) // bootstrap ONLY
	StageBuiltIn(ctx context.Context, runID uuid.UUID, srcDir string, f Fence, resource engine.ResourceFenceRef) (CheckpointRef, Manifest, error)
	StageDelta(ctx context.Context, runID uuid.UUID, base CheckpointRef, delta io.Reader) (StagedCheckpoint, error)
	Publish(ctx context.Context, staged StagedCheckpoint, f Fence) (CheckpointRef, error) // PREPARE only
	// StageResource writes an immutable artifact for a server-side resource
	// mutation (P3 T13 group mutation) and registers nothing; P3 T13
	// ResourceCommitRepo.CommitResource inserts it as committed.
	StageResource(ctx context.Context, runID uuid.UUID, srcDir string, base CheckpointRef) (StagedCheckpoint, error)
	// PreparedFor returns the exact prepared candidate of one job fence and
	// resource claim (P3 T6 FinishSuccess, including after a restart).
	PreparedFor(ctx context.Context, runID uuid.UUID, job Fence, resource engine.ResourceFenceRef) (CheckpointRef, bool, error)
	Restore(ctx context.Context, ref CheckpointRef, dstDir string) error
	Manifest(ctx context.Context, ref CheckpointRef) (Manifest, error)
	Latest(ctx context.Context, runID uuid.UUID) (CheckpointRef, bool, error) // COMMITTED only
	ReadFile(ctx context.Context, ref CheckpointRef, path string) (io.ReadCloser, error)
	Discard(ctx context.Context, staged StagedCheckpoint) error
	Delete(ctx context.Context, runID uuid.UUID) error
}

// CheckpointCatalog is the durable index of artifacts. Publish inserts a
// PREPARED row after validating the exact active job and resource fences and
// the committed base in one transaction; it never moves the committed pointer.
// Bootstrap records the first committed baseline when the run has no committed
// progress and no active writer. Latest reads only the committed pointer. Get
// returns committed rows only. PreparedFor returns the single prepared row of
// one exact job fence and resource claim, never a newest-row guess.
type CheckpointCatalog interface {
	Publish(ctx context.Context, c CheckpointCandidate) error
	Bootstrap(ctx context.Context, c CheckpointCandidate) error
	Latest(ctx context.Context, runID uuid.UUID) (CheckpointRef, string, bool, error)
	Get(ctx context.Context, runID uuid.UUID, version int64) (CheckpointRef, string, error)
	DeleteRun(ctx context.Context, runID uuid.UUID) error
	Retention(ctx context.Context, runID uuid.UUID) (CheckpointRetention, error)
	PreparedFor(ctx context.Context, runID uuid.UUID, job Fence, resource engine.ResourceFenceRef) (CheckpointRef, bool, error)
}

var (
	ErrCheckpointNotFound  = errors.New("runner: checkpoint not found")
	ErrCheckpointConflict  = errors.New("runner: checkpoint version or base conflict")
	ErrManifestMismatch    = errors.New("runner: staged workspace does not match the pushed manifest")
	ErrUnsupportedFileType = errors.New("runner: unsupported file type in workspace")
	ErrUnsupportedGit      = errors.New("runner: unsupported git metadata in workspace")
	ErrCorruptCheckpoint   = errors.New("runner: checkpoint content does not match its manifest")
	ErrNoCheckpointCatalog = errors.New("runner: checkpoint store has no catalog; refusing to record progress")
	ErrInvalidCandidate    = errors.New("runner: checkpoint candidate evidence is incomplete")
	// ErrPromisorObjectsMissing (RR-08, decision 2026-11-22) is always wrapped
	// together with ErrUnsupportedGit. P3 T6 answers it by marking the home
	// non-portable; it is never a step failure.
	ErrPromisorObjectsMissing = errors.New("runner: required promisor objects are missing locally")
)

// appended to domain/runner/checkpoint.go
// CheckpointRetention is what the catalog still references for one run; the
// store's Prune never removes anything it names.
type CheckpointRetention struct {
	Versions  map[int64]bool // every catalog row of the run, prepared or committed
	Committed int64          // the committed pointer's version (0 = none)
	Held      bool           // an active run:<uuid> lease or an offered/running job of the run
}
