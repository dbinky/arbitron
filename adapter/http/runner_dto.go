package http

import (
	"encoding/json"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
)

// Runner pool DTOs (contracts §14). adapter/mcp/runner_dto.go mirrors these
// json tags exactly; TestRunnerParity_RESTAndMCP keeps them identical.

type runnerCompatResponse struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type sourceHealthResponse struct {
	Auth           string   `json:"auth"`
	Capacity       string   `json:"capacity"`
	ResetAt        *string  `json:"resetAt"`
	Headroom       *float64 `json:"headroom"`
	Congested      bool     `json:"congested"`
	CongestedUntil *string  `json:"congestedUntil"`
	LastProbeAt    *string  `json:"lastProbeAt"`
	LastOutcome    string   `json:"lastOutcome"`
}

type runnerSourceResponse struct {
	ID                string               `json:"id"`
	Kind              string               `json:"kind"`
	CredentialRef     string               `json:"credentialRef"`
	UnavailableReason string               `json:"unavailableReason"`
	Families          []string             `json:"families"`
	Backends          []string             `json:"backends"`
	Models            []string             `json:"models"`
	Health            sourceHealthResponse `json:"health"`
}

type runnerTelemetryResponse struct {
	SampledAt     *string  `json:"sampledAt"`
	CPUPercent    *float64 `json:"cpuPercent"`
	MemUsedBytes  *uint64  `json:"memUsedBytes"`
	MemPressure   *string  `json:"memPressure"`
	Load1Pct      *float64 `json:"load1Pct"`
	DiskFreeBytes *uint64  `json:"diskFreeBytes"`
	Thermal       *string  `json:"thermal"`
	UptimeSeconds *uint64  `json:"uptimeSeconds"`
	Cores         *int     `json:"cores"`
	RAMBytes      *uint64  `json:"ramBytes"`
}

type runnerViewResponse struct {
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	BuiltIn         bool                     `json:"builtIn"`
	StepLimit       int                      `json:"stepLimit"`
	Labels          map[string]string        `json:"labels"`
	Cordoned        bool                     `json:"cordoned"`
	Draining        bool                     `json:"draining"`
	Compat          runnerCompatResponse     `json:"compat"`
	Liveness        string                   `json:"liveness"`
	LastContact     *string                  `json:"lastContact"`
	OS              string                   `json:"os"`
	RunnerVersion   string                   `json:"runnerVersion"`
	HarnessVersions map[string]string        `json:"harnessVersions"`
	SlotsUsed       int                      `json:"slotsUsed"`
	Queued          int                      `json:"queued"`
	HomedWorkspaces int                      `json:"homedWorkspaces"`
	Sources         []runnerSourceResponse   `json:"sources"`
	Telemetry       *runnerTelemetryResponse `json:"telemetry"`
	Drain           json.RawMessage          `json:"drain"` // P3 T12 fills; null in P1
}

type assignmentResponse struct {
	JobID    string  `json:"jobId"`
	RunID    *string `json:"runId"`
	NodeID   string  `json:"nodeId"`
	State    string  `json:"state"`
	SourceID string  `json:"sourceId"`
	Hidden   bool    `json:"hidden"`
}

type runnerDetailResponse struct {
	runnerViewResponse
	Assignments     []assignmentResponse      `json:"assignments"`
	RecentTelemetry []runnerTelemetryResponse `json:"recentTelemetry"`
	Sessions        []json.RawMessage         `json:"sessions"` // P4 T14 replaces with []sessionPinResponse
	Homes           []json.RawMessage         `json:"homes"`    // P3 T14 replaces with []homeResponse
}

func runnerTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func toRunnerViewResponse(v runner.RunnerView) runnerViewResponse {
	r := v.Runner
	out := runnerViewResponse{
		ID: string(r.ID), Name: r.Name, BuiltIn: r.BuiltIn, StepLimit: r.StepLimit, Labels: r.Labels,
		Cordoned: r.Cordoned, Draining: r.Draining,
		Compat:   runnerCompatResponse{State: string(r.Compat.State), Reason: r.Compat.Reason},
		Liveness: string(r.Liveness), LastContact: runnerTimePtr(r.LastContact),
		OS: r.OS, RunnerVersion: r.RunnerVersion, HarnessVersions: r.HarnessVersions,
		SlotsUsed: v.SlotsUsed, Queued: v.Queued, HomedWorkspaces: v.HomedWorkspaces,
		Sources: make([]runnerSourceResponse, 0, len(v.Sources)),
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	if out.HarnessVersions == nil {
		out.HarnessVersions = map[string]string{}
	}
	for _, s := range v.Sources {
		out.Sources = append(out.Sources, toRunnerSourceResponse(s))
	}
	if v.Telemetry != nil {
		t := toRunnerTelemetryResponse(*v.Telemetry)
		out.Telemetry = &t
	}
	return out
}

func toRunnerSourceResponse(s runner.Source) runnerSourceResponse {
	fams := make([]string, 0, len(s.Families))
	for _, f := range s.Families {
		fams = append(fams, string(f))
	}
	h := s.Health
	return runnerSourceResponse{
		ID: string(s.ID), Kind: string(s.Kind), CredentialRef: s.CredentialRef, UnavailableReason: s.UnavailableReason, Families: fams,
		Backends: append([]string{}, s.Backends...), Models: append([]string{}, s.Models...),
		Health: sourceHealthResponse{
			Auth: string(h.Auth), Capacity: string(h.Capacity), ResetAt: runnerTimePtr(h.ResetAt), Headroom: h.Headroom,
			Congested: h.Congested, CongestedUntil: runnerTimePtr(h.CongestedUntil), LastProbeAt: runnerTimePtr(h.LastProbeAt),
			LastOutcome: string(h.LastOutcome),
		},
	}
}

func toSourceHealthResponse(h runner.SourceHealth) sourceHealthResponse {
	return toRunnerSourceResponse(runner.Source{Health: h}).Health
}

func toRunnerTelemetryResponse(t runner.Telemetry) runnerTelemetryResponse {
	var sampled *string
	if !t.SampledAt.IsZero() {
		sampled = runnerTimePtr(&t.SampledAt)
	}
	return runnerTelemetryResponse{
		SampledAt: sampled, CPUPercent: t.CPUPercent, MemUsedBytes: t.MemUsedBytes, MemPressure: t.MemPressure,
		Load1Pct: t.Load1Pct, DiskFreeBytes: t.DiskFreeBytes, Thermal: t.Thermal, UptimeSeconds: t.UptimeSeconds,
		Cores: t.Cores, RAMBytes: t.RAMBytes,
	}
}

func toRunnerDetailResponse(d runner.RunnerDetail) runnerDetailResponse {
	out := runnerDetailResponse{
		runnerViewResponse: toRunnerViewResponse(d.RunnerView),
		Assignments:        make([]assignmentResponse, 0, len(d.Assignments)),
		RecentTelemetry:    make([]runnerTelemetryResponse, 0, len(d.RecentTelemetry)),
		Sessions:           []json.RawMessage{},
		Homes:              []json.RawMessage{},
	}
	for _, a := range d.Assignments {
		ar := assignmentResponse{JobID: a.JobID.String(), NodeID: a.NodeID, State: string(a.State), SourceID: string(a.SourceID), Hidden: a.Hidden}
		if a.RunID != nil {
			s := a.RunID.String()
			ar.RunID = &s
		}
		out.Assignments = append(out.Assignments, ar)
	}
	for _, t := range d.RecentTelemetry {
		out.RecentTelemetry = append(out.RecentTelemetry, toRunnerTelemetryResponse(t))
	}
	return out
}

func toPoolEventViews(evs []runner.PoolEvent) []runner.PoolEventView {
	out := make([]runner.PoolEventView, 0, len(evs))
	for _, ev := range evs {
		out = append(out, runner.NewPoolEventView(ev))
	}
	return out
}
