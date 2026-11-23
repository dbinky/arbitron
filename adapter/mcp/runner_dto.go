package mcp

import (
	"encoding/json"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
)

type runnerCompatJSON struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type sourceHealthJSON struct {
	Auth           string   `json:"auth"`
	Capacity       string   `json:"capacity"`
	ResetAt        *string  `json:"resetAt"`
	Headroom       *float64 `json:"headroom"`
	Congested      bool     `json:"congested"`
	CongestedUntil *string  `json:"congestedUntil"`
	LastProbeAt    *string  `json:"lastProbeAt"`
	LastOutcome    string   `json:"lastOutcome"`
}

type runnerSourceJSON struct {
	ID                string           `json:"id"`
	Kind              string           `json:"kind"`
	CredentialRef     string           `json:"credentialRef"`
	UnavailableReason string           `json:"unavailableReason"`
	Families          []string         `json:"families"`
	Backends          []string         `json:"backends"`
	Models            []string         `json:"models"`
	Health            sourceHealthJSON `json:"health"`
}

type runnerTelemetryJSON struct {
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

type runnerViewJSON struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	BuiltIn         bool                 `json:"builtIn"`
	StepLimit       int                  `json:"stepLimit"`
	Labels          map[string]string    `json:"labels"`
	Cordoned        bool                 `json:"cordoned"`
	Draining        bool                 `json:"draining"`
	Compat          runnerCompatJSON     `json:"compat"`
	Liveness        string               `json:"liveness"`
	LastContact     *string              `json:"lastContact"`
	OS              string               `json:"os"`
	RunnerVersion   string               `json:"runnerVersion"`
	HarnessVersions map[string]string    `json:"harnessVersions"`
	SlotsUsed       int                  `json:"slotsUsed"`
	Queued          int                  `json:"queued"`
	HomedWorkspaces int                  `json:"homedWorkspaces"`
	Sources         []runnerSourceJSON   `json:"sources"`
	Telemetry       *runnerTelemetryJSON `json:"telemetry"`
	Drain           json.RawMessage      `json:"drain"`
}

type assignmentJSON struct {
	JobID    string  `json:"jobId"`
	RunID    *string `json:"runId"`
	NodeID   string  `json:"nodeId"`
	State    string  `json:"state"`
	SourceID string  `json:"sourceId"`
	Hidden   bool    `json:"hidden"`
}

// homeJSON mirrors the REST home response, including hidden-run protection.
type homeJSON struct {
	RunID      *string `json:"runId"`
	Hidden     bool    `json:"hidden"`
	Generation int64   `json:"generation"`
	Dirty      bool    `json:"dirty"`
}

type runnerDetailJSON struct {
	runnerViewJSON
	Assignments     []assignmentJSON      `json:"assignments"`
	RecentTelemetry []runnerTelemetryJSON `json:"recentTelemetry"`
	Sessions        []json.RawMessage     `json:"sessions"`
	Homes           []homeJSON            `json:"homes"`
}

func runnerTimeJSON(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func toRunnerViewJSON(v runner.RunnerView) runnerViewJSON {
	r := v.Runner
	out := runnerViewJSON{
		ID: string(r.ID), Name: r.Name, BuiltIn: r.BuiltIn, StepLimit: r.StepLimit, Labels: r.Labels,
		Cordoned: r.Cordoned, Draining: r.Draining,
		Compat:   runnerCompatJSON{State: string(r.Compat.State), Reason: r.Compat.Reason},
		Liveness: string(r.Liveness), LastContact: runnerTimeJSON(r.LastContact),
		OS: r.OS, RunnerVersion: r.RunnerVersion, HarnessVersions: r.HarnessVersions,
		SlotsUsed: v.SlotsUsed, Queued: v.Queued, HomedWorkspaces: v.HomedWorkspaces,
		Sources: make([]runnerSourceJSON, 0, len(v.Sources)),
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	if out.HarnessVersions == nil {
		out.HarnessVersions = map[string]string{}
	}
	for _, s := range v.Sources {
		out.Sources = append(out.Sources, toRunnerSourceJSON(s))
	}
	if v.Telemetry != nil {
		t := toRunnerTelemetryJSON(*v.Telemetry)
		out.Telemetry = &t
	}
	return out
}

func toRunnerSourceJSON(s runner.Source) runnerSourceJSON {
	fams := make([]string, 0, len(s.Families))
	for _, f := range s.Families {
		fams = append(fams, string(f))
	}
	h := s.Health
	return runnerSourceJSON{
		ID: string(s.ID), Kind: string(s.Kind), CredentialRef: s.CredentialRef, UnavailableReason: s.UnavailableReason, Families: fams,
		Backends: append([]string{}, s.Backends...), Models: append([]string{}, s.Models...),
		Health: sourceHealthJSON{
			Auth: string(h.Auth), Capacity: string(h.Capacity), ResetAt: runnerTimeJSON(h.ResetAt), Headroom: h.Headroom,
			Congested: h.Congested, CongestedUntil: runnerTimeJSON(h.CongestedUntil), LastProbeAt: runnerTimeJSON(h.LastProbeAt),
			LastOutcome: string(h.LastOutcome),
		},
	}
}

func toRunnerTelemetryJSON(t runner.Telemetry) runnerTelemetryJSON {
	var sampled *string
	if !t.SampledAt.IsZero() {
		sampled = runnerTimeJSON(&t.SampledAt)
	}
	return runnerTelemetryJSON{
		SampledAt: sampled, CPUPercent: t.CPUPercent, MemUsedBytes: t.MemUsedBytes, MemPressure: t.MemPressure,
		Load1Pct: t.Load1Pct, DiskFreeBytes: t.DiskFreeBytes, Thermal: t.Thermal, UptimeSeconds: t.UptimeSeconds,
		Cores: t.Cores, RAMBytes: t.RAMBytes,
	}
}

func toRunnerDetailJSON(d runner.RunnerDetail) runnerDetailJSON {
	out := runnerDetailJSON{
		runnerViewJSON:  toRunnerViewJSON(d.RunnerView),
		Assignments:     make([]assignmentJSON, 0, len(d.Assignments)),
		RecentTelemetry: make([]runnerTelemetryJSON, 0, len(d.RecentTelemetry)),
		Sessions:        []json.RawMessage{},
		Homes:           make([]homeJSON, 0, len(d.Homes)),
	}
	for _, a := range d.Assignments {
		aj := assignmentJSON{JobID: a.JobID.String(), NodeID: a.NodeID, State: string(a.State), SourceID: string(a.SourceID), Hidden: a.Hidden}
		if a.RunID != nil {
			s := a.RunID.String()
			aj.RunID = &s
		}
		out.Assignments = append(out.Assignments, aj)
	}
	for _, t := range d.RecentTelemetry {
		out.RecentTelemetry = append(out.RecentTelemetry, toRunnerTelemetryJSON(t))
	}
	for _, h := range d.Homes {
		hj := homeJSON{Hidden: h.Hidden, Generation: h.Generation, Dirty: h.Dirty}
		if h.RunID != nil && !h.Hidden {
			s := h.RunID.String()
			hj.RunID = &s
		}
		out.Homes = append(out.Homes, hj)
	}
	return out
}
