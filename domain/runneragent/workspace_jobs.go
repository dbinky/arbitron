package runneragent

const (
	OpProcess = "process"
	OpAgent   = "agent"
)

// WorkspaceJob describes a workspace sync plan, never server credentials.
type WorkspaceJob struct {
	RunID             string
	WorkDir           string
	HomeGeneration    int64
	CheckpointVersion int64
	ManifestHash      string
	PullToken         string
	PushToken         string
	PartBytes         int64
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
