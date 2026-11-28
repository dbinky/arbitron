package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/adapter/runneragent/workspace"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

var (
	// ErrWorkspacePreparation marks incomplete pinned-base or resource scope.
	ErrWorkspacePreparation = errors.New("workspace_preparation_failed")
	// ErrCommandUnavailable: the interpreter / command / harness is not on
	// this runner (RR-09: only a PATH lookup is verified).
	ErrCommandUnavailable = errors.New("command_unavailable")
	// ErrUnsupportedAgentCredential: remote agent jobs run on the runner's own
	// Claude or Codex subscription only.
	ErrUnsupportedAgentCredential = errors.New("agent credential kind is not supported on remote runners")
)

type WorkspaceJobsDeps struct {
	Syncer           *workspace.Syncer
	Bundles          *BundleCache
	Process          engine.ProcessRunner // process.NewLocalProcessRunner(): env built HERE from passthrough names
	ClaudeBinary     string
	CodexBinary      string
	Rules            provider.CapabilityRules // loaded locally, required in production
	MaxResponseBytes int64
	HarnessRunner    claudecli.CommandRunner // nil: production os/exec runner (claude and codex)
	LookPath         func(string) (string, error)
}

// WorkspaceJobs executes OpProcess and OpAgent dispatches:
// pull → mark dirty → run → push → result. A failed step is not pushed (the
// copy stays dirty and the next pull restores the committed checkpoint).
type WorkspaceJobs struct {
	d   WorkspaceJobsDeps
	inv runneragent.LocalSources
}

func NewWorkspaceJobs(d WorkspaceJobsDeps) (*WorkspaceJobs, error) {
	if d.Syncer == nil || d.Bundles == nil || d.Process == nil {
		return nil, errors.New("workspace jobs: Syncer, Bundles and Process are required")
	}
	if d.Rules.Empty() {
		return nil, errors.New("workspace jobs: local capability rules are required")
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.ClaudeBinary == "" {
		d.ClaudeBinary = "claude"
	}
	if d.CodexBinary == "" {
		d.CodexBinary = "codex"
	}
	return &WorkspaceJobs{d: d}, nil
}

// Supports is the accept-time check (runneragent.JobExecutor contract): a
// dispatch this runner cannot execute is rejected so the dispatcher re-places it.
func (w *WorkspaceJobs) Supports(d runneragent.Dispatch) (bool, string) {
	if err := w.check(d); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (w *WorkspaceJobs) check(d runneragent.Dispatch) error {
	if d.Workspace == nil {
		return errors.New("workspace job without a workspace plan")
	}
	if _, err := w.d.Syncer.LocalPath(d.Workspace.WorkDir); err != nil {
		return err
	}
	if err := d.Workspace.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrWorkspacePreparation, err)
	}
	switch d.Op {
	case runneragent.OpProcess:
		if d.Process == nil {
			return errors.New("process dispatch without payload")
		}
		if d.Process.Kind == string(engine.ProcessScript) && (d.Process.BundleHash == "" || d.Process.BundleEntrypoint == "") {
			return runner.ErrBundleRequired
		}
		// RR-09 decision 2026-11-22: interpreter/command first, then every
		// declared `requires` name, in order; the first miss rejects. LookPath
		// resolves against the PATH of the environment this runner builds for
		// the child (procenv.BaseEnv on the runner), never the server's.
		for _, name := range append([]string{d.Process.Command}, d.Process.Requires...) {
			if _, err := w.d.LookPath(name); err != nil {
				return fmt.Errorf("%w: %s", ErrCommandUnavailable, name)
			}
		}
	case runneragent.OpAgent:
		if d.Agent == nil {
			return errors.New("agent dispatch without payload")
		}
		bin, err := w.harness(d.Agent)
		if err != nil {
			return err
		}
		if _, _, err := MatchNativeAgent(w.nativeSources(context.Background()), d.Agent, w.d.Rules); err != nil {
			return err
		}
		if _, err := w.d.LookPath(bin); err != nil {
			return fmt.Errorf("%w: %s", ErrCommandUnavailable, bin)
		}
	default:
		return fmt.Errorf("workspace jobs: op %q is not handled here", d.Op)
	}
	return nil
}

func (w *WorkspaceJobs) harness(a *runneragent.AgentJob) (string, error) {
	switch {
	case a.CredentialKind == string(runner.CredAnthropicSubscription) && (a.Backend == "" || a.Backend == "claude-code"):
		return w.d.ClaudeBinary, nil
	case a.CredentialKind == string(runner.CredOpenAISubscription) && a.Backend == "codex":
		return w.d.CodexBinary, nil
	}
	return "", fmt.Errorf("%w: %s/%s", ErrUnsupportedAgentCredential, a.CredentialKind, a.Backend)
}

