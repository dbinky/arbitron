// adapter/http/runner_translate.go
package http

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

func pullReceiptToDomain(w runnerproto.WirePullReceipt) (runner.PullReceiptPayload, error) {
	if err := w.Validate(); err != nil {
		return runner.PullReceiptPayload{}, malformedf("pull receipt: %v", err)
	}
	run, err := uuid.Parse(w.RunID)
	if err != nil {
		return runner.PullReceiptPayload{}, malformedf("pull receipt run id: %v", err)
	}
	ref, err := runner.ParseCheckpointRef(w.BaseRef)
	if err != nil {
		return runner.PullReceiptPayload{}, malformedf("pull receipt base ref: %v", err)
	}
	tok, err := uuid.Parse(w.FenceToken)
	if err != nil {
		return runner.PullReceiptPayload{}, malformedf("pull receipt fence token: %v", err)
	}
	return runner.PullReceiptPayload{OperationID: w.OperationID, RunID: run, Ref: ref, OK: w.OK, Error: w.Error,
		Resource: engine.ResourceFenceRef{Key: w.FenceKey, Owner: w.FenceOwner, Generation: w.FenceGeneration, Token: tok}}, nil
}

func malformedf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{runner.ErrMalformedMessage}, args...)...)
}

func parseJob(e runnerproto.Envelope) (uuid.UUID, error) {
	id, err := uuid.Parse(e.JobID)
	if err != nil || id == uuid.Nil || e.Generation < 1 {
		return uuid.Nil, malformedf("%s needs a job id and generation", e.Kind)
	}
	return id, nil
}

// toInbound maps one post-admission frame to the hub's domain message.
func toInbound(e runnerproto.Envelope) (runner.Inbound, error) {
	switch e.Kind {
	case runnerproto.KindPullReceipt:
		var w runnerproto.WirePullReceipt
		if err := runnerproto.DecodePayload(e, &w); err != nil {
			return runner.Inbound{}, malformedf("pull receipt: %v", err)
		}
		rc, err := pullReceiptToDomain(w)
		if err != nil {
			return runner.Inbound{}, err
		}
		return runner.Inbound{Kind: runner.MsgPullReceipt, PullReceipt: &rc}, nil
	case runnerproto.KindHeartbeat:
		var hb runnerproto.Heartbeat
		if err := runnerproto.DecodePayload(e, &hb); err != nil {
			return runner.Inbound{}, malformedf("heartbeat: %v", err)
		}
		info, err := heartbeatInfo(hb)
		if err != nil {
			return runner.Inbound{}, err
		}
		return runner.Inbound{Kind: runner.MsgHeartbeat, Heartbeat: &info}, nil
	case runnerproto.KindProbeResult:
		var pr runnerproto.ProbeResult
		if err := runnerproto.DecodePayload(e, &pr); err != nil {
			return runner.Inbound{}, malformedf("probe_result: %v", err)
		}
		id, err := uuid.Parse(pr.RequestID)
		if err != nil || id == uuid.Nil {
			return runner.Inbound{}, malformedf("probe_result needs a request id")
		}
		src := sourcesToDomain([]runnerproto.WireSource{pr.Source})[0]
		return runner.Inbound{Kind: runner.MsgProbeResult, ProbeResult: &runner.ProbeReply{RequestID: id, HealthRevision: pr.HealthRevision, Source: src}}, nil
	case runnerproto.KindAccept:
		id, err := parseJob(e)
		if err != nil {
			return runner.Inbound{}, err
		}
		return runner.Inbound{Kind: runner.MsgAccept, JobID: id, Generation: e.Generation, Accept: true}, nil
	case runnerproto.KindReject:
		id, err := parseJob(e)
		if err != nil {
			return runner.Inbound{}, err
		}
		var rj runnerproto.Reject
		if len(e.Payload) > 0 {
			if err := runnerproto.DecodePayload(e, &rj); err != nil {
				return runner.Inbound{}, malformedf("reject: %v", err)
			}
		}
		return runner.Inbound{Kind: runner.MsgReject, JobID: id, Generation: e.Generation, RejectReason: rj.Reason, RejectCode: rj.Code}, nil
	case runnerproto.KindOutput:
		id, err := parseJob(e)
		if err != nil {
			return runner.Inbound{}, err
		}
		var o runnerproto.Output
		if err := runnerproto.DecodePayload(e, &o); err != nil {
			return runner.Inbound{}, malformedf("output: %v", err)
		}
		if o.Seq != e.Seq {
			return runner.Inbound{}, malformedf("output envelope seq %d != payload seq %d", e.Seq, o.Seq)
		}
		return runner.Inbound{Kind: runner.MsgOutput, JobID: id, Generation: e.Generation, Seq: o.Seq,
			Output: &runner.OutputFrame{Seq: o.Seq, Token: o.Token}}, nil
	case runnerproto.KindResult:
		id, err := parseJob(e)
		if err != nil {
			return runner.Inbound{}, err
		}
		var res runnerproto.Result
		if err := runnerproto.DecodePayload(e, &res); err != nil {
			return runner.Inbound{}, malformedf("result: %v", err)
		}
		rr := resultToDomain(res)
		return runner.Inbound{Kind: runner.MsgResult, JobID: id, Generation: e.Generation, Result: &rr}, nil
	default:
		return runner.Inbound{}, malformedf("unexpected %q after admission", e.Kind)
	}
}

