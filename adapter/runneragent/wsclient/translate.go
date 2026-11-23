package wsclient

import (
	"encoding/base64"
	"fmt"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

func wireWindows(ws []runner.LimitWindow) []runnerproto.WireWindow {
	if len(ws) == 0 {
		return nil
	}
	out := make([]runnerproto.WireWindow, len(ws))
	for i, w := range ws {
		out[i] = runnerproto.WireWindow{LimitID: w.LimitID, ModelScope: w.ModelScope, DurationMins: w.DurationMins, UsedPercent: w.UsedPercent, ResetAt: w.ResetAt}
	}
	return out
}

func wireHealth(h runner.SourceHealth) runnerproto.WireHealth {
	return runnerproto.WireHealth{
		Auth: string(h.Auth), Capacity: string(h.Capacity), ResetAt: h.ResetAt, Headroom: h.Headroom,
		Windows: wireWindows(h.Windows), Congested: h.Congested, CongestedUntil: h.CongestedUntil,
		LastProbeAt: h.LastProbeAt, LastOutcome: string(h.LastOutcome), Detail: h.Detail,
	}
}

func wireSource(s runneragent.Source) runnerproto.WireSource {
	fams := make([]string, len(s.Families))
	for i, f := range s.Families {
		fams[i] = string(f)
	}
	return runnerproto.WireSource{Kind: string(s.Kind), CredentialRef: s.CredentialRef, Families: fams, Backends: s.Backends, Models: s.Models, Health: wireHealth(s.Health)}
}

func wireSources(ss []runneragent.Source) []runnerproto.WireSource {
	if len(ss) == 0 {
		return nil
	}
	out := make([]runnerproto.WireSource, len(ss))
	for i, s := range ss {
		out[i] = wireSource(s)
	}
	return out
}

func wireHeld(hs []runneragent.HeldJob) []runnerproto.HeldJob {
	if len(hs) == 0 {
		return nil
	}
	out := make([]runnerproto.HeldJob, len(hs))
	for i, h := range hs {
		out[i] = runnerproto.HeldJob{JobID: h.JobID, Generation: h.Generation, State: h.State, LastOutputSeq: h.LastOutputSeq}
	}
	return out
}

func wireTelemetry(t runner.Telemetry) runnerproto.WireTelemetry {
	return runnerproto.WireTelemetry{
		SampledAt: t.SampledAt, CPUPercent: t.CPUPercent, MemUsedBytes: t.MemUsedBytes, MemPressure: t.MemPressure,
		Load1Pct: t.Load1Pct, DiskFreeBytes: t.DiskFreeBytes, Thermal: t.Thermal, UptimeSeconds: t.UptimeSeconds,
		Cores: t.Cores, RAMBytes: t.RAMBytes,
	}
}

func wireOutcome(o runner.SourceOutcome) runnerproto.WireOutcome {
	return runnerproto.WireOutcome{Class: string(o.Class), ResetAt: o.ResetAt, RetryAfterMs: o.RetryAfter.Milliseconds(), Windows: wireWindows(o.Windows), HeadroomHint: o.HeadroomHint}
}

// toEnvelope translates a runner domain message into a protocol frame.
func toEnvelope(m runneragent.Outbound) (runnerproto.Envelope, error) {
	switch m.Kind {
	case runneragent.KindChallengeResponse:
		if m.ChallengeResponse == nil {
			break
		}
		return runnerproto.NewEnvelope(runnerproto.KindChallengeResponse, runnerproto.ChallengeResponse{
			PublicKey: base64.StdEncoding.EncodeToString(m.ChallengeResponse.PublicKey),
			Signature: base64.StdEncoding.EncodeToString(m.ChallengeResponse.Signature),
		})
	case runneragent.KindHello:
		if m.Hello == nil {
			break
		}
		h := m.Hello
		return runnerproto.NewEnvelope(runnerproto.KindHello, runnerproto.Hello{
			OS: h.OS, Arch: h.Arch, ProtocolVersion: h.ProtocolVersion, RunnerVersion: h.RunnerVersion,
			HarnessVersions: h.HarnessVersions, WorkspaceRoot: h.WorkspaceRoot, ScratchRoot: h.ScratchRoot,
			Capabilities: h.Capabilities, Sources: wireSources(h.Sources), Held: wireHeld(h.Held),
		})
	case runneragent.KindHeartbeat:
		if m.Heartbeat == nil {
			break
		}
		return runnerproto.NewEnvelope(runnerproto.KindHeartbeat, runnerproto.Heartbeat{
			Telemetry: wireTelemetry(m.Heartbeat.Telemetry), Sources: wireSources(m.Heartbeat.Sources), Jobs: wireHeld(m.Heartbeat.Jobs),
		})
	case runneragent.KindAccept:
		return runnerproto.NewJobEnvelope(runnerproto.KindAccept, m.JobID, m.Generation, 0, nil)
	case runneragent.KindReject:
		return runnerproto.NewJobEnvelope(runnerproto.KindReject, m.JobID, m.Generation, 0, runnerproto.Reject{Reason: m.RejectReason})
	case runneragent.KindOutput:
		if m.Output == nil {
			break
		}
		return runnerproto.NewJobEnvelope(runnerproto.KindOutput, m.JobID, m.Generation, m.Output.Seq, runnerproto.Output{Seq: m.Output.Seq, Token: m.Output.Token})
	case runneragent.KindResult:
		if m.Result == nil {
			break
		}
		r := runnerproto.Result{OK: m.Result.OK, Outcome: wireOutcome(m.Result.Outcome), ErrorText: m.Result.ErrorText}
		if m.Result.OK && m.Result.Chat != nil {
			c := m.Result.Chat
			r.Chat = &runnerproto.WireChatResult{Output: c.Output, Model: c.Model, TokensIn: c.TokensIn, TokensOut: c.TokensOut, SessionID: c.SessionID}
		}
		return runnerproto.NewJobEnvelope(runnerproto.KindResult, m.JobID, m.Generation, 0, r)
	case runneragent.KindProbeResult:
		if m.ProbeResult == nil {
			break
		}
		return runnerproto.NewEnvelope(runnerproto.KindProbeResult, runnerproto.ProbeResult{Source: wireSource(*m.ProbeResult)})
	}
	return runnerproto.Envelope{}, fmt.Errorf("wsclient: cannot encode %q message", m.Kind)
}

// fromEnvelope translates a server frame into the runner domain mirror.
// Kinds the runner does not handle pass through with only their envelope
// fields so the agent can ignore them.
func fromEnvelope(e runnerproto.Envelope) (runneragent.Inbound, error) {
	in := runneragent.Inbound{Kind: e.Kind, JobID: e.JobID, Generation: e.Generation}
	switch e.Kind {
	case runnerproto.KindChallenge:
		var c runnerproto.Challenge
		if err := runnerproto.DecodePayload(e, &c); err != nil {
			return runneragent.Inbound{}, err
		}
		nonce, err := base64.StdEncoding.DecodeString(c.Nonce)
		if err != nil {
			return runneragent.Inbound{}, fmt.Errorf("%w: challenge nonce", runnerproto.ErrMalformed)
		}
		in.Challenge = &runneragent.Challenge{KeyID: c.KeyID, Nonce: nonce}
	case runnerproto.KindAdmitted:
		var a runnerproto.Admitted
		if err := runnerproto.DecodePayload(e, &a); err != nil {
			return runneragent.Inbound{}, err
		}
		in.Admitted = &runneragent.Admission{RunnerID: a.RunnerID, HeartbeatSeconds: a.HeartbeatSeconds}
	case runnerproto.KindRefused:
		var r runnerproto.Refused
		if err := runnerproto.DecodePayload(e, &r); err != nil {
			return runneragent.Inbound{}, err
		}
		in.Refused = &runneragent.Refusal{Code: r.Code, Reason: r.Reason}
	case runnerproto.KindDispatch:
		var d runnerproto.Dispatch
		if err := runnerproto.DecodePayload(e, &d); err != nil {
			return runneragent.Inbound{}, err
		}
		c := d.Chat
		in.Dispatch = &runneragent.Dispatch{JobID: e.JobID, Generation: e.Generation, Op: d.Op, LeaseUntil: d.LeaseUntil, Chat: &runneragent.ChatJob{
			Model: c.Model, SystemPrompt: c.SystemPrompt, Prompt: c.Prompt, MaxTokens: c.MaxTokens, Temperature: c.Temperature,
			OutputSchema: c.OutputSchema, SessionID: c.SessionID, RequireExecutedModel: c.RequireExecutedModel, Stream: c.Stream,
			CredentialKind: c.CredentialKind, Backend: c.Backend, ContextWindow: c.ContextWindow, Effort: c.Effort, Tools: c.Tools,
		}}
	case runnerproto.KindAck:
		var a runnerproto.Ack
		if err := runnerproto.DecodePayload(e, &a); err != nil {
			return runneragent.Inbound{}, err
		}
		in.Seq, in.AckResult = a.Seq, a.Result
	case runnerproto.KindProbe:
		var p runnerproto.Probe
		if err := runnerproto.DecodePayload(e, &p); err != nil {
			return runneragent.Inbound{}, err
		}
		in.Probe = &runneragent.ProbeRequest{SourceKind: runner.CredentialKind(p.SourceKind), CredentialRef: p.CredentialRef}
	}
	return in, nil
}
