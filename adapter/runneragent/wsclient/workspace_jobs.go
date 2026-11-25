package wsclient

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
)

// WorkspaceJobsFromWire maps the Phase 3 dispatch payloads onto the
// runner-agent domain mirrors. The mirrors have identical field lists, so the
// struct conversions are checked by the compiler.
func WorkspaceJobsFromWire(d runnerproto.Dispatch) (*runneragent.ProcessJob, *runneragent.AgentJob, *runneragent.WorkspaceJob) {
	var p *runneragent.ProcessJob
	var a *runneragent.AgentJob
	var w *runneragent.WorkspaceJob
	if d.Process != nil {
		v := runneragent.ProcessJob(*d.Process)
		p = &v
	}
	if d.Agent != nil {
		v := runneragent.AgentJob(*d.Agent)
		a = &v
	}
	if d.Workspace != nil {
		v := runneragent.WorkspaceJob(*d.Workspace)
		w = &v
	}
	return p, a, w
}

// WorkspaceResultsToWire maps process/agent results back to the wire.
func WorkspaceResultsToWire(r runneragent.Result) (*runnerproto.WireProcessResult, *runnerproto.WireAgentResult) {
	var pr *runnerproto.WireProcessResult
	var ar *runnerproto.WireAgentResult
	if r.Process != nil {
		v := runnerproto.WireProcessResult(*r.Process)
		pr = &v
	}
	if r.Agent != nil {
		ar = &runnerproto.WireAgentResult{Summary: r.Agent.Summary, Model: r.Agent.Model, DroppedTools: r.Agent.DroppedTools,
			TokensIn: r.Agent.TokensIn, TokensOut: r.Agent.TokensOut, SessionID: r.Agent.SessionID}
		for _, s := range r.Agent.Steps {
			ar.Steps = append(ar.Steps, runnerproto.WireAgentStep{Kind: s.Kind, Summary: s.Summary})
		}
		for _, e := range r.Agent.ModelEvidence {
			ar.ModelEvidence = append(ar.ModelEvidence, runnerproto.WireModelEvidence{Model: e.Model, Provider: e.Provider, Source: e.Source, Kind: e.Kind})
		}
	}
	return pr, ar
}
