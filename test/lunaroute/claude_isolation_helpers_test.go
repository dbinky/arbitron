package lunaroute_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	adaptprobe "github.com/arbitron/arbitron/adapter/probe"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

const (
	claudeIsolationSource   = "docs/evidence/lunaroute-provider/sources/lr05-isolation.md"
	claudeForeignCredential = "conflicting-claude-credential"
)

// Protected input bytes/paths stay private. Runtime session/log files under the
// private home are not settings/auth inputs and are removed with the experiment.
type claudeIsolation struct {
	home, project string
	before        backendHomeSnapshot
	session       string
	memory        string // private source-formatted path/nonce, never serialized
	limits        claudeMainLimitProof
}

func seedClaudeIsolation(home, project string) (*claudeIsolation, error) {
	if requireEmptyAgentWorkspace(project) != nil || os.Mkdir(home, 0700) != nil {
		return nil, errAgentEvidence
	}
	home, homeErr := filepath.EvalSymlinks(home)
	project, projectErr := filepath.EvalSymlinks(project)
	if homeErr != nil || projectErr != nil {
		return nil, errAgentEvidence
	}
	memory, err := newClaudeMemoryText()
	if err != nil {
		return nil, errAgentEvidence
	}
	seeds := map[string]string{
		filepath.Join(project, "CLAUDE.md"):                   memory,
		filepath.Join(home, ".claude/settings.json"):          `{"model":"foreign/user","env":{"ANTHROPIC_BASE_URL":"https://foreign.example","ANTHROPIC_AUTH_TOKEN":"` + claudeForeignCredential + `","ANTHROPIC_MODEL":"foreign/user"}}`,
		filepath.Join(home, ".claude/.credentials.json"):      `{"claudeAiOauth":{"accessToken":"` + claudeForeignCredential + `","refreshToken":"` + claudeForeignCredential + `","expiresAt":1,"scopes":["user:inference"],"subscriptionType":"pro"}}`,
		filepath.Join(project, ".claude/settings.json"):       `{"model":"foreign/project","env":{"ANTHROPIC_API_KEY":"` + claudeForeignCredential + `","ANTHROPIC_MODEL":"foreign/project"}}`,
		filepath.Join(project, ".claude/settings.local.json"): `{"model":"foreign/local","env":{"ANTHROPIC_BASE_URL":"https://foreign.example"}}`,
	}
	for path, body := range seeds {
		if os.MkdirAll(filepath.Dir(path), 0700) != nil {
			return nil, errAgentEvidence
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, errAgentEvidence
		}
		_, writeErr := io.WriteString(f, body)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			return nil, errAgentEvidence
		}
	}
	isolation := &claudeIsolation{home: home, project: project, memory: "Contents of " + filepath.Join(project, "CLAUDE.md") + " (project instructions, checked into the codebase):\n\n" + strings.TrimSpace(memory)}
	before, err := isolation.snapshot()
	if err != nil {
		return nil, errAgentEvidence
	}
	isolation.before = before
	return isolation, nil
}

func (s *claudeIsolation) snapshot() (backendHomeSnapshot, error) {
	if s == nil {
		return nil, errAgentEvidence
	}
	return snapshotIsolationPaths(s.home, []isolationPath{
		{s.home, ".claude/settings.json"}, {s.home, ".claude/.credentials.json"}, {s.project, ".claude"}, {s.project, "CLAUDE.md"},
	})
}
func (s *claudeIsolation) unchanged() error {
	after, err := s.snapshot()
	if err != nil || len(s.before) == 0 || !reflect.DeepEqual(s.before, after) {
		return errAgentEvidence
	}
	return nil
}
func (s *claudeIsolation) validateLaunch(spec claudecli.RunSpec) error {
	if s == nil || s.unchanged() != nil {
		return errAgentEvidence
	}
	env := map[string]string{}
	for _, entry := range spec.Env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			return errAgentEvidence
		}
		if _, exists := env[name]; exists {
			return errAgentEvidence
		}
		env[name] = value
		switch name {
		case "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE":
			return errAgentEvidence
		}
	}
	home, homeErr := filepath.EvalSymlinks(env["HOME"])
	project, projectErr := filepath.EvalSymlinks(spec.Dir)
	if homeErr != nil || projectErr != nil || home != s.home || project != s.project {
		return errAgentEvidence
	}
	_, err := claudeStartupSelection(spec)
	return err
}

// Only this isolated experiment adds the guard. It never rewrites the actual
// adapter-emitted executable, argv, credential env or cwd.
type claudeIsolationRunner struct {
	next      claudecli.CommandRunner
	isolation *claudeIsolation
	rules     provider.CapabilityRules
	capture   *inferenceCapture
}