func heartbeatInfo(hb runnerproto.Heartbeat) (runner.HeartbeatInfo, error) {
	jobs, err := heldJobs(hb.Jobs)
	if err != nil {
		return runner.HeartbeatInfo{}, err
	}
	info := runner.HeartbeatInfo{Telemetry: telemetryToDomain(hb.Telemetry), TelemetryAgeMs: hb.Telemetry.SampleAgeMs, Jobs: jobs}
	if hb.Sources != nil {
		info.Sources = sourcesToDomain(hb.Sources)
	}
	return info, nil
}

// helloInfo maps the admission hello.
func helloInfo(h runnerproto.Hello) (runner.HelloInfo, error) {
	held, err := heldJobs(h.Held)
	if err != nil {
		return runner.HelloInfo{}, err
	}
	receipts := make([]runner.PullReceiptPayload, 0, len(h.PullReceipts))
	for _, w := range h.PullReceipts {
		rc, err := pullReceiptToDomain(w)
		if err != nil {
			return runner.HelloInfo{}, err
		}
		receipts = append(receipts, rc)
	}
	copies, err := fromWireCopies(h.Copies)
	if err != nil {
		return runner.HelloInfo{}, err
	}
	return runner.HelloInfo{Copies: copies, PullReceipts: receipts, OS: h.OS, Arch: h.Arch, ProtocolVersion: h.ProtocolVersion, RunnerVersion: h.RunnerVersion,
		HarnessVersions: h.HarnessVersions, WorkspaceRoot: h.WorkspaceRoot, ScratchRoot: h.ScratchRoot,
		Capabilities: h.Capabilities, Sources: sourcesToDomain(h.Sources), Held: held}, nil
}

func heldJobs(in []runnerproto.HeldJob) ([]runner.HeldJobInfo, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]runner.HeldJobInfo, 0, len(in))
	for _, j := range in {
		id, err := uuid.Parse(j.JobID)
		if err != nil {
			return nil, malformedf("held job id %q", j.JobID)
		}
		out = append(out, runner.HeldJobInfo{JobID: id, Generation: j.Generation, State: j.State, LastOutputSeq: j.LastOutputSeq})
	}
	return out, nil
}

func sourcesToDomain(in []runnerproto.WireSource) []runner.SourceReport {
	out := make([]runner.SourceReport, 0, len(in))
	for _, s := range in {
		fam := make([]modelidentity.Family, 0, len(s.Families))
		for _, f := range s.Families {
			fam = append(fam, modelidentity.Family(f))
		}
		out = append(out, runner.SourceReport{Kind: runner.CredentialKind(s.Kind), CredentialRef: s.CredentialRef,
			Families: fam, Backends: s.Backends, Models: s.Models, Health: healthToDomain(s.Health),
			Observation: observationToDomain(s.Observation), ObservationSeq: s.ObservationSeq, SampleAgeMs: s.SampleAgeMs,
			UnavailableReason: s.UnavailableReason, Capabilities: capabilitiesToDomain(s.Capabilities)})
	}
	return out
}

func capabilitiesToDomain(in []runnerproto.ExecutionCapability) []runner.ExecutionCapability {
	var out []runner.ExecutionCapability
	for _, c := range in {
		cap := runner.ExecutionCapability{ToolPolicy: runner.ToolPolicy(c.ToolPolicy), Op: runner.OpType(c.Op), Backend: c.Backend, Provider: c.Provider, EndpointFingerprint: c.EndpointFingerprint, Models: c.Models, Tools: c.Tools, ExecutedModel: c.ExecutedModel}
		for _, f := range c.Families {
			cap.Families = append(cap.Families, modelidentity.Family(f))
		}
		for _, w := range c.ContextWindows {
			cap.ContextWindows = append(cap.ContextWindows, provider.ContextWindow(w))
		}
		for _, e := range c.Efforts {
			cap.Efforts = append(cap.Efforts, provider.Effort(e))
		}
		out = append(out, cap)
	}
	return out
}