// Execute runs one workspace dispatch. Errors are classified into the
// result's Outcome (runner.ClassifyError); the text is the error's own
// (adapters sanitize before returning).
func (w *WorkspaceJobs) Execute(ctx context.Context, d runneragent.Dispatch) runneragent.Result {
	if err := w.check(d); err != nil {
		return jobFailure(err)
	}
	local, err := w.d.Syncer.Pull(ctx, *d.Workspace)
	if err != nil {
		return jobFailure(fmt.Errorf("pull: %w", err))
	}
	if err := w.d.Syncer.MarkDirty(ctx, *d.Workspace); err != nil {
		return jobFailure(err)
	}
	var res runneragent.Result
	switch d.Op {
	case runneragent.OpProcess:
		pr, err := w.runProcess(ctx, d.Process, local)
		if err != nil {
			return jobFailure(err)
		}
		res = runneragent.Result{OK: true, Process: &runneragent.ProcessResult{Stdout: pr.Stdout, Stderr: pr.Stderr, ExitCode: pr.ExitCode}}
		if pr.ExitCode != 0 {
			return res // completed but failed: nothing is pushed
		}
	case runneragent.OpAgent:
		ar, err := w.runAgent(ctx, d.Agent, local)
		if err != nil {
			return jobFailure(err)
		}
		if d.Agent.RequireExecutedModel {
			if err := VerifyExecutedModel(d.Agent.Model, ar); err != nil {
				return jobFailure(err)
			}
		}
		res = runneragent.Result{OK: true, Agent: toAgentResult(ar)}
	}
	if _, err := w.d.Syncer.Push(ctx, *d.Workspace); err != nil {
		return jobFailure(fmt.Errorf("push: %w", err))
	}
	return res
}

func (w *WorkspaceJobs) runProcess(ctx context.Context, p *runneragent.ProcessJob, local string) (engine.ProcessResult, error) {
	spec := engine.ProcessSpec{
		Kind: engine.ProcessKind(p.Kind), Command: p.Command, Args: p.Args, Stdin: p.Stdin,
		NodeEnv: p.NodeEnv, Passthrough: p.Passthrough, TimeoutSecs: p.TimeoutSecs, WorkDir: p.WorkDir,
	}
	switch spec.Kind {
	case engine.ProcessScript:
		dir, cleanup, err := w.d.Bundles.Unpack(ctx, p.BundleHash, p.BundleToken)
		if err != nil {
			return engine.ProcessResult{}, fmt.Errorf("bundle: %w", err)
		}
		defer cleanup()
		spec.Dir = dir // the script's own folder, exactly as on the server
		spec.Bundle = &engine.ScriptBundleRef{Hash: p.BundleHash, Entrypoint: p.BundleEntrypoint}
	case engine.ProcessToolCall:
		spec.Dir = local // the workspace copy
	default:
		return engine.ProcessResult{}, fmt.Errorf("unknown process kind %q", p.Kind)
	}
	return w.d.Process.RunProcess(ctx, spec)
}

func (w *WorkspaceJobs) runAgent(ctx context.Context, a *runneragent.AgentJob, local string) (*engine.AgentResult, error) {
	_, c, err := MatchNativeAgent(w.nativeSources(ctx), a, w.d.Rules)
	if err != nil {
		return nil, err
	}
	binding := provider.ResolvedBinding{
		Kind: provider.ProfileKindAgent, Provider: c.Provider, Model: a.Model, Backend: a.Backend,
		Pinned: a.RequireExecutedModel, AllowedTools: append([]string(nil), a.ToolScope...),
		Effort: provider.Effort(a.Effort), ContextWindow: provider.ContextWindow(a.ContextWindow),
	}
	rules := w.d.Rules // the same runner-local rules used to advertise capabilities
	mission := engine.AgentMission{
		Model: a.Model, Prompt: a.Prompt, WorkDir: local, ToolScope: a.ToolScope, SessionID: a.SessionID,
		Timeout: time.Duration(a.TimeoutSecs) * time.Second,
	}
	switch a.CredentialKind {
	case string(runner.CredAnthropicSubscription):
		ad := claudecli.NewAgentAdapterForBinding(claudecli.Config{
			BinaryPath: w.d.ClaudeBinary, WorkspaceRoot: w.d.Syncer.LocalRoot(), MaxResponseBytes: w.d.MaxResponseBytes,
			Runner: w.d.HarnessRunner, CapabilityRules: rules,
		}, binding)
		return ad.RunAgent(ctx, mission)
	case string(runner.CredOpenAISubscription):
		p := codex.New(codex.Config{Binding: binding, BinaryPath: w.d.CodexBinary, Runner: w.d.HarnessRunner,
			MaxResponseBytes: w.d.MaxResponseBytes, CapabilityRules: rules})
		return p.RunAgent(ctx, mission)
	}
	return nil, fmt.Errorf("%w: %s", ErrUnsupportedAgentCredential, a.CredentialKind)
}

func toAgentResult(r *engine.AgentResult) *runneragent.AgentResult {
	if r == nil {
		return nil
	}
	out := &runneragent.AgentResult{Summary: r.Summary, Model: r.Model, DroppedTools: r.DroppedTools,
		TokensIn: r.TokensIn, TokensOut: r.TokensOut, SessionID: r.SessionID}
	for _, s := range r.Steps {
		out.Steps = append(out.Steps, runneragent.AgentStep{Kind: s.Kind, Summary: s.Summary})
	}
	for _, e := range r.ModelEvidence {
		out.ModelEvidence = append(out.ModelEvidence, runneragent.ModelEvidence{Model: e.Model, Provider: e.Provider, Source: e.Source, Kind: string(e.Kind)})
	}
	return out
}

// jobFailure is named so it does not collide with executor.go's existing
// failure(runner.SourceOutcome, string) helper (P2 T9).
func jobFailure(err error) runneragent.Result {
	o := runner.ClassifyError(err)
	if o.Class == runner.OutcomeNone {
		o.Class = runner.OutcomeOther
	}
	return runneragent.Result{OK: false, ErrorText: err.Error(), Outcome: o}
}
