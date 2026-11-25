package http

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
)

func toWireProcess(p *runner.ProcessDispatch) *runnerproto.WireProcess {
	if p == nil {
		return nil
	}
	return &runnerproto.WireProcess{
		Kind: string(p.Kind), Command: p.Command, Args: p.Args, Dir: p.Dir, Stdin: p.Stdin,
		NodeEnv: p.NodeEnv, Passthrough: p.Passthrough, TimeoutSecs: p.TimeoutSecs, WorkDir: p.WorkDir,
		BundleHash: p.BundleHash, BundleEntrypoint: p.BundleEntrypoint, BundleToken: p.BundleToken, Requires: append([]string(nil), p.Requires...),
	}
}

func toWireAgent(a *runner.AgentDispatch) *runnerproto.WireAgent {
	if a == nil {
		return nil
	}
	return &runnerproto.WireAgent{
		Model: a.Model, Provider: a.Provider, Prompt: a.Prompt, WorkDir: a.WorkDir, ToolScope: a.ToolScope, SessionID: a.SessionID,
		TimeoutSecs: a.TimeoutSecs, CredentialKind: string(a.CredentialKind), Backend: a.Backend,
		Effort: a.Effort, ContextWindow: a.ContextWindow, RequireExecutedModel: a.RequireExecutedModel,
	}
}

func toWireWorkspace(w *runner.WorkspaceDispatch) *runnerproto.WireWorkspace {
	if w == nil {
		return nil
	}
	return &runnerproto.WireWorkspace{
		RunID: w.RunID.String(), WorkDir: w.WorkDir, HomeGeneration: w.HomeGeneration,
		CheckpointVersion: w.CheckpointVersion, ManifestHash: w.ManifestHash, BaseRef: w.BaseRef,
		FenceKey: w.Resource.Key, FenceOwner: w.Resource.Owner, FenceGeneration: w.Resource.Generation, FenceToken: w.Resource.Token.String(),
		PullToken: w.PullToken, PushToken: w.PushToken, PartBytes: w.PartBytes,
	}
}

func fromWireProcessResult(r *runnerproto.WireProcessResult) *engine.ProcessResult {
	if r == nil {
		return nil
	}
	return &engine.ProcessResult{Stdout: r.Stdout, Stderr: r.Stderr, ExitCode: r.ExitCode}
}

func fromWireAgentResult(r *runnerproto.WireAgentResult) *engine.AgentResult {
	if r == nil {
		return nil
	}
	out := &engine.AgentResult{
		Summary: r.Summary, Model: r.Model, DroppedTools: r.DroppedTools,
		TokensIn: r.TokensIn, TokensOut: r.TokensOut, SessionID: r.SessionID,
	}
	for _, s := range r.Steps {
		out.Steps = append(out.Steps, engine.AgentStep{Kind: s.Kind, Summary: s.Summary})
	}
	for _, e := range r.ModelEvidence {
		out.ModelEvidence = append(out.ModelEvidence, engine.ModelEvidence{
			Model: e.Model, Provider: e.Provider, Source: e.Source, Kind: engine.EvidenceKind(e.Kind),
		})
	}
	return out
}
