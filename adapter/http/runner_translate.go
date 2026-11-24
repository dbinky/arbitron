// adapter/http/runner_translate.go
package http

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

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
		return runner.Inbound{Kind: runner.MsgReject, JobID: id, Generation: e.Generation, RejectReason: rj.Reason}, nil
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
	return runner.HelloInfo{OS: h.OS, Arch: h.Arch, ProtocolVersion: h.ProtocolVersion, RunnerVersion: h.RunnerVersion,
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
	out := runner.RemoteResult{OK: r.OK, ErrorText: r.ErrorText, Outcome: runner.SourceOutcome{
		Class: runner.OutcomeClass(r.Outcome.Class), ResetAt: r.Outcome.ResetAt,
		RetryAfter: time.Duration(r.Outcome.RetryAfterMs) * time.Millisecond,
		Windows:    windowsToDomain(r.Outcome.Windows), HeadroomHint: r.Outcome.HeadroomHint}}
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
	case runner.MsgAdmitted:
		return runnerproto.NewEnvelope(runnerproto.KindAdmitted, &runnerproto.Admitted{RunnerID: string(id),
			HeartbeatSeconds: heartbeatSeconds, ProtocolVersion: runnerproto.ProtocolVersion})
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

// dispatchToWire copies exactly the hermetic chat fields; the domain payload
// carries no secrets, and neither does the wire type (P2 T1 reflection test).
func dispatchToWire(d runner.DispatchPayload) *runnerproto.Dispatch {
	out := &runnerproto.Dispatch{Op: string(d.Op), LeaseUntil: d.LeaseUntil}
	if c := d.Chat; c != nil {
		out.Chat = &runnerproto.WireChat{Provider: c.Provider, EndpointFingerprint: c.EndpointFingerprint, Model: c.Model, SystemPrompt: c.SystemPrompt, Prompt: c.Prompt, MaxTokens: c.MaxTokens,
			Temperature: c.Temperature, OutputSchema: c.OutputSchema, SessionID: c.SessionID,
			RequireExecutedModel: c.RequireExecutedModel, Stream: c.Stream, CredentialKind: string(c.CredentialKind),
			Backend: c.Backend, ContextWindow: c.ContextWindow, Effort: c.Effort, Tools: c.Tools}
	}
	return out
}