func observationToDomain(o *runnerproto.WireProbeObservation) *runner.ProbeResult {
	if o == nil {
		return nil
	}
	out := &runner.ProbeResult{HealthRevision: o.HealthRevision, Auth: runner.AuthState(o.Auth)}
	if c := o.Capacity; c != nil {
		out.Capacity = &runner.CapacityObservation{ModelScope: c.ModelScope, State: runner.CapacityState(c.State), ResetAt: c.ResetAt, Headroom: c.Headroom, Windows: windowsToDomain(c.Windows)}
		if c.OrdinaryUsageAllowed != nil {
			v := *c.OrdinaryUsageAllowed
			out.Capacity.OrdinaryUsageAllowed = &v
		}
	}
	return out
}

func healthToDomain(h runnerproto.WireHealth) runner.SourceHealth {
	auth := runner.AuthState(h.Auth)
	if auth == "" {
		auth = runner.AuthUnknown
	}
	capacity := runner.CapacityState(h.Capacity)
	if capacity == "" {
		capacity = runner.CapacityAvailable
	}
	return runner.SourceHealth{Auth: auth, Capacity: capacity, ResetAt: h.ResetAt, Headroom: h.Headroom,
		Windows: windowsToDomain(h.Windows), Congested: h.Congested, CongestedUntil: h.CongestedUntil,
		LastProbeAt: h.LastProbeAt, LastOutcome: runner.OutcomeClass(h.LastOutcome), Detail: h.Detail}
}

func windowsToDomain(in []runnerproto.WireWindow) []runner.LimitWindow {
	if len(in) == 0 {
		return nil
	}
	out := make([]runner.LimitWindow, 0, len(in))
	for _, w := range in {
		out = append(out, runner.LimitWindow{LimitID: w.LimitID, ModelScope: w.ModelScope, DurationMins: w.DurationMins,
			UsedPercent: w.UsedPercent, ResetAt: w.ResetAt})
	}
	return out
}

func telemetryToDomain(t runnerproto.WireTelemetry) runner.Telemetry {
	return runner.Telemetry{SampledAt: t.SampledAt, CPUPercent: t.CPUPercent, MemUsedBytes: t.MemUsedBytes,
		MemPressure: t.MemPressure, Load1Pct: t.Load1Pct, DiskFreeBytes: t.DiskFreeBytes, Thermal: t.Thermal,
		UptimeSeconds: t.UptimeSeconds, Cores: t.Cores, RAMBytes: t.RAMBytes}
}

func resultToDomain(r runnerproto.Result) runner.RemoteResult {
	out := runner.RemoteResult{Process: fromWireProcessResult(r.Process), Agent: fromWireAgentResult(r.Agent), OK: r.OK, ErrorText: r.ErrorText, Outcome: runner.SourceOutcome{
		Class: runner.OutcomeClass(r.Outcome.Class), ResetAt: r.Outcome.ResetAt,
		RetryAfter: time.Duration(r.Outcome.RetryAfterMs) * time.Millisecond,
		Windows:    windowsToDomain(r.Outcome.Windows), HeadroomHint: r.Outcome.HeadroomHint}}
	if r.Confirmation != nil {
		out.Confirmation = &runner.ConfirmationResult{RequestedModel: r.Confirmation.RequestedModel, ExecutedModel: r.Confirmation.ExecutedModel, SessionID: r.Confirmation.SessionID}
	}
	if r.Chat != nil {
		out.Chat = &runner.RemoteChatResult{Output: r.Chat.Output, Conversation: r.Chat.Conversation, Model: r.Chat.Model,
			TokensIn: r.Chat.TokensIn, TokensOut: r.Chat.TokensOut, SessionID: r.Chat.SessionID}
	}
	return out
}

