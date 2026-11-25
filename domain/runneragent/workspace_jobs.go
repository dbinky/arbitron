package runneragent

import (
	"errors"
	"fmt"
	"path"

	"github.com/google/uuid"
)

const (
	OpProcess = "process"
	OpAgent   = "agent"
)

// WorkspaceJob describes a workspace sync plan, never server credentials. Its
// field list follows the scoped wire contract (P3 T8 Cycle 4b).
type WorkspaceJob struct {
	RunID             string
	WorkDir           string
	HomeGeneration    int64
	CheckpointVersion int64
	ManifestHash      string
	BaseRef           string // exact committed base "<run>@<version>:<hash>"
	FenceKey          string // the run's live resource claim
	FenceOwner        string
	FenceGeneration   int64
	FenceToken        string
	PullToken         string
	PushToken         string
	PartBytes         int64
}

// Validate refuses incomplete scope evidence; the runner never invents a base
// or a claim.
func (j WorkspaceJob) Validate() error {
	run, err := uuid.Parse(j.RunID)
	if err != nil || run == uuid.Nil {
		return errors.New("runneragent: workspace job run id must be a nonzero UUID")
	}
	if j.WorkDir == "" || !path.IsAbs(j.WorkDir) || path.Clean(j.WorkDir) != j.WorkDir {
		return errors.New("runneragent: workspace job work dir must be a clean absolute path")
	}
	if j.CheckpointVersion < 1 || j.ManifestHash == "" ||
		j.BaseRef != fmt.Sprintf("%s@%d:%s", run, j.CheckpointVersion, j.ManifestHash) {
		return errors.New("runneragent: workspace job has no pinned base for this run")
	}
	if tok, err := uuid.Parse(j.FenceToken); err != nil || tok == uuid.Nil {
		return errors.New("runneragent: workspace job has no resource claim token")
	}
	if j.FenceKey != "run:"+run.String() || j.FenceOwner == "" || j.FenceGeneration < 1 {
		return errors.New("runneragent: workspace job claim must be this run's, with an owner and a positive generation")
	}
	if j.PullToken == "" || j.PushToken == "" || j.PartBytes <= 0 {
		return errors.New("runneragent: workspace job needs pull/push tokens and a part size")
	}
	return nil
}

// ProcessJob describes a process to execute with runner-local environment values.
type ProcessJob struct {
	Kind             string
	Command          string
	Args             []string
	Dir              string
	Stdin            []byte
	NodeEnv          map[string]string
	Passthrough      []string
	TimeoutSecs      int
	WorkDir          string
	BundleHash       string
	BundleEntrypoint string
	BundleToken      string
	Requires         []string
}

// AgentJob describes an agent binding without credentials.
type AgentJob struct {
	Provider             string
	Model                string
	Prompt               string
	WorkDir              string
	ToolScope            []string
	SessionID            string
	TimeoutSecs          int
	CredentialKind       string
	Backend              string
	Effort               string
	ContextWindow        string
	RequireExecutedModel bool
}

type ProcessResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type AgentStep struct{ Kind, Summary string }
type ModelEvidence struct{ Model, Provider, Source, Kind string }
type AgentResult struct {
	Summary       string
	Steps         []AgentStep
	Model         string
	ModelEvidence []ModelEvidence
	DroppedTools  []string
	TokensIn      int
	TokensOut     int
	SessionID     string
}