func (r claudeIsolationRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	fail := func() (<-chan string, func() error, error) {
		return nil, nil, provider.LocalLunaRouteError("config isolation", errAgentEvidence)
	}
	if r.isolation == nil || r.isolation.session != "" || r.isolation.validateLaunch(spec) != nil || r.capture.expectClaudeMemory(r.isolation) != nil {
		return fail()
	}
	lines, err := runCapturedLines(ctx, r.next, spec)
	if err != nil {
		return nil, nil, errAgentEvidence
	}
	if r.isolation.unchanged() != nil {
		return fail()
	}
	limits, err := collectClaudeMainLimits(spec, r.rules, lines)
	if err != nil {
		return fail()
	}
	r.isolation.limits = limits
	r.isolation.session = limits.Session
	out := make(chan string, len(lines))
	for _, line := range lines {
		out <- line
	}
	close(out)
	return out, func() error { return nil }, nil
}

func liveClaudeIsolationFactoryWithClient(ws, bin string, rules provider.CapabilityRules, client *http.Client, isolation *claudeIsolation, capture *inferenceCapture) *providersadapter.Factory {
	runner := claudeIsolationRunner{next: claudeStartupRunner{next: claudecli.NewExecRunner()}, isolation: isolation, rules: rules, capture: capture}
	return providersadapter.NewFactory(providersadapter.Deps{
		ClaudeAgentConfig: claudecli.Config{BinaryPath: bin, WorkspaceRoot: ws, Runner: runner, Timeout: agentExperimentTimeout,
			MaxResponseBytes: captureBodyLimit, AllowedTools: []string{"Read", "Write", "Bash"}, CapabilityRules: rules},
		CapabilityRules: rules, Probes: adaptprobe.NewProbeFactory(adaptprobe.Deps{HTTPClient: client, Timeout: agentExperimentTimeout, MaxResponseBytes: captureBodyLimit}),
	})
}

type claudeIsolationObservation struct {
	Scope           string                   `json:"scope"`
	Source          string                   `json:"source"`
	Version         string                   `json:"version"`
	Session         string                   `json:"session"`
	ConfigUnchanged bool                     `json:"config_unchanged"`
	AuthUnchanged   bool                     `json:"auth_unchanged"`
	Limits          claudeMainLimitProof     `json:"main_limits"`
	ProjectMemory   claudeProjectMemoryProof `json:"project_memory"`
	Inference       []capturedInference      `json:"inference"`
}

func collectClaudeIsolationEvidence(s *claudeIsolation, records []capturedInference, result *engine.AgentResult, started, finished time.Time) (claudeIsolationObservation, error) {
	fail := func() (claudeIsolationObservation, error) { return claudeIsolationObservation{}, errAgentEvidence }
	if s == nil || s.session == "" || s.unchanged() != nil || result == nil || result.SessionID != s.session || len(result.ModelEvidence) == 0 {
		return fail()
	}
	if validateCapturedInference(records, result.Model, []string{"main"}) != nil {
		return fail()
	}
	limits := s.limits
	if limits.Source != claudeLimitsSource || limits.Version != claudeStartupVersion || limits.Session != s.session || limits.Model != result.Model || limits.ContextTokens <= 0 || limits.RequestedOutputTokens <= 0 {
		return fail()
	}
	limits.RequestIDs = nil
	memory := claudeProjectMemoryProof{Source: claudeMemorySource}
	for _, record := range records {
		if record.RequestID == "request-1" && record.ClaudeProjectMemory {
			memory.Loaded = true
			memory.RequestIDs = append(memory.RequestIDs, record.RequestID)
		}
		if record.Backend != "claude-code" || record.Path != "/v1/messages" || record.StartedAt.Before(started) || record.FinishedAt.After(finished) || record.MaxTokens != limits.RequestedOutputTokens {
			return fail()
		}
		limits.RequestIDs = append(limits.RequestIDs, record.RequestID)
	}
	if !memory.Loaded {
		return fail()
	}
	tool := false
	for _, step := range result.Steps {
		tool = tool || step.Kind == "tool_call"
	}
	for _, model := range result.ModelEvidence {
		if model.Model != result.Model {
			return fail()
		}
	}
	proof, proofErr := workspaceProof(s.project, agentProofFile, agentProofLimit, started, finished)
	command, commandErr := workspaceProof(s.project, agentCommandFile, agentProofLimit, started, finished)
	if !tool || proofErr != nil || commandErr != nil || string(proof) != agentProofText || strings.TrimSpace(string(command)) != strconv.Itoa(len(agentProofText)) {
		return fail()
	}
	return claudeIsolationObservation{Scope: "main config/auth isolation, reported context, requested output budget and first-request project memory only; no helper or complete contract verification", Source: claudeIsolationSource,
		Version: claudeStartupVersion, Session: s.session, ConfigUnchanged: true, AuthUnchanged: true, Limits: limits, ProjectMemory: memory, Inference: records}, nil
}