// fromOutbound maps a hub message to a frame. id/heartbeatSeconds fill admitted.
func fromOutbound(m runner.Outbound, id runner.RunnerID, heartbeatSeconds int) (runnerproto.Envelope, error) {
	jobID := m.JobID.String()
	switch m.Kind {
	case runner.MsgDeleteCopy:
		if m.DeleteCopy == nil {
			return runnerproto.Envelope{}, fmt.Errorf("runner connect: delete_copy without payload")
		}
		return runnerproto.NewEnvelope(runnerproto.KindDeleteCopy, toWireDeleteCopy(m.DeleteCopy))
	case runner.MsgPullWorkspace:
		if m.PullWorkspace == nil {
			return runnerproto.Envelope{}, fmt.Errorf("runner connect: pull_workspace without payload")
		}
		p := m.PullWorkspace
		return runnerproto.NewEnvelope(runnerproto.KindPullWorkspace, runnerproto.PullWorkspace{OperationID: p.OperationID, RunID: p.RunID.String(),
			WorkDir: p.WorkDir, HomeGeneration: p.HomeGeneration, CheckpointVersion: p.Ref.Version, ManifestHash: p.Ref.ManifestHash,
			BaseRef: p.Ref.String(), FenceKey: p.Resource.Key, FenceOwner: p.Resource.Owner, FenceGeneration: p.Resource.Generation,
			FenceToken: p.Resource.Token.String(), PullToken: p.PullToken, PartBytes: p.PartBytes})
	case runner.MsgPullReceiptAck:
		return runnerproto.NewEnvelope(runnerproto.KindPullReceiptAck, runnerproto.PullReceiptAck{OperationID: m.PullReceiptAck})
	case runner.MsgAdmitted:
		a := &runnerproto.Admitted{RunnerID: string(id), HeartbeatSeconds: heartbeatSeconds, ProtocolVersion: runnerproto.ProtocolVersion}
		if m.Admitted != nil {
			a.ClaudeModels = append([]string(nil), m.Admitted.ClaudeModels...)
		}
		return runnerproto.NewEnvelope(runnerproto.KindAdmitted, a)
	case runner.MsgProbe:
		if m.Probe == nil {
			return runnerproto.Envelope{}, fmt.Errorf("runner connect: probe without payload")
		}
		return runnerproto.NewEnvelope(runnerproto.KindProbe, &runnerproto.Probe{RequestID: m.Probe.RequestID.String(), HealthRevision: m.Probe.HealthRevision, SourceKind: string(m.Probe.SourceKind), CredentialRef: m.Probe.CredentialRef})
	case runner.MsgDispatch:
		if m.Dispatch == nil {
			return runnerproto.Envelope{}, fmt.Errorf("runner connect: dispatch without payload")
		}
		return runnerproto.NewJobEnvelope(runnerproto.KindDispatch, jobID, m.Generation, 0, dispatchToWire(*m.Dispatch))
	case runner.MsgAck:
		if m.Ack == nil {
			return runnerproto.Envelope{}, fmt.Errorf("runner connect: ack without payload")
		}
		return runnerproto.NewJobEnvelope(runnerproto.KindAck, jobID, m.Generation, 0, &runnerproto.Ack{Seq: m.Ack.Seq, Result: m.Ack.Result})
	case runner.MsgFenced:
		return runnerproto.NewJobEnvelope(runnerproto.KindFenced, jobID, m.Generation, 0, &runnerproto.Fenced{Reason: "stale generation"})
	case runner.MsgCancel:
		return runnerproto.NewJobEnvelope(runnerproto.KindCancel, jobID, m.Generation, 0, &runnerproto.Cancel{Reason: "cancelled by server"})
	default:
		return runnerproto.Envelope{}, fmt.Errorf("runner connect: cannot send %q", m.Kind)
	}
}

// dispatchToWire copies the allowlisted operation and workspace descriptors.
// Credential material remains local to each runner.
func dispatchToWire(d runner.DispatchPayload) *runnerproto.Dispatch {
	out := &runnerproto.Dispatch{Op: string(d.Op), LeaseUntil: d.LeaseUntil,
		Process: toWireProcess(d.Process), Agent: toWireAgent(d.Agent), Workspace: toWireWorkspace(d.Workspace)}
	if c := d.Confirmation; c != nil {
		out.Confirmation = &runnerproto.WireConfirmation{Model: c.Model, Backend: c.Backend, Provider: c.Provider, EndpointFingerprint: c.EndpointFingerprint, ContextWindow: c.ContextWindow, Effort: c.Effort}
	}
	if c := d.Chat; c != nil {
		out.Chat = &runnerproto.WireChat{Provider: c.Provider, EndpointFingerprint: c.EndpointFingerprint, Model: c.Model, SystemPrompt: c.SystemPrompt, Prompt: c.Prompt, MaxTokens: c.MaxTokens,
			Temperature: c.Temperature, OutputSchema: c.OutputSchema, SessionID: c.SessionID,
			RequireExecutedModel: c.RequireExecutedModel, Stream: c.Stream, CredentialKind: string(c.CredentialKind),
			Backend: c.Backend, ContextWindow: c.ContextWindow, Effort: c.Effort, Tools: c.Tools}
	}
	return out
}
